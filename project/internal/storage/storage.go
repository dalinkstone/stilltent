package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dalinkstone/tent/internal/image"
)

// StorageManager handles rootfs creation and snapshot management
type Manager struct {
	baseDir string
}

// NewManager creates a new storage manager
func NewManager(baseDir string) (*Manager, error) {
	if baseDir == "" {
		baseDir = "/var/lib/tent"
	}

	return &Manager{
		baseDir: baseDir,
	}, nil
}

// GetBaseDir returns the base directory for storage
func (m *Manager) GetBaseDir() string {
	return m.baseDir
}

// ImageManager returns the image manager for the storage directory
func (m *Manager) ImageManager() (*image.Manager, error) {
	return image.NewManager(m.baseDir)
}

// createRootfsImage creates a rootfs image file using pure Go code
func (m *Manager) createRootfsImage(path string, sizeGB int) error {
	// Calculate size in bytes
	sizeBytes := sizeGB * 1024 * 1024 * 1024

	// Create empty file with specified size
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create image file: %w", err)
	}
	defer file.Close()

	// Truncate to size
	if err := file.Truncate(int64(sizeBytes)); err != nil {
		return fmt.Errorf("failed to truncate file: %w", err)
	}

	// Write ext4 magic number at offset 1024 to mark as ext4 filesystem
	// This is a simplified approach - in production, you'd use a proper ext4 library
	ext4Magic := []byte{0x53, 0xEF} // ext2/3/4 magic
	if _, err := file.WriteAt(ext4Magic, 1024+56); err != nil {
		return fmt.Errorf("failed to write ext4 magic: %w", err)
	}

	return nil
}

// CreateSnapshot creates a snapshot of a VM's rootfs
func (m *Manager) CreateSnapshot(vmName string, tag string) (string, error) {
	rootfsPath := filepath.Join(m.baseDir, "rootfs", vmName, "rootfs.img")
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		return "", fmt.Errorf("rootfs not found for VM %s", vmName)
	}

	// Create snapshot directory
	snapshotDir := filepath.Join(m.baseDir, "snapshots", vmName)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create snapshot directory: %w", err)
	}

	// Create snapshot filename
	snapshotPath := filepath.Join(snapshotDir, fmt.Sprintf("%s.img", tag))

	// Copy rootfs to snapshot using Go's file operations
	if err := m.copyFile(rootfsPath, snapshotPath); err != nil {
		return "", fmt.Errorf("failed to create snapshot: %w", err)
	}

	return snapshotPath, nil
}

// copyFile copies a file using Go's io package
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

	// Copy contents
	if _, err := dstFile.ReadFrom(srcFile); err != nil {
		return fmt.Errorf("failed to copy file contents: %w", err)
	}

	// Get source file info for permissions
	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat source file: %w", err)
	}

	// Set permissions on destination
	if err := dstFile.Chmod(srcInfo.Mode()); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}

	return nil
}

// RestoreSnapshot restores a VM's rootfs from a snapshot
func (m *Manager) RestoreSnapshot(vmName string, tag string) error {
	snapshotPath := filepath.Join(m.baseDir, "snapshots", vmName, fmt.Sprintf("%s.img", tag))
	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		return fmt.Errorf("snapshot not found: %s", snapshotPath)
	}

	rootfsPath := filepath.Join(m.baseDir, "rootfs", vmName, "rootfs.img")

	// Copy snapshot to rootfs
	if err := m.copyFile(snapshotPath, rootfsPath); err != nil {
		return fmt.Errorf("failed to restore snapshot: %w", err)
	}

	return nil
}

// ListSnapshots lists all snapshots for a VM
func (m *Manager) ListSnapshots(vmName string) ([]*SnapshotInfo, error) {
	snapshotDir := filepath.Join(m.baseDir, "snapshots", vmName)
	if _, err := os.Stat(snapshotDir); os.IsNotExist(err) {
		return []*SnapshotInfo{}, nil
	}

	var snapshots []*SnapshotInfo

	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read snapshot directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".img") {
			info, err := entry.Info()
			if err == nil {
				snapshots = append(snapshots, &SnapshotInfo{
					Tag:       strings.TrimSuffix(entry.Name(), ".img"),
					SizeMB:    int(info.Size() / (1024 * 1024)),
					CreatedAt: info.ModTime().Format("2006-01-02 15:04:05"),
				})
			}
		}
	}

	return snapshots, nil
}

// SnapshotInfo represents information about a snapshot
type SnapshotInfo struct {
	Tag       string `json:"tag"`
	SizeMB    int    `json:"size_mb"`
	CreatedAt string `json:"created_at"`
}

// DeleteSnapshot deletes a specific snapshot for a VM
func (m *Manager) DeleteSnapshot(vmName string, tag string) error {
	snapshotPath := filepath.Join(m.baseDir, "snapshots", vmName, fmt.Sprintf("%s.img", tag))
	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		return fmt.Errorf("snapshot '%s' not found for VM %s", tag, vmName)
	}

	if err := os.Remove(snapshotPath); err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	// Clean up empty snapshot directory
	snapshotDir := filepath.Join(m.baseDir, "snapshots", vmName)
	entries, err := os.ReadDir(snapshotDir)
	if err == nil && len(entries) == 0 {
		os.Remove(snapshotDir)
	}

	return nil
}

// DeleteAllSnapshots deletes all snapshots for a VM
func (m *Manager) DeleteAllSnapshots(vmName string) (int, error) {
	snapshotDir := filepath.Join(m.baseDir, "snapshots", vmName)
	if _, err := os.Stat(snapshotDir); os.IsNotExist(err) {
		return 0, nil
	}

	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		return 0, fmt.Errorf("failed to read snapshot directory: %w", err)
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".img") {
			path := filepath.Join(snapshotDir, entry.Name())
			if err := os.Remove(path); err != nil {
				return count, fmt.Errorf("failed to delete snapshot %s: %w", entry.Name(), err)
			}
			count++
		}
	}

	// Clean up empty directory
	entries, err = os.ReadDir(snapshotDir)
	if err == nil && len(entries) == 0 {
		os.Remove(snapshotDir)
	}

	return count, nil
}

// PullImage downloads a base image from a URL to the storage directory
func (m *Manager) PullImage(name string, url string) (string, error) {
	// Create base image directory
	baseImageDir := filepath.Join(m.baseDir, "images")
	if err := os.MkdirAll(baseImageDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create images directory: %w", err)
	}

	// Create image file path
	imagePath := filepath.Join(baseImageDir, fmt.Sprintf("%s.img", name))

	// Download the image
	if err := m.downloadFile(imagePath, url); err != nil {
		return "", fmt.Errorf("failed to download image: %w", err)
	}

	return imagePath, nil
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

// ListImages lists all available base images
func (m *Manager) ListImages() ([]*ImageInfo, error) {
	imagesDir := filepath.Join(m.baseDir, "images")
	if _, err := os.Stat(imagesDir); os.IsNotExist(err) {
		return []*ImageInfo{}, nil
	}

	var images []*ImageInfo

	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read images directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".img") {
			info, err := entry.Info()
			if err == nil {
				images = append(images, &ImageInfo{
					Name:      strings.TrimSuffix(entry.Name(), ".img"),
					SizeMB:    int(info.Size() / (1024 * 1024)),
					CreatedAt: info.ModTime().Format("2006-01-02 15:04:05"),
				})
			}
		}
	}

	return images, nil
}

// ImageInfo represents information about a base image
type ImageInfo struct {
	Name      string `json:"name"`
	SizeMB    int    `json:"size_mb"`
	CreatedAt string `json:"created_at"`
}

// KernelInfo contains information about an extracted kernel
type KernelInfo struct {
	KernelPath string `json:"kernel_path"`
	InitrdPath string `json:"initrd_path,omitempty"`
	Cmdline    string `json:"cmdline,omitempty"`
}

// ExtractKernel attempts to extract the kernel from a rootfs image
// Platform-specific implementations can be added later if needed
func (m *Manager) ExtractKernel(rootfsPath string) (*KernelInfo, error) {
	// Check if the rootfs file exists
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("rootfs not found: %s", rootfsPath)
	}

	// For now, return a placeholder kernel path
	// In a production implementation, this would:
	// 1. Mount the rootfs image
	// 2. Copy /boot/vmlinuz and /boot/initrd to a staging directory
	// 3. Return the paths to these files
	//
	// For a minimal implementation, we use the kernel that's already
	// available in the system. This assumes the host kernel is compatible
	// with the guest VM.

	// Try to use the host kernel as a fallback
	hostKernel := "/vmlinuz"
	if _, err := os.Stat("/vmlinuz"); err == nil {
		hostKernel = "/vmlinuz"
	} else if _, err := os.Stat("/boot/vmlinuz"); err == nil {
		hostKernel = "/boot/vmlinuz"
	} else if _, err := os.Stat("/boot/vmlinuz-linux"); err == nil {
		hostKernel = "/boot/vmlinuz-linux"
	} else if _, err := os.Stat("/boot/vmlinuz-$(uname -r)"); err == nil {
		// Try with current kernel version
		// This is a placeholder - in production you'd exec uname -r
		hostKernel = "/boot/vmlinuz-$(uname -r)"
	} else {
		// Last resort - return a path that will fail at runtime
		hostKernel = "/boot/vmlinuz"
	}

	// Return placeholder paths for initrd and cmdline
	return &KernelInfo{
		KernelPath: hostKernel,
		InitrdPath: "/boot/initrd.img",
		Cmdline:    "root=/dev/vda console=hvc0 rw ip=dhcp init=/sbin/init",
	}, nil
}
