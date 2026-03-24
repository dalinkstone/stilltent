//go:build darwin

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dalinkstone/tent/pkg/models"
)

// CreateRootFS creates a root filesystem for a VM on macOS.
// If config.RootFS is set (from a resolved OCI image), the extracted rootfs
// directory is symlinked into the VM's storage area and the image file is used
// as the boot disk. Otherwise a blank ext4 image is created.
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

	rootfsPath := filepath.Join(baseImageDir, "rootfs.img")

	// If a resolved image is available, use it instead of creating a blank filesystem
	if config.RootFS != "" {
		// The resolved image has a companion _rootfs directory with the extracted
		// OCI layer contents (virtiofs share). Symlink it into the VM's storage.
		srcRootfsDir := strings.TrimSuffix(config.RootFS, ".img") + "_rootfs"
		if info, err := os.Stat(srcRootfsDir); err == nil && info.IsDir() {
			vmRootfsDir := filepath.Join(baseImageDir, "rootfs")
			os.Remove(vmRootfsDir) // remove stale symlink if any
			if err := os.Symlink(srcRootfsDir, vmRootfsDir); err != nil {
				// Symlink failed — fall back to copying the directory
				if cpErr := copyDirRecursive(srcRootfsDir, vmRootfsDir); cpErr != nil {
					return "", fmt.Errorf("failed to setup rootfs from image: %w", cpErr)
				}
			}
		}

		// Copy the boot disk image so the VM has its own writable copy
		if err := m.copyFile(config.RootFS, rootfsPath); err != nil {
			return "", fmt.Errorf("failed to copy rootfs image from resolved image: %w", err)
		}

		return rootfsPath, nil
	}

	// No resolved image — create a blank rootfs
	if err := m.createRootfsImage(rootfsPath, config.DiskGB); err != nil {
		return "", fmt.Errorf("failed to create rootfs image: %w", err)
	}

	// On macOS, we don't mount the image - we just return the path
	// The hypervisor will attach it as a virtio-blk device directly
	return rootfsPath, nil
}

// copyDirRecursive recursively copies a directory tree.
func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}

		// Handle symlinks
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			os.Remove(target)
			return os.Symlink(linkTarget, target)
		}

		// Copy regular file
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = dstFile.ReadFrom(srcFile)
		return err
	})
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
