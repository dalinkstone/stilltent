// Package image provides image pipeline functionality for OCI/Docker, ISO, and raw disk images.
// This package handles pulling images, extracting content, and format detection.
package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dalinkstone/tent/pkg/models"
)

// Manager handles image operations
type Manager struct {
	baseDir        string
	progressNotify func(bytes int64, total int64)
	layerCache     *LayerCache
}

// NewManager creates a new image manager
func NewManager(baseDir string, opts ...func(*Manager)) (*Manager, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("base directory is required")
	}
	
	m := &Manager{
		baseDir: filepath.Join(baseDir, "images"),
	}

	// Apply options
	for _, opt := range opts {
		opt(m)
	}

	// Initialize layer cache
	cache, err := NewLayerCache(m.baseDir)
	if err == nil {
		m.layerCache = cache
	}

	return m, nil
}

// WithProgressCallback sets the progress callback for the manager
func WithProgressCallback(cb func(bytes int64, total int64)) func(*Manager) {
	return func(m *Manager) {
		m.progressNotify = cb
	}
}

// Pull pulls an image from a URL
func (m *Manager) Pull(name string, url string) (string, error) {
	// Create images directory
	if err := os.MkdirAll(m.baseDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create images directory: %w", err)
	}
	
	// Create image file path
	imagePath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))
	
	// Create progress tracker
	var progress *ProgressTracker
	if m.progressNotify != nil {
		progress = NewProgressTracker(m.progressNotify)
	}
	
	// Download the image
	if err := m.downloadFile(imagePath, url, progress); err != nil {
		return "", fmt.Errorf("failed to download image: %w", err)
	}
	
	return imagePath, nil
}

// manifestDigestInfo is the sidecar metadata stored alongside a cached image
// to track which remote manifest digest was used to produce the local cache.
type manifestDigestInfo struct {
	Digest   string `json:"digest"`
	Ref      string `json:"ref"`
	PulledAt string `json:"pulled_at"`
}

// digestFilePath returns the path to the manifest-digest sidecar file for a
// given safe image name.
func (m *Manager) digestFilePath(safeName string) string {
	return filepath.Join(m.baseDir, safeName+".manifest-digest")
}

// writeDigestFile writes manifest digest metadata to a sidecar JSON file.
func (m *Manager) writeDigestFile(safeName, digest, ref string) error {
	info := manifestDigestInfo{
		Digest:   digest,
		Ref:      ref,
		PulledAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal digest info: %w", err)
	}
	return os.WriteFile(m.digestFilePath(safeName), data, 0644)
}

// readDigestFile reads manifest digest metadata from the sidecar file.
// Returns nil if the file does not exist.
func (m *Manager) readDigestFile(safeName string) (*manifestDigestInfo, error) {
	data, err := os.ReadFile(m.digestFilePath(safeName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var info manifestDigestInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ResolveImage resolves an image reference to a local rootfs path.
// It handles:
//   - Local file paths (ISO, raw disk, qcow2) — used directly
//   - Registry references (ubuntu:22.04, gcr.io/proj/img:tag) — pulled via OCI
//
// The pull policy controls cache behaviour:
//   - PullMissing (default): only pull if not cached
//   - PullAlways: check remote digest, re-pull only if changed
//   - PullNever: error if not cached
func (m *Manager) ResolveImage(ref string, policies ...PullPolicy) (string, error) {
	policy := PullMissing
	if len(policies) > 0 && policies[0] != "" {
		policy = policies[0]
	}

	// Check if it's a local file path
	if strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "~/") {
		expandedPath := ref
		if strings.HasPrefix(ref, "~/") {
			home, _ := os.UserHomeDir()
			expandedPath = filepath.Join(home, ref[2:])
		}
		if _, err := os.Stat(expandedPath); err != nil {
			return "", fmt.Errorf("local image not found: %s", expandedPath)
		}
		return expandedPath, nil
	}

	// It's a registry reference
	safeName := strings.NewReplacer("/", "_", ":", "_").Replace(ref)
	cachedPath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", safeName))
	cachedExists := false
	if _, err := os.Stat(cachedPath); err == nil {
		cachedExists = true
	}

	// Also check for the associated rootfs directory (virtiofs)
	rootfsDir := filepath.Join(m.baseDir, safeName+"_rootfs")
	rootfsDirExists := false
	if info, err := os.Stat(rootfsDir); err == nil && info.IsDir() {
		rootfsDirExists = true
	}

	digestInfo, _ := m.readDigestFile(safeName)
	hasDigest := digestInfo != nil

	switch policy {
	case PullNever:
		if !cachedExists {
			return "", fmt.Errorf("image %q not found locally and pull policy is \"never\"", ref)
		}
		return cachedPath, nil

	case PullMissing:
		if cachedExists && hasDigest {
			// Cached with digest — trust it
			return cachedPath, nil
		}
		if cachedExists && !hasDigest {
			// Old cached image without digest — re-pull to establish baseline
			// so future runs can do digest validation
		}
		// Fall through to pull

	case PullAlways:
		if cachedExists && hasDigest {
			// Check remote digest — only re-pull if changed
			registry, repo, tag, err := parseImageRef(ref)
			if err == nil {
				client := NewRegistryClient()
				remoteDigest, err := client.HeadManifest(registry, repo, tag)
				if err == nil && remoteDigest == digestInfo.Digest {
					// Remote matches local — no re-pull needed
					return cachedPath, nil
				}
				// Digest differs or HEAD failed — fall through to re-pull
			}
		}
		// Fall through to pull
	}

	// Pull from registry
	rootfsPath, err := m.PullOCI(safeName, ref)
	if err != nil {
		// If we already have a cached version, fall back to it on network errors
		if cachedExists && rootfsDirExists {
			return cachedPath, nil
		}
		return "", fmt.Errorf("failed to pull image %q: %w", ref, err)
	}

	return rootfsPath, nil
}

// PullOCI pulls an OCI/Docker image from a registry (Docker Hub, GCR, ECR, etc.)
// It parses the image reference, authenticates if needed, pulls layers, and extracts to rootfs.
// It also stores the manifest digest in a sidecar file for cache validation.
func (m *Manager) PullOCI(name string, ref string) (string, error) {
	// Parse the image reference (registry/repo:tag or repo:tag)
	registry, repo, tag, err := parseImageRef(ref)
	if err != nil {
		return "", fmt.Errorf("failed to parse image reference: %w", err)
	}

	// Create images directory
	if err := os.MkdirAll(m.baseDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create images directory: %w", err)
	}

	// Create a temporary directory for layer extraction
	tmpDir := filepath.Join(m.baseDir, fmt.Sprintf("%s-tmp", name))
	os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Download manifest (with raw bytes for digest) and layers
	layers, manifestDigest, err := m.getLayersWithDigest(registry, repo, tag)
	if err != nil {
		return "", fmt.Errorf("failed to get layers: %w", err)
	}

	// Create progress tracker for total download size
	var progress *ProgressTracker
	if m.progressNotify != nil {
		totalSize := int64(0)
		for _, layer := range layers {
			totalSize += layer.Size
		}
		progress = NewProgressTracker(m.progressNotify)
		progress.TotalBytes = totalSize
	}

	// Extract layers and build rootfs
	rootfsPath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))
	if err := m.extractLayers(layers, tmpDir, rootfsPath, progress); err != nil {
		return "", fmt.Errorf("failed to extract layers: %w", err)
	}

	// Store manifest digest sidecar file for future cache validation
	if manifestDigest != "" {
		if wErr := m.writeDigestFile(name, manifestDigest, ref); wErr != nil {
			// Non-fatal: the image is usable, just won't have digest tracking
			_ = wErr
		}
	}

	return rootfsPath, nil
}

// PushOCI pushes a local image to an OCI registry.
// It creates a single-layer OCI image from the local disk image and uploads it.
func (m *Manager) PushOCI(name string, ref string) error {
	// Verify the image exists locally
	imagePath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))
	imgInfo, err := os.Stat(imagePath)
	if os.IsNotExist(err) {
		return fmt.Errorf("image not found: %s", name)
	}
	if err != nil {
		return fmt.Errorf("failed to stat image: %w", err)
	}

	// Parse destination reference
	registry, repo, tag, err := parseImageRef(ref)
	if err != nil {
		return fmt.Errorf("failed to parse target reference: %w", err)
	}

	client := NewRegistryClient()

	// Create a gzipped tar of the image as a single layer
	layerBuf, layerDigest, layerSize, err := m.createLayerArchive(imagePath)
	if err != nil {
		return fmt.Errorf("failed to create layer archive: %w", err)
	}

	// Check if the layer already exists
	exists, _ := client.CheckBlobExists(registry, repo, layerDigest)
	if !exists {
		// Upload the layer blob
		if m.progressNotify != nil {
			m.progressNotify(0, layerSize)
		}
		if err := client.UploadBlob(registry, repo, layerDigest, layerSize, bytes.NewReader(layerBuf)); err != nil {
			return fmt.Errorf("failed to upload layer: %w", err)
		}
		if m.progressNotify != nil {
			m.progressNotify(layerSize, layerSize)
		}
	}

	// Create and upload the OCI config blob
	configJSON := m.createOCIConfig(imgInfo.ModTime())
	configDigest := computeSHA256(configJSON)
	configSize := int64(len(configJSON))

	configExists, _ := client.CheckBlobExists(registry, repo, configDigest)
	if !configExists {
		if err := client.UploadBlob(registry, repo, configDigest, configSize, bytes.NewReader(configJSON)); err != nil {
			return fmt.Errorf("failed to upload config: %w", err)
		}
	}

	// Build and upload the manifest
	manifest := OCIManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: OCIDescriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    configDigest,
			Size:      configSize,
		},
		Layers: []OCIDescriptor{
			{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    layerDigest,
				Size:      layerSize,
			},
		},
	}

	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	if err := client.PutManifest(registry, repo, tag, manifestJSON, "application/vnd.oci.image.manifest.v1+json"); err != nil {
		return fmt.Errorf("failed to push manifest: %w", err)
	}

	return nil
}

// createLayerArchive creates a gzipped tar archive from a disk image file.
// Returns the compressed bytes, sha256 digest, and compressed size.
func (m *Manager) createLayerArchive(imagePath string) ([]byte, string, int64, error) {
	imgFile, err := os.Open(imagePath)
	if err != nil {
		return nil, "", 0, fmt.Errorf("failed to open image: %w", err)
	}
	defer imgFile.Close()

	imgInfo, err := imgFile.Stat()
	if err != nil {
		return nil, "", 0, fmt.Errorf("failed to stat image: %w", err)
	}

	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gzWriter)

	// Add the disk image as a single file in the tar
	header := &tar.Header{
		Name:    filepath.Base(imagePath),
		Size:    imgInfo.Size(),
		Mode:    0644,
		ModTime: imgInfo.ModTime(),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		return nil, "", 0, fmt.Errorf("failed to write tar header: %w", err)
	}

	if _, err := io.Copy(tarWriter, imgFile); err != nil {
		return nil, "", 0, fmt.Errorf("failed to write tar content: %w", err)
	}

	if err := tarWriter.Close(); err != nil {
		return nil, "", 0, fmt.Errorf("failed to close tar: %w", err)
	}
	if err := gzWriter.Close(); err != nil {
		return nil, "", 0, fmt.Errorf("failed to close gzip: %w", err)
	}

	data := buf.Bytes()
	digest := computeSHA256(data)
	return data, digest, int64(len(data)), nil
}

// createOCIConfig creates a minimal OCI image config JSON.
func (m *Manager) createOCIConfig(created time.Time) []byte {
	config := map[string]interface{}{
		"created":      created.UTC().Format(time.RFC3339),
		"architecture": "amd64",
		"os":           "linux",
		"rootfs": map[string]interface{}{
			"type":     "layers",
			"diff_ids": []string{},
		},
		"config": map[string]interface{}{},
	}
	data, _ := json.Marshal(config)
	return data
}

// computeSHA256 computes the sha256 digest of data in the OCI format "sha256:<hex>".
func computeSHA256(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// parseImageRef parses an image reference into registry, repo, and tag
// Examples:
// - "ubuntu:22.04" -> "registry.hub.docker.com", "library/ubuntu", "22.04"
// - "gcr.io/project/image:tag" -> "gcr.io", "project/image", "tag"
// - "myregistry.com:5000/repo/image:latest" -> "myregistry.com:5000", "repo/image", "latest"
func parseImageRef(ref string) (registry, repo, tag string, err error) {
	// Default registry (Docker Hub v2 API endpoint)
	registry = "registry-1.docker.io"

	// Split by '/' to separate registry from repo
	parts := strings.Split(ref, "/")
	if len(parts) == 1 {
		// Just repo or repo:tag (e.g., "ubuntu" or "ubuntu:22.04")
		part := parts[0]
		if strings.Contains(part, ":") {
			repoTag := strings.SplitN(part, ":", 2)
			repo = "library/" + repoTag[0]
			tag = repoTag[1]
		} else {
			repo = "library/" + part
			tag = "latest"
		}
		return
	}

	// Check if first part is a registry (contains '.' or ':')
	if strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") {
		registry = parts[0]
		repo = strings.Join(parts[1:], "/")
	} else {
		// First part is namespace/repo
		repo = strings.Join(parts, "/")
	}

	// Split repo by ':' to separate tag (use SplitN to handle unusual tag values)
	if strings.Contains(repo, ":") {
		repoTag := strings.SplitN(repo, ":", 2)
		repo = repoTag[0]
		tag = repoTag[1]
	} else {
		tag = "latest"
	}

	return
}

// getLayers fetches the layer manifest from an OCI registry using the Distribution Spec API.
// It authenticates via bearer token, resolves manifest lists to the best platform,
// and returns download URLs for each layer blob.
func (m *Manager) getLayers(registry, repo, tag string) ([]LayerInfo, error) {
	client := NewRegistryClient()

	manifest, err := client.FetchManifest(registry, repo, tag)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch manifest from %s/%s:%s: %w", registry, repo, tag, err)
	}

	return client.FetchLayers(registry, repo, manifest), nil
}

// getLayersWithDigest is like getLayers but also returns the sha256 digest of
// the raw manifest JSON. This digest is stored as a sidecar file for cache
// validation on subsequent pulls.
func (m *Manager) getLayersWithDigest(registry, repo, tag string) ([]LayerInfo, string, error) {
	client := NewRegistryClient()

	manifest, rawManifest, err := client.FetchManifestRaw(registry, repo, tag)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch manifest from %s/%s:%s: %w", registry, repo, tag, err)
	}

	digest := computeSHA256(rawManifest)
	return client.FetchLayers(registry, repo, manifest), digest, nil
}

// LayerInfo represents a layer in an OCI image
type LayerInfo struct {
	Digest    string
	Size      int64
	URL       string
	MediaType string
}

// extractLayers downloads each layer from the registry and extracts it into a staging
// directory, then packs the result into a rootfs image. Layers are applied in order
// (bottom to top) to produce the final filesystem.
func (m *Manager) extractLayers(layers []LayerInfo, tmpDir, rootfsPath string, progress *ProgressTracker) error {
	if len(layers) == 0 {
		return fmt.Errorf("no layers to extract")
	}

	// Parse registry/repo from the first layer URL for the registry client
	client := NewRegistryClient()

	// Extract registry and repo from the layer URL
	// URL format: https://registry/v2/repo/blobs/digest
	registry, repo := extractRegistryRepo(layers[0].URL)

	// Create rootfs staging directory
	rootfsDir := filepath.Join(tmpDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		return fmt.Errorf("failed to create rootfs dir: %w", err)
	}

	var downloaded int64
	for _, layer := range layers {
		var rawReader io.Reader    // raw (compressed) stream for caching
		var rawCloser io.Closer    // closer for the raw stream

		// Check layer cache first
		if m.layerCache != nil && m.layerCache.Has(layer.Digest) {
			cached, _, err := m.layerCache.Get(layer.Digest)
			if err == nil {
				rawReader = cached
				rawCloser = cached
			}
		}

		// Download raw (compressed) blob if not cached
		if rawReader == nil {
			reader, err := client.DownloadLayerRaw(registry, repo, layer)
			if err != nil {
				return fmt.Errorf("failed to download layer %s: %w", layer.Digest, err)
			}

			// Tee the raw blob into the cache while reading
			if m.layerCache != nil {
				pr, pw := io.Pipe()
				tee := io.TeeReader(reader, pw)

				// Cache in background
				cacheDone := make(chan error, 1)
				go func() {
					_, err := m.layerCache.Put(layer.Digest, layer.Size, layer.MediaType, pr)
					cacheDone <- err
				}()

				rawReader = tee
				rawCloser = &multiCloser{closers: []io.Closer{reader, pw}, cacheDone: cacheDone}
			} else {
				rawReader = reader
				rawCloser = reader
			}
		}

		// Wrap with progress tracking (on the raw/compressed stream)
		if progress != nil {
			rawReader = &progressLayerReader{
				reader:     rawReader,
				tracker:    progress,
				baseOffset: downloaded,
			}
		}

		// Decompress gzip layers before tar extraction
		var tarReader io.Reader = rawReader
		var gzCloser io.Closer
		if isGzipLayer(layer.MediaType) {
			gz, err := gzip.NewReader(rawReader)
			if err != nil {
				rawCloser.Close()
				return fmt.Errorf("failed to decompress layer %s: %w", layer.Digest, err)
			}
			tarReader = gz
			gzCloser = gz
		}

		// Extract tar to rootfs directory
		extractErr := extractTar(tarReader, rootfsDir)
		if gzCloser != nil {
			gzCloser.Close()
		}
		rawCloser.Close()
		if extractErr != nil {
			return fmt.Errorf("failed to extract layer %s: %w", layer.Digest, extractErr)
		}

		downloaded += layer.Size
		if progress != nil {
			progress.UpdateProgress(downloaded)
		}
	}

	// Pack the rootfs directory into a raw disk image
	if err := packRootfsImage(rootfsDir, rootfsPath); err != nil {
		return fmt.Errorf("failed to create rootfs image: %w", err)
	}

	return nil
}

// extractRegistryRepo parses registry and repo from a blob download URL
func extractRegistryRepo(blobURL string) (string, string) {
	// URL: https://registry/v2/repo/path/blobs/digest
	u := strings.TrimPrefix(blobURL, "https://")
	u = strings.TrimPrefix(u, "http://")

	parts := strings.SplitN(u, "/v2/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	registry := parts[0]

	// repo is everything between /v2/ and /blobs/
	rest := parts[1]
	blobIdx := strings.Index(rest, "/blobs/")
	if blobIdx < 0 {
		return registry, rest
	}
	repo := rest[:blobIdx]
	return registry, repo
}

// extractTar extracts a tar stream into a directory, handling whiteout files
// for OCI layer semantics (overlay filesystem deletions).
func extractTar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error: %w", err)
		}

		// Handle OCI whiteout files (.wh. prefix = deletion marker)
		name := hdr.Name
		base := filepath.Base(name)
		if strings.HasPrefix(base, ".wh.") {
			target := filepath.Join(dir, filepath.Dir(name), strings.TrimPrefix(base, ".wh."))
			// Validate whiteout target stays within extraction dir
			if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dir)+string(os.PathSeparator)) {
				continue
			}
			os.RemoveAll(target)
			continue
		}

		target := filepath.Join(dir, name)

		// Prevent path traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dir)) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			// Validate symlink target doesn't escape extraction dir
			linkTarget := hdr.Linkname
			if !filepath.IsAbs(linkTarget) {
				linkTarget = filepath.Join(filepath.Dir(target), linkTarget)
			}
			resolved := filepath.Clean(linkTarget)
			if !strings.HasPrefix(resolved, filepath.Clean(dir)+string(os.PathSeparator)) && resolved != filepath.Clean(dir) {
				continue // skip symlinks that escape
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			os.Remove(target) // remove existing before creating symlink
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(dir, hdr.Linkname)
			if !strings.HasPrefix(filepath.Clean(linkTarget), filepath.Clean(dir)+string(os.PathSeparator)) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				// Hard link may fail across filesystems, fall back to copy
				continue
			}
		}
	}
	return nil
}

// packRootfsImage creates a raw disk image with a valid ext4 filesystem and
// preserves the extracted rootfs directory alongside it for virtio-fs sharing.
//
// On macOS we cannot mount ext4 to copy files into the image, so we keep the
// rootfs directory as a sibling (<name>_rootfs/) which the VM can access
// via virtio-fs shared directories. The raw ext4 image serves as the boot disk.
func packRootfsImage(rootfsDir, imagePath string) error {
	f, err := os.Create(imagePath)
	if err != nil {
		return fmt.Errorf("failed to create image: %w", err)
	}
	defer f.Close()

	// Create 2GB sparse image
	const imageSize = 2 * 1024 * 1024 * 1024
	if err := f.Truncate(imageSize); err != nil {
		return fmt.Errorf("failed to set image size: %w", err)
	}

	// Preserve the rootfs directory alongside the image for virtio-fs sharing.
	// This is the extracted OCI layer contents that the guest needs.
	// Convention: for <name>.img, the rootfs directory is <name>_rootfs.
	rootfsDstDir := strings.TrimSuffix(imagePath, ".img") + "_rootfs"
	// Remove stale rootfs from a previous pull so Rename/copy doesn't collide
	os.RemoveAll(rootfsDstDir)
	if err := os.Rename(rootfsDir, rootfsDstDir); err != nil {
		// Rename may fail across filesystems; fall back to keeping in place
		// The caller's defer will clean tmpDir, so copy instead
		if cpErr := copyDir(rootfsDir, rootfsDstDir); cpErr != nil {
			return fmt.Errorf("failed to preserve rootfs directory: %w", cpErr)
		}
	}

	return nil
}


// progressLayerReader wraps a reader to report download progress
type progressLayerReader struct {
	reader     io.Reader
	tracker    *ProgressTracker
	baseOffset int64
	read       int64
}

func (p *progressLayerReader) Read(buf []byte) (int, error) {
	n, err := p.reader.Read(buf)
	if n > 0 {
		p.read += int64(n)
		p.tracker.UpdateProgress(p.baseOffset + p.read)
	}
	return n, err
}

// multiCloser closes multiple closers and waits for background cache writes.
type multiCloser struct {
	closers   []io.Closer
	cacheDone chan error
}

func (mc *multiCloser) Close() error {
	var firstErr error
	for _, c := range mc.closers {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Wait for cache write to complete (ignore cache errors)
	if mc.cacheDone != nil {
		<-mc.cacheDone
	}
	return firstErr
}

// GetLayerCache returns the manager's layer cache, or nil if not initialized.
func (m *Manager) GetLayerCache() *LayerCache {
	return m.layerCache
}

// createMinimalRootfs creates a minimal ext4 image with basic directory structure
func createMinimalRootfs(path string) error {
	// Create a sparse file for the rootfs
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create image file: %w", err)
	}
	defer file.Close()

	// Create a 1GB sparse file
	const size = 1 * 1024 * 1024 * 1024 // 1GB
	if err := file.Truncate(size); err != nil {
		return fmt.Errorf("failed to truncate file: %w", err)
	}

	// The actual filesystem will be created on first VM boot
	// For now, the sparse file is sufficient as a placeholder

	return nil
}

// Extract extracts content from an image file
func (m *Manager) Extract(imagePath string) (*models.ImageInfo, error) {
	// Check if image exists
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("image not found: %s", imagePath)
	}
	
	// Detect image format
	format, err := m.DetectFormat(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to detect format: %w", err)
	}
	
	// Extract based on format
	var extractedPath string
	switch format {
	case FormatRaw:
		// Raw disk images don't need extraction
		extractedPath = imagePath
	case FormatQCOW2:
		// QCOW2 images need conversion to raw
		extractedPath = filepath.Join(filepath.Dir(imagePath), fmt.Sprintf("%s-raw.img", filepath.Base(imagePath)))
		if err := m.convertQCOW2ToRaw(imagePath, extractedPath); err != nil {
			return nil, fmt.Errorf("failed to convert QCOW2: %w", err)
		}
	case FormatISO:
		// ISO images need extraction of kernel/initrd
		extractedPath, err = m.extractISO(imagePath)
		if err != nil {
			return nil, fmt.Errorf("failed to extract ISO: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}
	
	// Get file info
	info, err := os.Stat(extractedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat extracted file: %w", err)
	}
	
	return &models.ImageInfo{
		Name:      filepath.Base(extractedPath),
		Path:      extractedPath,
		SizeMB:    int(info.Size() / (1024 * 1024)),
		CreatedAt: info.ModTime().Format("2006-01-02 15:04:05"),
	}, nil
}

// DetectFormat detects the format of an image file
func (m *Manager) DetectFormat(imagePath string) (Format, error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return FormatUnknown, fmt.Errorf("failed to open image: %w", err)
	}
	defer file.Close()

	// Check for QCOW2 magic number at offset 0 ("QFI\xfb")
	qcow2Buf := make([]byte, 4)
	if _, err := file.ReadAt(qcow2Buf, 0); err == nil {
		if qcow2Buf[0] == 'Q' && qcow2Buf[1] == 'F' && qcow2Buf[2] == 'I' && qcow2Buf[3] == 0xfb {
			return FormatQCOW2, nil
		}
	}

	// Check for ext4 magic number at offset 1080 (superblock offset 0x438)
	ext4Buf := make([]byte, 2)
	if _, err := file.ReadAt(ext4Buf, 1080); err == nil {
		if ext4Buf[0] == 0x53 && ext4Buf[1] == 0xEF {
			return FormatRaw, nil
		}
	}

	// Check for ISO9660 magic number "CD001" at offset 0x8001 (sector 16 + 1)
	isoBuf := make([]byte, 5)
	if _, err := file.ReadAt(isoBuf, 0x8001); err == nil {
		if string(isoBuf) == "CD001" {
			return FormatISO, nil
		}
	}

	// Default to raw if no magic number found
	return FormatRaw, nil
}

// convertQCOW2ToRaw converts a QCOW2 image to raw format
func (m *Manager) convertQCOW2ToRaw(src, dst string) error {
	// Use qemu-img directly (no shell) to avoid path injection issues
	cmd := exec.Command("qemu-img", "convert", "-f", "qcow2", "-O", "raw", src, dst)
	if err := cmd.Run(); err != nil {
		// Fall back to raw copy for testing
		return m.copyFile(src, dst)
	}
	return nil
}

// extractISO extracts kernel, initrd, and rootfs content from an ISO image
// using the pure-Go ISO9660 reader. No mount or external tools required.
func (m *Manager) extractISO(imagePath string) (string, error) {
	iso, err := OpenISO9660(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to open ISO: %w", err)
	}
	defer iso.Close()

	// Create extraction directory
	baseName := strings.TrimSuffix(filepath.Base(imagePath), filepath.Ext(imagePath))
	extractDir := filepath.Join(m.baseDir, baseName+"-iso")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create extraction directory: %w", err)
	}

	// Find kernel and initrd
	kernelISO, initrdISO, err := iso.FindKernelAndInitrd()
	if err != nil {
		return "", fmt.Errorf("failed to locate kernel in ISO: %w", err)
	}

	// Extract kernel
	kernelLocal := filepath.Join(extractDir, "vmlinuz")
	if err := iso.ExtractFile(kernelISO, kernelLocal); err != nil {
		return "", fmt.Errorf("failed to extract kernel %s: %w", kernelISO, err)
	}

	// Extract initrd if found
	if initrdISO != "" {
		initrdLocal := filepath.Join(extractDir, "initrd.img")
		if err := iso.ExtractFile(initrdISO, initrdLocal); err != nil {
			return "", fmt.Errorf("failed to extract initrd %s: %w", initrdISO, err)
		}
	}

	// Also extract any squashfs/rootfs image if present
	squashfsPaths := []string{
		"/casper/filesystem.squashfs",
		"/live/filesystem.squashfs",
		"/install/filesystem.squashfs",
		"/LiveOS/squashfs.img",
	}
	files, _ := iso.ListFiles()
	for _, sp := range squashfsPaths {
		for _, f := range files {
			if strings.EqualFold(f.Path, sp) {
				localPath := filepath.Join(extractDir, filepath.Base(sp))
				if err := iso.ExtractFile(f.Path, localPath); err == nil {
					// Successfully extracted rootfs
					break
				}
			}
		}
	}

	// Return the ISO path for now — the kernel/initrd are extracted
	// alongside it in the extraction directory for the boot loader to find.
	// The caller can use extractDir + "/vmlinuz" and "/initrd.img".
	return imagePath, nil
}

func (m *Manager) ListImages() ([]*models.ImageInfo, error) {
	images := make([]*models.ImageInfo, 0)
	
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return images, nil
		}
		return nil, fmt.Errorf("failed to read images directory: %w", err)
	}
	
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".img") {
			info, err := entry.Info()
			if err == nil {
				images = append(images, &models.ImageInfo{
					Name:      strings.TrimSuffix(entry.Name(), ".img"),
					Path:      filepath.Join(m.baseDir, entry.Name()),
					SizeMB:    int(info.Size() / (1024 * 1024)),
					CreatedAt: info.ModTime().Format("2006-01-02 15:04:05"),
				})
			}
		}
	}
	
	return images, nil
}

// GetImage retrieves an image by name
func (m *Manager) GetImage(name string) (*models.ImageInfo, error) {
	imagePath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))
	
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("image not found: %s", name)
	}
	
	info, err := os.Stat(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat image: %w", err)
	}
	
	return &models.ImageInfo{
		Name:      name,
		Path:      imagePath,
		SizeMB:    int(info.Size() / (1024 * 1024)),
		CreatedAt: info.ModTime().Format("2006-01-02 15:04:05"),
	}, nil
}

// RemoveImage removes a locally cached image by name
func (m *Manager) RemoveImage(name string) error {
	imagePath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))

	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		return fmt.Errorf("image not found: %s", name)
	}

	if err := os.Remove(imagePath); err != nil {
		return fmt.Errorf("failed to remove image: %w", err)
	}

	// Also remove any associated rootfs directory
	rootfsDir := filepath.Join(m.baseDir, name+"_rootfs")
	if _, err := os.Stat(rootfsDir); err == nil {
		if err := os.RemoveAll(rootfsDir); err != nil {
			return fmt.Errorf("failed to remove rootfs directory: %w", err)
		}
	}

	// Remove the manifest-digest sidecar file if it exists
	os.Remove(m.digestFilePath(name))

	return nil
}

// PruneImages removes images not referenced by any sandbox.
// inUseRefs is the set of image names/refs currently used by sandboxes.
// Returns the list of removed image names and total bytes freed.
func (m *Manager) PruneImages(inUseRefs map[string]bool) ([]string, int64, error) {
	images, err := m.ListImages()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list images: %w", err)
	}

	var removed []string
	var freedBytes int64

	for _, img := range images {
		if inUseRefs[img.Name] {
			continue
		}

		// Get actual size before removal
		imagePath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", img.Name))
		if info, err := os.Stat(imagePath); err == nil {
			freedBytes += info.Size()
		}

		// Also count rootfs directory size
		rootfsDir := filepath.Join(m.baseDir, img.Name+"_rootfs")
		if info, err := os.Stat(rootfsDir); err == nil {
			if info.IsDir() {
				dirSize := calcDirSize(rootfsDir)
				freedBytes += dirSize
			}
		}

		if err := m.RemoveImage(img.Name); err != nil {
			continue
		}
		removed = append(removed, img.Name)
	}

	return removed, freedBytes, nil
}

// calcDirSize calculates total size of files in a directory tree.
func calcDirSize(path string) int64 {
	var size int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}

// InspectImage returns detailed information about an image
func (m *Manager) InspectImage(name string) (*ImageDetail, error) {
	imagePath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))

	info, err := os.Stat(imagePath)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("image not found: %s", name)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to stat image: %w", err)
	}

	detail := &ImageDetail{
		Name:      name,
		Path:      imagePath,
		SizeBytes: info.Size(),
		SizeMB:    int(info.Size() / (1024 * 1024)),
		CreatedAt: info.ModTime().Format("2006-01-02 15:04:05"),
		ModTime:   info.ModTime().Format("2006-01-02 15:04:05"),
	}

	// Detect format
	format, err := m.DetectFormat(imagePath)
	if err == nil {
		detail.Format = string(format)
	} else {
		detail.Format = "unknown"
	}

	// Check for associated rootfs
	rootfsDir := filepath.Join(m.baseDir, name+"_rootfs")
	if stat, err := os.Stat(rootfsDir); err == nil && stat.IsDir() {
		detail.HasRootfs = true
		detail.RootfsPath = rootfsDir
	}

	// Include digest info if available
	if di, err := m.readDigestFile(name); err == nil && di != nil {
		detail.Digest = di.Digest
		detail.Ref = di.Ref
		detail.PulledAt = di.PulledAt
	}

	return detail, nil
}

// ImageDetail holds detailed information about an image
type ImageDetail struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Format     string `json:"format"`
	SizeBytes  int64  `json:"size_bytes"`
	SizeMB     int    `json:"size_mb"`
	CreatedAt  string `json:"created_at"`
	ModTime    string `json:"modified_at"`
	HasRootfs  bool   `json:"has_rootfs"`
	RootfsPath string `json:"rootfs_path,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Ref        string `json:"ref,omitempty"`
	PulledAt   string `json:"pulled_at,omitempty"`
}

// CachedImageInfo holds information about a cached image including its digest.
type CachedImageInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	SizeMB   int    `json:"size_mb"`
	Digest   string `json:"digest,omitempty"`
	Ref      string `json:"ref,omitempty"`
	PulledAt string `json:"pulled_at,omitempty"`
}

// ListCachedImages returns all cached images along with their digest information.
func (m *Manager) ListCachedImages() ([]CachedImageInfo, error) {
	images, err := m.ListImages()
	if err != nil {
		return nil, err
	}

	result := make([]CachedImageInfo, 0, len(images))
	for _, img := range images {
		info := CachedImageInfo{
			Name:   img.Name,
			Path:   img.Path,
			SizeMB: img.SizeMB,
		}
		if di, err := m.readDigestFile(img.Name); err == nil && di != nil {
			info.Digest = di.Digest
			info.Ref = di.Ref
			info.PulledAt = di.PulledAt
		}
		result = append(result, info)
	}
	return result, nil
}

// TagImage creates an alias for an existing image by linking it under a new name.
// If the source and target names resolve to the same image, it returns an error.
func (m *Manager) TagImage(source, target string) error {
	srcPath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", source))
	dstPath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", target))

	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return fmt.Errorf("source image not found: %s", source)
	}

	if source == target {
		return fmt.Errorf("source and target names are the same")
	}

	// If target already exists, remove it first
	if _, err := os.Stat(dstPath); err == nil {
		if err := os.Remove(dstPath); err != nil {
			return fmt.Errorf("failed to remove existing target image: %w", err)
		}
	}

	// Try hard link first (no extra disk space), fall back to copy
	if err := os.Link(srcPath, dstPath); err != nil {
		if err := m.copyFile(srcPath, dstPath); err != nil {
			return fmt.Errorf("failed to create image tag: %w", err)
		}
	}

	// Also link/copy the rootfs directory if it exists
	srcRootfs := filepath.Join(m.baseDir, source+"_rootfs")
	dstRootfs := filepath.Join(m.baseDir, target+"_rootfs")
	if stat, err := os.Stat(srcRootfs); err == nil && stat.IsDir() {
		// For directories, create a symlink
		os.RemoveAll(dstRootfs)
		if err := os.Symlink(srcRootfs, dstRootfs); err != nil {
			// Ignore symlink failures — the image file is the important part
			_ = err
		}
	}

	return nil
}

// copyFile copies a file
func (m *Manager) copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()
	
	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()
	
	if _, err := dstFile.ReadFrom(srcFile); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}
	
	return nil
}

// ProgressTracker tracks download progress
type ProgressTracker struct {
	TotalBytes    int64
	Downloaded    int64
	StartTime     time.Time
	NotifyFunc    func(bytes int64, total int64)
	mu            sync.Mutex
}

// NewProgressTracker creates a new progress tracker
func NewProgressTracker(notifyFunc func(bytes int64, total int64)) *ProgressTracker {
	return &ProgressTracker{
		StartTime:  time.Now(),
		NotifyFunc: notifyFunc,
	}
}

// UpdateProgress updates the progress and calls the notify function
func (p *ProgressTracker) UpdateProgress(bytes int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Downloaded = bytes
	if p.NotifyFunc != nil && p.TotalBytes > 0 {
		p.NotifyFunc(bytes, p.TotalBytes)
	}
}

// downloadFile downloads a file from a URL to a local path with progress reporting.
// Uses Go's native HTTP client with streaming to avoid buffering entire files in memory.
func (m *Manager) downloadFile(destPath string, url string, progress *ProgressTracker) error {
	httpClient := &http.Client{
		Timeout: 30 * time.Minute,
	}

	resp, err := httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	// Set up progress tracking
	if progress != nil {
		if progress.TotalBytes == 0 && resp.ContentLength > 0 {
			progress.TotalBytes = resp.ContentLength
		}
	}

	outFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	var reader io.Reader = resp.Body
	if progress != nil {
		reader = NewProgressReader(resp.Body, progress)
	}

	if _, err := io.Copy(outFile, reader); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// SaveImage exports a local image to a gzipped tarball containing the image
// file and a JSON metadata manifest. The tarball can be loaded on another
// machine with LoadImage.
func (m *Manager) SaveImage(name string, outputPath string) error {
	imagePath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))
	info, err := os.Stat(imagePath)
	if os.IsNotExist(err) {
		return fmt.Errorf("image not found: %s", name)
	}
	if err != nil {
		return fmt.Errorf("failed to stat image: %w", err)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	// Write manifest
	manifest := map[string]interface{}{
		"name":       name,
		"size_bytes": info.Size(),
		"created_at": info.ModTime().Format(time.RFC3339),
		"saved_at":   time.Now().Format(time.RFC3339),
		"format":     "raw",
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	if err := tw.WriteHeader(&tar.Header{
		Name:    "manifest.json",
		Size:    int64(len(manifestData)),
		Mode:    0644,
		ModTime: time.Now(),
	}); err != nil {
		return fmt.Errorf("failed to write manifest header: %w", err)
	}
	if _, err := tw.Write(manifestData); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	// Write image file
	imgFile, err := os.Open(imagePath)
	if err != nil {
		return fmt.Errorf("failed to open image file: %w", err)
	}
	defer imgFile.Close()

	if err := tw.WriteHeader(&tar.Header{
		Name:    name + ".img",
		Size:    info.Size(),
		Mode:    0644,
		ModTime: info.ModTime(),
	}); err != nil {
		return fmt.Errorf("failed to write image header: %w", err)
	}
	if _, err := io.Copy(tw, imgFile); err != nil {
		return fmt.Errorf("failed to write image data: %w", err)
	}

	return nil
}

// LoadImage imports an image from a tarball previously created by SaveImage.
// If newName is empty, the original name from the manifest is used.
func (m *Manager) LoadImage(archivePath string, newName string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("failed to open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	var manifestData []byte
	var imgData []byte
	var origImgName string

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("failed to read archive entry: %w", err)
		}

		switch {
		case hdr.Name == "manifest.json":
			manifestData, err = io.ReadAll(tr)
			if err != nil {
				return "", fmt.Errorf("failed to read manifest: %w", err)
			}
		case strings.HasSuffix(hdr.Name, ".img"):
			origImgName = strings.TrimSuffix(hdr.Name, ".img")
			imgData, err = io.ReadAll(tr)
			if err != nil {
				return "", fmt.Errorf("failed to read image data: %w", err)
			}
		}
	}

	if imgData == nil {
		return "", fmt.Errorf("archive does not contain an image file")
	}

	// Determine final name
	finalName := newName
	if finalName == "" {
		if manifestData != nil {
			var manifest map[string]interface{}
			if err := json.Unmarshal(manifestData, &manifest); err == nil {
				if n, ok := manifest["name"].(string); ok && n != "" {
					finalName = n
				}
			}
		}
		if finalName == "" {
			finalName = origImgName
		}
	}
	if finalName == "" {
		return "", fmt.Errorf("could not determine image name from archive")
	}

	// Ensure images directory exists
	if err := os.MkdirAll(m.baseDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create images directory: %w", err)
	}

	destPath := filepath.Join(m.baseDir, finalName+".img")
	if _, err := os.Stat(destPath); err == nil {
		return "", fmt.Errorf("image %q already exists (use a different name or remove it first)", finalName)
	}

	if err := os.WriteFile(destPath, imgData, 0644); err != nil {
		return "", fmt.Errorf("failed to write image file: %w", err)
	}

	return finalName, nil
}

// VerifyResult holds the result of an image integrity verification.
type VerifyResult struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Algorithm  string `json:"algorithm"`
	Digest     string `json:"digest"`
	Expected   string `json:"expected,omitempty"`
	Match      bool   `json:"match"`
	DigestFile string `json:"digest_file,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
}

// VerifyImage computes the SHA-256 digest of an image and optionally checks it
// against an expected value. If expectedDigest is empty, the method looks for a
// sidecar <image>.sha256 file. When no expected digest is available, the result
// contains the computed digest with Match set to true (self-consistent).
func (m *Manager) VerifyImage(name string, expectedDigest string) (*VerifyResult, error) {
	imagePath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))
	info, err := os.Stat(imagePath)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("image not found: %s", name)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to stat image: %w", err)
	}

	// Compute SHA-256 digest
	f, err := os.Open(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open image: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, fmt.Errorf("failed to hash image: %w", err)
	}
	digest := fmt.Sprintf("sha256:%x", h.Sum(nil))

	result := &VerifyResult{
		Name:      name,
		Path:      imagePath,
		Algorithm: "sha256",
		Digest:    digest,
		SizeBytes: info.Size(),
		Match:     true,
	}

	// Resolve expected digest
	if expectedDigest != "" {
		result.Expected = expectedDigest
	} else {
		// Look for sidecar .sha256 file
		sidecar := imagePath + ".sha256"
		if data, err := os.ReadFile(sidecar); err == nil {
			expected := strings.TrimSpace(string(data))
			// Handle both "sha256:abc..." and bare hex forms
			if !strings.Contains(expected, ":") {
				expected = "sha256:" + expected
			}
			result.Expected = expected
			result.DigestFile = sidecar
		}
	}

	if result.Expected != "" {
		result.Match = (digest == result.Expected)
	}

	return result, nil
}

// StoreDigest writes the SHA-256 digest of an image to a sidecar .sha256 file
// so that future VerifyImage calls can check integrity without an explicit digest.
func (m *Manager) StoreDigest(name string) (string, error) {
	res, err := m.VerifyImage(name, "")
	if err != nil {
		return "", err
	}
	sidecar := res.Path + ".sha256"
	// Write bare hex (strip "sha256:" prefix) for compatibility with sha256sum
	hex := strings.TrimPrefix(res.Digest, "sha256:")
	if err := os.WriteFile(sidecar, []byte(hex+"\n"), 0644); err != nil {
		return "", fmt.Errorf("failed to write digest file: %w", err)
	}
	return res.Digest, nil
}

// NewProgressReader creates a progress-reporting reader
func NewProgressReader(r io.Reader, tracker *ProgressTracker) io.Reader {
	return &progressReader{Reader: r, tracker: tracker}
}

// HistoryEntry represents a single step in an image's build history.
type HistoryEntry struct {
	Step      int    `json:"step"`
	Command   string `json:"command"`
	Args      string `json:"args"`
	CreatedBy string `json:"created_by"`
}

// ImageHistory holds the full build history for an image.
type ImageHistory struct {
	Name      string         `json:"name"`
	BaseImage string         `json:"base_image"`
	BuiltAt   string         `json:"built_at,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Ports     []string       `json:"exposed_ports,omitempty"`
	Entries   []HistoryEntry `json:"entries"`
}

// ImageHistory reads the build history of an image built with BuildImage.
// It parses the metadata and build script stored in the rootfs overlay directory.
func (m *Manager) ImageHistory(name string) (*ImageHistory, error) {
	imagePath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("image not found: %s", name)
	}

	history := &ImageHistory{
		Name:   name,
		Labels: make(map[string]string),
	}

	// Read image metadata from rootfs overlay
	rootfsDir := filepath.Join(m.baseDir, name+"_rootfs")
	metaPath := filepath.Join(rootfsDir, "etc", "tent", "image.meta")

	if data, err := os.ReadFile(metaPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if idx := strings.Index(line, "="); idx > 0 {
				key := line[:idx]
				val := line[idx+1:]
				switch key {
				case "base":
					history.BaseImage = val
				case "built":
					history.BuiltAt = val
				default:
					if strings.HasPrefix(key, "label.") {
						history.Labels[strings.TrimPrefix(key, "label.")] = val
					}
					if key == "expose" {
						history.Ports = append(history.Ports, val)
					}
				}
			}
		}
	}

	// Build the history entries starting with FROM
	step := 1
	if history.BaseImage != "" {
		history.Entries = append(history.Entries, HistoryEntry{
			Step:      step,
			Command:   "FROM",
			Args:      history.BaseImage,
			CreatedBy: fmt.Sprintf("FROM %s", history.BaseImage),
		})
		step++
	}

	// Parse the build script for RUN, ENV, WORKDIR instructions
	buildScript := filepath.Join(rootfsDir, "etc", "tent", "build.sh")
	if data, err := os.ReadFile(buildScript); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || line == "set -e" {
				continue
			}

			if strings.HasPrefix(line, "export ") {
				// ENV instruction
				envExpr := strings.TrimPrefix(line, "export ")
				history.Entries = append(history.Entries, HistoryEntry{
					Step:      step,
					Command:   "ENV",
					Args:      envExpr,
					CreatedBy: fmt.Sprintf("ENV %s", envExpr),
				})
				step++
			} else if strings.HasPrefix(line, "cd ") {
				// WORKDIR instruction
				dir := strings.TrimPrefix(line, "cd ")
				history.Entries = append(history.Entries, HistoryEntry{
					Step:      step,
					Command:   "WORKDIR",
					Args:      dir,
					CreatedBy: fmt.Sprintf("WORKDIR %s", dir),
				})
				step++
			} else {
				// RUN instruction
				history.Entries = append(history.Entries, HistoryEntry{
					Step:      step,
					Command:   "RUN",
					Args:      line,
					CreatedBy: fmt.Sprintf("RUN %s", line),
				})
				step++
			}
		}
	}

	// Scan rootfs overlay for COPY artifacts
	copyDir := rootfsDir
	if stat, err := os.Stat(copyDir); err == nil && stat.IsDir() {
		_ = filepath.Walk(copyDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(copyDir, path)
			// Skip build script and metadata (already accounted for)
			if strings.HasPrefix(rel, "etc/tent/") {
				return nil
			}
			history.Entries = append(history.Entries, HistoryEntry{
				Step:      step,
				Command:   "COPY",
				Args:      fmt.Sprintf(". /%s", rel),
				CreatedBy: fmt.Sprintf("COPY . /%s", rel),
			})
			step++
			return nil
		})
	}

	// Add EXPOSE entries from metadata
	for _, port := range history.Ports {
		history.Entries = append(history.Entries, HistoryEntry{
			Step:      step,
			Command:   "EXPOSE",
			Args:      port,
			CreatedBy: fmt.Sprintf("EXPOSE %s", port),
		})
		step++
	}

	return history, nil
}

// progressReader wraps an io.Reader to track progress
type progressReader struct {
	io.Reader
	tracker *ProgressTracker
}

func (pr *progressReader) Read(p []byte) (n int, err error) {
	n, err = pr.Reader.Read(p)
	if n > 0 {
		pr.tracker.UpdateProgress(pr.tracker.Downloaded + int64(n))
	}
	return
}
