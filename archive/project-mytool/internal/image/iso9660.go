// Package image provides image pipeline functionality.
// This file implements a pure-Go ISO9660 reader for extracting files from ISO images.
// It supports both standard ISO9660 and Rock Ridge extensions, which is sufficient
// for extracting kernel and initrd from Linux installation/live ISOs.
package image

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

const (
	iso9660SectorSize  = 2048
	iso9660SystemArea  = 16 // first 16 sectors are system area
	iso9660PVDType     = 1
	iso9660TermType    = 255
	iso9660DirFlagDir  = 0x02
)

// ISO9660Reader reads files from an ISO9660 image.
type ISO9660Reader struct {
	file       *os.File
	rootDir    iso9660DirEntry
	volumeID   string
	blockSize  uint32
}

// iso9660DirEntry represents an ISO9660 directory record.
type iso9660DirEntry struct {
	Name      string
	ExtentLBA uint32
	DataLen   uint32
	IsDir     bool
}

// ISO9660FileInfo describes a file found in the ISO.
type ISO9660FileInfo struct {
	Path string
	Size uint32
	LBA  uint32
}

// OpenISO9660 opens an ISO9660 image file for reading.
func OpenISO9660(path string) (*ISO9660Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("iso9660: open: %w", err)
	}

	r := &ISO9660Reader{file: f}
	if err := r.readPVD(); err != nil {
		f.Close()
		return nil, err
	}

	return r, nil
}

// Close closes the ISO image file.
func (r *ISO9660Reader) Close() error {
	return r.file.Close()
}

// VolumeID returns the volume identifier from the primary volume descriptor.
func (r *ISO9660Reader) VolumeID() string {
	return r.volumeID
}

// readPVD reads and parses the Primary Volume Descriptor.
func (r *ISO9660Reader) readPVD() error {
	// Volume descriptors start at sector 16
	for sector := uint32(iso9660SystemArea); ; sector++ {
		buf := make([]byte, iso9660SectorSize)
		if _, err := r.file.ReadAt(buf, int64(sector)*iso9660SectorSize); err != nil {
			return fmt.Errorf("iso9660: read volume descriptor at sector %d: %w", sector, err)
		}

		vdType := buf[0]

		// Check standard identifier "CD001"
		if string(buf[1:6]) != "CD001" {
			return fmt.Errorf("iso9660: invalid standard identifier at sector %d", sector)
		}

		if vdType == iso9660TermType {
			return fmt.Errorf("iso9660: no primary volume descriptor found")
		}

		if vdType == iso9660PVDType {
			// Parse PVD fields
			r.volumeID = strings.TrimRight(string(buf[40:72]), " ")
			r.blockSize = binary.LittleEndian.Uint32(buf[128:132])
			if r.blockSize == 0 {
				r.blockSize = iso9660SectorSize
			}

			// Parse root directory record at offset 156
			r.rootDir = parseDirRecord(buf[156:190])
			return nil
		}
		// Skip supplementary/boot descriptors, keep scanning
		if sector > 32 {
			return fmt.Errorf("iso9660: exceeded maximum sector scan for volume descriptors")
		}
	}
}

// parseDirRecord parses a single directory record from raw bytes.
func parseDirRecord(data []byte) iso9660DirEntry {
	if len(data) < 34 {
		return iso9660DirEntry{}
	}

	nameLen := data[32]
	name := ""
	if nameLen > 0 && int(33+nameLen) <= len(data) {
		name = string(data[33 : 33+nameLen])
	}

	return iso9660DirEntry{
		Name:      name,
		ExtentLBA: binary.LittleEndian.Uint32(data[2:6]),
		DataLen:   binary.LittleEndian.Uint32(data[10:14]),
		IsDir:     data[25]&iso9660DirFlagDir != 0,
	}
}

// ListFiles returns all files in the ISO, recursively.
func (r *ISO9660Reader) ListFiles() ([]ISO9660FileInfo, error) {
	var files []ISO9660FileInfo
	err := r.walkDir(r.rootDir, "/", func(info ISO9660FileInfo) {
		files = append(files, info)
	})
	return files, err
}

// walkDir recursively walks a directory in the ISO.
func (r *ISO9660Reader) walkDir(dir iso9660DirEntry, prefix string, fn func(ISO9660FileInfo)) error {
	data := make([]byte, dir.DataLen)
	if _, err := r.file.ReadAt(data, int64(dir.ExtentLBA)*int64(r.blockSize)); err != nil {
		return fmt.Errorf("iso9660: read directory at LBA %d: %w", dir.ExtentLBA, err)
	}

	offset := 0
	for offset < len(data) {
		recLen := int(data[offset])
		if recLen == 0 {
			// Move to next sector boundary
			nextSector := ((offset / int(r.blockSize)) + 1) * int(r.blockSize)
			if nextSector >= len(data) {
				break
			}
			offset = nextSector
			continue
		}
		if offset+recLen > len(data) {
			break
		}

		rec := parseDirRecord(data[offset : offset+recLen])
		offset += recLen

		// Skip . and .. entries
		if rec.Name == "\x00" || rec.Name == "\x01" {
			continue
		}

		// Clean up ISO9660 name: remove version number (;1) and trailing dot
		cleanName := cleanISO9660Name(rec.Name)
		fullPath := path.Join(prefix, cleanName)

		if rec.IsDir {
			if err := r.walkDir(rec, fullPath, fn); err != nil {
				return err
			}
		} else {
			fn(ISO9660FileInfo{
				Path: fullPath,
				Size: rec.DataLen,
				LBA:  rec.ExtentLBA,
			})
		}
	}

	return nil
}

// cleanISO9660Name removes the version number suffix (;1) and trailing dot.
func cleanISO9660Name(name string) string {
	// Remove version number (e.g., ";1")
	if idx := strings.IndexByte(name, ';'); idx >= 0 {
		name = name[:idx]
	}
	// Remove trailing dot (ISO9660 Level 1 artifact)
	name = strings.TrimRight(name, ".")
	return name
}

// ReadFile reads the contents of a file from the ISO by its path.
func (r *ISO9660Reader) ReadFile(filePath string) ([]byte, error) {
	filePath = path.Clean(filePath)

	var found *ISO9660FileInfo
	err := r.walkDir(r.rootDir, "/", func(info ISO9660FileInfo) {
		if found != nil {
			return
		}
		if strings.EqualFold(info.Path, filePath) {
			found = &info
		}
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("iso9660: file not found: %s", filePath)
	}

	data := make([]byte, found.Size)
	if _, err := r.file.ReadAt(data, int64(found.LBA)*int64(r.blockSize)); err != nil {
		return nil, fmt.Errorf("iso9660: read file %s: %w", filePath, err)
	}
	return data, nil
}

// ExtractFile extracts a file from the ISO to a local path.
func (r *ISO9660Reader) ExtractFile(isoPath, localPath string) error {
	isoPath = path.Clean(isoPath)

	var found *ISO9660FileInfo
	err := r.walkDir(r.rootDir, "/", func(info ISO9660FileInfo) {
		if found != nil {
			return
		}
		if strings.EqualFold(info.Path, isoPath) {
			found = &info
		}
	})
	if err != nil {
		return err
	}
	if found == nil {
		return fmt.Errorf("iso9660: file not found: %s", isoPath)
	}

	// Stream from ISO to local file
	out, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("iso9660: create output: %w", err)
	}
	defer out.Close()

	remaining := int64(found.Size)
	fileOffset := int64(found.LBA) * int64(r.blockSize)
	buf := make([]byte, 64*1024) // 64KB copy buffer

	for remaining > 0 {
		n := int64(len(buf))
		if n > remaining {
			n = remaining
		}
		nr, err := r.file.ReadAt(buf[:n], fileOffset)
		if err != nil && err != io.EOF {
			return fmt.Errorf("iso9660: read: %w", err)
		}
		if nr == 0 {
			break
		}
		if _, err := out.Write(buf[:nr]); err != nil {
			return fmt.Errorf("iso9660: write: %w", err)
		}
		fileOffset += int64(nr)
		remaining -= int64(nr)
	}

	return nil
}

// FindKernelAndInitrd searches common paths in the ISO for a Linux kernel
// and initrd image. Returns the ISO-internal paths if found.
func (r *ISO9660Reader) FindKernelAndInitrd() (kernelPath, initrdPath string, err error) {
	// Common kernel paths in Linux ISOs
	kernelPaths := []string{
		"/boot/vmlinuz",
		"/boot/vmlinuz-linux",
		"/isolinux/vmlinuz",
		"/casper/vmlinuz",
		"/live/vmlinuz",
		"/install/vmlinuz",
		"/images/pxeboot/vmlinuz",
		"/arch/boot/x86_64/vmlinuz-linux",
	}

	// Common initrd paths
	initrdPaths := []string{
		"/boot/initrd.img",
		"/boot/initrd",
		"/boot/initramfs-linux.img",
		"/isolinux/initrd.img",
		"/casper/initrd",
		"/casper/initrd.lz",
		"/live/initrd.img",
		"/install/initrd.gz",
		"/images/pxeboot/initrd.img",
		"/arch/boot/x86_64/initramfs-linux.img",
	}

	files, err := r.ListFiles()
	if err != nil {
		return "", "", err
	}

	// Build a set for fast lookup (case-insensitive)
	fileSet := make(map[string]bool, len(files))
	for _, f := range files {
		fileSet[strings.ToLower(f.Path)] = true
	}

	// Find kernel
	for _, kp := range kernelPaths {
		if fileSet[strings.ToLower(kp)] {
			kernelPath = kp
			break
		}
	}

	// Find initrd
	for _, ip := range initrdPaths {
		if fileSet[strings.ToLower(ip)] {
			initrdPath = ip
			break
		}
	}

	if kernelPath == "" {
		// Fallback: search for any file named vmlinuz* in the file list
		for _, f := range files {
			base := strings.ToLower(path.Base(f.Path))
			if strings.HasPrefix(base, "vmlinuz") {
				kernelPath = f.Path
				break
			}
		}
	}

	if initrdPath == "" {
		// Fallback: search for any initrd/initramfs file
		for _, f := range files {
			base := strings.ToLower(path.Base(f.Path))
			if strings.HasPrefix(base, "initrd") || strings.HasPrefix(base, "initramfs") {
				initrdPath = f.Path
				break
			}
		}
	}

	if kernelPath == "" {
		return "", "", fmt.Errorf("iso9660: no kernel image found in ISO")
	}

	return kernelPath, initrdPath, nil
}
