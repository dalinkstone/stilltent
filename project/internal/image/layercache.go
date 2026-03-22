// Package image provides image pipeline functionality for OCI/Docker, ISO, and raw disk images.
// This file implements a content-addressable layer cache for the image pipeline.
// Layers are stored by their OCI digest so subsequent pulls of images sharing layers
// can skip re-downloading them. The cache uses a simple on-disk layout:
//
//	<baseDir>/layers/<algo>/<hex>  — the raw (compressed) layer blob
//	<baseDir>/layers/index.json   — metadata index mapping digests to sizes and timestamps
package image

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// LayerCache provides content-addressable caching for OCI image layers.
// Downloaded layers are stored by their digest so they can be reused across
// different image pulls that share layers.
type LayerCache struct {
	dir   string
	mu    sync.Mutex
	index *layerIndex
}

// layerIndex is the on-disk metadata for cached layers.
type layerIndex struct {
	Layers map[string]*cachedLayer `json:"layers"`
}

// cachedLayer tracks metadata for a single cached layer blob.
type cachedLayer struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	CachedAt  string `json:"cached_at"`
	LastUsed  string `json:"last_used"`
	UseCount  int    `json:"use_count"`
	MediaType string `json:"media_type,omitempty"`
}

// NewLayerCache creates or opens a layer cache at the given base directory.
func NewLayerCache(baseDir string) (*LayerCache, error) {
	dir := filepath.Join(baseDir, "layers")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create layer cache directory: %w", err)
	}

	lc := &LayerCache{dir: dir}
	if err := lc.loadIndex(); err != nil {
		// Start with empty index if load fails
		lc.index = &layerIndex{Layers: make(map[string]*cachedLayer)}
	}
	return lc, nil
}

// Has returns true if the layer with the given digest is in the cache.
func (lc *LayerCache) Has(digest string) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	entry, ok := lc.index.Layers[digest]
	if !ok {
		return false
	}

	// Verify the blob still exists on disk
	blobPath := lc.blobPath(digest)
	info, err := os.Stat(blobPath)
	if err != nil || info.Size() != entry.Size {
		// Blob is missing or corrupted, remove from index
		delete(lc.index.Layers, digest)
		_ = lc.saveIndex()
		return false
	}
	return true
}

// Get opens a cached layer for reading and updates usage statistics.
// Returns nil, ErrLayerNotCached if the layer is not in the cache.
func (lc *LayerCache) Get(digest string) (io.ReadCloser, int64, error) {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	entry, ok := lc.index.Layers[digest]
	if !ok {
		return nil, 0, ErrLayerNotCached
	}

	blobPath := lc.blobPath(digest)
	f, err := os.Open(blobPath)
	if err != nil {
		delete(lc.index.Layers, digest)
		_ = lc.saveIndex()
		return nil, 0, ErrLayerNotCached
	}

	// Update usage stats
	entry.LastUsed = time.Now().UTC().Format(time.RFC3339)
	entry.UseCount++
	_ = lc.saveIndex()

	return f, entry.Size, nil
}

// Put stores a layer blob in the cache. The reader is consumed fully,
// the content is verified against the expected digest, and then moved
// into the cache. Returns the number of bytes written.
func (lc *LayerCache) Put(digest string, size int64, mediaType string, r io.Reader) (int64, error) {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	// If already cached and valid, skip
	if entry, ok := lc.index.Layers[digest]; ok {
		blobPath := lc.blobPath(digest)
		if info, err := os.Stat(blobPath); err == nil && info.Size() == entry.Size {
			return entry.Size, nil
		}
	}

	// Write to a temp file first, then rename
	tmpFile, err := os.CreateTemp(lc.dir, "layer-*.tmp")
	if err != nil {
		return 0, fmt.Errorf("failed to create temp file for layer cache: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath) // clean up on error; no-op if renamed
	}()

	// Write and compute digest for verification
	hasher := sha256.New()
	tee := io.TeeReader(r, hasher)
	written, err := io.Copy(tmpFile, tee)
	if err != nil {
		return 0, fmt.Errorf("failed to write layer to cache: %w", err)
	}
	tmpFile.Close()

	// Verify digest if it's a sha256 digest
	if strings.HasPrefix(digest, "sha256:") {
		expected := strings.TrimPrefix(digest, "sha256:")
		actual := hex.EncodeToString(hasher.Sum(nil))
		if actual != expected {
			return 0, fmt.Errorf("layer digest mismatch: expected %s, got %s", expected, actual)
		}
	}

	// Move into place
	blobPath := lc.blobPath(digest)
	if err := os.MkdirAll(filepath.Dir(blobPath), 0755); err != nil {
		return 0, fmt.Errorf("failed to create blob directory: %w", err)
	}
	if err := os.Rename(tmpPath, blobPath); err != nil {
		return 0, fmt.Errorf("failed to move layer into cache: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	lc.index.Layers[digest] = &cachedLayer{
		Digest:    digest,
		Size:      written,
		CachedAt:  now,
		LastUsed:  now,
		UseCount:  1,
		MediaType: mediaType,
	}
	if err := lc.saveIndex(); err != nil {
		return written, fmt.Errorf("layer cached but index save failed: %w", err)
	}

	return written, nil
}

// Remove deletes a layer from the cache.
func (lc *LayerCache) Remove(digest string) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	blobPath := lc.blobPath(digest)
	os.Remove(blobPath)
	delete(lc.index.Layers, digest)
	return lc.saveIndex()
}

// Stats returns cache statistics.
type LayerCacheStats struct {
	TotalLayers   int   `json:"total_layers"`
	TotalSizeBytes int64 `json:"total_size_bytes"`
	TotalHits     int   `json:"total_hits"`
}

func (lc *LayerCache) Stats() LayerCacheStats {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	stats := LayerCacheStats{}
	for _, entry := range lc.index.Layers {
		stats.TotalLayers++
		stats.TotalSizeBytes += entry.Size
		stats.TotalHits += entry.UseCount - 1 // first use is the initial put
	}
	return stats
}

// List returns all cached layer entries sorted by last used time (most recent first).
func (lc *LayerCache) List() []cachedLayer {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	result := make([]cachedLayer, 0, len(lc.index.Layers))
	for _, entry := range lc.index.Layers {
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastUsed > result[j].LastUsed
	})
	return result
}

// Prune removes layers not used since the given cutoff time and returns
// the number of layers removed and bytes freed.
func (lc *LayerCache) Prune(olderThan time.Time) (int, int64, error) {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	var removed int
	var freed int64

	cutoff := olderThan.UTC().Format(time.RFC3339)
	for digest, entry := range lc.index.Layers {
		if entry.LastUsed < cutoff {
			blobPath := lc.blobPath(digest)
			if info, err := os.Stat(blobPath); err == nil {
				freed += info.Size()
			}
			os.Remove(blobPath)
			delete(lc.index.Layers, digest)
			removed++
		}
	}

	if removed > 0 {
		if err := lc.saveIndex(); err != nil {
			return removed, freed, fmt.Errorf("pruned %d layers but index save failed: %w", removed, err)
		}
	}
	return removed, freed, nil
}

// PruneToSize removes least-recently-used layers until the cache is at or below
// the target size in bytes.
func (lc *LayerCache) PruneToSize(maxBytes int64) (int, int64, error) {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	// Calculate current size
	var currentSize int64
	for _, entry := range lc.index.Layers {
		currentSize += entry.Size
	}
	if currentSize <= maxBytes {
		return 0, 0, nil
	}

	// Sort by last used (oldest first) for LRU eviction
	type digestEntry struct {
		digest string
		entry  *cachedLayer
	}
	entries := make([]digestEntry, 0, len(lc.index.Layers))
	for d, e := range lc.index.Layers {
		entries = append(entries, digestEntry{d, e})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].entry.LastUsed < entries[j].entry.LastUsed
	})

	var removed int
	var freed int64
	for _, de := range entries {
		if currentSize <= maxBytes {
			break
		}
		blobPath := lc.blobPath(de.digest)
		if info, err := os.Stat(blobPath); err == nil {
			freed += info.Size()
			currentSize -= info.Size()
		}
		os.Remove(blobPath)
		delete(lc.index.Layers, de.digest)
		removed++
	}

	if removed > 0 {
		if err := lc.saveIndex(); err != nil {
			return removed, freed, err
		}
	}
	return removed, freed, nil
}

// blobPath returns the on-disk path for a layer blob by its digest.
func (lc *LayerCache) blobPath(digest string) string {
	// digest format: "sha256:abcdef..." -> layers/sha256/abcdef...
	parts := strings.SplitN(digest, ":", 2)
	if len(parts) == 2 {
		return filepath.Join(lc.dir, parts[0], parts[1])
	}
	return filepath.Join(lc.dir, "unknown", digest)
}

func (lc *LayerCache) indexPath() string {
	return filepath.Join(lc.dir, "index.json")
}

func (lc *LayerCache) loadIndex() error {
	data, err := os.ReadFile(lc.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			lc.index = &layerIndex{Layers: make(map[string]*cachedLayer)}
			return nil
		}
		return err
	}
	var idx layerIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return err
	}
	if idx.Layers == nil {
		idx.Layers = make(map[string]*cachedLayer)
	}
	lc.index = &idx
	return nil
}

func (lc *LayerCache) saveIndex() error {
	data, err := json.MarshalIndent(lc.index, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := lc.indexPath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, lc.indexPath())
}

// ErrLayerNotCached indicates the requested layer is not in the cache.
var ErrLayerNotCached = fmt.Errorf("layer not found in cache")
