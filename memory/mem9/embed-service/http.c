/*
 * http.c - HTTP request parsing and response generation
 *
 * Minimal HTTP/1.1 implementation for the embedding service.
 * Parses method, path, content-length, content-type headers.
 * Generates proper HTTP responses with CORS headers.
 */

#include "http.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <ctype.h>
#include <unistd.h>
#include <sys/socket.h>
#include <errno.h>

/* ------------------------------------------------------------------ */
/*  HTTP Request Parsing                                              */
/* ------------------------------------------------------------------ */

/* Case-insensitive prefix match */
static int header_prefix(const char *line, const char *prefix)
{
    while (*prefix) {
        if (tolower((unsigned char)*line) != tolower((unsigned char)*prefix))
            return 0;
        line++;
        prefix++;
    }
    return 1;
}

int http_parse_request(const char *buf, size_t buf_len, http_request_t *req)
{
    if (!buf || !req) return -1;

    memset(req, 0, sizeof(*req));

    /* Find end of headers (\r\n\r\n) */
    const char *header_end = NULL;
    for (size_t i = 0; i + 3 < buf_len; i++) {
        if (buf[i] == '\r' && buf[i+1] == '\n' &&
            buf[i+2] == '\r' && buf[i+3] == '\n') {
            header_end = buf + i + 4;
            break;
        }
    }

    if (!header_end) {
        /* Headers not yet complete */
        if (buf_len > HTTP_MAX_HEADER_SIZE) return -1; /* Too large */
        return 0; /* Need more data */
    }

    size_t header_size = (size_t)(header_end - buf);

    /* Parse request line */
    const char *p = buf;
    const char *end = header_end;

    /* Method */
    if (buf_len >= 4 && memcmp(p, "GET ", 4) == 0) {
        req->method = HTTP_GET;
        p += 4;
    } else if (buf_len >= 5 && memcmp(p, "POST ", 5) == 0) {
        req->method = HTTP_POST;
        p += 5;
    } else if (buf_len >= 8 && memcmp(p, "OPTIONS ", 8) == 0) {
        req->method = HTTP_OPTIONS;
        p += 8;
    } else {
        req->method = HTTP_UNKNOWN;
        /* Find past the method */
        while (p < end && *p != ' ') p++;
        if (p < end) p++;
    }

    /* Path */
    {
        const char *path_start = p;
        while (p < end && *p != ' ' && *p != '\r' && *p != '\n') p++;

        size_t path_len = (size_t)(p - path_start);
        if (path_len >= sizeof(req->path)) path_len = sizeof(req->path) - 1;
        memcpy(req->path, path_start, path_len);
        req->path[path_len] = '\0';
    }

    /* Skip to end of request line */
    while (p < end && *p != '\n') p++;
    if (p < end) p++;

    /* Parse headers */
    while (p < end) {
        /* Empty line = end of headers */
        if (*p == '\r' || *p == '\n') break;

        const char *line_start = p;
        while (p < end && *p != '\n') p++;
        /* p now points at \n or end */
        size_t line_len = (size_t)(p - line_start);
        if (p < end) p++; /* skip \n */

        /* Remove trailing \r */
        if (line_len > 0 && line_start[line_len - 1] == '\r')
            line_len--;

        if (header_prefix(line_start, "content-length:")) {
            const char *val = line_start + 15;
            while (val < line_start + line_len && *val == ' ') val++;
            req->content_length = (size_t)atol(val);
        } else if (header_prefix(line_start, "content-type:")) {
            const char *val = line_start + 13;
            while (val < line_start + line_len && *val == ' ') val++;
            size_t ct_len = (size_t)(line_start + line_len - val);
            if (ct_len >= sizeof(req->content_type))
                ct_len = sizeof(req->content_type) - 1;
            memcpy(req->content_type, val, ct_len);
            req->content_type[ct_len] = '\0';
        }
    }

    /* Check if we have the full body */
    if (req->content_length > 0) {
        if (req->content_length > HTTP_MAX_BODY_SIZE) return -1;

        size_t available_body = buf_len - header_size;
        if (available_body < req->content_length) {
            return 0; /* Need more data */
        }

        req->body = (char *)(buf + header_size);
        req->body_len = req->content_length;
    }

    return (int)(header_size + req->content_length);
}

/* ------------------------------------------------------------------ */
/*  HTTP Response Sending                                             */
/* ------------------------------------------------------------------ */

/* Send all bytes on a socket, handling partial writes */
static int send_all(int fd, const char *buf, size_t len)
{
    size_t sent = 0;
    while (sent < len) {
        ssize_t n = send(fd, buf + sent, len - sent, 0);
        if (n < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        sent += (size_t)n;
    }
    return 0;
}

int http_send_response(int fd, int status_code, const char *status_text,
                       const char *content_type,
                       const char *body, size_t body_len)
{
    char header[1024];
    int n = snprintf(header, sizeof(header),
        "HTTP/1.1 %d %s\r\n"
        "Content-Type: %s\r\n"
        "Content-Length: %zu\r\n"
        "Access-Control-Allow-Origin: *\r\n"
        "Access-Control-Allow-Methods: GET, POST, OPTIONS\r\n"
        "Access-Control-Allow-Headers: Content-Type, Authorization\r\n"
        "Connection: keep-alive\r\n"
        "\r\n",
        status_code, status_text,
        content_type,
        body_len);

    if (n < 0 || (size_t)n >= sizeof(header)) return -1;

    if (send_all(fd, header, (size_t)n) < 0) return -1;
    if (body_len > 0 && body) {
        if (send_all(fd, body, body_len) < 0) return -1;
    }

    return 0;
}

int http_send_cors_preflight(int fd)
{
    const char *resp =
        "HTTP/1.1 204 No Content\r\n"
        "Access-Control-Allow-Origin: *\r\n"
        "Access-Control-Allow-Methods: GET, POST, OPTIONS\r\n"
        "Access-Control-Allow-Headers: Content-Type, Authorization\r\n"
        "Access-Control-Max-Age: 86400\r\n"
        "Content-Length: 0\r\n"
        "Connection: keep-alive\r\n"
        "\r\n";

    return send_all(fd, resp, strlen(resp));
}
