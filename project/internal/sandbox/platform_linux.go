//go:build linux
// +build linux

// Package vm provides Linux-specific VM management operations.
// This file contains platform-specific initialization code for the Linux/KVM platform.
package vm

import (
	"github.com/dalinkstone/tent/internal/hypervisor/kvm"
)

// NewPlatformBackend creates a new hypervisor backend for Linux/KVM
func NewPlatformBackend(baseDir string) (HypervisorBackend, error) {
	return kvm.NewBackend(baseDir)
}
