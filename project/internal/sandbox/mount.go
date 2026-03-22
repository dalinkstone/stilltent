// Package vm provides cross-platform VM management operations.
// This file implements host-to-guest directory mount management using virtio-9p (Plan 9).
package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dalinkstone/tent/pkg/models"
)

// MountShare represents a prepared host-to-guest mount share ready for the hypervisor.
// Each share maps to a virtio-9p device with a unique tag that the guest uses to mount.
type MountShare struct {
	// Tag is the 9p mount tag the guest uses to identify this share (e.g., "mount0")
	Tag string `json:"tag"`
	// HostPath is the absolute path on the host
	HostPath string `json:"host_path"`
	// GuestPath is the mount point inside the guest
	GuestPath string `json:"guest_path"`
	// ReadOnly indicates whether the share is read-only
	ReadOnly bool `json:"read_only"`
}

// MountManager handles validation, resolution, and persistence of host-to-guest mounts.
type MountManager struct {
	baseDir string
}

// NewMountManager creates a new mount manager.
func NewMountManager(baseDir string) *MountManager {
	return &MountManager{baseDir: baseDir}
}

// PrepareMounts validates mount configs and produces MountShare descriptors for the hypervisor.
// Host paths are resolved to absolute paths. Relative paths are resolved relative to cwd.
func (mm *MountManager) PrepareMounts(vmName string, mounts []models.MountConfig) ([]MountShare, error) {
	if len(mounts) == 0 {
		return nil, nil
	}

	shares := make([]MountShare, 0, len(mounts))
	seenTags := make(map[string]bool)
	seenGuest := make(map[string]bool)

	for i, m := range mounts {
		// Resolve host path to absolute
		hostPath := m.Host
		if !filepath.IsAbs(hostPath) {
			abs, err := filepath.Abs(hostPath)
			if err != nil {
				return nil, fmt.Errorf("mount[%d]: failed to resolve host path %q: %w", i, hostPath, err)
			}
			hostPath = abs
		}

		// Validate host path exists and is a directory
		info, err := os.Stat(hostPath)
		if err != nil {
			return nil, fmt.Errorf("mount[%d]: host path %q does not exist: %w", i, hostPath, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("mount[%d]: host path %q is not a directory", i, hostPath)
		}

		// Validate guest path is absolute
		if !filepath.IsAbs(m.Guest) {
			return nil, fmt.Errorf("mount[%d]: guest path %q must be absolute", i, m.Guest)
		}

		// Check for duplicate guest mount points
		guestNorm := filepath.Clean(m.Guest)
		if seenGuest[guestNorm] {
			return nil, fmt.Errorf("mount[%d]: duplicate guest mount point %q", i, m.Guest)
		}
		seenGuest[guestNorm] = true

		// Generate unique 9p tag
		tag := fmt.Sprintf("mount%d", i)
		if seenTags[tag] {
			return nil, fmt.Errorf("mount[%d]: duplicate tag %q (internal error)", i, tag)
		}
		seenTags[tag] = true

		shares = append(shares, MountShare{
			Tag:       tag,
			HostPath:  hostPath,
			GuestPath: guestNorm,
			ReadOnly:  m.Readonly,
		})
	}

	return shares, nil
}

// SaveMounts persists mount share state for a VM so it can be recovered after restart.
func (mm *MountManager) SaveMounts(vmName string, shares []MountShare) error {
	mountDir := filepath.Join(mm.baseDir, "mounts")
	if err := os.MkdirAll(mountDir, 0755); err != nil {
		return fmt.Errorf("failed to create mounts directory: %w", err)
	}

	data, err := json.MarshalIndent(shares, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal mounts: %w", err)
	}

	mountPath := filepath.Join(mountDir, fmt.Sprintf("%s.json", vmName))
	return os.WriteFile(mountPath, data, 0644)
}

// LoadMounts loads persisted mount shares for a VM.
func (mm *MountManager) LoadMounts(vmName string) ([]MountShare, error) {
	mountPath := filepath.Join(mm.baseDir, "mounts", fmt.Sprintf("%s.json", vmName))
	data, err := os.ReadFile(mountPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read mounts: %w", err)
	}

	var shares []MountShare
	if err := json.Unmarshal(data, &shares); err != nil {
		return nil, fmt.Errorf("failed to parse mounts: %w", err)
	}
	return shares, nil
}

// RemoveMounts removes persisted mount state for a VM.
func (mm *MountManager) RemoveMounts(vmName string) error {
	mountPath := filepath.Join(mm.baseDir, "mounts", fmt.Sprintf("%s.json", vmName))
	if err := os.Remove(mountPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// FormatMountSummary returns a human-readable summary of mount shares.
func FormatMountSummary(shares []MountShare) string {
	if len(shares) == 0 {
		return "(none)"
	}
	var parts []string
	for _, s := range shares {
		mode := "rw"
		if s.ReadOnly {
			mode = "ro"
		}
		parts = append(parts, fmt.Sprintf("%s -> %s (%s)", s.HostPath, s.GuestPath, mode))
	}
	return strings.Join(parts, ", ")
}
