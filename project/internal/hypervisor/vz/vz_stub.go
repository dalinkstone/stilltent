//go:build !darwin || !cgo
// +build !darwin !cgo

// Package vz provides a stub Virtualization.framework backend.
// When not on macOS with CGO, this stub allows compilation but returns
// errors at runtime indicating that the VZ backend is unavailable.
package vz

import (
	"fmt"
	"io"

	"github.com/dalinkstone/tent/internal/hypervisor"
	"github.com/dalinkstone/tent/pkg/models"
)

// Backend implements hypervisor.Backend (stub).
type Backend struct {
	baseDir string
}

// VM represents a VZ virtual machine (stub).
type VM struct {
	config *models.VMConfig
}

// NewBackend creates a new VZ backend stub.
func NewBackend(baseDir string) (*Backend, error) {
	return &Backend{baseDir: baseDir}, nil
}

// CreateVM returns an error — VZ requires macOS with CGO.
func (b *Backend) CreateVM(config *models.VMConfig) (hypervisor.VM, error) {
	return nil, fmt.Errorf("Virtualization.framework backend requires macOS with CGO enabled")
}

// ListVMs returns nil on non-macOS platforms.
func (b *Backend) ListVMs() ([]hypervisor.VM, error) {
	return nil, nil
}

// DestroyVM is a no-op on non-macOS platforms.
func (b *Backend) DestroyVM(vm hypervisor.VM) error {
	return nil
}

// Start returns an error — VZ requires macOS.
func (v *VM) Start() error {
	return fmt.Errorf("Virtualization.framework backend requires macOS with CGO enabled")
}

// Stop is a no-op stub.
func (v *VM) Stop() error { return nil }

// Pause returns an error — VZ requires macOS.
func (v *VM) Pause() error {
	return fmt.Errorf("Virtualization.framework backend requires macOS with CGO enabled")
}

// Unpause returns an error — VZ requires macOS.
func (v *VM) Unpause() error {
	return fmt.Errorf("Virtualization.framework backend requires macOS with CGO enabled")
}

// Kill is a no-op stub.
func (v *VM) Kill() error { return nil }

// Status returns unknown on non-macOS platforms.
func (v *VM) Status() (models.VMStatus, error) {
	return models.VMStatusUnknown, fmt.Errorf("Virtualization.framework backend requires macOS with CGO enabled")
}

// GetConfig returns the VM configuration.
func (v *VM) GetConfig() *models.VMConfig { return v.config }

// GetIP returns empty string.
func (v *VM) GetIP() string { return "" }

// SetIP is a no-op stub.
func (v *VM) SetIP(ip string) {}

// SetNetwork is a no-op stub.
func (v *VM) SetNetwork(tapDevice string, ip string) {}

// GetPID returns 0.
func (v *VM) GetPID() int { return 0 }

// SetConsoleOutput is a no-op stub.
func (v *VM) SetConsoleOutput(w io.Writer) {}

// AddMounts is a no-op stub.
func (v *VM) AddMounts(mounts []hypervisor.MountTag) {}

// Cleanup is a no-op stub.
func (v *VM) Cleanup() error { return nil }
