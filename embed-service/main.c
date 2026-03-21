/*
 * main.c - Embedding service HTTP server
 *
 * Multi-threaded HTTP server implementing an OpenAI-compatible
 * /v1/embeddings endpoint using POSIX sockets and pthreads.
 *
 * Environment variables:
 *   EMBED_PORT - Port to listen on (default: 8090)
 *
 * Endpoints:
 *   POST /v1/embeddings - Compute embeddings (OpenAI-compatible)
 *   POST /embeddings    - Alias (some clients omit /v1 prefix)
 *   GET  /health        - Health check
 *   GET  /metrics       - Detailed metrics (pool size, queue depth, avg latency)
 *   OPTIONS *           - CORS preflight
 */

#include "embedder.h"
#include "http.h"
#include "json.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <signal.h>
#include <errno.h>
#include <unistd.h>
#include <pthread.h>
#include <time.h>

#include <sys/socket.h>
#include <sys/types.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <arpa/inet.h>
#include <sys/time.h>


/* ------------------------------------------------------------------ */
/*  Configuration                                                     */
/* ------------------------------------------------------------------ */

#define DEFAULT_PORT        8090
#define MAX_THREAD_POOL     32
#define LISTEN_BACKLOG      128
#define READ_BUF_SIZE       (4 * 1024)
#define MAX_QUEUE_DEPTH     32

/* Response buffer: 256 floats × ~10 chars + JSON overhead ≈ 3.5KB; 8KB is plenty */
#define RESPONSE_BUF_SIZE   (8 * 1024)

/* Keep-alive settings (reduced for resource-constrained droplet) */
#define KEEPALIVE_TIMEOUT_SEC   30
#define KEEPALIVE_MAX_REQUESTS  100

/* Dynamic thread pool size (resolved at startup) */
static int g_thread_pool_size = 0;

/* ------------------------------------------------------------------ */
/*  Thread Pool with Work Queue                                       */
/* ------------------------------------------------------------------ */

typedef struct work_item {
    int client_fd;
    struct work_item *next;
} work_item_t;

typedef struct {
    work_item_t *head;
    work_item_t *tail;
    pthread_mutex_t mutex;
    pthread_cond_t cond;
    int shutdown;
    int depth;
} work_queue_t;

static work_queue_t g_queue;

/* ------------------------------------------------------------------ */
/*  Metrics                                                           */
/* ------------------------------------------------------------------ */

static volatile long g_requests_served = 0;
static time_t g_start_time = 0;

/* Running average of embedding computation time in microseconds */
static volatile long g_total_embed_us = 0;
static volatile long g_embed_count = 0;

static void queue_init(work_queue_t *q)
{
    q->head = NULL;
    q->tail = NULL;
    q->shutdown = 0;
    q->depth = 0;
    pthread_mutex_init(&q->mutex, NULL);
    pthread_cond_init(&q->cond, NULL);
}

/* Returns 0 on success, -1 if queue is full */
static int queue_push(work_queue_t *q, int client_fd)
{
    work_item_t *item = (work_item_t *)malloc(sizeof(work_item_t));
    if (!item) {
        fprintf(stderr, "[error] malloc failed for work item\n");
        return -1;
    }
    item->client_fd = client_fd;
    item->next = NULL;

    pthread_mutex_lock(&q->mutex);
    if (q->depth >= MAX_QUEUE_DEPTH) {
        pthread_mutex_unlock(&q->mutex);
        free(item);
        return -1;
    }
    if (q->tail) {
        q->tail->next = item;
        q->tail = item;
    } else {
        q->head = item;
        q->tail = item;
    }
    q->depth++;
    pthread_cond_signal(&q->cond);
    pthread_mutex_unlock(&q->mutex);
    return 0;
}

static int queue_pop(work_queue_t *q)
{
    pthread_mutex_lock(&q->mutex);

    while (!q->head && !q->shutdown) {
        pthread_cond_wait(&q->cond, &q->mutex);
    }

    if (q->shutdown && !q->head) {
        pthread_mutex_unlock(&q->mutex);
        return -1;
    }

    work_item_t *item = q->head;
    q->head = item->next;
    if (!q->head) q->tail = NULL;
    q->depth--;

    pthread_mutex_unlock(&q->mutex);

    int fd = item->client_fd;
    free(item);
    return fd;
}

static void queue_shutdown(work_queue_t *q)
{
    pthread_mutex_lock(&q->mutex);
    q->shutdown = 1;
    pthread_cond_broadcast(&q->cond);
    pthread_mutex_unlock(&q->mutex);
}

static void queue_destroy(work_queue_t *q)
{
    /* Free any remaining items */
    work_item_t *item = q->head;
    while (item) {
        work_item_t *next = item->next;
        close(item->client_fd);
        free(item);
        item = next;
    }
    pthread_mutex_destroy(&q->mutex);
    pthread_cond_destroy(&q->cond);
}

/* ------------------------------------------------------------------ */
/*  Request Handling                                                  */
/* ------------------------------------------------------------------ */

static void send_json_error(int fd, int status, const char *status_text,
                            const char *message)
{
    char buf[512];
    int len = json_generate_error(message, "invalid_request_error", buf, sizeof(buf));
    if (len < 0) {
        const char *fallback = "{\"error\":{\"message\":\"Internal error\"}}";
        http_send_response(fd, 500, "Internal Server Error",
                          "application/json", fallback, strlen(fallback));
        return;
    }
    http_send_response(fd, status, status_text,
                      "application/json", buf, (size_t)len);
}

static void handle_health(int fd)
{
    char buf[256];
    long uptime = (long)(time(NULL) - g_start_time);
    int len = json_generate_health_metrics(g_requests_served, uptime, buf, sizeof(buf));
    if (len > 0) {
        http_send_response(fd, 200, "OK", "application/json", buf, (size_t)len);
    }
}

static void handle_metrics(int fd)
{
    char buf[512];
    long uptime = (long)(time(NULL) - g_start_time);
    long count = g_embed_count;
    long avg_us = (count > 0) ? (g_total_embed_us / count) : 0;
    int depth;
    pthread_mutex_lock(&g_queue.mutex);
    depth = g_queue.depth;
    pthread_mutex_unlock(&g_queue.mutex);

    int len = snprintf(buf, sizeof(buf),
        "{\"status\":\"ok\","
        "\"requests_served\":%ld,"
        "\"uptime_seconds\":%ld,"
        "\"thread_pool_size\":%d,"
        "\"queue_depth\":%d,"
        "\"avg_embed_us\":%ld}",
        g_requests_served, uptime, g_thread_pool_size, depth, avg_us);

    if (len > 0 && (size_t)len < sizeof(buf)) {
        http_send_response(fd, 200, "OK", "application/json", buf, (size_t)len);
    }
}

static void handle_embeddings(int fd, const char *body, size_t body_len)
{
    /* Parse request */
    embed_request_t req;
    if (json_parse_embed_request(body, body_len, &req) < 0) {
        send_json_error(fd, 400, "Bad Request", "Failed to parse JSON request body");
        return;
    }

    /* Compute embedding with timing */
    struct timeval t_start, t_end;
    gettimeofday(&t_start, NULL);

    float embedding[EMBED_DIM];
    if (embed_text(req.input, req.input_len, embedding) < 0) {
        send_json_error(fd, 500, "Internal Server Error",
                       "Embedding computation failed");
        json_free_embed_request(&req);
        return;
    }

    gettimeofday(&t_end, NULL);
    {
        long elapsed_us = (t_end.tv_sec - t_start.tv_sec) * 1000000L
                        + (t_end.tv_usec - t_start.tv_usec);
        __sync_fetch_and_add(&g_total_embed_us, elapsed_us);
        __sync_fetch_and_add(&g_embed_count, 1);
    }

    /* Generate response using thread-local buffer (zero malloc per request) */
    static _Thread_local char tl_resp_buf[RESPONSE_BUF_SIZE];

    int resp_len = json_generate_embed_response(
        embedding, EMBED_DIM, req.model, tl_resp_buf, RESPONSE_BUF_SIZE);

    if (resp_len < 0) {
        send_json_error(fd, 500, "Internal Server Error",
                       "Response generation failed");
    } else {
        http_send_response(fd, 200, "OK", "application/json",
                          tl_resp_buf, (size_t)resp_len);
    }

    json_free_embed_request(&req);
}

static void handle_connection(int client_fd)
{
    /* Thread-local read buffer: eliminates malloc/free per request */
    static _Thread_local char tl_read_buf[READ_BUF_SIZE];

    /* Set keep-alive recv timeout */
    struct timeval ka_tv;
    ka_tv.tv_sec = KEEPALIVE_TIMEOUT_SEC;
    ka_tv.tv_usec = 0;
    setsockopt(client_fd, SOL_SOCKET, SO_RCVTIMEO, &ka_tv, sizeof(ka_tv));

    int requests_on_conn = 0;

    while (requests_on_conn < KEEPALIVE_MAX_REQUESTS) {
        /* Read request data */
        size_t total_read = 0;
        http_request_t req;
        int request_complete = 0;

        while (total_read < READ_BUF_SIZE) {
            ssize_t n = recv(client_fd, tl_read_buf + total_read,
                            READ_BUF_SIZE - total_read, 0);
            if (n <= 0) {
                if (n < 0 && errno == EINTR) continue;
                goto close_conn; /* Connection closed, timeout, or error */
            }
            total_read += (size_t)n;

            /* Try to parse what we have */
            int result = http_parse_request(tl_read_buf, total_read, &req);
            if (result > 0) {
                request_complete = 1;
                break;
            } else if (result < 0) {
                send_json_error(client_fd, 400, "Bad Request",
                               "Malformed HTTP request");
                goto close_conn;
            }
            /* result == 0: need more data, keep reading */
        }

        if (!request_complete || total_read == 0) {
            goto close_conn;
        }

        /* Route request */
        if (req.method == HTTP_OPTIONS) {
            http_send_cors_preflight(client_fd);
        } else if (req.method == HTTP_GET && strcmp(req.path, "/health") == 0) {
            handle_health(client_fd);
        } else if (req.method == HTTP_GET && strcmp(req.path, "/metrics") == 0) {
            handle_metrics(client_fd);
        } else if (req.method == HTTP_POST &&
                   (strcmp(req.path, "/v1/embeddings") == 0 ||
                    strcmp(req.path, "/embeddings") == 0)) {
            if (req.body && req.body_len > 0) {
                char saved = req.body[req.body_len];
                ((char *)req.body)[req.body_len] = '\0';
                handle_embeddings(client_fd, req.body, req.body_len);
                ((char *)req.body)[req.body_len] = saved;
            } else {
                send_json_error(client_fd, 400, "Bad Request",
                               "Missing request body");
            }
        } else {
            send_json_error(client_fd, 404, "Not Found",
                           "Unknown endpoint");
        }

        requests_on_conn++;
        __sync_fetch_and_add(&g_requests_served, 1);

        /* After first request, use shorter timeout for subsequent keep-alive reads */
        if (requests_on_conn == 1) {
            ka_tv.tv_sec = KEEPALIVE_TIMEOUT_SEC;
            ka_tv.tv_usec = 0;
            setsockopt(client_fd, SOL_SOCKET, SO_RCVTIMEO, &ka_tv, sizeof(ka_tv));
        }
    }

close_conn:
    /* Graceful socket shutdown: signal we're done writing, then drain any
       remaining data the client might send before closing. This prevents
       the TCP RST that causes "Remote end closed connection" errors. */
    shutdown(client_fd, SHUT_WR);
    {
        char drain[256];
        while (recv(client_fd, drain, sizeof(drain), 0) > 0)
            ; /* drain */
    }
    close(client_fd);
}

/* ------------------------------------------------------------------ */
/*  Worker Thread                                                     */
/* ------------------------------------------------------------------ */

static void *worker_thread(void *arg)
{
    (void)arg;

    while (1) {
        int fd = queue_pop(&g_queue);
        if (fd < 0) break; /* Shutdown */
        handle_connection(fd);
    }

    return NULL;
}

/* ------------------------------------------------------------------ */
/*  Signal Handling                                                   */
/* ------------------------------------------------------------------ */

static volatile sig_atomic_t g_running = 1;

static void signal_handler(int sig)
{
    (void)sig;
    g_running = 0;
}

/* ------------------------------------------------------------------ */
/*  Main                                                              */
/* ------------------------------------------------------------------ */

int main(int argc, char **argv)
{
    (void)argc;
    (void)argv;

    /* Parse port from environment */
    int port = DEFAULT_PORT;
    const char *port_env = getenv("EMBED_PORT");
    if (port_env) {
        int p = atoi(port_env);
        if (p > 0 && p < 65536) {
            port = p;
        } else {
            fprintf(stderr, "[warn] Invalid EMBED_PORT '%s', using default %d\n",
                    port_env, DEFAULT_PORT);
        }
    }

    /* Determine thread pool size from environment or CPU count */
    {
        const char *threads_env = getenv("EMBED_THREADS");
        if (threads_env) {
            int t = atoi(threads_env);
            if (t > 0 && t <= MAX_THREAD_POOL) {
                g_thread_pool_size = t;
            } else {
                fprintf(stderr, "[warn] Invalid EMBED_THREADS '%s', using auto\n",
                        threads_env);
            }
        }
        if (g_thread_pool_size == 0) {
            long ncpus = 0;
#if defined(__APPLE__) || defined(__FreeBSD__)
            {
                /* Use sysctlbyname which doesn't require sys/sysctl.h types */
                int cpu_count = 0;
                size_t len = sizeof(cpu_count);
                /* Declared in <unistd.h> on Apple platforms */
                extern int sysctlbyname(const char *, void *, size_t *,
                                        void *, size_t);
                if (sysctlbyname("hw.ncpu", &cpu_count, &len, NULL, 0) == 0)
                    ncpus = cpu_count;
            }
#elif defined(_SC_NPROCESSORS_ONLN)
            ncpus = sysconf(_SC_NPROCESSORS_ONLN);
#endif
            if (ncpus < 1) ncpus = 2;
            g_thread_pool_size = (int)(ncpus < 4 ? ncpus : 4);
            if (g_thread_pool_size > MAX_THREAD_POOL)
                g_thread_pool_size = MAX_THREAD_POOL;
        }
    }

    /* Record startup time for uptime metric */
    g_start_time = time(NULL);

    /* Set up signal handling */
    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_handler = signal_handler;
    sigemptyset(&sa.sa_mask);
    sigaction(SIGINT, &sa, NULL);
    sigaction(SIGTERM, &sa, NULL);

    /* Ignore SIGPIPE (broken pipe from closed connections) */
    signal(SIGPIPE, SIG_IGN);

    /* Create server socket */
    int server_fd = socket(AF_INET, SOCK_STREAM, 0);
    if (server_fd < 0) {
        perror("[fatal] socket");
        return 1;
    }

    /* Allow port reuse */
    int opt = 1;
    setsockopt(server_fd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));
#ifdef SO_REUSEPORT
    setsockopt(server_fd, SOL_SOCKET, SO_REUSEPORT, &opt, sizeof(opt));
#endif

    /* Bind */
    struct sockaddr_in addr;
    memset(&addr, 0, sizeof(addr));
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = INADDR_ANY;
    addr.sin_port = htons((uint16_t)port);

    if (bind(server_fd, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
        perror("[fatal] bind");
        close(server_fd);
        return 1;
    }

    /* Listen */
    if (listen(server_fd, LISTEN_BACKLOG) < 0) {
        perror("[fatal] listen");
        close(server_fd);
        return 1;
    }

    fprintf(stdout,
        "============================================\n"
        "  embed-service v1.0\n"
        "  Listening on port %d\n"
        "  Thread pool: %d workers\n"
        "  Embedding dims: %d\n"
        "  Endpoints:\n"
        "    POST /v1/embeddings\n"
        "    POST /embeddings\n"
        "    GET  /health\n"
        "    GET  /metrics\n"
        "============================================\n",
        port, g_thread_pool_size, EMBED_DIM);
    fflush(stdout);

    /* Initialize work queue and thread pool */
    queue_init(&g_queue);

    /* Worker threads need a larger stack than musl's 128KB default.
     * embed_text() + channel_code() use ~132KB of stack (two token_list_t
     * arrays of 64KB each), which overflows musl's default and causes
     * SIGSEGV on every embedding request. Set 512KB for safety. */
    pthread_attr_t thread_attr;
    pthread_attr_init(&thread_attr);
    pthread_attr_setstacksize(&thread_attr, 512 * 1024);

    pthread_t threads[MAX_THREAD_POOL];
    for (int i = 0; i < g_thread_pool_size; i++) {
        if (pthread_create(&threads[i], &thread_attr, worker_thread, NULL) != 0) {
            perror("[fatal] pthread_create");
            close(server_fd);
            return 1;
        }
    }
    pthread_attr_destroy(&thread_attr);

    /* Accept loop */
    while (g_running) {
        struct sockaddr_in client_addr;
        socklen_t client_len = sizeof(client_addr);

        int client_fd = accept(server_fd, (struct sockaddr *)&client_addr,
                              &client_len);
        if (client_fd < 0) {
            if (errno == EINTR) continue;
            if (!g_running) break;
            perror("[error] accept");
            continue;
        }

        /* Set TCP_NODELAY for low latency */
        int flag = 1;
        setsockopt(client_fd, IPPROTO_TCP, TCP_NODELAY, &flag, sizeof(flag));

        /* Set send timeout (recv timeout is set per keep-alive phase) */
        struct timeval tv;
        tv.tv_sec = 10;
        tv.tv_usec = 0;
        setsockopt(client_fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));

        if (queue_push(&g_queue, client_fd) < 0) {
            /* Queue full: send 503 and close immediately */
            static const char resp_503[] =
                "HTTP/1.1 503 Service Unavailable\r\n"
                "Content-Type: application/json\r\n"
                "Content-Length: 61\r\n"
                "Connection: close\r\n"
                "\r\n"
                "{\"error\":{\"message\":\"Server overloaded\",\"type\":\"overloaded\"}}";
            send(client_fd, resp_503, sizeof(resp_503) - 1, 0);
            close(client_fd);
        }
    }

    /* Graceful shutdown */
    fprintf(stdout, "\n[info] Shutting down...\n");
    fflush(stdout);

    queue_shutdown(&g_queue);

    for (int i = 0; i < g_thread_pool_size; i++) {
        pthread_join(threads[i], NULL);
    }

    queue_destroy(&g_queue);
    close(server_fd);

    fprintf(stdout, "[info] Shutdown complete.\n");
    return 0;
}
