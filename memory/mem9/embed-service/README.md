# embed-service

Local embedding service implementing an OpenAI-compatible `/v1/embeddings` HTTP API. Written in C with zero external dependencies beyond libc/POSIX. Produces 256-dimensional L2-normalized float32 vectors using multi-channel feature hashing.

## Build

```bash
make          # Build with gcc -O3 -march=native
make test     # Build and run smoke tests
make clean    # Remove build artifacts
```

## Run

```bash
# Default port 8090
./embed-service

# Custom port
EMBED_PORT=9000 ./embed-service
```

## Docker

```bash
docker build -t embed-service .
docker run -p 8090:8090 embed-service
```

## API

### POST /v1/embeddings

OpenAI-compatible embedding endpoint.

```bash
curl -X POST http://localhost:8090/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{"model": "local-embed", "input": "text to embed", "encoding_format": "float"}'
```

Response:
```json
{
  "object": "list",
  "data": [{"object": "embedding", "embedding": [0.123, 0.456, ...], "index": 0}],
  "model": "local-embed",
  "usage": {"prompt_tokens": 0, "total_tokens": 0}
}
```

Also available at `POST /embeddings` (without `/v1` prefix).

### GET /health

```bash
curl http://localhost:8090/health
```

Returns `{"status": "ok"}`.

## Embedding Algorithm

Five-channel feature hashing producing 256-dimensional vectors:

| Channel | Dims | Description |
|---------|------|-------------|
| 1 | 0-63 | Character trigram hashing with TF normalization |
| 2 | 64-127 | Word-level features with IDF approximation (word length proxy) |
| 3 | 128-191 | Code-specific features (keywords, operators, structure, camelCase/snake_case) |
| 4 | 192-223 | Word bigram features for local context |
| 5 | 224-255 | Global statistical features (length, richness, density ratios) |

Output vectors are L2-normalized, suitable for cosine distance search (e.g., TiDB `VEC_COSINE_DISTANCE()`).

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `EMBED_PORT` | `8090` | Port to listen on |

## Architecture

- POSIX socket server with 8-thread worker pool
- Lock-free-style work queue with mutex + condition variable
- Graceful shutdown on SIGINT/SIGTERM
- CORS headers for browser compatibility
- TCP_NODELAY for low latency
- 10-second socket timeouts to prevent worker starvation
