package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// reqBufPool reuses buffers for JSON-encoded embedding request bodies,
// avoiding a heap allocation per Embed call.
var reqBufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// Embedder generates vector embeddings from text.
type Embedder struct {
	apiKey  string
	baseURL string
	model   string
	dims    int
	client  *http.Client
}

// Config holds embedding provider configuration.
type Config struct {
	APIKey  string // OpenAI key; "local" or empty for Ollama
	BaseURL string // Override for Ollama/LM Studio (e.g., http://localhost:11434/v1)
	Model   string // Model name (default: text-embedding-3-small)
	Dims    int    // Vector dimensions (default: 256)
}

const (
	defaultModel   = "local-embed"
	defaultDims    = 256
	defaultBaseURL = "http://embed-service:8090/v1"
)

// New creates an Embedder from config. Returns nil if not configured
// (no API key and no base URL).
func New(cfg Config) *Embedder {
	if cfg.APIKey == "" && cfg.BaseURL == "" {
		return nil
	}
	model := cfg.Model
	if model == "" {
		model = defaultModel
	}
	dims := cfg.Dims
	if dims <= 0 {
		dims = defaultDims
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = "local"
	}
	return &Embedder{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		dims:    dims,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
				DisableKeepAlives:   false,
			},
		},
	}
}

// Dims returns the configured vector dimensions.
func (e *Embedder) Dims() int {
	return e.dims
}

// embeddingRequest is the OpenAI-compatible request body (single input).
type embeddingRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	EncodingFormat string `json:"encoding_format"`
}

// batchEmbeddingRequest is the OpenAI-compatible request body for batch input.
type batchEmbeddingRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed generates a vector embedding for the given text.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	reqBody := embeddingRequest{
		Model:          e.model,
		Input:          text,
		EncodingFormat: "float", // Required for Ollama/LM Studio; safe for OpenAI too.
	}

	// Reuse a pooled buffer to avoid per-call allocations.
	buf := reqBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer reqBufPool.Put(buf)

	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", e.baseURL+"/embeddings", buf)
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding API call: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("embedding API returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Read full body then close immediately to release the connection back
	// to the pool before unmarshaling.
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read embedding response: %w", err)
	}

	var result embeddingResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}
	if len(result.Data[0].Embedding) != e.dims {
		return nil, fmt.Errorf("embedding dimension mismatch: got %d, expected %d", len(result.Data[0].Embedding), e.dims)
	}
	return result.Data[0].Embedding, nil
}

// EmbedBatch generates vector embeddings for multiple texts in a single HTTP
// request. This eliminates per-item round-trip overhead that dominates latency
// when embedding many short texts (e.g., BulkCreate). The OpenAI-compatible
// embeddings API accepts an array of strings as input and returns one embedding
// per input in the same order.
//
// Returns a slice of embeddings parallel to the input texts. If the batch
// request fails, falls back to concurrent single-item Embed calls with a
// semaphore to bound parallelism.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) == 1 {
		emb, err := e.Embed(ctx, texts[0])
		if err != nil {
			return nil, err
		}
		return [][]float32{emb}, nil
	}

	// Try batch request first.
	embeddings, err := e.doBatchRequest(ctx, texts)
	if err == nil {
		return embeddings, nil
	}

	// Batch request failed (endpoint may not support array input).
	// Fall back to concurrent single-item requests with bounded parallelism.
	return e.embedConcurrent(ctx, texts)
}

// doBatchRequest sends a single HTTP request with multiple input texts.
func (e *Embedder) doBatchRequest(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := batchEmbeddingRequest{
		Model:          e.model,
		Input:          texts,
		EncodingFormat: "float",
	}

	buf := reqBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer reqBufPool.Put(buf)

	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		return nil, fmt.Errorf("marshal batch embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", e.baseURL+"/embeddings", buf)
	if err != nil {
		return nil, fmt.Errorf("create batch embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("batch embedding API call: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("batch embedding API returned %d: %s", resp.StatusCode, string(respBody))
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read batch embedding response: %w", err)
	}

	var result embeddingResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode batch embedding response: %w", err)
	}
	if len(result.Data) != len(texts) {
		return nil, fmt.Errorf("batch embedding count mismatch: got %d, expected %d", len(result.Data), len(texts))
	}

	embeddings := make([][]float32, len(texts))
	for i, d := range result.Data {
		if len(d.Embedding) != e.dims {
			return nil, fmt.Errorf("batch embedding dimension mismatch at index %d: got %d, expected %d", i, len(d.Embedding), e.dims)
		}
		embeddings[i] = d.Embedding
	}
	return embeddings, nil
}

// embedConcurrent embeds texts concurrently with bounded parallelism.
// Used as a fallback when the batch endpoint is not available.
func (e *Embedder) embedConcurrent(ctx context.Context, texts []string) ([][]float32, error) {
	const maxConcurrency = 4

	embeddings := make([][]float32, len(texts))
	errs := make([]error, len(texts))

	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for i := range texts {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			emb, err := e.Embed(ctx, texts[idx])
			if err != nil {
				errs[idx] = err
				return
			}
			embeddings[idx] = emb
		}(i)
	}
	wg.Wait()

	// Return the first error encountered.
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return embeddings, nil
}
