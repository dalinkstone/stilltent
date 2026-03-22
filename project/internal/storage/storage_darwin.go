//go:build darwin

package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dalinkstone/tent/pkg/models"
)

// CreateRootFS creates a root filesystem for a VM on macOS
// On macOS, we skip the mount/unmount/initialize steps since there's no loop mount
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

	// On macOS, we don't mount the image - we just return the path
	// The hypervisor will attach it as a virtio-blk device directly
	return rootfsPath, nil
}

// CloneRootFS copies the rootfs from one VM to a new VM directory on macOS
func (m *Manager) CloneRootFS(srcName string, dstName string) (string, error) {
	srcPath := filepath.Join(m.baseDir, "rootfs", srcName, "rootfs.img")
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return "", fmt.Errorf("rootfs not found for VM %s", srcName)
	}

	dstDir := filepath.Join(m.baseDir, "rootfs", dstName)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	dstPath := filepath.Join(dstDir, "rootfs.img")
	if err := m.copyFile(srcPath, dstPath); err != nil {
		return "", fmt.Errorf("failed to clone rootfs: %w", err)
	}

	return dstPath, nil
}

// DestroyVMStorage destroys storage resources for a VM on macOS
func (m *Manager) DestroyVMStorage(vmName string) error {
	rootfsDir := filepath.Join(m.baseDir, "rootfs", vmName)

	// Check if directory exists
	if _, err := os.Stat(rootfsDir); os.IsNotExist(err) {
		return nil // Nothing to destroy
	}

	// On macOS, there's no mount to unmount - just remove the directory
	return os.RemoveAll(rootfsDir)
}
