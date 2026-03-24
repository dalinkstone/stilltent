/*
 * embedder.h - Core embedding algorithm interface
 *
 * Produces 256-dimensional float32 vectors using multi-channel
 * feature hashing. Thread-safe, deterministic, zero external dependencies.
 */

#ifndef EMBEDDER_H
#define EMBEDDER_H

#include <stddef.h>
#include <stdint.h>

/* Total embedding dimensions */
#define EMBED_DIM 256

/* Channel layout */
#define CH1_START   0
#define CH1_END    64   /* Character trigram hashing: dims 0-63 */

#define CH2_START  64
#define CH2_END   128   /* Word-level features: dims 64-127 */

#define CH3_START 128
#define CH3_END   192   /* Code-specific features: dims 128-191 */

#define CH4_START 192
#define CH4_END   224   /* Bigram features: dims 192-223 */

#define CH5_START 224
#define CH5_END   256   /* Global/statistical features: dims 224-255 */

/*
 * embed_text - Compute a 256-dim L2-normalized embedding for the given text.
 *
 * Parameters:
 *   text     - Input text (UTF-8, null-terminated)
 *   text_len - Length of text in bytes (excluding null terminator)
 *   out      - Output buffer, must have room for EMBED_DIM floats
 *
 * Returns 0 on success, -1 on error (null pointers, etc.).
 * The function is thread-safe and deterministic.
 */
int embed_text(const char *text, size_t text_len, float *out);

#endif /* EMBEDDER_H */
