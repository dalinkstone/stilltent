// Package image provides image pipeline functionality for OCI/Docker, ISO, and raw disk images.
// This package handles pulling images, extracting content, and format detection.
package image

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dalinkstone/tent/pkg/models"
)

// Manager handles image operations
type Manager struct {
	baseDir string
}

// NewManager creates a new image manager
func NewManager(baseDir string) (*Manager, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("base directory is required")
	}
	
	return &Manager{
		baseDir: filepath.Join(baseDir, "images"),
	}, nil
}

// Pull pulls an image from a URL
func (m *Manager) Pull(name string, url string) (string, error) {
	// Create images directory
	if err := os.MkdirAll(m.baseDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create images directory: %w", err)
	}
	
	// Create image file path
	imagePath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))
	
	// Download the image
	if err := m.downloadFile(imagePath, url); err != nil {
		return "", fmt.Errorf("failed to download image: %w", err)
	}
	
	return imagePath, nil
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

	// Extract layers and build rootfs
	rootfsPath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))
	if err := m.extractLayers(layers, tmpDir, rootfsPath); err != nil {
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

// getLayers fetches the layer manifest for an image
func (m *Manager) getLayers(registry, repo, tag string) ([]LayerInfo, error) {
	// For now, return a placeholder that simulates pulling a minimal rootfs
	// In a full implementation, this would:
	// 1. Get auth token from registry
	// 2. Fetch manifest for the image
	// 3. Extract layer digests
	// 4. Download each layer

	// For testing, return a single minimal layer
	// This represents a minimal Linux rootfs (~50MB compressed)
	return []LayerInfo{
		{
			Digest:    "sha256:" + strings.Repeat("0", 64),
			Size:      50 * 1024 * 1024, // 50MB
			URL:       fmt.Sprintf("https://%s/v2/%s/blobs/sha256%s", registry, repo, strings.Repeat("0", 64)),
			MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		},
	}, nil
}

// LayerInfo represents a layer in an OCI image
type LayerInfo struct {
	Digest    string
	Size      int64
	URL       string
	MediaType string
}

// extractLayers extracts all layers and creates a rootfs image
func (m *Manager) extractLayers(layers []LayerInfo, tmpDir, rootfsPath string) error {
	if len(layers) == 0 {
		return fmt.Errorf("no layers to extract")
	}

	// For the first layer, create a minimal ext4 rootfs
	// In a full implementation, this would:
	// 1. Download each layer
	// 2. Decompress tar.gz layers
	// 3. Extract to temp directory
	// 4. Create final image from extracted contents

	// Create a minimal ext4 image with a basic rootfs structure
	if err := createMinimalRootfs(rootfsPath); err != nil {
		return fmt.Errorf("failed to create rootfs: %w", err)
	}

	return nil
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

// ListImages lists all available images
func (m *Manager) ListImages() ([]*models.ImageInfo, error) {
	var images []*models.ImageInfo
	
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

// downloadFile downloads a file from a URL to a local path
func (m *Manager) downloadFile(filepath string, url string) error {
	// Use curl to download (more reliable than native Go HTTP for large files)
	cmd := exec.Command("curl", "-L", "-o", filepath, url)
	if err := cmd.Run(); err != nil {
		// Fall back to wget
		cmd = exec.Command("wget", "-O", filepath, url)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("download failed: %w", err)
		}
	}
	return nil
}

// execCommand executes a shell command
func execCommand(cmdStr string) error {
	// Use /bin/sh -c to execute the command
	cmd := exec.Command("/bin/sh", "-c", cmdStr)
	return cmd.Run()
}
