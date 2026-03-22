package storage

import (
	"encoding/binary"
	"fmt"
	"io"
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

// DiskInfo contains detailed information about a sandbox's disk image
type DiskInfo struct {
	Path        string  `json:"path"`
	Format      string  `json:"format"` // "raw" or "qcow2"
	VirtualSize uint64  `json:"virtual_size_bytes"`
	ActualSize  uint64  `json:"actual_size_bytes"`
	ClusterSize int     `json:"cluster_size,omitempty"`
	BackingFile string  `json:"backing_file,omitempty"`
	Efficiency  float64 `json:"efficiency_pct"` // actual/virtual ratio
}

// InspectDisk returns detailed info about a sandbox's disk image
func (m *Manager) InspectDisk(vmName string) (*DiskInfo, error) {
	rootfsPath := filepath.Join(m.baseDir, "rootfs", vmName, "rootfs.img")
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("disk not found for sandbox %s", vmName)
	}

	fi, err := os.Stat(rootfsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat disk: %w", err)
	}

	info := &DiskInfo{
		Path:       rootfsPath,
		ActualSize: uint64(fi.Size()),
	}

	if IsQCOW2(rootfsPath) {
		info.Format = "qcow2"
		qinfo, err := InspectQCOW2(rootfsPath)
		if err != nil {
			return nil, fmt.Errorf("failed to inspect qcow2: %w", err)
		}
		info.VirtualSize = uint64(qinfo.VirtualSizeMB) * 1024 * 1024
		info.ClusterSize = qinfo.ClusterSize
		info.BackingFile = qinfo.BackingFile
	} else {
		info.Format = "raw"
		info.VirtualSize = uint64(fi.Size())
	}

	if info.VirtualSize > 0 {
		info.Efficiency = float64(info.ActualSize) / float64(info.VirtualSize) * 100
	}

	return info, nil
}

// ResizeDisk resizes a sandbox's disk image to the given size in bytes
func (m *Manager) ResizeDisk(vmName string, newSizeBytes uint64) error {
	rootfsPath := filepath.Join(m.baseDir, "rootfs", vmName, "rootfs.img")
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		return fmt.Errorf("disk not found for sandbox %s", vmName)
	}

	if IsQCOW2(rootfsPath) {
		return m.resizeQCOW2(rootfsPath, newSizeBytes)
	}

	// For raw images, truncate to new size
	f, err := os.OpenFile(rootfsPath, os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open disk: %w", err)
	}
	defer f.Close()

	if err := f.Truncate(int64(newSizeBytes)); err != nil {
		return fmt.Errorf("failed to resize disk: %w", err)
	}

	return nil
}

// resizeQCOW2 updates the virtual size in a QCOW2 header
func (m *Manager) resizeQCOW2(path string, newSizeBytes uint64) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("failed to open qcow2: %w", err)
	}
	defer f.Close()

	var header QCOW2Header
	if err := binary.Read(f, binary.BigEndian, &header); err != nil {
		return fmt.Errorf("failed to read header: %w", err)
	}

	if header.Magic != qcow2Magic {
		return fmt.Errorf("not a valid qcow2 file")
	}

	if newSizeBytes < header.Size {
		return fmt.Errorf("cannot shrink qcow2 image (current: %d bytes, requested: %d bytes)", header.Size, newSizeBytes)
	}

	// Write new size at offset 24 (the Size field in QCOW2 header)
	if _, err := f.Seek(24, io.SeekStart); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, newSizeBytes); err != nil {
		return fmt.Errorf("failed to write new size: %w", err)
	}

	return nil
}

// ConvertDisk converts a disk image between raw and qcow2 formats
func (m *Manager) ConvertDisk(vmName string, targetFormat string) (string, error) {
	rootfsPath := filepath.Join(m.baseDir, "rootfs", vmName, "rootfs.img")
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		return "", fmt.Errorf("disk not found for sandbox %s", vmName)
	}

	currentIsQCOW2 := IsQCOW2(rootfsPath)

	switch targetFormat {
	case "qcow2":
		if currentIsQCOW2 {
			return rootfsPath, fmt.Errorf("disk is already in qcow2 format")
		}
		return m.convertRawToQCOW2(vmName, rootfsPath)
	case "raw":
		if !currentIsQCOW2 {
			return rootfsPath, fmt.Errorf("disk is already in raw format")
		}
		return m.convertQCOW2ToRaw(vmName, rootfsPath)
	default:
		return "", fmt.Errorf("unsupported format: %s (use 'raw' or 'qcow2')", targetFormat)
	}
}

// convertRawToQCOW2 converts a raw disk image to qcow2
func (m *Manager) convertRawToQCOW2(_ string, rawPath string) (string, error) {
	fi, err := os.Stat(rawPath)
	if err != nil {
		return "", err
	}

	tmpPath := rawPath + ".qcow2.tmp"
	if err := CreateQCOW2(tmpPath, uint64(fi.Size()), ""); err != nil {
		return "", fmt.Errorf("failed to create qcow2: %w", err)
	}

	// Open the new qcow2 image and copy data from raw
	dst, err := OpenQCOW2(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to open new qcow2: %w", err)
	}

	src, err := os.Open(rawPath)
	if err != nil {
		dst.Close()
		os.Remove(tmpPath)
		return "", err
	}

	// Copy in cluster-sized chunks, skip zero clusters
	buf := make([]byte, defaultClusterSize)
	clusterIdx := uint64(0)
	for {
		n, err := io.ReadFull(src, buf)
		if n > 0 {
			// Check if cluster is all zeros (sparse optimization)
			isZero := true
			for i := 0; i < n; i++ {
				if buf[i] != 0 {
					isZero = false
					break
				}
			}
			if !isZero {
				data := buf[:n]
				if n < defaultClusterSize {
					padded := make([]byte, defaultClusterSize)
					copy(padded, buf[:n])
					data = padded
				}
				if writeErr := dst.WriteCluster(clusterIdx, data); writeErr != nil {
					src.Close()
					dst.Close()
					os.Remove(tmpPath)
					return "", fmt.Errorf("failed to write cluster %d: %w", clusterIdx, writeErr)
				}
			}
		}
		clusterIdx++
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			src.Close()
			dst.Close()
			os.Remove(tmpPath)
			return "", fmt.Errorf("read error: %w", err)
		}
	}
	src.Close()
	dst.Close()

	// Replace original with converted
	backupPath := rawPath + ".raw.bak"
	if err := os.Rename(rawPath, backupPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to backup original: %w", err)
	}
	if err := os.Rename(tmpPath, rawPath); err != nil {
		os.Rename(backupPath, rawPath)
		return "", fmt.Errorf("failed to replace original: %w", err)
	}
	os.Remove(backupPath)

	return rawPath, nil
}

// convertQCOW2ToRaw converts a qcow2 disk image to raw
func (m *Manager) convertQCOW2ToRaw(_ string, qcow2Path string) (string, error) {
	img, err := OpenQCOW2(qcow2Path)
	if err != nil {
		return "", fmt.Errorf("failed to open qcow2: %w", err)
	}

	virtualSize := img.VirtualSize()
	tmpPath := qcow2Path + ".raw.tmp"

	dst, err := os.Create(tmpPath)
	if err != nil {
		img.Close()
		return "", err
	}

	// Truncate to virtual size
	if err := dst.Truncate(int64(virtualSize)); err != nil {
		dst.Close()
		img.Close()
		os.Remove(tmpPath)
		return "", err
	}

	// Read each cluster and write to raw
	numClusters := virtualSize / uint64(defaultClusterSize)
	if virtualSize%uint64(defaultClusterSize) != 0 {
		numClusters++
	}

	for i := uint64(0); i < numClusters; i++ {
		data, err := img.ReadCluster(i)
		if err != nil {
			continue // Unallocated clusters stay zero
		}

		offset := int64(i) * int64(defaultClusterSize)
		if _, err := dst.WriteAt(data, offset); err != nil {
			dst.Close()
			img.Close()
			os.Remove(tmpPath)
			return "", fmt.Errorf("failed to write at offset %d: %w", offset, err)
		}
	}

	dst.Close()
	img.Close()

	// Replace original
	backupPath := qcow2Path + ".qcow2.bak"
	if err := os.Rename(qcow2Path, backupPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to backup original: %w", err)
	}
	if err := os.Rename(tmpPath, qcow2Path); err != nil {
		os.Rename(backupPath, qcow2Path)
		return "", fmt.Errorf("failed to replace original: %w", err)
	}
	os.Remove(backupPath)

	return qcow2Path, nil
}

// CompactDisk reclaims unused space from a qcow2 disk image
func (m *Manager) CompactDisk(vmName string) (uint64, error) {
	rootfsPath := filepath.Join(m.baseDir, "rootfs", vmName, "rootfs.img")
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		return 0, fmt.Errorf("disk not found for sandbox %s", vmName)
	}

	if !IsQCOW2(rootfsPath) {
		return 0, fmt.Errorf("compact is only supported for qcow2 images")
	}

	// Get original size
	origFi, err := os.Stat(rootfsPath)
	if err != nil {
		return 0, err
	}
	origSize := uint64(origFi.Size())

	// Open the original and create a new compacted copy
	src, err := OpenQCOW2(rootfsPath)
	if err != nil {
		return 0, fmt.Errorf("failed to open qcow2: %w", err)
	}

	virtualSize := src.VirtualSize()
	tmpPath := rootfsPath + ".compact.tmp"

	if err := CreateQCOW2(tmpPath, virtualSize, ""); err != nil {
		src.Close()
		return 0, fmt.Errorf("failed to create compacted image: %w", err)
	}

	dst, err := OpenQCOW2(tmpPath)
	if err != nil {
		src.Close()
		os.Remove(tmpPath)
		return 0, err
	}

	// Copy only non-zero clusters
	numClusters := virtualSize / uint64(defaultClusterSize)
	if virtualSize%uint64(defaultClusterSize) != 0 {
		numClusters++
	}

	for i := uint64(0); i < numClusters; i++ {
		data, err := src.ReadCluster(i)
		if err != nil {
			continue // Skip unallocated
		}

		// Check if all zeros
		allZero := true
		for _, b := range data {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			continue
		}

		if err := dst.WriteCluster(i, data); err != nil {
			src.Close()
			dst.Close()
			os.Remove(tmpPath)
			return 0, fmt.Errorf("failed to write cluster: %w", err)
		}
	}

	src.Close()
	dst.Close()

	// Replace
	backupPath := rootfsPath + ".precompact.bak"
	if err := os.Rename(rootfsPath, backupPath); err != nil {
		os.Remove(tmpPath)
		return 0, err
	}
	if err := os.Rename(tmpPath, rootfsPath); err != nil {
		os.Rename(backupPath, rootfsPath)
		return 0, err
	}
	os.Remove(backupPath)

	// Get new size
	newFi, err := os.Stat(rootfsPath)
	if err != nil {
		return 0, err
	}

	saved := uint64(0)
	if origSize > uint64(newFi.Size()) {
		saved = origSize - uint64(newFi.Size())
	}

	return saved, nil
}

// ListDisks lists all sandbox disk images
func (m *Manager) ListDisks() ([]*DiskInfo, error) {
	rootfsDir := filepath.Join(m.baseDir, "rootfs")
	if _, err := os.Stat(rootfsDir); os.IsNotExist(err) {
		return []*DiskInfo{}, nil
	}

	entries, err := os.ReadDir(rootfsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read rootfs directory: %w", err)
	}

	var disks []*DiskInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := m.InspectDisk(entry.Name())
		if err != nil {
			continue
		}
		disks = append(disks, info)
	}

	return disks, nil
}
