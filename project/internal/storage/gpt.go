// Package storage provides disk management for tent microVMs.
// This file implements a pure-Go GPT (GUID Partition Table) writer for creating
// properly partitioned bootable disk images without requiring system tools.
//
// The GPT layout follows the UEFI specification:
//
//	LBA 0:     Protective MBR
//	LBA 1:     Primary GPT Header
//	LBA 2-33:  Primary Partition Entry Array (128 entries × 128 bytes)
//	LBA 34+:   Partition data
//	LBA -33:   Secondary Partition Entry Array
//	LBA -1:    Secondary GPT Header
//
// This enables creating bootable disk images on macOS without shelling out
// to fdisk, sgdisk, or other Linux-only partition tools.
package storage

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

// Well-known GPT partition type GUIDs (mixed-endian encoding per UEFI spec)
var (
	// GPTTypeEFISystem is the EFI System Partition type GUID
	// C12A7328-F81F-11D2-BA4B-00A0C93EC93B
	GPTTypeEFISystem = GUID{0x28, 0x73, 0x2A, 0xC1, 0x1F, 0xF8, 0xD2, 0x11, 0xBA, 0x4B, 0x00, 0xA0, 0xC9, 0x3E, 0xC9, 0x3B}

	// GPTTypeLinuxFS is the Linux filesystem data type GUID
	// 0FC63DAF-8483-4772-8E79-3D69D8477DE4
	GPTTypeLinuxFS = GUID{0xAF, 0x3D, 0xC6, 0x0F, 0x83, 0x84, 0x72, 0x47, 0x8E, 0x79, 0x3D, 0x69, 0xD8, 0x47, 0x7D, 0xE4}

	// GPTTypeLinuxSwap is the Linux swap partition type GUID
	// 0657FD6D-A4AB-43C4-84E5-0933C84B4F4F
	GPTTypeLinuxSwap = GUID{0x6D, 0xFD, 0x57, 0x06, 0xAB, 0xA4, 0xC4, 0x43, 0x84, 0xE5, 0x09, 0x33, 0xC8, 0x4B, 0x4F, 0x4F}

	// GPTTypeBIOSBoot is the BIOS boot partition type GUID
	// 21686148-6449-6E6F-744E-656564454649
	GPTTypeBIOSBoot = GUID{0x48, 0x61, 0x68, 0x21, 0x49, 0x64, 0x6F, 0x6E, 0x74, 0x4E, 0x65, 0x65, 0x64, 0x45, 0x46, 0x49}
)

// GUID is a 16-byte GUID in mixed-endian format per the UEFI specification.
type GUID [16]byte

// NewRandomGUID generates a random version-4 UUID.
func NewRandomGUID() GUID {
	var g GUID
	_, _ = rand.Read(g[:])
	// Set version 4 (random)
	g[7] = (g[7] & 0x0F) | 0x40
	// Set variant (RFC 4122)
	g[8] = (g[8] & 0x3F) | 0x80
	return g
}

// IsZero returns true if the GUID is all zeros.
func (g GUID) IsZero() bool {
	for _, b := range g {
		if b != 0 {
			return false
		}
	}
	return true
}

const (
	// gptSignature is the EFI PART magic at offset 0 of the GPT header
	gptSignature = 0x5452415020494645 // "EFI PART"

	// gptRevision is the GPT header revision (1.0)
	gptRevision = 0x00010000

	// gptHeaderSize is the standard GPT header size
	gptHeaderSize = 92

	// gptEntrySize is the size of a single partition entry
	gptEntrySize = 128

	// gptMaxEntries is the standard number of partition entries
	gptMaxEntries = 128

	// sectorSize is the standard disk sector size
	sectorSize = 512

	// partitionArraySectors is the number of sectors for the partition array
	// 128 entries × 128 bytes = 16384 bytes = 32 sectors
	partitionArraySectors = 32
)

// GPTHeader represents the on-disk GPT header structure (92 bytes).
type GPTHeader struct {
	Signature      uint64
	Revision       uint32
	HeaderSize     uint32
	HeaderCRC32    uint32
	Reserved       uint32
	MyLBA          uint64
	AlternateLBA   uint64
	FirstUsableLBA uint64
	LastUsableLBA  uint64
	DiskGUID       GUID
	PartitionStart uint64
	NumPartEntries uint32
	PartEntrySize  uint32
	PartArrayCRC32 uint32
}

// GPTPartitionEntry represents a single partition entry (128 bytes).
type GPTPartitionEntry struct {
	TypeGUID   GUID
	UniqueGUID GUID
	StartLBA   uint64
	EndLBA     uint64
	Attributes uint64
	Name       [72]byte // UTF-16LE encoded name (36 UTF-16 code units)
}

// GPTPartition is a user-friendly partition description for creating GPT layouts.
type GPTPartition struct {
	// TypeGUID is the partition type (use GPTTypeLinuxFS, GPTTypeEFISystem, etc.)
	TypeGUID GUID
	// Name is the human-readable partition name (max 36 chars)
	Name string
	// SizeMB is the partition size in megabytes (0 = use remaining space)
	SizeMB uint64
	// Attributes are GPT partition attribute flags
	Attributes uint64
}

// GPTLayout describes a complete disk layout to be written.
type GPTLayout struct {
	// DiskSizeMB is the total disk size in megabytes
	DiskSizeMB uint64
	// Partitions is the ordered list of partitions to create
	Partitions []GPTPartition
}

// GPTDisk holds the result of creating a GPT-partitioned disk.
type GPTDisk struct {
	// Path is the path to the created disk image
	Path string
	// DiskGUID is the disk's unique GUID
	DiskGUID GUID
	// Partitions maps partition index to the created entry details
	Partitions []GPTPartitionResult
	// TotalSectors is the total number of sectors in the disk
	TotalSectors uint64
}

// GPTPartitionResult describes a created partition.
type GPTPartitionResult struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	StartLBA uint64 `json:"start_lba"`
	EndLBA   uint64 `json:"end_lba"`
	SizeMB   uint64 `json:"size_mb"`
}

// CreateGPTDisk creates a new disk image with a GPT partition table at the given path.
func CreateGPTDisk(path string, layout *GPTLayout) (*GPTDisk, error) {
	if layout.DiskSizeMB < 1 {
		return nil, fmt.Errorf("disk size must be at least 1 MB")
	}
	if len(layout.Partitions) == 0 {
		return nil, fmt.Errorf("at least one partition is required")
	}
	if len(layout.Partitions) > gptMaxEntries {
		return nil, fmt.Errorf("too many partitions: %d (max %d)", len(layout.Partitions), gptMaxEntries)
	}

	totalSectors := layout.DiskSizeMB * 1024 * 1024 / sectorSize
	if totalSectors < 2048 {
		return nil, fmt.Errorf("disk too small: need at least 2048 sectors")
	}

	// Create the disk image file
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create disk image: %w", err)
	}
	defer f.Close()

	// Truncate to target size
	if err := f.Truncate(int64(totalSectors * sectorSize)); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("failed to allocate disk: %w", err)
	}

	diskGUID := NewRandomGUID()

	// LBA 0: Protective MBR
	if err := writeProtectiveMBR(f, totalSectors); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("failed to write protective MBR: %w", err)
	}

	// Calculate partition layout
	firstUsableLBA := uint64(2 + partitionArraySectors) // After primary header + entries
	lastUsableLBA := totalSectors - 1 - partitionArraySectors - 1

	partResults, entries, err := layoutPartitions(layout.Partitions, firstUsableLBA, lastUsableLBA)
	if err != nil {
		os.Remove(path)
		return nil, err
	}

	// Serialize partition entries
	entryBytes := serializePartitionEntries(entries)
	entryCRC := crc32.ChecksumIEEE(entryBytes)

	// Primary GPT header at LBA 1
	primaryHeader := GPTHeader{
		Signature:      gptSignature,
		Revision:       gptRevision,
		HeaderSize:     gptHeaderSize,
		MyLBA:          1,
		AlternateLBA:   totalSectors - 1,
		FirstUsableLBA: firstUsableLBA,
		LastUsableLBA:  lastUsableLBA,
		DiskGUID:       diskGUID,
		PartitionStart: 2,
		NumPartEntries: gptMaxEntries,
		PartEntrySize:  gptEntrySize,
		PartArrayCRC32: entryCRC,
	}
	primaryHeader.HeaderCRC32 = computeHeaderCRC(&primaryHeader)

	if err := writeGPTHeader(f, 1, &primaryHeader); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("failed to write primary GPT header: %w", err)
	}

	// Primary partition entries at LBA 2
	if _, err := f.WriteAt(entryBytes, 2*sectorSize); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("failed to write primary partition entries: %w", err)
	}

	// Secondary partition entries
	secondaryEntriesLBA := totalSectors - 1 - partitionArraySectors
	if _, err := f.WriteAt(entryBytes, int64(secondaryEntriesLBA*sectorSize)); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("failed to write secondary partition entries: %w", err)
	}

	// Secondary GPT header at last LBA
	secondaryHeader := primaryHeader
	secondaryHeader.MyLBA = totalSectors - 1
	secondaryHeader.AlternateLBA = 1
	secondaryHeader.PartitionStart = secondaryEntriesLBA
	secondaryHeader.HeaderCRC32 = 0
	secondaryHeader.HeaderCRC32 = computeHeaderCRC(&secondaryHeader)

	if err := writeGPTHeader(f, totalSectors-1, &secondaryHeader); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("failed to write secondary GPT header: %w", err)
	}

	if err := f.Sync(); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("failed to sync disk image: %w", err)
	}

	return &GPTDisk{
		Path:         path,
		DiskGUID:     diskGUID,
		Partitions:   partResults,
		TotalSectors: totalSectors,
	}, nil
}

// ReadGPTHeader reads and validates the GPT header from a disk image.
func ReadGPTHeader(path string) (*GPTHeader, []GPTPartitionEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open disk: %w", err)
	}
	defer f.Close()

	// Read primary header at LBA 1
	headerBuf := make([]byte, sectorSize)
	if _, err := f.ReadAt(headerBuf, sectorSize); err != nil {
		return nil, nil, fmt.Errorf("failed to read GPT header: %w", err)
	}

	var header GPTHeader
	header.Signature = binary.LittleEndian.Uint64(headerBuf[0:8])
	if header.Signature != gptSignature {
		return nil, nil, fmt.Errorf("invalid GPT signature: %x", header.Signature)
	}

	header.Revision = binary.LittleEndian.Uint32(headerBuf[8:12])
	header.HeaderSize = binary.LittleEndian.Uint32(headerBuf[12:16])
	header.HeaderCRC32 = binary.LittleEndian.Uint32(headerBuf[16:20])
	header.MyLBA = binary.LittleEndian.Uint64(headerBuf[24:32])
	header.AlternateLBA = binary.LittleEndian.Uint64(headerBuf[32:40])
	header.FirstUsableLBA = binary.LittleEndian.Uint64(headerBuf[40:48])
	header.LastUsableLBA = binary.LittleEndian.Uint64(headerBuf[48:56])
	copy(header.DiskGUID[:], headerBuf[56:72])
	header.PartitionStart = binary.LittleEndian.Uint64(headerBuf[72:80])
	header.NumPartEntries = binary.LittleEndian.Uint32(headerBuf[80:84])
	header.PartEntrySize = binary.LittleEndian.Uint32(headerBuf[84:88])
	header.PartArrayCRC32 = binary.LittleEndian.Uint32(headerBuf[88:92])

	// Verify header CRC
	storedCRC := header.HeaderCRC32
	header.HeaderCRC32 = 0
	computedCRC := computeHeaderCRC(&header)
	header.HeaderCRC32 = storedCRC
	if storedCRC != computedCRC {
		return nil, nil, fmt.Errorf("GPT header CRC mismatch: stored=%x computed=%x", storedCRC, computedCRC)
	}

	// Read partition entries
	numEntries := header.NumPartEntries
	if numEntries > gptMaxEntries {
		numEntries = gptMaxEntries
	}
	entryArraySize := int64(numEntries) * int64(header.PartEntrySize)
	entryBuf := make([]byte, entryArraySize)
	if _, err := f.ReadAt(entryBuf, int64(header.PartitionStart*sectorSize)); err != nil {
		return nil, nil, fmt.Errorf("failed to read partition entries: %w", err)
	}

	// Verify partition array CRC
	arrayCRC := crc32.ChecksumIEEE(entryBuf)
	if arrayCRC != header.PartArrayCRC32 {
		return nil, nil, fmt.Errorf("partition array CRC mismatch: stored=%x computed=%x",
			header.PartArrayCRC32, arrayCRC)
	}

	// Parse partition entries
	var entries []GPTPartitionEntry
	for i := uint32(0); i < numEntries; i++ {
		offset := int(i) * int(header.PartEntrySize)
		entry := parsePartitionEntry(entryBuf[offset : offset+int(header.PartEntrySize)])
		if !entry.TypeGUID.IsZero() {
			entries = append(entries, entry)
		}
	}

	return &header, entries, nil
}

// layoutPartitions calculates the LBA ranges for each partition.
func layoutPartitions(parts []GPTPartition, firstLBA, lastLBA uint64) ([]GPTPartitionResult, []GPTPartitionEntry, error) {
	currentLBA := firstLBA
	// Align partitions to 1 MiB boundaries (2048 sectors)
	alignSectors := uint64(2048)
	if currentLBA%alignSectors != 0 {
		currentLBA = ((currentLBA / alignSectors) + 1) * alignSectors
	}

	var results []GPTPartitionResult
	var entries []GPTPartitionEntry

	for i, p := range parts {
		if p.TypeGUID.IsZero() {
			return nil, nil, fmt.Errorf("partition %d has zero type GUID", i)
		}

		var sizeSectors uint64
		if p.SizeMB == 0 {
			// Use remaining space
			if currentLBA > lastLBA {
				return nil, nil, fmt.Errorf("no space left for partition %d (%s)", i, p.Name)
			}
			sizeSectors = lastLBA - currentLBA + 1
		} else {
			sizeSectors = p.SizeMB * 1024 * 1024 / sectorSize
		}

		endLBA := currentLBA + sizeSectors - 1
		if endLBA > lastLBA {
			return nil, nil, fmt.Errorf("partition %d (%s) exceeds disk: needs LBA %d but last usable is %d",
				i, p.Name, endLBA, lastLBA)
		}

		entry := GPTPartitionEntry{
			TypeGUID:   p.TypeGUID,
			UniqueGUID: NewRandomGUID(),
			StartLBA:   currentLBA,
			EndLBA:     endLBA,
			Attributes: p.Attributes,
		}
		encodeUTF16Name(&entry, p.Name)

		entries = append(entries, entry)
		results = append(results, GPTPartitionResult{
			Index:    i,
			Name:     p.Name,
			StartLBA: currentLBA,
			EndLBA:   endLBA,
			SizeMB:   sizeSectors * sectorSize / (1024 * 1024),
		})

		// Advance to next aligned boundary
		currentLBA = endLBA + 1
		if currentLBA%alignSectors != 0 {
			currentLBA = ((currentLBA / alignSectors) + 1) * alignSectors
		}
	}

	return results, entries, nil
}

// writeProtectiveMBR writes a protective MBR at LBA 0.
func writeProtectiveMBR(w io.WriterAt, totalSectors uint64) error {
	mbr := make([]byte, sectorSize)

	// Partition entry 1 at offset 446 (protective GPT partition)
	pe := mbr[446:]
	pe[0] = 0x00       // Boot indicator (not bootable)
	pe[1] = 0x00       // Start CHS (0/0/1)
	pe[2] = 0x01       //
	pe[3] = 0x00       //
	pe[4] = 0xEE       // Partition type: GPT protective
	pe[5] = 0xFF       // End CHS (max values)
	pe[6] = 0xFF       //
	pe[7] = 0xFF       //

	// Start LBA = 1 (the GPT header)
	binary.LittleEndian.PutUint32(pe[8:12], 1)

	// Size in sectors (cap at 0xFFFFFFFF for MBR compatibility)
	mbrSectors := totalSectors - 1
	if mbrSectors > 0xFFFFFFFF {
		mbrSectors = 0xFFFFFFFF
	}
	binary.LittleEndian.PutUint32(pe[12:16], uint32(mbrSectors))

	// MBR signature
	mbr[510] = 0x55
	mbr[511] = 0xAA

	_, err := w.WriteAt(mbr, 0)
	return err
}

// writeGPTHeader serializes and writes a GPT header at the given LBA.
func writeGPTHeader(w io.WriterAt, lba uint64, header *GPTHeader) error {
	buf := make([]byte, sectorSize)

	binary.LittleEndian.PutUint64(buf[0:8], header.Signature)
	binary.LittleEndian.PutUint32(buf[8:12], header.Revision)
	binary.LittleEndian.PutUint32(buf[12:16], header.HeaderSize)
	binary.LittleEndian.PutUint32(buf[16:20], header.HeaderCRC32)
	// buf[20:24] = reserved (zeros)
	binary.LittleEndian.PutUint64(buf[24:32], header.MyLBA)
	binary.LittleEndian.PutUint64(buf[32:40], header.AlternateLBA)
	binary.LittleEndian.PutUint64(buf[40:48], header.FirstUsableLBA)
	binary.LittleEndian.PutUint64(buf[48:56], header.LastUsableLBA)
	copy(buf[56:72], header.DiskGUID[:])
	binary.LittleEndian.PutUint64(buf[72:80], header.PartitionStart)
	binary.LittleEndian.PutUint32(buf[80:84], header.NumPartEntries)
	binary.LittleEndian.PutUint32(buf[84:88], header.PartEntrySize)
	binary.LittleEndian.PutUint32(buf[88:92], header.PartArrayCRC32)

	_, err := w.WriteAt(buf, int64(lba*sectorSize))
	return err
}

// serializePartitionEntries serializes all 128 partition entries (including empty ones).
func serializePartitionEntries(entries []GPTPartitionEntry) []byte {
	buf := make([]byte, gptMaxEntries*gptEntrySize)

	for i, entry := range entries {
		offset := i * gptEntrySize
		copy(buf[offset:offset+16], entry.TypeGUID[:])
		copy(buf[offset+16:offset+32], entry.UniqueGUID[:])
		binary.LittleEndian.PutUint64(buf[offset+32:offset+40], entry.StartLBA)
		binary.LittleEndian.PutUint64(buf[offset+40:offset+48], entry.EndLBA)
		binary.LittleEndian.PutUint64(buf[offset+48:offset+56], entry.Attributes)
		copy(buf[offset+56:offset+128], entry.Name[:])
	}

	return buf
}

// parsePartitionEntry deserializes a partition entry from a byte slice.
func parsePartitionEntry(data []byte) GPTPartitionEntry {
	var entry GPTPartitionEntry
	copy(entry.TypeGUID[:], data[0:16])
	copy(entry.UniqueGUID[:], data[16:32])
	entry.StartLBA = binary.LittleEndian.Uint64(data[32:40])
	entry.EndLBA = binary.LittleEndian.Uint64(data[40:48])
	entry.Attributes = binary.LittleEndian.Uint64(data[48:56])
	copy(entry.Name[:], data[56:128])
	return entry
}

// computeHeaderCRC computes the CRC32 of the GPT header (with HeaderCRC32 set to 0).
func computeHeaderCRC(header *GPTHeader) uint32 {
	buf := make([]byte, gptHeaderSize)

	binary.LittleEndian.PutUint64(buf[0:8], header.Signature)
	binary.LittleEndian.PutUint32(buf[8:12], header.Revision)
	binary.LittleEndian.PutUint32(buf[12:16], header.HeaderSize)
	// buf[16:20] = 0 (HeaderCRC32 zeroed for computation)
	// buf[20:24] = 0 (Reserved)
	binary.LittleEndian.PutUint64(buf[24:32], header.MyLBA)
	binary.LittleEndian.PutUint64(buf[32:40], header.AlternateLBA)
	binary.LittleEndian.PutUint64(buf[40:48], header.FirstUsableLBA)
	binary.LittleEndian.PutUint64(buf[48:56], header.LastUsableLBA)
	copy(buf[56:72], header.DiskGUID[:])
	binary.LittleEndian.PutUint64(buf[72:80], header.PartitionStart)
	binary.LittleEndian.PutUint32(buf[80:84], header.NumPartEntries)
	binary.LittleEndian.PutUint32(buf[84:88], header.PartEntrySize)
	binary.LittleEndian.PutUint32(buf[88:92], header.PartArrayCRC32)

	return crc32.ChecksumIEEE(buf)
}

// encodeUTF16Name encodes a partition name as UTF-16LE into the entry.
func encodeUTF16Name(entry *GPTPartitionEntry, name string) {
	// Truncate to 36 characters (72 bytes / 2 bytes per UTF-16 code unit)
	runes := []rune(name)
	if len(runes) > 36 {
		runes = runes[:36]
	}

	for i, r := range runes {
		if r > 0xFFFF {
			r = 0xFFFD // replacement character for non-BMP
		}
		offset := i * 2
		binary.LittleEndian.PutUint16(entry.Name[offset:offset+2], uint16(r))
	}
}

// DecodeUTF16Name decodes a UTF-16LE partition name from an entry.
func DecodeUTF16Name(entry *GPTPartitionEntry) string {
	var runes []rune
	for i := 0; i < len(entry.Name); i += 2 {
		codeUnit := binary.LittleEndian.Uint16(entry.Name[i : i+2])
		if codeUnit == 0 {
			break
		}
		runes = append(runes, rune(codeUnit))
	}
	return string(runes)
}

// CreateBootableDisk creates a typical bootable disk layout with an EFI system
// partition and a root filesystem partition. This is the standard layout for
// microVM disk images.
func CreateBootableDisk(path string, diskSizeMB uint64, efiSizeMB uint64) (*GPTDisk, error) {
	if efiSizeMB == 0 {
		efiSizeMB = 64 // Default 64 MB EFI partition
	}
	if diskSizeMB <= efiSizeMB+2 {
		return nil, fmt.Errorf("disk too small: need at least %d MB for EFI + root", efiSizeMB+2)
	}

	layout := &GPTLayout{
		DiskSizeMB: diskSizeMB,
		Partitions: []GPTPartition{
			{
				TypeGUID: GPTTypeEFISystem,
				Name:     "EFI System",
				SizeMB:   efiSizeMB,
			},
			{
				TypeGUID: GPTTypeLinuxFS,
				Name:     "tent-root",
				SizeMB:   0, // Use remaining space
			},
		},
	}

	return CreateGPTDisk(path, layout)
}
