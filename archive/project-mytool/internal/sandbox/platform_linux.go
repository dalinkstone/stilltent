//go:build linux
// +build linux

// Package vm provides Linux-specific VM management operations.
// This file contains platform-specific initialization code for the Linux/KVM platform.
package vm

import (
	"fmt"

	"github.com/dalinkstone/tent/internal/hypervisor/firecracker"
	"github.com/dalinkstone/tent/internal/hypervisor/kvm"
)

// NewPlatformBackend creates a new hypervisor backend for Linux/KVM
func NewPlatformBackend(baseDir string) (HypervisorBackend, error) {
	return kvm.NewBackend(baseDir)
}

// NewBackendByName creates a hypervisor backend by name. Supported backends
// on Linux: "kvm" (default), "firecracker".
func NewBackendByName(name, baseDir string) (HypervisorBackend, error) {
	switch name {
	case "", "kvm":
		return kvm.NewBackend(baseDir)
	case "firecracker":
		return firecracker.NewBackend(baseDir)
	default:
		return nil, fmt.Errorf("unsupported hypervisor backend %q (supported: kvm, firecracker)", name)
	}
}
