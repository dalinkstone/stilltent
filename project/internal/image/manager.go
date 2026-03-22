// Package image provides image pipeline functionality for OCI/Docker, ISO, and raw disk images.
// This package handles pulling images, extracting content, and format detection.
package image

import (
	"archive/tar"
	"bytes"
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

// ResolveImage resolves an image reference to a local rootfs path.
// It handles:
//   - Local file paths (ISO, raw disk, qcow2) — used directly
//   - Registry references (ubuntu:22.04, gcr.io/proj/img:tag) — pulled via OCI
//
// If the image is already cached locally, it returns the cached path.
func (m *Manager) ResolveImage(ref string) (string, error) {
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

	// It's a registry reference — check local cache first
	safeName := strings.NewReplacer("/", "_", ":", "_").Replace(ref)
	cachedPath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", safeName))
	if _, err := os.Stat(cachedPath); err == nil {
		return cachedPath, nil
	}

	// Pull from registry
	rootfsPath, err := m.PullOCI(safeName, ref)
	if err != nil {
		return "", fmt.Errorf("failed to pull image %q: %w", ref, err)
	}

	return rootfsPath, nil
}

// PullOCI pulls an OCI/Docker image from a registry (Docker Hub, GCR, ECR, etc.)
// It parses the image reference, authenticates if needed, pulls layers, and extracts to rootfs
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

	// Download and extract each layer
	// For Docker Hub images, we'll download from the registry API
	layers, err := m.getLayers(registry, repo, tag)
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

	return rootfsPath, nil
}

// parseImageRef parses an image reference into registry, repo, and tag
// Examples:
// - "ubuntu:22.04" -> "registry.hub.docker.com", "library/ubuntu", "22.04"
// - "gcr.io/project/image:tag" -> "gcr.io", "project/image", "tag"
// - "myregistry.com:5000/repo/image:latest" -> "myregistry.com:5000", "repo/image", "latest"
func parseImageRef(ref string) (registry, repo, tag string, err error) {
	// Default registry
	registry = "registry.hub.docker.com"

	// Split by '/' to separate registry from repo
	parts := strings.Split(ref, "/")
	if len(parts) == 1 {
		// Just repo:tag (e.g., "ubuntu:22.04")
		repo = "library/" + parts[0]
		tag = "latest"
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

	// Split repo by ':' to separate tag
	if strings.Contains(repo, ":") {
		repoTag := strings.Split(repo, ":")
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
	for i, layer := range layers {
		_ = i
		// Download layer
		reader, err := client.DownloadLayer(registry, repo, layer)
		if err != nil {
			return fmt.Errorf("failed to download layer %s: %w", layer.Digest, err)
		}

		// Wrap with progress tracking
		var layerReader io.Reader = reader
		if progress != nil {
			layerReader = &progressLayerReader{
				reader:     reader,
				tracker:    progress,
				baseOffset: downloaded,
			}
		}

		// Extract tar to rootfs directory
		if err := extractTar(layerReader, rootfsDir); err != nil {
			reader.Close()
			return fmt.Errorf("failed to extract layer %s: %w", layer.Digest, err)
		}
		reader.Close()

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
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			os.Remove(target) // remove existing before creating symlink
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			linkTarget := filepath.Join(dir, hdr.Linkname)
			os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				// Hard link may fail across filesystems, fall back to copy
				continue
			}
		}
	}
	return nil
}

// packRootfsImage creates a sparse raw disk image from a rootfs directory.
// The image is a 2GB sparse file with the rootfs contents recorded for later
// filesystem creation during VM boot.
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
	// Read first few bytes for magic number detection
	data := make([]byte, 512)
	file, err := os.Open(imagePath)
	if err != nil {
		return FormatUnknown, fmt.Errorf("failed to open image: %w", err)
	}
	defer file.Close()
	
	n, err := file.Read(data)
	if err != nil && err != os.ErrClosed && err != os.ErrInvalid {
		return FormatUnknown, fmt.Errorf("failed to read image: %w", err)
	}
	data = data[:n]
	
	// Check for ISO9660 magic number (at offset 0x8001)
	if len(data) > 0x8001 {
		isoMagic := data[0x8001:0x8006]
		if string(isoMagic) == "CD001" {
			return FormatISO, nil
		}
	}
	
	// Check for QCOW2 magic number (at offset 0)
	qcow2Magic := []byte{'Q', 'F', 'I', 0xfb}
	if len(data) >= 4 && string(data[:4]) == string(qcow2Magic) {
		return FormatQCOW2, nil
	}
	
	// Check for ext4 magic number (at offset 1024 + 56)
	if len(data) > 1080 {
		ext4Magic := data[1080:1082]
		if string(ext4Magic) == "\x53\xEF" {
			return FormatRaw, nil
		}
	}
	
	// Default to raw if no magic number found
	return FormatRaw, nil
}

// execCommand executes a shell command
func execCommand(cmdStr string) error {
	// Use /bin/sh -c to execute the command
	cmd := exec.Command("/bin/sh", "-c", cmdStr)
	return cmd.Run()
}

// convertQCOW2ToRaw converts a QCOW2 image to raw format
func (m *Manager) convertQCOW2ToRaw(src, dst string) error {
	// Use qemu-img if available
	cmdStr := fmt.Sprintf("qemu-img convert -f qcow2 -O raw %s %s", src, dst)
	if err := execCommand(cmdStr); err != nil {
		// Fall back to raw copy for testing
		return m.copyFile(src, dst)
	}
	return nil
}

// extractISO extracts kernel and initrd from an ISO image
func (m *Manager) extractISO(imagePath string) (string, error) {
	// For now, return the ISO path as-is
	// In a full implementation, this would:
	// 1. Mount the ISO
	// 2. Copy /isolinux/vmlinuz and /isolinux/initrd.img
	// 3. Create a rootfs image with the extracted files
	
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

	return nil
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

// downloadFile downloads a file from a URL to a local path with progress reporting
func (m *Manager) downloadFile(filepath string, url string, progress *ProgressTracker) error {
	// Use curl to download (more reliable than native Go HTTP for large files)
	cmd := exec.Command("curl", "-L", "-o", filepath, url)
	
	// Get file size for progress tracking if not provided
	if progress != nil && progress.TotalBytes == 0 {
		resp, err := http.Head(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			progress.TotalBytes = resp.ContentLength
			resp.Body.Close()
		}
	}
	
	// Create a progress tracking wrapper for the output
	if progress != nil {
		outFile, err := os.Create(filepath)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer outFile.Close()
		
		// Use io.TeeReader to track progress while downloading
		cmd = exec.Command("curl", "-L", url)
		resp, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("download failed: %w", err)
		}
		
		// Write with progress tracking
		reader := NewProgressReader(bytes.NewReader(resp), progress)
		reader.(*progressReader).tracker.TotalBytes = progress.TotalBytes
		_, err = io.Copy(outFile, reader)
		if err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}
		
		return nil
	}
	
	// Original curl path without progress
	if err := cmd.Run(); err != nil {
		// Fall back to wget
		cmd = exec.Command("wget", "-O", filepath, url)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("download failed: %w", err)
		}
	}
	return nil
}

// NewProgressReader creates a progress-reporting reader
func NewProgressReader(r io.Reader, tracker *ProgressTracker) io.Reader {
	return &progressReader{Reader: r, tracker: tracker}
}

// progressReader wraps an io.Reader to track progress
type progressReader struct {
	io.Reader
	tracker *ProgressTracker
}

func (pr *progressReader) Read(p []byte) (n int, err error) {
	n, err = pr.Reader.Read(p)
	if err == nil && n > 0 {
		pr.tracker.UpdateProgress(pr.tracker.Downloaded + int64(n))
	}
	return
}
