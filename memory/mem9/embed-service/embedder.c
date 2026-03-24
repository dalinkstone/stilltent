/*
 * embedder.c - Multi-channel feature hashing embedding algorithm
 *
 * Produces 256-dimensional L2-normalized float32 vectors.
 * Designed for semantic similarity on code and natural language about code.
 *
 * Channel layout:
 *   [0,  64)  - Character trigram hashing
 *   [64, 128) - Word-level features (TF-weighted, IDF-approximated)
 *   [128,192) - Code-specific features
 *   [192,224) - Word bigram features
 *   [224,256) - Global/statistical features
 */

#include "embedder.h"

#include <math.h>
#include <string.h>
#include <stdlib.h>
#include <ctype.h>

/* ------------------------------------------------------------------ */
/*  FNV-1a hash (64-bit)                                              */
/* ------------------------------------------------------------------ */

#define FNV_OFFSET_BASIS 0xcbf29ce484222325ULL
#define FNV_PRIME        0x100000003bULL

static uint64_t fnv1a(const void *data, size_t len)
{
    const unsigned char *p = (const unsigned char *)data;
    uint64_t h = FNV_OFFSET_BASIS;
    for (size_t i = 0; i < len; i++) {
        h ^= p[i];
        h *= FNV_PRIME;
    }
    return h;
}

static uint64_t fnv1a_str(const char *s, size_t len)
{
    return fnv1a(s, len);
}

/* Hash with a seed (for different channels) */
static uint64_t fnv1a_seeded(const void *data, size_t len, uint64_t seed)
{
    const unsigned char *p = (const unsigned char *)data;
    uint64_t h = FNV_OFFSET_BASIS ^ seed;
    for (size_t i = 0; i < len; i++) {
        h ^= p[i];
        h *= FNV_PRIME;
    }
    return h;
}

/* ------------------------------------------------------------------ */
/*  Utility: lowercase a character for case-insensitive hashing       */
/* ------------------------------------------------------------------ */

static inline char lower(char c)
{
    return (c >= 'A' && c <= 'Z') ? (c + 32) : c;
}

/* ------------------------------------------------------------------ */
/*  Tokenizer - splits text into words                                */
/* ------------------------------------------------------------------ */

#define MAX_TOKENS 4096
#define MAX_TOKEN_LEN 256

typedef struct {
    const char *start;
    size_t len;
} token_t;

typedef struct {
    token_t tokens[MAX_TOKENS];
    int count;
} token_list_t;

/*
 * Tokenize text on whitespace and punctuation boundaries.
 * Also splits camelCase and snake_case for code awareness.
 */
static void tokenize(const char *text, size_t text_len, token_list_t *out)
{
    out->count = 0;
    size_t i = 0;

    while (i < text_len && out->count < MAX_TOKENS) {
        /* Skip whitespace */
        while (i < text_len && (isspace((unsigned char)text[i]))) {
            i++;
        }
        if (i >= text_len) break;

        /* Start of a token */
        size_t start = i;

        if (isalnum((unsigned char)text[i]) || text[i] == '_') {
            /* Alphanumeric token */
            while (i < text_len &&
                   (isalnum((unsigned char)text[i]) || text[i] == '_')) {
                i++;
            }
        } else {
            /* Punctuation/operator token (single char or multi-char operators) */
            char c = text[i];
            i++;
            /* Recognize common multi-char operators */
            if (i < text_len) {
                char next = text[i];
                if ((c == '=' && next == '=') ||
                    (c == '!' && next == '=') ||
                    (c == '<' && next == '=') ||
                    (c == '>' && next == '=') ||
                    (c == '&' && next == '&') ||
                    (c == '|' && next == '|') ||
                    (c == '-' && next == '>') ||
                    (c == '=' && next == '>') ||
                    (c == ':' && next == ':') ||
                    (c == '+' && next == '+') ||
                    (c == '-' && next == '-')) {
                    i++;
                }
            }
        }

        size_t len = i - start;
        if (len > 0 && len <= MAX_TOKEN_LEN) {
            out->tokens[out->count].start = text + start;
            out->tokens[out->count].len = len;
            out->count++;
        }
    }
}

/*
 * Sub-tokenize a word by splitting camelCase and snake_case.
 * Returns sub-tokens in the provided list, appending to existing tokens.
 */
static void subtokenize_camel_snake(const char *word, size_t word_len,
                                    token_list_t *out)
{
    size_t i = 0;
    while (i < word_len && out->count < MAX_TOKENS) {
        /* Skip underscores */
        while (i < word_len && word[i] == '_') i++;
        if (i >= word_len) break;

        size_t start = i;
        if (isupper((unsigned char)word[i])) {
            i++;
            /* If next chars are lowercase, it's the start of a camelCase word */
            while (i < word_len && islower((unsigned char)word[i])) i++;
        } else if (islower((unsigned char)word[i])) {
            while (i < word_len && islower((unsigned char)word[i])) i++;
        } else if (isdigit((unsigned char)word[i])) {
            while (i < word_len && isdigit((unsigned char)word[i])) i++;
        } else {
            i++;
        }

        size_t len = i - start;
        if (len > 0) {
            out->tokens[out->count].start = word + start;
            out->tokens[out->count].len = len;
            out->count++;
        }
    }
}

/* ------------------------------------------------------------------ */
/*  Channel 1: Character trigram hashing (dims 0-63)                  */
/* ------------------------------------------------------------------ */

static void channel_trigrams(const char *text, size_t text_len, float *out)
{
    int ch_size = CH1_END - CH1_START;
    float counts[64];
    memset(counts, 0, sizeof(counts));

    if (text_len < 3) {
        /* For very short text, use character unigrams */
        for (size_t i = 0; i < text_len; i++) {
            char lc = lower(text[i]);
            uint64_t h = fnv1a_seeded(&lc, 1, 0x1234);
            int dim = (int)(h % (uint64_t)ch_size);
            /* Use sign bit for random projection effect */
            float sign = (h & 0x100) ? 1.0f : -1.0f;
            counts[dim] += sign;
        }
    } else {
        char trigram[3];
        int n_trigrams = 0;
        for (size_t i = 0; i <= text_len - 3; i++) {
            trigram[0] = lower(text[i]);
            trigram[1] = lower(text[i + 1]);
            trigram[2] = lower(text[i + 2]);

            uint64_t h = fnv1a_seeded(trigram, 3, 0x1234);
            int dim = (int)(h % (uint64_t)ch_size);
            float sign = (h & 0x100) ? 1.0f : -1.0f;
            counts[dim] += sign;
            n_trigrams++;
        }

        /* TF normalization: divide by sqrt of count */
        if (n_trigrams > 0) {
            float norm = sqrtf((float)n_trigrams);
            for (int i = 0; i < ch_size; i++) {
                counts[i] /= norm;
            }
        }
    }

    for (int i = 0; i < ch_size; i++) {
        out[CH1_START + i] = counts[i];
    }
}

/* ------------------------------------------------------------------ */
/*  Channel 2: Word-level features (dims 64-127)                      */
/* ------------------------------------------------------------------ */

static void channel_words(const token_list_t *tokens, float *out)
{
    int ch_size = CH2_END - CH2_START;
    float counts[64];
    memset(counts, 0, sizeof(counts));

    /* Lowercase buffer for hashing */
    char lc_buf[MAX_TOKEN_LEN];

    int word_count = 0;

    for (int t = 0; t < tokens->count; t++) {
        const token_t *tok = &tokens->tokens[t];

        /* Only process alphanumeric tokens */
        if (!isalnum((unsigned char)tok->start[0]) && tok->start[0] != '_')
            continue;

        size_t len = tok->len < MAX_TOKEN_LEN ? tok->len : MAX_TOKEN_LEN - 1;
        for (size_t i = 0; i < len; i++) {
            lc_buf[i] = lower(tok->start[i]);
        }

        uint64_t h = fnv1a_seeded(lc_buf, len, 0x5678);
        int dim = (int)(h % (uint64_t)ch_size);
        float sign = (h & 0x100) ? 1.0f : -1.0f;

        /*
         * IDF approximation: longer words are rarer and more informative.
         * Weight = 1 + log(1 + word_length)
         * This gives common short words (a, the, is) lower weight
         * and longer domain-specific words higher weight.
         */
        float idf_weight = 1.0f + logf(1.0f + (float)len);

        counts[dim] += sign * idf_weight;
        word_count++;
    }

    /* TF normalization */
    if (word_count > 0) {
        float norm = sqrtf((float)word_count);
        for (int i = 0; i < ch_size; i++) {
            counts[i] /= norm;
        }
    }

    for (int i = 0; i < ch_size; i++) {
        out[CH2_START + i] = counts[i];
    }
}

/* ------------------------------------------------------------------ */
/*  Channel 3: Code-specific features (dims 128-191)                  */
/* ------------------------------------------------------------------ */

/* Common programming keywords to detect */
static const char *code_keywords[] = {
    "function", "def", "class", "return", "if", "else", "for", "while",
    "switch", "case", "break", "continue", "import", "from", "export",
    "const", "let", "var", "int", "float", "double", "char", "void",
    "struct", "enum", "typedef", "static", "public", "private", "protected",
    "try", "catch", "throw", "async", "await", "yield", "lambda",
    "interface", "implements", "extends", "override", "virtual",
    "package", "module", "require", "include", "pragma",
    "true", "false", "null", "nil", "None", "undefined",
    "self", "this", "super", "new", "delete", "sizeof",
    "select", "insert", "update", "where", "join", "create", "table",
    NULL
};

static int is_code_keyword(const char *word, size_t len)
{
    char lc_buf[64];
    if (len >= sizeof(lc_buf)) return 0;

    for (size_t i = 0; i < len; i++) {
        lc_buf[i] = lower(word[i]);
    }
    lc_buf[len] = '\0';

    for (int i = 0; code_keywords[i] != NULL; i++) {
        if (strcmp(lc_buf, code_keywords[i]) == 0)
            return 1;
    }
    return 0;
}

static int is_bracket(char c)
{
    return c == '(' || c == ')' || c == '{' || c == '}' ||
           c == '[' || c == ']' || c == '<' || c == '>';
}

static int is_operator_char(char c)
{
    return c == '+' || c == '-' || c == '*' || c == '/' ||
           c == '=' || c == '!' || c == '&' || c == '|' ||
           c == '^' || c == '~' || c == '%';
}

static void channel_code(const char *text, size_t text_len,
                         const token_list_t *tokens, float *out)
{
    int ch_size = CH3_END - CH3_START;
    float counts[64];
    memset(counts, 0, sizeof(counts));

    /* Sub-tokenize for camelCase/snake_case */
    token_list_t subtokens;
    subtokens.count = 0;

    int bracket_count = 0;
    int operator_count = 0;
    int keyword_count = 0;
    int dot_count = 0;
    int semicolon_count = 0;
    int max_indent = 0;
    int current_indent = 0;
    int line_start = 1;

    /* Scan raw text for structural features */
    for (size_t i = 0; i < text_len; i++) {
        char c = text[i];
        if (c == '\n') {
            line_start = 1;
            current_indent = 0;
        } else if (line_start && (c == ' ' || c == '\t')) {
            current_indent += (c == '\t') ? 4 : 1;
            if (current_indent > max_indent)
                max_indent = current_indent;
        } else {
            line_start = 0;
        }

        if (is_bracket(c)) bracket_count++;
        if (is_operator_char(c)) operator_count++;
        if (c == '.') dot_count++;
        if (c == ';') semicolon_count++;
    }

    /* Process tokens for code features */
    char lc_buf[MAX_TOKEN_LEN];

    for (int t = 0; t < tokens->count; t++) {
        const token_t *tok = &tokens->tokens[t];

        if (isalnum((unsigned char)tok->start[0]) || tok->start[0] == '_') {
            /* Check for code keywords */
            if (is_code_keyword(tok->start, tok->len)) {
                keyword_count++;

                /* Hash keyword to code channel */
                size_t len = tok->len < MAX_TOKEN_LEN ? tok->len : MAX_TOKEN_LEN - 1;
                for (size_t i = 0; i < len; i++)
                    lc_buf[i] = lower(tok->start[i]);

                uint64_t h = fnv1a_seeded(lc_buf, len, 0xC0DE);
                int dim = (int)(h % (uint64_t)(ch_size - 8)); /* Reserve last 8 dims */
                float sign = (h & 0x100) ? 1.0f : -1.0f;
                counts[dim] += sign * 2.0f; /* Keywords get extra weight */
            }

            /* Sub-tokenize camelCase/snake_case */
            subtokenize_camel_snake(tok->start, tok->len, &subtokens);

            /* Check if this looks like a function call (followed by '(') */
            if (t + 1 < tokens->count &&
                tokens->tokens[t + 1].len == 1 &&
                tokens->tokens[t + 1].start[0] == '(') {

                size_t len = tok->len < MAX_TOKEN_LEN ? tok->len : MAX_TOKEN_LEN - 1;
                for (size_t i = 0; i < len; i++)
                    lc_buf[i] = lower(tok->start[i]);

                uint64_t h = fnv1a_seeded(lc_buf, len, 0xFCA11);
                int dim = (int)(h % (uint64_t)(ch_size - 8));
                float sign = (h & 0x100) ? 1.0f : -1.0f;
                counts[dim] += sign * 1.5f; /* Function calls are important */
            }
        } else {
            /* Hash operators and brackets to code channel */
            uint64_t h = fnv1a_seeded(tok->start, tok->len, 0x0BEF);
            int dim = (int)(h % (uint64_t)(ch_size - 8));
            float sign = (h & 0x100) ? 1.0f : -1.0f;
            counts[dim] += sign;
        }
    }

    /* Hash sub-tokens (camelCase/snake_case parts) */
    for (int t = 0; t < subtokens.count; t++) {
        const token_t *tok = &subtokens.tokens[t];
        size_t len = tok->len < MAX_TOKEN_LEN ? tok->len : MAX_TOKEN_LEN - 1;
        for (size_t i = 0; i < len; i++)
            lc_buf[i] = lower(tok->start[i]);

        uint64_t h = fnv1a_seeded(lc_buf, len, 0x5B70);
        int dim = (int)(h % (uint64_t)(ch_size - 8));
        float sign = (h & 0x100) ? 1.0f : -1.0f;
        counts[dim] += sign * 0.8f;
    }

    /* Structural features in last 8 dims of the channel */
    int base = ch_size - 8;
    counts[base + 0] = logf(1.0f + (float)bracket_count);
    counts[base + 1] = logf(1.0f + (float)operator_count);
    counts[base + 2] = logf(1.0f + (float)keyword_count);
    counts[base + 3] = logf(1.0f + (float)dot_count);
    counts[base + 4] = logf(1.0f + (float)semicolon_count);
    counts[base + 5] = logf(1.0f + (float)max_indent);
    counts[base + 6] = (keyword_count > 3) ? 1.0f : 0.0f;  /* "is code" signal */
    counts[base + 7] = logf(1.0f + (float)subtokens.count);

    /* Normalize code channel */
    int total_code_tokens = tokens->count + subtokens.count;
    if (total_code_tokens > 0) {
        float norm = sqrtf((float)total_code_tokens);
        for (int i = 0; i < ch_size - 8; i++) {
            counts[i] /= norm;
        }
    }

    for (int i = 0; i < ch_size; i++) {
        out[CH3_START + i] = counts[i];
    }
}

/* ------------------------------------------------------------------ */
/*  Channel 4: Word bigram features (dims 192-223)                    */
/* ------------------------------------------------------------------ */

static void channel_bigrams(const token_list_t *tokens, float *out)
{
    int ch_size = CH4_END - CH4_START;
    float counts[32];
    memset(counts, 0, sizeof(counts));

    char lc_buf1[MAX_TOKEN_LEN];
    char lc_buf2[MAX_TOKEN_LEN];
    char bigram_buf[MAX_TOKEN_LEN * 2 + 1];
    int bigram_count = 0;

    for (int t = 0; t + 1 < tokens->count; t++) {
        const token_t *tok1 = &tokens->tokens[t];
        const token_t *tok2 = &tokens->tokens[t + 1];

        /* Only use alphanumeric tokens for bigrams */
        if (!isalnum((unsigned char)tok1->start[0]) &&
            tok1->start[0] != '_')
            continue;
        if (!isalnum((unsigned char)tok2->start[0]) &&
            tok2->start[0] != '_')
            continue;

        size_t len1 = tok1->len < MAX_TOKEN_LEN ? tok1->len : MAX_TOKEN_LEN - 1;
        size_t len2 = tok2->len < MAX_TOKEN_LEN ? tok2->len : MAX_TOKEN_LEN - 1;

        for (size_t i = 0; i < len1; i++)
            lc_buf1[i] = lower(tok1->start[i]);
        for (size_t i = 0; i < len2; i++)
            lc_buf2[i] = lower(tok2->start[i]);

        /* Concatenate with separator */
        memcpy(bigram_buf, lc_buf1, len1);
        bigram_buf[len1] = ' ';
        memcpy(bigram_buf + len1 + 1, lc_buf2, len2);
        size_t bigram_len = len1 + 1 + len2;

        uint64_t h = fnv1a_seeded(bigram_buf, bigram_len, 0xB16A);
        int dim = (int)(h % (uint64_t)ch_size);
        float sign = (h & 0x100) ? 1.0f : -1.0f;

        counts[dim] += sign;
        bigram_count++;
    }

    /* TF normalization */
    if (bigram_count > 0) {
        float norm = sqrtf((float)bigram_count);
        for (int i = 0; i < ch_size; i++) {
            counts[i] /= norm;
        }
    }

    for (int i = 0; i < ch_size; i++) {
        out[CH4_START + i] = counts[i];
    }
}

/* ------------------------------------------------------------------ */
/*  Channel 5: Global/statistical features (dims 224-255)             */
/* ------------------------------------------------------------------ */

static void channel_global(const char *text, size_t text_len,
                           const token_list_t *tokens, float *out)
{
    int ch_size = CH5_END - CH5_START;
    float features[32];
    memset(features, 0, sizeof(features));

    /* Count various character classes */
    int upper_count = 0, digit_count = 0;
    int punct_count = 0, space_count = 0, newline_count = 0;
    int alpha_count = 0;

    for (size_t i = 0; i < text_len; i++) {
        unsigned char c = (unsigned char)text[i];
        if (isupper(c)) { upper_count++; alpha_count++; }
        else if (islower(c)) { alpha_count++; }
        if (isdigit(c)) digit_count++;
        if (ispunct(c)) punct_count++;
        if (c == ' ' || c == '\t') space_count++;
        if (c == '\n') newline_count++;
    }

    /* Count unique words for vocabulary richness */
    /* Use a small hash set (256 buckets) */
    uint8_t seen[256];
    memset(seen, 0, sizeof(seen));
    int unique_words = 0;
    int total_words = 0;
    long total_word_len = 0;

    char lc_buf[MAX_TOKEN_LEN];

    for (int t = 0; t < tokens->count; t++) {
        const token_t *tok = &tokens->tokens[t];
        if (!isalnum((unsigned char)tok->start[0]) && tok->start[0] != '_')
            continue;

        total_words++;
        total_word_len += (long)tok->len;

        size_t len = tok->len < MAX_TOKEN_LEN ? tok->len : MAX_TOKEN_LEN - 1;
        for (size_t i = 0; i < len; i++)
            lc_buf[i] = lower(tok->start[i]);

        uint64_t h = fnv1a_str(lc_buf, len);
        int bucket = (int)(h % 256);
        if (!seen[bucket]) {
            seen[bucket] = 1;
            unique_words++;
        }
    }

    /* Code-like token ratio */
    int code_tokens = 0;
    for (int t = 0; t < tokens->count; t++) {
        const token_t *tok = &tokens->tokens[t];
        if (tok->len == 1 && (is_bracket(tok->start[0]) ||
                              is_operator_char(tok->start[0]) ||
                              tok->start[0] == ';'))
            code_tokens++;
        else if (isalnum((unsigned char)tok->start[0]) &&
                 is_code_keyword(tok->start, tok->len))
            code_tokens++;
    }

    float len_f = (float)text_len;
    float total_words_f = (float)(total_words > 0 ? total_words : 1);

    /* Feature 0: Text length (log-scaled, normalized) */
    features[0] = logf(1.0f + len_f) / 10.0f;

    /* Feature 1: Vocabulary richness */
    features[1] = (float)unique_words / total_words_f;

    /* Feature 2: Average word length */
    features[2] = (float)total_word_len / (total_words_f * 10.0f);

    /* Feature 3: Punctuation density */
    features[3] = (float)punct_count / (len_f > 0 ? len_f : 1.0f);

    /* Feature 4: Code density */
    features[4] = (float)code_tokens / (float)(tokens->count > 0 ? tokens->count : 1);

    /* Feature 5: Uppercase ratio */
    features[5] = (float)upper_count / (float)(alpha_count > 0 ? alpha_count : 1);

    /* Feature 6: Digit ratio */
    features[6] = (float)digit_count / (len_f > 0 ? len_f : 1.0f);

    /* Feature 7: Line count (log-scaled) */
    features[7] = logf(1.0f + (float)newline_count) / 5.0f;

    /* Feature 8: Space ratio */
    features[8] = (float)space_count / (len_f > 0 ? len_f : 1.0f);

    /* Feature 9: Token density (tokens per character) */
    features[9] = (float)tokens->count / (len_f > 0 ? len_f : 1.0f) * 10.0f;

    /* Feature 10: Average token length */
    {
        float avg = 0;
        for (int t = 0; t < tokens->count; t++)
            avg += (float)tokens->tokens[t].len;
        if (tokens->count > 0)
            avg /= (float)tokens->count;
        features[10] = avg / 20.0f;
    }

    /* Feature 11: Newline-to-length ratio (code tends to have more newlines) */
    features[11] = (float)newline_count / (len_f > 0 ? len_f : 1.0f) * 10.0f;

    /* Feature 12: Max word length (log-scaled) */
    {
        size_t max_wl = 0;
        for (int t = 0; t < tokens->count; t++) {
            if (tokens->tokens[t].len > max_wl)
                max_wl = tokens->tokens[t].len;
        }
        features[12] = logf(1.0f + (float)max_wl) / 5.0f;
    }

    /* Feature 13: Starts-with-uppercase ratio (sentence detection) */
    {
        int starts_upper = 0;
        for (int t = 0; t < tokens->count; t++) {
            if (isupper((unsigned char)tokens->tokens[t].start[0]))
                starts_upper++;
        }
        features[13] = (float)starts_upper / total_words_f;
    }

    /* Features 14-19: Character class distribution hash */
    /* Hash character pairs into remaining dimensions for texture */
    for (size_t i = 0; i + 1 < text_len && i < 2000; i += 2) {
        char pair[2] = { lower(text[i]), lower(text[i + 1]) };
        uint64_t h = fnv1a_seeded(pair, 2, 0x67AB);
        int dim = 14 + (int)(h % (uint64_t)(ch_size - 14));
        float sign = (h & 0x200) ? 0.1f : -0.1f;
        features[dim] += sign;
    }

    /* Normalize the hash-based features (14+) */
    {
        float hash_norm = 0;
        for (int i = 14; i < ch_size; i++)
            hash_norm += features[i] * features[i];
        if (hash_norm > 0) {
            hash_norm = sqrtf(hash_norm);
            for (int i = 14; i < ch_size; i++)
                features[i] /= hash_norm;
        }
    }

    for (int i = 0; i < ch_size; i++) {
        out[CH5_START + i] = features[i];
    }
}

/* ------------------------------------------------------------------ */
/*  L2 normalization                                                  */
/* ------------------------------------------------------------------ */

static void l2_normalize(float *vec, int dims)
{
    float norm_sq = 0.0f;
    for (int i = 0; i < dims; i++) {
        norm_sq += vec[i] * vec[i];
    }

    if (norm_sq < 1e-12f) {
        /* Near-zero vector: produce a small uniform vector */
        float val = 1.0f / sqrtf((float)dims);
        for (int i = 0; i < dims; i++) {
            vec[i] = val;
        }
        return;
    }

    float inv_norm = 1.0f / sqrtf(norm_sq);
    for (int i = 0; i < dims; i++) {
        vec[i] *= inv_norm;
    }
}

/* ------------------------------------------------------------------ */
/*  Public API                                                        */
/* ------------------------------------------------------------------ */

int embed_text(const char *text, size_t text_len, float *out)
{
    if (!text || !out) return -1;

    /* Initialize output to zero */
    memset(out, 0, EMBED_DIM * sizeof(float));

    /* Handle empty text */
    if (text_len == 0) {
        l2_normalize(out, EMBED_DIM);
        return 0;
    }

    /* Tokenize */
    token_list_t tokens;
    tokenize(text, text_len, &tokens);

    /* Compute each channel */
    channel_trigrams(text, text_len, out);
    channel_words(&tokens, out);
    channel_code(text, text_len, &tokens, out);
    channel_bigrams(&tokens, out);
    channel_global(text, text_len, &tokens, out);

    /* Final L2 normalization across all dimensions */
    l2_normalize(out, EMBED_DIM);

    return 0;
}
