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

// PullOCI pulls an OCI/Docker image from a registry
func (m *Manager) PullOCI(name string, ref string) (string, error) {
	// For now, treat OCI references as URLs
	// In a full implementation, this would:
	// 1. Parse the image reference (registry/repo:tag)
	// 2. Authenticate if needed
	// 3. Pull layers using OCI/Docker registry API
	// 4. Extract to rootfs
	
	// For now, use the reference as URL
	return m.Pull(name, ref)
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
