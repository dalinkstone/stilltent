/*
 * json.h - Minimal JSON parser and generator
 *
 * Supports only the subset needed for the embeddings API:
 * - Parse: extract "model", "input", "encoding_format" from request
 * - Generate: produce {"data":[{"embedding":[...]}]} response
 */

#ifndef JSON_H
#define JSON_H

#include <stddef.h>

/* Maximum input text length we accept (64 KB) */
#define JSON_MAX_INPUT_LEN (64 * 1024)

/* Maximum model name length */
#define JSON_MAX_MODEL_LEN 128

/* Parsed embedding request */
typedef struct {
    char model[JSON_MAX_MODEL_LEN];
    char *input;          /* Heap-allocated, caller must free */
    size_t input_len;
    int encoding_float;   /* 1 if encoding_format is "float" */
} embed_request_t;

/*
 * json_parse_embed_request - Parse a JSON embedding request body.
 *
 * Parameters:
 *   body     - Raw JSON body (null-terminated)
 *   body_len - Length of body
 *   req      - Output parsed request (caller must free req->input)
 *
 * Returns 0 on success, -1 on parse error.
 */
int json_parse_embed_request(const char *body, size_t body_len, embed_request_t *req);

/*
 * json_free_embed_request - Free heap memory in a parsed request.
 */
void json_free_embed_request(embed_request_t *req);

/*
 * json_generate_embed_response - Generate a JSON embedding response.
 *
 * Parameters:
 *   embedding - Array of floats (the embedding vector)
 *   dims      - Number of dimensions
 *   model     - Model name string
 *   out_buf   - Output buffer (caller-allocated)
 *   out_cap   - Capacity of output buffer
 *
 * Returns number of bytes written (excluding null terminator), or -1 on error.
 */
int json_generate_embed_response(const float *embedding, int dims,
                                 const char *model,
                                 char *out_buf, size_t out_cap);

/*
 * json_generate_error - Generate a JSON error response.
 *
 * Returns bytes written, or -1 on error.
 */
int json_generate_error(const char *message, const char *type,
                        char *out_buf, size_t out_cap);

/*
 * json_generate_health - Generate health check response.
 */
int json_generate_health(char *out_buf, size_t out_cap);

/*
 * json_generate_health_metrics - Generate health check with metrics.
 */
int json_generate_health_metrics(long requests_served, long uptime_seconds,
                                 char *out_buf, size_t out_cap);

#endif /* JSON_H */
