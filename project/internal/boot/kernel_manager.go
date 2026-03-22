// Package boot provides kernel management for microVM boot.
// This file implements kernel discovery, cataloging, and metadata inspection.
package boot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// KernelEntry represents a locally stored kernel.
type KernelEntry struct {
	// Version is the kernel version string (e.g., "6.1.0", "5.15.0")
	Version string `json:"version"`
	// Path is the absolute path to the kernel image file
	Path string `json:"path"`
	// InitrdPath is the optional path to a matching initrd
	InitrdPath string `json:"initrd_path,omitempty"`
	// Format is the detected kernel format (bzImage, Image, raw)
	Format string `json:"format"`
	// Arch is the detected architecture (x86_64, arm64)
	Arch string `json:"arch"`
	// Size is the kernel file size in bytes
	Size int64 `json:"size"`
	// SHA256 is the hex-encoded SHA-256 hash
	SHA256 string `json:"sha256"`
	// AddedAt is when the kernel was added to the store
	AddedAt time.Time `json:"added_at"`
	// Default marks this as the default kernel
	Default bool `json:"default,omitempty"`
	// Labels are user-defined metadata
	Labels map[string]string `json:"labels,omitempty"`
}

// KernelStore manages a local collection of kernels.
type KernelStore struct {
	baseDir  string
	kernDir  string
	manifest string
}

// KernelManifest is the on-disk catalog of known kernels.
type KernelManifest struct {
	Kernels        []KernelEntry `json:"kernels"`
	DefaultVersion string        `json:"default_version,omitempty"`
}

// KernelInspection holds detailed info from inspecting a kernel image.
type KernelInspection struct {
	Path          string            `json:"path"`
	Size          int64             `json:"size"`
	SHA256        string            `json:"sha256"`
	IsBzImage     bool              `json:"is_bzimage"`
	ProtoVersion  string            `json:"proto_version,omitempty"`
	SetupSects    uint8             `json:"setup_sects,omitempty"`
	ProtModeSize  int               `json:"prot_mode_size,omitempty"`
	SetupDataSize int               `json:"setup_data_size,omitempty"`
	KernelAlign   uint32            `json:"kernel_alignment,omitempty"`
	LoadFlags     uint8             `json:"load_flags,omitempty"`
	Format        string            `json:"format"`
	Bootable      bool              `json:"bootable"`
	Details       map[string]string `json:"details,omitempty"`
}

// NewKernelStore creates a kernel store at the given base directory.
func NewKernelStore(baseDir string) (*KernelStore, error) {
	kernDir := filepath.Join(baseDir, "kernels")
	if err := os.MkdirAll(kernDir, 0755); err != nil {
		return nil, fmt.Errorf("create kernel directory: %w", err)
	}
	return &KernelStore{
		baseDir:  baseDir,
		kernDir:  kernDir,
		manifest: filepath.Join(kernDir, "manifest.json"),
	}, nil
}

// loadManifest reads the manifest from disk.
func (ks *KernelStore) loadManifest() (*KernelManifest, error) {
	data, err := os.ReadFile(ks.manifest)
	if err != nil {
		if os.IsNotExist(err) {
			return &KernelManifest{}, nil
		}
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m KernelManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

// saveManifest writes the manifest to disk.
func (ks *KernelStore) saveManifest(m *KernelManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return os.WriteFile(ks.manifest, data, 0644)
}

// List returns all known kernels, sorted by version descending.
func (ks *KernelStore) List() ([]KernelEntry, error) {
	m, err := ks.loadManifest()
	if err != nil {
		return nil, err
	}
	entries := make([]KernelEntry, len(m.Kernels))
	copy(entries, m.Kernels)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Version > entries[j].Version
	})
	// Mark default
	for i := range entries {
		if entries[i].Version == m.DefaultVersion {
			entries[i].Default = true
		}
	}
	return entries, nil
}

// Add imports a kernel file into the store.
func (ks *KernelStore) Add(srcPath, version string, labels map[string]string) (*KernelEntry, error) {
	info, err := os.Stat(srcPath)
	if err != nil {
		return nil, fmt.Errorf("stat kernel: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%q is a directory", srcPath)
	}

	// Read and hash
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("read kernel: %w", err)
	}
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	// Auto-detect version from filename if not provided
	if version == "" {
		version = detectVersionFromPath(srcPath)
	}
	if version == "" {
		version = hashStr[:12] // fallback to hash prefix
	}

	// Detect format
	format, arch := detectKernelFormat(data)

	// Copy to store
	destDir := filepath.Join(ks.kernDir, version)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("create kernel dir: %w", err)
	}
	destPath := filepath.Join(destDir, filepath.Base(srcPath))
	if err := os.WriteFile(destPath, data, 0644); err != nil {
		return nil, fmt.Errorf("write kernel: %w", err)
	}

	entry := &KernelEntry{
		Version: version,
		Path:    destPath,
		Format:  format,
		Arch:    arch,
		Size:    info.Size(),
		SHA256:  hashStr,
		AddedAt: time.Now().UTC(),
		Labels:  labels,
	}

	// Check for initrd alongside kernel
	dir := filepath.Dir(srcPath)
	for _, name := range []string{"initrd", "initrd.img", "initramfs.img", "initrd.gz"} {
		initrdPath := filepath.Join(dir, name)
		if _, err := os.Stat(initrdPath); err == nil {
			destInitrd := filepath.Join(destDir, name)
			initrdData, err := os.ReadFile(initrdPath)
			if err == nil {
				if err := os.WriteFile(destInitrd, initrdData, 0644); err == nil {
					entry.InitrdPath = destInitrd
				}
			}
			break
		}
	}

	// Update manifest
	m, err := ks.loadManifest()
	if err != nil {
		return nil, err
	}

	// Replace existing entry with same version
	found := false
	for i, e := range m.Kernels {
		if e.Version == version {
			m.Kernels[i] = *entry
			found = true
			break
		}
	}
	if !found {
		m.Kernels = append(m.Kernels, *entry)
	}

	// Set as default if it's the first kernel
	if m.DefaultVersion == "" {
		m.DefaultVersion = version
		entry.Default = true
	}

	return entry, ks.saveManifest(m)
}

// Remove deletes a kernel from the store.
func (ks *KernelStore) Remove(version string) error {
	m, err := ks.loadManifest()
	if err != nil {
		return err
	}

	found := false
	for i, e := range m.Kernels {
		if e.Version == version {
			// Remove kernel directory
			dir := filepath.Dir(e.Path)
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("remove kernel files: %w", err)
			}
			m.Kernels = append(m.Kernels[:i], m.Kernels[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("kernel version %q not found", version)
	}

	// Clear default if we just removed it
	if m.DefaultVersion == version {
		m.DefaultVersion = ""
		if len(m.Kernels) > 0 {
			m.DefaultVersion = m.Kernels[0].Version
		}
	}

	return ks.saveManifest(m)
}

// SetDefault sets the default kernel version.
func (ks *KernelStore) SetDefault(version string) error {
	m, err := ks.loadManifest()
	if err != nil {
		return err
	}
	found := false
	for _, e := range m.Kernels {
		if e.Version == version {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("kernel version %q not found", version)
	}
	m.DefaultVersion = version
	return ks.saveManifest(m)
}

// GetDefault returns the default kernel, if any.
func (ks *KernelStore) GetDefault() (*KernelEntry, error) {
	m, err := ks.loadManifest()
	if err != nil {
		return nil, err
	}
	if m.DefaultVersion == "" {
		return nil, fmt.Errorf("no default kernel set")
	}
	for _, e := range m.Kernels {
		if e.Version == m.DefaultVersion {
			e.Default = true
			return &e, nil
		}
	}
	return nil, fmt.Errorf("default kernel %q not found in manifest", m.DefaultVersion)
}

// Get returns a specific kernel entry by version.
func (ks *KernelStore) Get(version string) (*KernelEntry, error) {
	m, err := ks.loadManifest()
	if err != nil {
		return nil, err
	}
	for _, e := range m.Kernels {
		if e.Version == version {
			if e.Version == m.DefaultVersion {
				e.Default = true
			}
			return &e, nil
		}
	}
	return nil, fmt.Errorf("kernel version %q not found", version)
}

// Inspect examines a kernel image file and returns detailed information.
func Inspect(path string) (*KernelInspection, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	hash := sha256.Sum256(data)
	format, _ := detectKernelFormat(data)

	inspection := &KernelInspection{
		Path:    path,
		Size:    info.Size(),
		SHA256:  hex.EncodeToString(hash[:]),
		Format:  format,
		Details: make(map[string]string),
	}

	// Try parsing bzImage header
	hdr, err := ParseBzImageHeader(data)
	if err == nil && hdr != nil {
		inspection.IsBzImage = true
		inspection.Bootable = true
		inspection.ProtoVersion = fmt.Sprintf("%d.%d", hdr.ProtoVersion>>8, hdr.ProtoVersion&0xFF)
		inspection.SetupSects = hdr.SetupSects
		inspection.ProtModeSize = hdr.ProtModeSize
		inspection.SetupDataSize = hdr.SetupDataSize
		inspection.KernelAlign = hdr.KernelAlign
		inspection.LoadFlags = hdr.LoadFlags

		inspection.Details["loaded_high"] = fmt.Sprintf("%v", hdr.LoadFlags&LoadedHigh != 0)
		inspection.Details["can_use_heap"] = fmt.Sprintf("%v", hdr.LoadFlags&CanUseHeap != 0)
		inspection.Details["keep_segments"] = fmt.Sprintf("%v", hdr.LoadFlags&KeepSegments != 0)

		if hdr.ProtoVersion >= ProtoVersion2_10 {
			inspection.Details["relocatable"] = "true"
		}
	} else {
		// Check for ARM64 Image header (magic at offset 56)
		if len(data) > 64 {
			arm64Magic := string(data[56:60])
			if arm64Magic == "ARM\x64" {
				inspection.Format = "Image (ARM64)"
				inspection.Bootable = true
				inspection.Details["arch"] = "arm64"
			}
		}
	}

	return inspection, nil
}

// detectKernelFormat tries to identify the kernel image format.
func detectKernelFormat(data []byte) (format, arch string) {
	if len(data) < 512 {
		return "unknown", "unknown"
	}

	// Check bzImage (x86_64)
	if len(data) > 0x260 {
		magic := uint32(data[BootMagicOff]) | uint32(data[BootMagicOff+1])<<8 |
			uint32(data[BootMagicOff+2])<<16 | uint32(data[BootMagicOff+3])<<24
		if magic == BootMagic {
			return "bzImage", "x86_64"
		}
	}

	// Check ARM64 Image header
	if len(data) > 64 && string(data[56:60]) == "ARM\x64" {
		return "Image", "arm64"
	}

	// Check ELF
	if len(data) > 18 && string(data[:4]) == "\x7fELF" {
		archStr := "unknown"
		if data[4] == 2 { // 64-bit
			machine := uint16(data[18]) | uint16(data[19])<<8
			switch machine {
			case 0x3E:
				archStr = "x86_64"
			case 0xB7:
				archStr = "arm64"
			}
		}
		return "ELF", archStr
	}

	// Check gzip (compressed kernel)
	if data[0] == 0x1f && data[1] == 0x8b {
		return "gzip", "unknown"
	}

	return "raw", "unknown"
}

// detectVersionFromPath tries to extract a kernel version string from a file path.
func detectVersionFromPath(path string) string {
	base := filepath.Base(path)

	// Common patterns: vmlinuz-6.1.0, linux-6.1.0, bzImage-6.1.0
	for _, prefix := range []string{"vmlinuz-", "vmlinux-", "linux-", "bzImage-", "Image-"} {
		if strings.HasPrefix(base, prefix) {
			return strings.TrimPrefix(base, prefix)
		}
	}

	// Check parent directory for version-like names
	dir := filepath.Base(filepath.Dir(path))
	if len(dir) > 0 && (dir[0] >= '0' && dir[0] <= '9') {
		return dir
	}

	return ""
}

// ScanDirectory scans a directory for kernel images and returns found entries.
func (ks *KernelStore) ScanDirectory(dir string) ([]KernelEntry, error) {
	var found []KernelEntry

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isKernelFilename(name) {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		format, arch := detectKernelFormat(data)
		if format == "unknown" || format == "raw" {
			continue
		}

		info, _ := entry.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}

		hash := sha256.Sum256(data)
		version := detectVersionFromPath(path)
		if version == "" {
			version = name
		}

		found = append(found, KernelEntry{
			Version: version,
			Path:    path,
			Format:  format,
			Arch:    arch,
			Size:    size,
			SHA256:  hex.EncodeToString(hash[:]),
		})
	}

	return found, nil
}

// isKernelFilename checks if a filename looks like a kernel image.
func isKernelFilename(name string) bool {
	lower := strings.ToLower(name)
	prefixes := []string{"vmlinuz", "vmlinux", "bzimage", "image", "linux", "kernel"}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// hashFile computes SHA-256 of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
