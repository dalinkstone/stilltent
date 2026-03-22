//go:build darwin
// +build darwin

// Package vm provides macOS-specific VM management operations.
// This file contains platform-specific initialization code for the macOS/Hypervisor.framework platform.
package vm

import (
	"fmt"

	"github.com/dalinkstone/tent/internal/hypervisor/hvf"
)

// NewPlatformBackend creates a new hypervisor backend for macOS/Hypervisor.framework
func NewPlatformBackend(baseDir string) (HypervisorBackend, error) {
	return hvf.NewBackend(baseDir)
}

// NewBackendByName creates a hypervisor backend by name. On macOS only "hvf"
// is supported (Firecracker requires Linux/KVM).
func NewBackendByName(name, baseDir string) (HypervisorBackend, error) {
	switch name {
	case "", "hvf":
		return hvf.NewBackend(baseDir)
	default:
		return nil, fmt.Errorf("unsupported hypervisor backend %q on macOS (supported: hvf)", name)
	}
}
