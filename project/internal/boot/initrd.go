// Package boot provides Linux boot protocol support.
// This file implements initramfs (initrd) building for microVM boot.
// It creates minimal cpio archives in the newc format that contain
// just enough to mount the root filesystem, set up devices, and
// pivot into the real root. This avoids depending on external tools
// like mkinitramfs or dracut.
package boot

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// cpio newc format constants
const (
	cpioMagic     = "070701"
	cpioTrailer   = "TRAILER!!!"
	cpioBlockSize = 512
)

// InitrdBuilder creates minimal initramfs archives for microVM boot.
type InitrdBuilder struct {
	entries  []cpioEntry
	compress bool
}

// cpioEntry represents a single file/directory in the cpio archive.
type cpioEntry struct {
	name    string
	mode    uint32
	uid     uint32
	gid     uint32
	nlink   uint32
	mtime   uint32
	devMaj  uint32
	devMin  uint32
	rdevMaj uint32
	rdevMin uint32
	data    []byte
}

// InitrdConfig configures the generated initramfs.
type InitrdConfig struct {
	// RootFSType is the filesystem type for the root mount (e.g., "ext4", "9p", "virtiofs")
	RootFSType string
	// RootDevice is the root device path (e.g., "/dev/vda", "rootfs")
	RootDevice string
	// RootFlags are mount flags for the root filesystem
	RootFlags string
	// ExtraModules are additional kernel module names to load
	ExtraModules []string
	// ExtraFiles maps guest path -> host path for additional files to include
	ExtraFiles map[string]string
	// InitScript is a custom /init script (overrides the generated one)
	InitScript string
	// Compress enables gzip compression of the output
	Compress bool
	// Hostname sets /etc/hostname in the initramfs
	Hostname string
	// ExtraDirectories are additional directories to create
	ExtraDirectories []string
}

// NewInitrdBuilder creates a new initramfs builder.
func NewInitrdBuilder(compress bool) *InitrdBuilder {
	return &InitrdBuilder{
		compress: compress,
	}
}

// AddDirectory adds a directory entry to the archive.
func (b *InitrdBuilder) AddDirectory(path string, mode uint32) {
	b.entries = append(b.entries, cpioEntry{
		name:  strings.TrimPrefix(path, "/"),
		mode:  0040000 | (mode & 07777), // S_IFDIR
		nlink: 2,
		mtime: uint32(time.Now().Unix()),
	})
}

// AddFile adds a regular file entry with the given content.
func (b *InitrdBuilder) AddFile(path string, mode uint32, data []byte) {
	b.entries = append(b.entries, cpioEntry{
		name:  strings.TrimPrefix(path, "/"),
		mode:  0100000 | (mode & 07777), // S_IFREG
		nlink: 1,
		mtime: uint32(time.Now().Unix()),
		data:  data,
	})
}

// AddSymlink adds a symbolic link entry.
func (b *InitrdBuilder) AddSymlink(path, target string) {
	b.entries = append(b.entries, cpioEntry{
		name:  strings.TrimPrefix(path, "/"),
		mode:  0120000 | 0777, // S_IFLNK
		nlink: 1,
		mtime: uint32(time.Now().Unix()),
		data:  []byte(target),
	})
}

// AddCharDev adds a character device node.
func (b *InitrdBuilder) AddCharDev(path string, mode uint32, major, minor uint32) {
	b.entries = append(b.entries, cpioEntry{
		name:    strings.TrimPrefix(path, "/"),
		mode:    0020000 | (mode & 07777), // S_IFCHR
		nlink:   1,
		mtime:   uint32(time.Now().Unix()),
		rdevMaj: major,
		rdevMin: minor,
	})
}

// AddHostFile reads a file from the host and adds it to the archive.
func (b *InitrdBuilder) AddHostFile(guestPath, hostPath string, mode uint32) error {
	data, err := os.ReadFile(hostPath)
	if err != nil {
		return fmt.Errorf("read host file %q: %w", hostPath, err)
	}
	b.AddFile(guestPath, mode, data)
	return nil
}

// Build serializes the cpio archive and returns its bytes.
func (b *InitrdBuilder) Build() ([]byte, error) {
	var buf bytes.Buffer

	// Sort entries to ensure deterministic output — directories before files
	sorted := make([]cpioEntry, len(b.entries))
	copy(sorted, b.entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].name < sorted[j].name
	})

	var ino uint32
	for _, entry := range sorted {
		ino++
		if err := writeCpioNewcEntry(&buf, ino, &entry); err != nil {
			return nil, fmt.Errorf("write cpio entry %q: %w", entry.name, err)
		}
	}

	// Write trailer
	ino++
	trailer := &cpioEntry{
		name:  cpioTrailer,
		nlink: 1,
	}
	if err := writeCpioNewcEntry(&buf, ino, trailer); err != nil {
		return nil, fmt.Errorf("write cpio trailer: %w", err)
	}

	// Pad to block boundary
	if remainder := buf.Len() % cpioBlockSize; remainder != 0 {
		padding := make([]byte, cpioBlockSize-remainder)
		buf.Write(padding)
	}

	if !b.compress {
		return buf.Bytes(), nil
	}

	// Gzip compress
	var compressed bytes.Buffer
	gz, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("create gzip writer: %w", err)
	}
	if _, err := io.Copy(gz, &buf); err != nil {
		return nil, fmt.Errorf("gzip compress: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}

	return compressed.Bytes(), nil
}

// writeCpioNewcEntry writes a single entry in cpio newc format.
func writeCpioNewcEntry(w *bytes.Buffer, ino uint32, entry *cpioEntry) error {
	nameBytes := []byte(entry.name)
	nameSize := len(nameBytes) + 1 // include null terminator

	// Header is 110 bytes: 6 magic + 13 * 8-char hex fields
	header := fmt.Sprintf("%s%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		cpioMagic,
		ino,
		entry.mode,
		entry.uid,
		entry.gid,
		entry.nlink,
		entry.mtime,
		len(entry.data),   // filesize
		entry.devMaj,      // devmajor
		entry.devMin,      // devminor
		entry.rdevMaj,     // rdevmajor
		entry.rdevMin,     // rdevminor
		nameSize,          // namesize
		0,                 // check (always 0 for newc)
	)

	w.WriteString(header)
	w.Write(nameBytes)
	w.WriteByte(0) // null terminator

	// Pad name+header to 4-byte boundary
	headerTotal := 110 + nameSize
	if pad := (4 - headerTotal%4) % 4; pad > 0 {
		w.Write(make([]byte, pad))
	}

	// Write file data
	if len(entry.data) > 0 {
		w.Write(entry.data)
		// Pad data to 4-byte boundary
		if pad := (4 - len(entry.data)%4) % 4; pad > 0 {
			w.Write(make([]byte, pad))
		}
	}

	return nil
}

// BuildMicroVMInitrd creates a complete minimal initramfs suitable for
// booting a tent microVM. It includes:
// - /init script that mounts essential filesystems, loads virtio modules,
//   mounts the root filesystem, and pivots into it
// - Essential device nodes (console, null, zero, urandom)
// - Required directory structure
func BuildMicroVMInitrd(cfg *InitrdConfig) ([]byte, error) {
	if cfg == nil {
		cfg = &InitrdConfig{}
	}

	// Apply defaults
	if cfg.RootFSType == "" {
		cfg.RootFSType = "ext4"
	}
	if cfg.RootDevice == "" {
		cfg.RootDevice = "/dev/vda"
	}

	builder := NewInitrdBuilder(cfg.Compress)

	// Create directory structure
	dirs := []string{
		"bin", "dev", "etc", "lib", "mnt", "newroot",
		"proc", "run", "sbin", "sys", "tmp",
		"usr", "usr/bin", "usr/sbin", "usr/lib",
	}
	for _, d := range cfg.ExtraDirectories {
		dirs = append(dirs, strings.TrimPrefix(d, "/"))
	}
	// Deduplicate
	seen := make(map[string]bool)
	for _, d := range dirs {
		if !seen[d] {
			builder.AddDirectory(d, 0755)
			seen[d] = true
		}
	}
	// /tmp needs sticky bit
	builder.AddDirectory("tmp", 01777)

	// Essential device nodes
	builder.AddCharDev("dev/console", 0600, 5, 1)
	builder.AddCharDev("dev/null", 0666, 1, 3)
	builder.AddCharDev("dev/zero", 0666, 1, 5)
	builder.AddCharDev("dev/urandom", 0444, 1, 9)
	builder.AddCharDev("dev/tty", 0666, 5, 0)
	builder.AddCharDev("dev/ttyS0", 0660, 4, 64) // serial console

	// /etc/hostname
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = "tent"
	}
	builder.AddFile("etc/hostname", 0644, []byte(hostname+"\n"))

	// Generate /init script
	initScript := cfg.InitScript
	if initScript == "" {
		initScript = generateInitScript(cfg)
	}
	builder.AddFile("init", 0755, []byte(initScript))

	// Convenience symlinks
	builder.AddSymlink("sbin/init", "/init")

	// Add extra files from host
	for guestPath, hostPath := range cfg.ExtraFiles {
		if err := builder.AddHostFile(guestPath, hostPath, 0755); err != nil {
			return nil, fmt.Errorf("add extra file %q: %w", guestPath, err)
		}
	}

	return builder.Build()
}

// generateInitScript creates the /init shell script for microVM boot.
func generateInitScript(cfg *InitrdConfig) string {
	var sb strings.Builder

	sb.WriteString("#!/bin/sh\n")
	sb.WriteString("# tent microVM init — generated by tent initrd builder\n")
	sb.WriteString("set -e\n\n")

	// Mount essential pseudo-filesystems
	sb.WriteString("# Mount essential filesystems\n")
	sb.WriteString("mount -t proc proc /proc\n")
	sb.WriteString("mount -t sysfs sysfs /sys\n")
	sb.WriteString("mount -t devtmpfs devtmpfs /dev 2>/dev/null || true\n")
	sb.WriteString("mount -t tmpfs tmpfs /run\n\n")

	// Print boot message
	sb.WriteString("echo \"tent: initramfs starting\"\n\n")

	// Load virtio modules if available
	sb.WriteString("# Load virtio modules\n")
	sb.WriteString("for mod in virtio virtio_pci virtio_blk virtio_net virtio_console virtio_ring")
	for _, mod := range cfg.ExtraModules {
		sb.WriteString(" " + mod)
	}
	sb.WriteString("; do\n")
	sb.WriteString("    modprobe \"$mod\" 2>/dev/null || true\n")
	sb.WriteString("done\n\n")

	// Wait for root device
	sb.WriteString("# Wait for root device\n")
	sb.WriteString(fmt.Sprintf("echo \"tent: waiting for root device %s\"\n", cfg.RootDevice))
	sb.WriteString("retries=0\n")
	sb.WriteString(fmt.Sprintf("while [ ! -e \"%s\" ] && [ \"$retries\" -lt 50 ]; do\n", cfg.RootDevice))
	sb.WriteString("    sleep 0.1\n")
	sb.WriteString("    retries=$((retries + 1))\n")
	sb.WriteString("done\n\n")

	sb.WriteString(fmt.Sprintf("if [ ! -e \"%s\" ]; then\n", cfg.RootDevice))
	sb.WriteString(fmt.Sprintf("    echo \"tent: ERROR: root device %s not found\"\n", cfg.RootDevice))
	sb.WriteString("    echo \"tent: available block devices:\"\n")
	sb.WriteString("    ls -la /dev/vd* /dev/sd* 2>/dev/null || true\n")
	sb.WriteString("    exec /bin/sh\n")
	sb.WriteString("fi\n\n")

	// Mount root filesystem
	mountFlags := cfg.RootFlags
	flagsOpt := ""
	if mountFlags != "" {
		flagsOpt = fmt.Sprintf(" -o %s", mountFlags)
	}
	sb.WriteString("# Mount root filesystem\n")
	sb.WriteString(fmt.Sprintf("echo \"tent: mounting root (%s on %s)\"\n", cfg.RootFSType, cfg.RootDevice))
	sb.WriteString(fmt.Sprintf("mount -t %s%s %s /newroot\n\n", cfg.RootFSType, flagsOpt, cfg.RootDevice))

	// Set hostname
	sb.WriteString("# Set hostname\n")
	sb.WriteString("if [ -f /etc/hostname ]; then\n")
	sb.WriteString("    hostname \"$(cat /etc/hostname)\"\n")
	sb.WriteString("fi\n\n")

	// Move mount points
	sb.WriteString("# Move pseudo-filesystems to new root\n")
	sb.WriteString("mkdir -p /newroot/proc /newroot/sys /newroot/dev /newroot/run\n")
	sb.WriteString("mount --move /proc /newroot/proc\n")
	sb.WriteString("mount --move /sys /newroot/sys\n")
	sb.WriteString("mount --move /dev /newroot/dev\n")
	sb.WriteString("mount --move /run /newroot/run\n\n")

	// Pivot root
	sb.WriteString("# Switch to real root\n")
	sb.WriteString("echo \"tent: switching to root filesystem\"\n")
	sb.WriteString("cd /newroot\n")
	sb.WriteString("mkdir -p oldroot\n")
	sb.WriteString("pivot_root . oldroot\n\n")

	// Clean up old root
	sb.WriteString("# Clean up initramfs mount\n")
	sb.WriteString("umount /oldroot 2>/dev/null || true\n")
	sb.WriteString("rmdir /oldroot 2>/dev/null || true\n\n")

	// Exec real init
	sb.WriteString("# Exec real init\n")
	sb.WriteString("echo \"tent: executing /sbin/init\"\n")
	sb.WriteString("exec /sbin/init \"$@\"\n")

	return sb.String()
}

// BuildInitrdFile creates a minimal initramfs and writes it to the given path.
func BuildInitrdFile(path string, cfg *InitrdConfig) error {
	data, err := BuildMicroVMInitrd(cfg)
	if err != nil {
		return fmt.Errorf("build initrd: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write initrd: %w", err)
	}

	return nil
}

// InitrdInfo contains metadata about a generated initrd.
type InitrdInfo struct {
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	Compressed   bool   `json:"compressed"`
	EntryCount   int    `json:"entry_count"`
	RootFSType   string `json:"rootfs_type"`
	RootDevice   string `json:"root_device"`
}

// ParseCpioHeader reads the header of a cpio newc archive and returns
// entry count and whether it looks valid.
func ParseCpioHeader(data []byte) (int, bool) {
	if len(data) < 110 {
		return 0, false
	}

	// Check for newc magic
	if string(data[:6]) != cpioMagic {
		// Check for gzip header — may be compressed
		if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
			return 0, true // compressed but valid
		}
		return 0, false
	}

	// Count entries by scanning through the archive
	count := 0
	offset := 0
	for offset+110 <= len(data) {
		if string(data[offset:offset+6]) != cpioMagic {
			break
		}

		// Parse namesize and filesize from hex
		namesizeHex := string(data[offset+94 : offset+102])
		filesizeHex := string(data[offset+54 : offset+62])

		var namesize, filesize uint32
		fmt.Sscanf(namesizeHex, "%08X", &namesize)
		fmt.Sscanf(filesizeHex, "%08X", &filesize)

		// Check for trailer
		nameStart := offset + 110
		if nameStart+int(namesize) <= len(data) {
			name := string(data[nameStart : nameStart+int(namesize)-1])
			if name == cpioTrailer {
				break
			}
		}

		count++

		// Advance past header + name (padded to 4 bytes)
		headerAndName := 110 + int(namesize)
		headerAndName = (headerAndName + 3) &^ 3

		// Advance past data (padded to 4 bytes)
		dataSize := int(filesize)
		dataSize = (dataSize + 3) &^ 3

		offset += headerAndName + dataSize
	}

	return count, true
}

// InspectInitrd reads an initrd file and returns information about it.
func InspectInitrd(path string) (*InitrdInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read initrd: %w", err)
	}

	info := &InitrdInfo{
		Path: path,
		Size: int64(len(data)),
	}

	// Check if gzip compressed
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		info.Compressed = true
	}

	count, valid := ParseCpioHeader(data)
	if !valid {
		return nil, fmt.Errorf("not a valid cpio/initrd archive")
	}
	info.EntryCount = count

	return info, nil
}

