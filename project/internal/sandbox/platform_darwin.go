//go:build darwin
// +build darwin

// Package vm provides macOS-specific VM management operations.
// This file contains platform-specific initialization code for the macOS/Hypervisor.framework platform.
package vm

import (
	"github.com/dalinkstone/tent/internal/hypervisor/hvf"
)

// NewPlatformBackend creates a new hypervisor backend for macOS/Hypervisor.framework
func NewPlatformBackend(baseDir string) (HypervisorBackend, error) {
	return hvf.NewBackend(baseDir)
}
