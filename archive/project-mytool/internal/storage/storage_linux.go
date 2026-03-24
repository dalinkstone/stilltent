//go:build linux

package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dalinkstone/tent/pkg/models"
)

// CreateRootFS creates a root filesystem for a VM on Linux.
// If config.RootFS is set (from a resolved OCI image), the extracted layer
// contents are copied into the disk image. Otherwise a blank ext4 image is
// created with a basic directory skeleton.
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

	// If a resolved image is available, copy its content into the mounted filesystem
	if config.RootFS != "" {
		srcRootfsDir := strings.TrimSuffix(config.RootFS, ".img") + "_rootfs"
		if info, err := os.Stat(srcRootfsDir); err == nil && info.IsDir() {
			// Copy the extracted OCI layers into the mounted disk image
			if err := copyDirContents(srcRootfsDir, mountPoint); err != nil {
				m.umountRootfs(mountPoint)
				return "", fmt.Errorf("failed to copy image contents to rootfs: %w", err)
			}
		} else {
			// No rootfs directory — initialize with basic structure
			if err := m.initializeFilesystem(mountPoint); err != nil {
				m.umountRootfs(mountPoint)
				return "", fmt.Errorf("failed to initialize filesystem: %w", err)
			}
		}
	} else {
		// No resolved image — initialize basic filesystem structure
		if err := m.initializeFilesystem(mountPoint); err != nil {
			m.umountRootfs(mountPoint)
			return "", fmt.Errorf("failed to initialize filesystem: %w", err)
		}
	}

	// Unmount
	if err := m.umountRootfs(mountPoint); err != nil {
		return "", fmt.Errorf("failed to unmount rootfs: %w", err)
	}

	return rootfsPath, nil
}

// copyDirContents recursively copies all entries from src into dst.
func copyDirContents(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		info, err := entry.Info()
		if err != nil {
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, linkErr := os.Readlink(srcPath)
			if linkErr != nil {
				return linkErr
			}
			os.Remove(dstPath)
			if err := os.Symlink(linkTarget, dstPath); err != nil {
				return err
			}
			continue
		}

		if entry.IsDir() {
			if err := os.MkdirAll(dstPath, info.Mode()); err != nil {
				return err
			}
			if err := copyDirContents(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			srcFile, err := os.Open(srcPath)
			if err != nil {
				return err
			}
			dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
			if err != nil {
				srcFile.Close()
				return err
			}
			_, cpErr := dstFile.ReadFrom(srcFile)
			srcFile.Close()
			dstFile.Close()
			if cpErr != nil {
				return cpErr
			}
		}
	}
	return nil
}

// CloneRootFS copies the rootfs from one VM to a new VM directory on Linux
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

// DestroyVMStorage destroys storage resources for a VM on Linux
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

// mountRootfs mounts the rootfs image (Linux-specific)
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

// umountRootfs unmounts the rootfs image (Linux-specific)
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

// isMounted checks if a mount point is mounted (Linux-specific)
func (m *Manager) isMounted(mountPoint string) bool {
	cmd := exec.Command("mountpoint", mountPoint)
	return cmd.Run() == nil
}

// initializeFilesystem creates the basic directory structure (Linux-specific)
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

// createBasicConfig creates basic configuration files (Linux-specific)
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
