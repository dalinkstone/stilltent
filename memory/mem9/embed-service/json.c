/*
 * json.c - Minimal JSON parser and generator
 *
 * Hand-written, no external dependencies. Handles only the subset
 * of JSON needed for the OpenAI-compatible embeddings API.
 */

#include "json.h"

#include <string.h>
#include <stdlib.h>
#include <stdio.h>
#include <stdint.h>
#include <ctype.h>
#include <math.h>

/* ------------------------------------------------------------------ */
/*  JSON Parser Utilities                                             */
/* ------------------------------------------------------------------ */

/* Skip whitespace */
static const char *skip_ws(const char *p, const char *end)
{
    while (p < end && (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r'))
        p++;
    return p;
}

/* Parse a JSON string value (handles basic escapes) */
/* Returns pointer past the closing quote, or NULL on error */
/* Writes unescaped string to out_buf (up to out_cap - 1 bytes) */
static const char *parse_string(const char *p, const char *end,
                                char *out_buf, size_t out_cap, size_t *out_len)
{
    if (p >= end || *p != '"') return NULL;
    p++; /* skip opening quote */

    size_t len = 0;

    while (p < end && *p != '"') {
        if (*p == '\\') {
            p++;
            if (p >= end) return NULL;

            char c;
            switch (*p) {
                case '"':  c = '"'; break;
                case '\\': c = '\\'; break;
                case '/':  c = '/'; break;
                case 'b':  c = '\b'; break;
                case 'f':  c = '\f'; break;
                case 'n':  c = '\n'; break;
                case 'r':  c = '\r'; break;
                case 't':  c = '\t'; break;
                case 'u':
                    /* Unicode escape: \uXXXX - simplified handling */
                    if (p + 4 >= end) return NULL;
                    /* Just skip the 4 hex digits, replace with '?' */
                    p += 4;
                    c = '?';
                    break;
                default:
                    c = *p;
                    break;
            }

            if (out_cap > 0 && len < out_cap - 1)
                out_buf[len] = c;
            len++;
        } else {
            if (out_cap > 0 && len < out_cap - 1)
                out_buf[len] = *p;
            len++;
        }
        p++;
    }

    if (p >= end) return NULL; /* Unterminated string */
    p++; /* skip closing quote */

    if (out_buf && out_cap > 0) {
        if (len < out_cap)
            out_buf[len] = '\0';
        else
            out_buf[out_cap - 1] = '\0';
    }

    if (out_len) *out_len = len;
    return p;
}

/* Skip a JSON value (string, number, object, array, bool, null) */
static const char *skip_value(const char *p, const char *end)
{
    p = skip_ws(p, end);
    if (p >= end) return NULL;

    switch (*p) {
        case '"': {
            /* String */
            p++;
            while (p < end && *p != '"') {
                if (*p == '\\') {
                    p++;
                    if (p >= end) return NULL;
                }
                p++;
            }
            if (p >= end) return NULL;
            return p + 1;
        }
        case '{': {
            /* Object */
            int depth = 1;
            p++;
            while (p < end && depth > 0) {
                if (*p == '{') depth++;
                else if (*p == '}') depth--;
                else if (*p == '"') {
                    p++;
                    while (p < end && *p != '"') {
                        if (*p == '\\') p++;
                        p++;
                    }
                    if (p >= end) return NULL;
                }
                p++;
            }
            return (depth == 0) ? p : NULL;
        }
        case '[': {
            /* Array */
            int depth = 1;
            p++;
            while (p < end && depth > 0) {
                if (*p == '[') depth++;
                else if (*p == ']') depth--;
                else if (*p == '"') {
                    p++;
                    while (p < end && *p != '"') {
                        if (*p == '\\') p++;
                        p++;
                    }
                    if (p >= end) return NULL;
                }
                p++;
            }
            return (depth == 0) ? p : NULL;
        }
        case 't': /* true */
            if (p + 4 <= end && memcmp(p, "true", 4) == 0)
                return p + 4;
            return NULL;
        case 'f': /* false */
            if (p + 5 <= end && memcmp(p, "false", 5) == 0)
                return p + 5;
            return NULL;
        case 'n': /* null */
            if (p + 4 <= end && memcmp(p, "null", 4) == 0)
                return p + 4;
            return NULL;
        default:
            /* Number */
            if (*p == '-' || isdigit((unsigned char)*p)) {
                if (*p == '-') p++;
                while (p < end && isdigit((unsigned char)*p)) p++;
                if (p < end && *p == '.') {
                    p++;
                    while (p < end && isdigit((unsigned char)*p)) p++;
                }
                if (p < end && (*p == 'e' || *p == 'E')) {
                    p++;
                    if (p < end && (*p == '+' || *p == '-')) p++;
                    while (p < end && isdigit((unsigned char)*p)) p++;
                }
                return p;
            }
            return NULL;
    }
}

/* ------------------------------------------------------------------ */
/*  Parse embedding request                                           */
/* ------------------------------------------------------------------ */

int json_parse_embed_request(const char *body, size_t body_len, embed_request_t *req)
{
    if (!body || !req) return -1;

    memset(req, 0, sizeof(*req));
    req->encoding_float = 1; /* Default to float */

    const char *p = body;
    const char *end = body + body_len;

    p = skip_ws(p, end);
    if (p >= end || *p != '{') return -1;
    p++;

    int found_input = 0;

    while (p < end) {
        p = skip_ws(p, end);
        if (p >= end) return -1;
        if (*p == '}') break;
        if (*p == ',') { p++; continue; }

        /* Parse key */
        char key[64];
        size_t key_len;
        p = parse_string(p, end, key, sizeof(key), &key_len);
        if (!p) return -1;

        p = skip_ws(p, end);
        if (p >= end || *p != ':') return -1;
        p++;
        p = skip_ws(p, end);
        if (p >= end) return -1;

        if (strcmp(key, "model") == 0) {
            p = parse_string(p, end, req->model, sizeof(req->model), NULL);
            if (!p) return -1;
        } else if (strcmp(key, "input") == 0) {
            if (*p == '"') {
                /* Single string input */
                /* First pass: determine length */
                size_t input_len;
                const char *after = parse_string(p, end, NULL, 0, &input_len);
                if (!after) return -1;

                if (input_len > JSON_MAX_INPUT_LEN) return -1;

                req->input = (char *)malloc(input_len + 1);
                if (!req->input) return -1;

                p = parse_string(p, end, req->input, input_len + 1, &req->input_len);
                if (!p) { free(req->input); req->input = NULL; return -1; }
                found_input = 1;
            } else if (*p == '[') {
                /* Array of strings - take the first one */
                p++;
                p = skip_ws(p, end);
                if (p >= end) return -1;

                if (*p == '"') {
                    size_t input_len;
                    const char *after = parse_string(p, end, NULL, 0, &input_len);
                    if (!after) return -1;

                    if (input_len > JSON_MAX_INPUT_LEN) return -1;

                    req->input = (char *)malloc(input_len + 1);
                    if (!req->input) return -1;

                    p = parse_string(p, end, req->input, input_len + 1, &req->input_len);
                    if (!p) { free(req->input); req->input = NULL; return -1; }
                    found_input = 1;
                }

                /* Skip rest of array */
                while (p < end && *p != ']') {
                    if (*p == '"') {
                        p = skip_value(p, end);
                        if (!p) return -1;
                    } else {
                        p++;
                    }
                }
                if (p < end) p++; /* skip ']' */
            } else {
                p = skip_value(p, end);
                if (!p) return -1;
            }
        } else if (strcmp(key, "encoding_format") == 0) {
            char fmt[32];
            p = parse_string(p, end, fmt, sizeof(fmt), NULL);
            if (!p) return -1;
            req->encoding_float = (strcmp(fmt, "float") == 0) ? 1 : 0;
        } else {
            /* Skip unknown field */
            p = skip_value(p, end);
            if (!p) return -1;
        }
    }

    if (!found_input) return -1;
    return 0;
}

void json_free_embed_request(embed_request_t *req)
{
    if (req && req->input) {
        free(req->input);
        req->input = NULL;
    }
}

/* ------------------------------------------------------------------ */
/*  Fast float-to-string for L2-normalized embeddings [-1.0, 1.0]     */
/* ------------------------------------------------------------------ */

/*
 * Writes a float in [-1.0, 1.0] to buf with 8 significant digits.
 * Returns number of chars written. buf must have at least 14 bytes.
 * Falls back to snprintf for values outside [-1.0, 1.0].
 */
static int fast_ftoa(float val, char *buf)
{
    /* Handle special cases */
    if (val == 0.0f) {
        buf[0] = '0';
        return 1;
    }

    /* Fallback for out-of-range values (should not happen for L2-normalized) */
    if (val > 1.0f || val < -1.0f || isnan(val) || isinf(val)) {
        return snprintf(buf, 14, "%.8g", (double)val);
    }

    int pos = 0;

    if (val < 0.0f) {
        buf[pos++] = '-';
        val = -val;
    }

    /* val is now in (0.0, 1.0].
     * For val == 1.0, integer part is 1, fractional part is 0. */
    int integer_part = (int)val;
    float frac = val - (float)integer_part;

    buf[pos++] = '0' + (char)integer_part;

    if (frac < 0.000000005f) {
        /* No fractional part needed (e.g., val=1.0 or val=0.0) */
        return pos;
    }

    buf[pos++] = '.';

    /* Extract 8 decimal digits from the fractional part.
     * Multiply by 1e8 and round to get the digits as an integer. */
    uint32_t frac_int = (uint32_t)(frac * 100000000.0f + 0.5f);

    /* Cap at 99999999 to avoid overflow from rounding */
    if (frac_int > 99999999u) frac_int = 99999999u;

    /* Write 8 digits, then strip trailing zeros */
    char digits[8];
    for (int i = 7; i >= 0; i--) {
        digits[i] = '0' + (char)(frac_int % 10);
        frac_int /= 10;
    }

    /* Find last non-zero digit */
    int last_nonzero = 7;
    while (last_nonzero > 0 && digits[last_nonzero] == '0')
        last_nonzero--;

    for (int i = 0; i <= last_nonzero; i++) {
        buf[pos++] = digits[i];
    }

    return pos;
}

/* ------------------------------------------------------------------ */
/*  JSON Response Generation                                          */
/* ------------------------------------------------------------------ */

int json_generate_embed_response(const float *embedding, int dims,
                                 const char *model,
                                 char *out_buf, size_t out_cap)
{
    if (!embedding || !out_buf || out_cap < 256) return -1;

    /* Build JSON response matching OpenAI format */
    int pos = 0;
    int remaining = (int)out_cap;

    /* Header */
    int n = snprintf(out_buf + pos, (size_t)remaining,
        "{\"object\":\"list\","
        "\"data\":[{\"object\":\"embedding\","
        "\"embedding\":[");
    if (n < 0 || n >= remaining) return -1;
    pos += n;
    remaining -= n;

    /* Embedding values - fast path for L2-normalized floats */
    for (int i = 0; i < dims; i++) {
        if (i > 0) {
            if (remaining < 2) return -1;
            out_buf[pos++] = ',';
            remaining--;
        }

        if (remaining < 14) return -1;
        n = fast_ftoa(embedding[i], out_buf + pos);
        if (n <= 0 || n >= remaining) return -1;
        pos += n;
        remaining -= n;
    }

    /* Footer */
    n = snprintf(out_buf + pos, (size_t)remaining,
        "],\"index\":0}],"
        "\"model\":\"%s\","
        "\"usage\":{\"prompt_tokens\":0,\"total_tokens\":0}}",
        model ? model : "local-embed");
    if (n < 0 || n >= remaining) return -1;
    pos += n;

    return pos;
}

int json_generate_error(const char *message, const char *type,
                        char *out_buf, size_t out_cap)
{
    if (!out_buf || out_cap < 64) return -1;

    int n = snprintf(out_buf, out_cap,
        "{\"error\":{\"message\":\"%s\",\"type\":\"%s\",\"code\":null}}",
        message ? message : "Unknown error",
        type ? type : "invalid_request_error");

    if (n < 0 || (size_t)n >= out_cap) return -1;
    return n;
}

int json_generate_health(char *out_buf, size_t out_cap)
{
    if (!out_buf || out_cap < 32) return -1;
    int n = snprintf(out_buf, out_cap, "{\"status\":\"ok\"}");
    if (n < 0 || (size_t)n >= out_cap) return -1;
    return n;
}

int json_generate_health_metrics(long requests_served, long uptime_seconds,
                                 char *out_buf, size_t out_cap)
{
    if (!out_buf || out_cap < 64) return -1;
    int n = snprintf(out_buf, out_cap,
        "{\"status\":\"ok\",\"requests_served\":%ld,\"uptime_seconds\":%ld}",
        requests_served, uptime_seconds);
    if (n < 0 || (size_t)n >= out_cap) return -1;
    return n;
}
