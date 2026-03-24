//go:build darwin
// +build darwin

// Package vm provides macOS-specific VM management operations.
// This file contains platform-specific initialization code for the macOS platform,
// supporting both Hypervisor.framework (HVF) and Virtualization.framework (VZ) backends.
package vm

import (
	"fmt"

	"github.com/dalinkstone/tent/internal/hypervisor/hvf"
	"github.com/dalinkstone/tent/internal/hypervisor/vz"
)

// NewPlatformBackend creates a new hypervisor backend for macOS.
// Defaults to Virtualization.framework (VZ) which provides full VM lifecycle
// management, native virtio device support, and VZLinuxBootLoader.
func NewPlatformBackend(baseDir string) (HypervisorBackend, error) {
	return vz.NewBackend(baseDir)
}

// NewBackendByName creates a hypervisor backend by name on macOS.
// Supported backends:
//   - "vz" (default): Apple Virtualization.framework — high-level VM API with native
//     virtio device emulation, VZLinuxBootLoader, NAT networking, and virtio-fs shared
//     directories. Preferred for production Linux guest workloads.
//   - "hvf": Apple Hypervisor.framework — low-level vCPU API with direct
//     register/memory control. Best for custom boot sequences and full control.
func NewBackendByName(name, baseDir string) (HypervisorBackend, error) {
	switch name {
	case "", "vz":
		return vz.NewBackend(baseDir)
	case "hvf":
		return hvf.NewBackend(baseDir)
	default:
		return nil, fmt.Errorf("unsupported hypervisor backend %q on macOS (supported: vz, hvf)", name)
	}
}
