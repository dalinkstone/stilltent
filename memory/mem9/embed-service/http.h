/*
 * http.h - HTTP request parsing and response generation
 *
 * Minimal HTTP/1.1 implementation supporting:
 *   POST /v1/embeddings
 *   GET  /health
 */

#ifndef HTTP_H
#define HTTP_H

#include <stddef.h>

/* Maximum HTTP header size */
#define HTTP_MAX_HEADER_SIZE  (8 * 1024)

/* Maximum HTTP body size (128 KB) */
#define HTTP_MAX_BODY_SIZE    (128 * 1024)

/* HTTP methods */
typedef enum {
    HTTP_GET,
    HTTP_POST,
    HTTP_OPTIONS,
    HTTP_UNKNOWN
} http_method_t;

/* Parsed HTTP request */
typedef struct {
    http_method_t method;
    char path[256];
    char content_type[128];
    size_t content_length;
    char *body;             /* Points into the read buffer, not heap-allocated */
    size_t body_len;
} http_request_t;

/*
 * http_parse_request - Parse an HTTP request from a raw buffer.
 *
 * Parameters:
 *   buf     - Raw bytes read from socket
 *   buf_len - Number of bytes in buf
 *   req     - Output parsed request
 *
 * Returns:
 *   > 0 : Total bytes consumed (headers + body). Request is complete.
 *     0 : Need more data (incomplete request).
 *    -1 : Parse error (malformed request).
 */
int http_parse_request(const char *buf, size_t buf_len, http_request_t *req);

/*
 * http_send_response - Format and send an HTTP response on a socket fd.
 *
 * Parameters:
 *   fd          - Socket file descriptor
 *   status_code - HTTP status code (200, 400, 404, 500, etc.)
 *   status_text - Status text ("OK", "Bad Request", etc.)
 *   content_type- Content-Type header value
 *   body        - Response body
 *   body_len    - Length of response body
 *
 * Returns 0 on success, -1 on error.
 */
int http_send_response(int fd, int status_code, const char *status_text,
                       const char *content_type,
                       const char *body, size_t body_len);

/*
 * http_send_cors_headers - Send CORS preflight response.
 */
int http_send_cors_preflight(int fd);

#endif /* HTTP_H */
