package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dalinkstone/tent/pkg/models"
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

// CreateRootFS creates a root filesystem for a VM
func (m *Manager) CreateRootFS(vmName string, config *models.VMConfig) (string, error) {
	// Create directories
	rootfsDir := filepath.Join(m.baseDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create rootfs directory: %w", err)
	}

	// Create base image directory
	baseImageDir := filepath.Join(rootfsDir, vmName)
	if err := os.MkdirAll(baseImageDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create VM directory: %w", err)
	}

	// Create rootfs image file
	rootfsPath := filepath.Join(baseImageDir, "rootfs.img")
	if err := m.createRootfsImage(rootfsPath, config.DiskGB); err != nil {
		return "", fmt.Errorf("failed to create rootfs image: %w", err)
	}

	// Create mount point
	mountPoint := filepath.Join(baseImageDir, "mnt")
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		return "", fmt.Errorf("failed to create mount point: %w", err)
	}

	// Mount the rootfs image
	if err := m.mountRootfs(rootfsPath, mountPoint); err != nil {
		return "", fmt.Errorf("failed to mount rootfs: %w", err)
	}

	// Initialize basic filesystem structure
	if err := m.initializeFilesystem(mountPoint); err != nil {
		m.umountRootfs(mountPoint)
		return "", fmt.Errorf("failed to initialize filesystem: %w", err)
	}

	// Unmount
	if err := m.umountRootfs(mountPoint); err != nil {
		return "", fmt.Errorf("failed to unmount rootfs: %w", err)
	}

	return rootfsPath, nil
}

// DestroyVMStorage destroys storage resources for a VM
func (m *Manager) DestroyVMStorage(vmName string) error {
	rootfsDir := filepath.Join(m.baseDir, "rootfs", vmName)

	// Check if directory exists
	if _, err := os.Stat(rootfsDir); os.IsNotExist(err) {
		return nil // Nothing to destroy
	}

	// Unmount if mounted
	mountPoint := filepath.Join(rootfsDir, "mnt")
	if m.isMounted(mountPoint) {
		if err := m.umountRootfs(mountPoint); err != nil {
			return fmt.Errorf("failed to unmount: %w", err)
		}
	}

	// Remove directory
	return os.RemoveAll(rootfsDir)
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

// mountRootfs mounts the rootfs image
func (m *Manager) mountRootfs(imagePath, mountPoint string) error {
	cmd := exec.Command("sudo", "mount", "-o", "loop", imagePath, mountPoint)
	if err := cmd.Run(); err != nil {
		// Try without sudo
		cmd = exec.Command("mount", "-o", "loop", imagePath, mountPoint)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to mount: %w", err)
		}
	}
	return nil
}

// umountRootfs unmounts the rootfs image
func (m *Manager) umountRootfs(mountPoint string) error {
	cmd := exec.Command("sudo", "umount", mountPoint)
	if err := cmd.Run(); err != nil {
		// Try without sudo
		cmd = exec.Command("umount", mountPoint)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to unmount: %w", err)
		}
	}
	return nil
}

// isMounted checks if a mount point is mounted
func (m *Manager) isMounted(mountPoint string) bool {
	cmd := exec.Command("mountpoint", mountPoint)
	return cmd.Run() == nil
}

// initializeFilesystem creates the basic directory structure
func (m *Manager) initializeFilesystem(mountPoint string) error {
	dirs := []string{
		"bin", "boot", "dev", "etc", "home", "lib", "lib64", "media",
		"mnt", "opt", "proc", "root", "run", "sbin", "srv", "sys",
		"tmp", "usr", "var",
	}

	for _, dir := range dirs {
		path := filepath.Join(mountPoint, dir)
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Create some basic config files
	if err := m.createBasicConfig(mountPoint); err != nil {
		return err
	}

	return nil
}

// createBasicConfig creates basic configuration files
func (m *Manager) createBasicConfig(mountPoint string) error {
	// Create /etc/passwd
	passwdContent := `root:x:0:0:root:/root:/bin/bash
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
`
	if err := os.WriteFile(filepath.Join(mountPoint, "etc", "passwd"), []byte(passwdContent), 0644); err != nil {
		return fmt.Errorf("failed to create passwd: %w", err)
	}

	// Create /etc/group
	groupContent := `root:x:0:
nogroup:x:65534:
`
	if err := os.WriteFile(filepath.Join(mountPoint, "etc", "group"), []byte(groupContent), 0644); err != nil {
		return fmt.Errorf("failed to create group: %w", err)
	}

	// Create /etc/hosts
	hostsContent := `127.0.0.1	localhost
::1		localhost ip6-localhost ip6-loopback
`
	if err := os.WriteFile(filepath.Join(mountPoint, "etc", "hosts"), []byte(hostsContent), 0644); err != nil {
		return fmt.Errorf("failed to create hosts: %w", err)
	}

	// Create /etc/fstab
	fstabContent := `proc            /proc           proc    defaults          0       0
/dev/mmcblk0p1  /               ext4    defaults,noatime  0       1
`
	if err := os.WriteFile(filepath.Join(mountPoint, "etc", "fstab"), []byte(fstabContent), 0644); err != nil {
		return fmt.Errorf("failed to create fstab: %w", err)
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
