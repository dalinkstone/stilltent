package storage

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// QCOW2 format constants
const (
	qcow2Magic   uint32 = 0x514649FB // "QFI\xfb"
	qcow2Version uint32 = 3

	// Cluster sizes
	defaultClusterBits = 16                          // 64KB clusters
	defaultClusterSize = 1 << defaultClusterBits     // 65536
	l2EntriesPerTable  = defaultClusterSize / 8      // 8192 entries per L2 table
	l1EntrySize        = 8

	// L2 entry flags
	l2EntryCompressed uint64 = 1 << 62
	l2EntryCopied     uint64 = 1 << 63
	l2OffsetMask      uint64 = 0x00FFFFFFFFFFFE00 // bits 9-55

	// Refcount
	defaultRefcountOrder = 4 // 16-bit refcounts
)

// QCOW2Header represents the on-disk header of a QCOW2 image
type QCOW2Header struct {
	Magic                 uint32
	Version               uint32
	BackingFileOffset     uint64
	BackingFileSize       uint32
	ClusterBits           uint32
	Size                  uint64 // Virtual size in bytes
	CryptMethod           uint32
	L1Size                uint32
	L1TableOffset         uint64
	RefcountTableOffset   uint64
	RefcountTableClusters uint32
	NbSnapshots           uint32
	SnapshotsOffset       uint64
	// V3 fields
	IncompatibleFeatures uint64
	CompatibleFeatures   uint64
	AutoclearFeatures    uint64
	RefcountOrder        uint32
	HeaderLength         uint32
}

// QCOW2Image represents an open QCOW2 disk image with CoW support
type QCOW2Image struct {
	mu          sync.RWMutex
	file        *os.File
	header      QCOW2Header
	backingFile string
	backing     *QCOW2Image // Opened backing image for CoW reads
	clusterSize uint64
	l1Table     []uint64
	l2Cache     map[uint64][]uint64 // L1 index -> L2 table
}

// CreateQCOW2 creates a new QCOW2 image file
func CreateQCOW2(path string, virtualSizeBytes uint64, backingFile string) error {
	clusterSize := uint64(defaultClusterSize)
	l2Entries := clusterSize / 8
	l1Size := (virtualSizeBytes + (l2Entries * clusterSize) - 1) / (l2Entries * clusterSize)

	header := QCOW2Header{
		Magic:                 qcow2Magic,
		Version:               qcow2Version,
		ClusterBits:           defaultClusterBits,
		Size:                  virtualSizeBytes,
		CryptMethod:           0,
		L1Size:                uint32(l1Size),
		RefcountOrder:         defaultRefcountOrder,
		HeaderLength:          104,
		IncompatibleFeatures:  0,
		CompatibleFeatures:    0,
		AutoclearFeatures:     0,
	}

	// Layout: cluster 0 = header, cluster 1 = L1 table, cluster 2 = refcount table
	headerCluster := uint64(0)
	_ = headerCluster
	l1Offset := clusterSize       // cluster 1
	refcountOffset := clusterSize * 2 // cluster 2

	header.L1TableOffset = l1Offset
	header.RefcountTableOffset = refcountOffset
	header.RefcountTableClusters = 1

	// Handle backing file
	backingBytes := []byte(backingFile)
	if backingFile != "" {
		header.BackingFileOffset = 104 // right after v3 header
		header.BackingFileSize = uint32(len(backingBytes))
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create qcow2 file: %w", err)
	}
	defer f.Close()

	// Write header
	if err := binary.Write(f, binary.BigEndian, &header); err != nil {
		return fmt.Errorf("failed to write qcow2 header: %w", err)
	}

	// Write backing file name after header
	if backingFile != "" {
		if _, err := f.WriteAt(backingBytes, int64(header.BackingFileOffset)); err != nil {
			return fmt.Errorf("failed to write backing file path: %w", err)
		}
	}

	// Zero-fill L1 table (cluster 1)
	l1Bytes := make([]byte, l1Size*l1EntrySize)
	if _, err := f.WriteAt(l1Bytes, int64(l1Offset)); err != nil {
		return fmt.Errorf("failed to write L1 table: %w", err)
	}

	// Zero-fill refcount table (cluster 2)
	refcountBytes := make([]byte, clusterSize)
	if _, err := f.WriteAt(refcountBytes, int64(refcountOffset)); err != nil {
		return fmt.Errorf("failed to write refcount table: %w", err)
	}

	// Extend file to at least 3 clusters
	if err := f.Truncate(int64(clusterSize * 3)); err != nil {
		return fmt.Errorf("failed to extend qcow2 file: %w", err)
	}

	return nil
}

// OpenQCOW2 opens an existing QCOW2 image
func OpenQCOW2(path string) (*QCOW2Image, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open qcow2 file: %w", err)
	}

	img := &QCOW2Image{
		file:    f,
		l2Cache: make(map[uint64][]uint64),
	}

	// Read header
	if err := binary.Read(f, binary.BigEndian, &img.header); err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to read qcow2 header: %w", err)
	}

	if img.header.Magic != qcow2Magic {
		f.Close()
		return nil, fmt.Errorf("not a qcow2 file: bad magic 0x%08x", img.header.Magic)
	}

	if img.header.Version < 2 || img.header.Version > 3 {
		f.Close()
		return nil, fmt.Errorf("unsupported qcow2 version: %d", img.header.Version)
	}

	img.clusterSize = 1 << img.header.ClusterBits

	// Read backing file path
	if img.header.BackingFileOffset > 0 && img.header.BackingFileSize > 0 {
		backingBytes := make([]byte, img.header.BackingFileSize)
		if _, err := f.ReadAt(backingBytes, int64(img.header.BackingFileOffset)); err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to read backing file path: %w", err)
		}
		img.backingFile = string(backingBytes)
	}

	// Read L1 table
	img.l1Table = make([]uint64, img.header.L1Size)
	if _, err := f.Seek(int64(img.header.L1TableOffset), io.SeekStart); err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to seek to L1 table: %w", err)
	}
	if err := binary.Read(f, binary.BigEndian, &img.l1Table); err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to read L1 table: %w", err)
	}

	// Open backing image if present
	if img.backingFile != "" {
		// Resolve relative paths against the directory of the image
		backingPath := img.backingFile
		if !filepath.IsAbs(backingPath) {
			backingPath = filepath.Join(filepath.Dir(path), backingPath)
		}
		backing, err := OpenQCOW2(backingPath)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to open backing file %s: %w", backingPath, err)
		}
		img.backing = backing
	}

	return img, nil
}

// Close closes the QCOW2 image and its backing chain
func (img *QCOW2Image) Close() error {
	img.mu.Lock()
	defer img.mu.Unlock()

	if img.backing != nil {
		img.backing.Close()
	}
	return img.file.Close()
}

// VirtualSize returns the virtual disk size in bytes
func (img *QCOW2Image) VirtualSize() uint64 {
	return img.header.Size
}

// BackingFile returns the backing file path, if any
func (img *QCOW2Image) BackingFile() string {
	return img.backingFile
}

// HasBacking returns true if this image has a backing file
func (img *QCOW2Image) HasBacking() bool {
	return img.backingFile != ""
}

// ReadCluster reads a cluster from the image, following the backing chain if needed
func (img *QCOW2Image) ReadCluster(clusterIndex uint64) ([]byte, error) {
	img.mu.RLock()
	defer img.mu.RUnlock()

	return img.readClusterLocked(clusterIndex)
}

func (img *QCOW2Image) readClusterLocked(clusterIndex uint64) ([]byte, error) {
	// Calculate L1 and L2 indices
	l2Entries := img.clusterSize / 8
	l1Index := clusterIndex / l2Entries
	l2Index := clusterIndex % l2Entries

	if l1Index >= uint64(img.header.L1Size) {
		return make([]byte, img.clusterSize), nil // Beyond virtual size, return zeros
	}

	l1Entry := img.l1Table[l1Index]
	if l1Entry == 0 {
		// No L2 table allocated — cluster is unallocated
		if img.backing != nil {
			// Use the backing image's public ReadCluster to acquire its own lock
			return img.backing.ReadCluster(clusterIndex)
		}
		return make([]byte, img.clusterSize), nil
	}

	// Read L2 table (with caching)
	l2Table, err := img.getL2Table(l1Index, l1Entry)
	if err != nil {
		return nil, err
	}

	l2Entry := l2Table[l2Index]
	if l2Entry == 0 {
		// Cluster not allocated in this layer
		if img.backing != nil {
			// Use the backing image's public ReadCluster to acquire its own lock
			return img.backing.ReadCluster(clusterIndex)
		}
		return make([]byte, img.clusterSize), nil
	}

	// Read the actual cluster data
	dataOffset := l2Entry & l2OffsetMask
	buf := make([]byte, img.clusterSize)
	if _, err := img.file.ReadAt(buf, int64(dataOffset)); err != nil {
		return nil, fmt.Errorf("failed to read cluster data at offset %d: %w", dataOffset, err)
	}

	return buf, nil
}

// WriteCluster writes a cluster to the image (copy-on-write)
func (img *QCOW2Image) WriteCluster(clusterIndex uint64, data []byte) error {
	img.mu.Lock()
	defer img.mu.Unlock()

	if uint64(len(data)) != img.clusterSize {
		return fmt.Errorf("data size %d does not match cluster size %d", len(data), img.clusterSize)
	}

	l2Entries := img.clusterSize / 8
	l1Index := clusterIndex / l2Entries
	l2Index := clusterIndex % l2Entries

	if l1Index >= uint64(img.header.L1Size) {
		return fmt.Errorf("cluster index %d beyond virtual size", clusterIndex)
	}

	// Ensure L2 table exists
	l1Entry := img.l1Table[l1Index]
	var l2Table []uint64
	var err error

	if l1Entry == 0 {
		// Allocate a new L2 table
		l2Table, l1Entry, err = img.allocateL2Table(l1Index)
		if err != nil {
			return err
		}
	} else {
		l2Table, err = img.getL2Table(l1Index, l1Entry)
		if err != nil {
			return err
		}
	}

	// Allocate a new data cluster
	dataOffset, err := img.allocateCluster()
	if err != nil {
		return err
	}

	// Write data to the new cluster
	if _, err := img.file.WriteAt(data, int64(dataOffset)); err != nil {
		return fmt.Errorf("failed to write cluster data: %w", err)
	}

	// Update L2 entry with the new cluster offset and copied flag
	l2Table[l2Index] = dataOffset | l2EntryCopied

	// Write L2 entry back to disk
	l2Offset := l1Entry & l2OffsetMask
	entryBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(entryBuf, l2Table[l2Index])
	if _, err := img.file.WriteAt(entryBuf, int64(l2Offset)+int64(l2Index*8)); err != nil {
		return fmt.Errorf("failed to write L2 entry: %w", err)
	}

	return nil
}

// ReadAt reads data at a byte offset within the virtual disk
func (img *QCOW2Image) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || uint64(off) >= img.header.Size {
		return 0, io.EOF
	}

	totalRead := 0
	for len(p) > 0 && uint64(off) < img.header.Size {
		clusterIndex := uint64(off) / img.clusterSize
		clusterOffset := uint64(off) % img.clusterSize

		cluster, err := img.ReadCluster(clusterIndex)
		if err != nil {
			return totalRead, err
		}

		n := copy(p, cluster[clusterOffset:])
		p = p[n:]
		off += int64(n)
		totalRead += n
	}

	if totalRead == 0 {
		return 0, io.EOF
	}
	return totalRead, nil
}

// WriteAt writes data at a byte offset within the virtual disk
func (img *QCOW2Image) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || uint64(off) >= img.header.Size {
		return 0, fmt.Errorf("offset %d out of range", off)
	}

	totalWritten := 0
	for len(p) > 0 && uint64(off) < img.header.Size {
		clusterIndex := uint64(off) / img.clusterSize
		clusterOffset := uint64(off) % img.clusterSize

		// Read-modify-write for partial cluster writes
		cluster, err := img.ReadCluster(clusterIndex)
		if err != nil {
			return totalWritten, err
		}

		n := copy(cluster[clusterOffset:], p)
		if err := img.WriteCluster(clusterIndex, cluster); err != nil {
			return totalWritten, err
		}

		p = p[n:]
		off += int64(n)
		totalWritten += n
	}

	return totalWritten, nil
}

// getL2Table reads or returns a cached L2 table
func (img *QCOW2Image) getL2Table(l1Index, l1Entry uint64) ([]uint64, error) {
	if cached, ok := img.l2Cache[l1Index]; ok {
		return cached, nil
	}

	l2Offset := l1Entry & l2OffsetMask
	entries := img.clusterSize / 8
	l2Table := make([]uint64, entries)

	buf := make([]byte, img.clusterSize)
	if _, err := img.file.ReadAt(buf, int64(l2Offset)); err != nil {
		return nil, fmt.Errorf("failed to read L2 table: %w", err)
	}

	for i := uint64(0); i < entries; i++ {
		l2Table[i] = binary.BigEndian.Uint64(buf[i*8:])
	}

	// Cache with size limit
	if len(img.l2Cache) > 256 {
		// Evict a random entry
		for k := range img.l2Cache {
			delete(img.l2Cache, k)
			break
		}
	}
	img.l2Cache[l1Index] = l2Table
	return l2Table, nil
}

// allocateL2Table allocates a new L2 table cluster
func (img *QCOW2Image) allocateL2Table(l1Index uint64) ([]uint64, uint64, error) {
	offset, err := img.allocateCluster()
	if err != nil {
		return nil, 0, err
	}

	// Zero-fill the new L2 table cluster
	zeros := make([]byte, img.clusterSize)
	if _, err := img.file.WriteAt(zeros, int64(offset)); err != nil {
		return nil, 0, fmt.Errorf("failed to zero L2 table: %w", err)
	}

	// Update L1 entry
	l1Entry := offset | l2EntryCopied
	img.l1Table[l1Index] = l1Entry

	// Write L1 entry to disk
	entryBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(entryBuf, l1Entry)
	l1DiskOffset := int64(img.header.L1TableOffset) + int64(l1Index*8)
	if _, err := img.file.WriteAt(entryBuf, l1DiskOffset); err != nil {
		return nil, 0, fmt.Errorf("failed to write L1 entry: %w", err)
	}

	// Create in-memory L2 table
	entries := img.clusterSize / 8
	l2Table := make([]uint64, entries)
	img.l2Cache[l1Index] = l2Table

	return l2Table, l1Entry, nil
}

// allocateCluster finds the next free cluster by extending the file
func (img *QCOW2Image) allocateCluster() (uint64, error) {
	fi, err := img.file.Stat()
	if err != nil {
		return 0, fmt.Errorf("failed to stat file: %w", err)
	}

	// Align to cluster boundary
	currentSize := uint64(fi.Size())
	offset := (currentSize + img.clusterSize - 1) & ^(img.clusterSize - 1)

	// Extend the file
	if err := img.file.Truncate(int64(offset + img.clusterSize)); err != nil {
		return 0, fmt.Errorf("failed to extend file: %w", err)
	}

	return offset, nil
}

// CreateOverlay creates a new QCOW2 image that uses the given image as its backing file
func (m *Manager) CreateOverlay(vmName string, baseImagePath string) (string, error) {
	// Resolve base image to absolute path
	absBase, err := filepath.Abs(baseImagePath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base image path: %w", err)
	}

	// Verify base image exists
	if _, err := os.Stat(absBase); os.IsNotExist(err) {
		return "", fmt.Errorf("base image not found: %s", absBase)
	}

	// Create overlay directory
	overlayDir := filepath.Join(m.baseDir, "rootfs", vmName)
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create overlay directory: %w", err)
	}

	overlayPath := filepath.Join(overlayDir, "overlay.qcow2")

	// Get virtual size from base image
	virtualSize, err := getImageVirtualSize(absBase)
	if err != nil {
		return "", fmt.Errorf("failed to get base image size: %w", err)
	}

	// Create the overlay QCOW2 with backing file reference
	if err := CreateQCOW2(overlayPath, virtualSize, absBase); err != nil {
		return "", fmt.Errorf("failed to create overlay: %w", err)
	}

	return overlayPath, nil
}

// CreateSnapshotQCOW2 creates a CoW snapshot by making the current image
// a backing file for a new overlay
func (m *Manager) CreateSnapshotQCOW2(vmName string, tag string) (string, error) {
	rootfsDir := filepath.Join(m.baseDir, "rootfs", vmName)
	currentImage := filepath.Join(rootfsDir, "overlay.qcow2")

	// Fall back to raw image if no overlay exists
	if _, err := os.Stat(currentImage); os.IsNotExist(err) {
		currentImage = filepath.Join(rootfsDir, "rootfs.img")
		if _, err := os.Stat(currentImage); os.IsNotExist(err) {
			return "", fmt.Errorf("no disk image found for VM %s", vmName)
		}
	}

	// Create snapshot directory
	snapshotDir := filepath.Join(m.baseDir, "snapshots", vmName)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create snapshot directory: %w", err)
	}

	// Copy the current overlay as the snapshot point
	snapshotPath := filepath.Join(snapshotDir, fmt.Sprintf("%s.qcow2", tag))
	if err := m.copyFile(currentImage, snapshotPath); err != nil {
		return "", fmt.Errorf("failed to save snapshot: %w", err)
	}

	return snapshotPath, nil
}

// getImageVirtualSize returns the virtual size of a QCOW2 or raw image
func getImageVirtualSize(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	// Check if it's a QCOW2 file
	var magic uint32
	if err := binary.Read(f, binary.BigEndian, &magic); err != nil {
		return 0, err
	}

	if magic == qcow2Magic {
		// Read the size field from the header
		var header QCOW2Header
		f.Seek(0, io.SeekStart)
		if err := binary.Read(f, binary.BigEndian, &header); err != nil {
			return 0, err
		}
		return header.Size, nil
	}

	// For raw images, use the file size
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return uint64(fi.Size()), nil
}

// IsQCOW2 checks if a file is in QCOW2 format
func IsQCOW2(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var magic uint32
	if err := binary.Read(f, binary.BigEndian, &magic); err != nil {
		return false
	}
	return magic == qcow2Magic
}

// QCOW2Info returns information about a QCOW2 image
type QCOW2Info struct {
	VirtualSizeMB int    `json:"virtual_size_mb"`
	ActualSizeMB  int    `json:"actual_size_mb"`
	ClusterSize   int    `json:"cluster_size"`
	BackingFile   string `json:"backing_file,omitempty"`
	Version       int    `json:"version"`
	HasBacking    bool   `json:"has_backing"`
}

// InspectQCOW2 returns metadata about a QCOW2 image without fully opening it
func InspectQCOW2(path string) (*QCOW2Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	var header QCOW2Header
	if err := binary.Read(f, binary.BigEndian, &header); err != nil {
		return nil, fmt.Errorf("failed to read header: %w", err)
	}

	if header.Magic != qcow2Magic {
		return nil, fmt.Errorf("not a qcow2 file")
	}

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	info := &QCOW2Info{
		VirtualSizeMB: int(header.Size / (1024 * 1024)),
		ActualSizeMB:  int(fi.Size() / (1024 * 1024)),
		ClusterSize:   1 << header.ClusterBits,
		Version:       int(header.Version),
	}

	if header.BackingFileOffset > 0 && header.BackingFileSize > 0 {
		backingBytes := make([]byte, header.BackingFileSize)
		if _, err := f.ReadAt(backingBytes, int64(header.BackingFileOffset)); err == nil {
			info.BackingFile = string(backingBytes)
			info.HasBacking = true
		}
	}

	return info, nil
}

// ImageFormat represents a disk image format.
type ImageFormat string

const (
	FormatRaw   ImageFormat = "raw"
	FormatQCOW2 ImageFormat = "qcow2"
)

// DetectFormat detects the image format of a file.
func DetectFormat(path string) (ImageFormat, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	var magic uint32
	if err := binary.Read(f, binary.BigEndian, &magic); err != nil {
		return "", fmt.Errorf("read magic: %w", err)
	}
	if magic == qcow2Magic {
		return FormatQCOW2, nil
	}
	return FormatRaw, nil
}

// ConvertResult holds the result of an image conversion.
type ConvertResult struct {
	SourcePath   string      `json:"source_path"`
	SourceFormat ImageFormat `json:"source_format"`
	OutputPath   string      `json:"output_path"`
	OutputFormat ImageFormat `json:"output_format"`
	SourceBytes  int64       `json:"source_bytes"`
	OutputBytes  int64       `json:"output_bytes"`
	VirtualSize  uint64      `json:"virtual_size"`
}

// ConvertImage converts a disk image between raw and qcow2 formats.
// It reads the source image and writes it to outputPath in the target format.
// The flatten flag causes qcow2 images with backing files to be fully resolved.
func ConvertImage(srcPath, dstPath string, targetFormat ImageFormat, flatten bool) (*ConvertResult, error) {
	srcFormat, err := DetectFormat(srcPath)
	if err != nil {
		return nil, fmt.Errorf("detect source format: %w", err)
	}

	if srcFormat == targetFormat && !flatten {
		return nil, fmt.Errorf("source is already in %s format; use a different target format or --flatten", targetFormat)
	}

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return nil, fmt.Errorf("stat source: %w", err)
	}

	switch {
	case srcFormat == FormatQCOW2 && targetFormat == FormatRaw:
		return convertQCOW2ToRaw(srcPath, dstPath, srcInfo.Size())
	case srcFormat == FormatRaw && targetFormat == FormatQCOW2:
		return convertRawToQCOW2(srcPath, dstPath, srcInfo.Size())
	case srcFormat == FormatQCOW2 && targetFormat == FormatQCOW2 && flatten:
		return convertQCOW2Flatten(srcPath, dstPath, srcInfo.Size())
	default:
		return nil, fmt.Errorf("unsupported conversion: %s -> %s", srcFormat, targetFormat)
	}
}

// convertQCOW2ToRaw converts a QCOW2 image to raw format.
func convertQCOW2ToRaw(srcPath, dstPath string, srcSize int64) (*ConvertResult, error) {
	img, err := OpenQCOW2(srcPath)
	if err != nil {
		return nil, fmt.Errorf("open qcow2: %w", err)
	}
	defer img.Close()

	virtualSize := img.VirtualSize()

	dst, err := os.Create(dstPath)
	if err != nil {
		return nil, fmt.Errorf("create output: %w", err)
	}
	defer dst.Close()

	if err := dst.Truncate(int64(virtualSize)); err != nil {
		return nil, fmt.Errorf("truncate output: %w", err)
	}

	// Read cluster by cluster and write non-zero clusters to raw output
	clusterSize := uint64(defaultClusterSize)
	totalClusters := (virtualSize + clusterSize - 1) / clusterSize
	buf := make([]byte, clusterSize)

	for i := uint64(0); i < totalClusters; i++ {
		data, err := img.ReadCluster(i)
		if err != nil {
			return nil, fmt.Errorf("read cluster %d: %w", i, err)
		}
		if data == nil {
			continue // unallocated cluster, raw file is already zero
		}
		copy(buf, data)
		offset := int64(i * clusterSize)
		remaining := int64(virtualSize) - offset
		writeLen := int64(clusterSize)
		if remaining < writeLen {
			writeLen = remaining
		}
		if _, err := dst.WriteAt(buf[:writeLen], offset); err != nil {
			return nil, fmt.Errorf("write at offset %d: %w", offset, err)
		}
	}

	dstInfo, err := dst.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat output: %w", err)
	}

	return &ConvertResult{
		SourcePath:   srcPath,
		SourceFormat: FormatQCOW2,
		OutputPath:   dstPath,
		OutputFormat: FormatRaw,
		SourceBytes:  srcSize,
		OutputBytes:  dstInfo.Size(),
		VirtualSize:  virtualSize,
	}, nil
}

// convertRawToQCOW2 converts a raw image to QCOW2 format.
func convertRawToQCOW2(srcPath, dstPath string, srcSize int64) (*ConvertResult, error) {
	virtualSize := uint64(srcSize)

	if err := CreateQCOW2(dstPath, virtualSize, ""); err != nil {
		return nil, fmt.Errorf("create qcow2: %w", err)
	}

	img, err := OpenQCOW2(dstPath)
	if err != nil {
		os.Remove(dstPath)
		return nil, fmt.Errorf("open new qcow2: %w", err)
	}
	defer img.Close()

	src, err := os.Open(srcPath)
	if err != nil {
		os.Remove(dstPath)
		return nil, fmt.Errorf("open source: %w", err)
	}
	defer src.Close()

	clusterSize := uint64(defaultClusterSize)
	totalClusters := (virtualSize + clusterSize - 1) / clusterSize
	buf := make([]byte, clusterSize)
	zero := make([]byte, clusterSize)

	for i := uint64(0); i < totalClusters; i++ {
		offset := int64(i * clusterSize)
		readLen := clusterSize
		remaining := virtualSize - (i * clusterSize)
		if remaining < readLen {
			readLen = remaining
		}

		n, err := src.ReadAt(buf[:readLen], offset)
		if err != nil && err != io.EOF {
			os.Remove(dstPath)
			return nil, fmt.Errorf("read source at offset %d: %w", offset, err)
		}
		// Zero out any extra bytes
		for j := n; j < int(clusterSize); j++ {
			buf[j] = 0
		}

		// Skip all-zero clusters to keep qcow2 sparse
		if string(buf[:clusterSize]) == string(zero) {
			continue
		}

		if err := img.WriteCluster(i, buf[:clusterSize]); err != nil {
			os.Remove(dstPath)
			return nil, fmt.Errorf("write cluster %d: %w", i, err)
		}
	}

	dstInfo, err := os.Stat(dstPath)
	if err != nil {
		return nil, fmt.Errorf("stat output: %w", err)
	}

	return &ConvertResult{
		SourcePath:   srcPath,
		SourceFormat: FormatRaw,
		OutputPath:   dstPath,
		OutputFormat: FormatQCOW2,
		SourceBytes:  srcSize,
		OutputBytes:  dstInfo.Size(),
		VirtualSize:  virtualSize,
	}, nil
}

// convertQCOW2Flatten converts a QCOW2 image with backing to a standalone QCOW2.
func convertQCOW2Flatten(srcPath, dstPath string, srcSize int64) (*ConvertResult, error) {
	img, err := OpenQCOW2(srcPath)
	if err != nil {
		return nil, fmt.Errorf("open qcow2: %w", err)
	}
	defer img.Close()

	virtualSize := img.VirtualSize()

	if err := CreateQCOW2(dstPath, virtualSize, ""); err != nil {
		return nil, fmt.Errorf("create output qcow2: %w", err)
	}

	dst, err := OpenQCOW2(dstPath)
	if err != nil {
		os.Remove(dstPath)
		return nil, fmt.Errorf("open output qcow2: %w", err)
	}
	defer dst.Close()

	clusterSize := uint64(defaultClusterSize)
	totalClusters := (virtualSize + clusterSize - 1) / clusterSize
	zero := make([]byte, clusterSize)

	for i := uint64(0); i < totalClusters; i++ {
		data, err := img.ReadCluster(i)
		if err != nil {
			os.Remove(dstPath)
			return nil, fmt.Errorf("read cluster %d: %w", i, err)
		}
		if data == nil || string(data) == string(zero) {
			continue
		}
		if err := dst.WriteCluster(i, data); err != nil {
			os.Remove(dstPath)
			return nil, fmt.Errorf("write cluster %d: %w", i, err)
		}
	}

	dstInfo, err := os.Stat(dstPath)
	if err != nil {
		return nil, fmt.Errorf("stat output: %w", err)
	}

	return &ConvertResult{
		SourcePath:   srcPath,
		SourceFormat: FormatQCOW2,
		OutputPath:   dstPath,
		OutputFormat: FormatQCOW2,
		SourceBytes:  srcSize,
		OutputBytes:  dstInfo.Size(),
		VirtualSize:  virtualSize,
	}, nil
}
