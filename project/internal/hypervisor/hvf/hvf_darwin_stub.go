//go:build darwin && !cgo
// +build darwin,!cgo

// Package hvf provides a stub Hypervisor.framework backend for macOS.
// When CGO is not available, this stub allows compilation but fails at runtime.
package hvf

import (
	"fmt"
	"io"

	"github.com/dalinkstone/tent/internal/hypervisor"
	"github.com/dalinkstone/tent/pkg/models"
)

// Backend implements hypervisor.Backend for macOS/Hypervisor.framework
type Backend struct {
	baseDir string
}

// VM represents a Hypervisor.framework virtual machine (stub)
type VM struct {
	config *models.VMConfig
}

// NewBackend creates a new Hypervisor.framework backend
func NewBackend(baseDir string) (*Backend, error) {
	return &Backend{
		baseDir: baseDir,
	}, nil
}

// CreateVM creates a new Hypervisor.framework virtual machine
func (b *Backend) CreateVM(config *models.VMConfig) (hypervisor.VM, error) {
	return nil, fmt.Errorf("Hypervisor.framework backend requires CGO and macOS C compiler (clang)")
}

// ListVMs returns all active VMs
func (b *Backend) ListVMs() ([]hypervisor.VM, error) {
	return nil, nil
}

// DestroyVM releases all resources for a VM
func (b *Backend) DestroyVM(vm hypervisor.VM) error {
	return nil
}

// Start boots the VM
func (v *VM) Start() error {
	return fmt.Errorf("Hypervisor.framework backend requires CGO and macOS C compiler (clang)")
}

// Stop gracefully shuts down the VM
func (v *VM) Stop() error {
	return nil
}

// Kill forcefully terminates the VM
func (v *VM) Kill() error {
	return nil
}

// Status returns the current VM state
func (v *VM) Status() (models.VMStatus, error) {
	return models.VMStatusUnknown, fmt.Errorf("Hypervisor.framework backend requires CGO and macOS C compiler (clang)")
}

// GetConfig returns the VM's configuration
func (v *VM) GetConfig() *models.VMConfig {
	return v.config
}

// GetIP returns the VM's network IP address
func (v *VM) GetIP() string {
	return ""
}

// SetIP sets the VM's IP address
func (v *VM) SetIP(ip string) {
}

// SetNetwork configures the VM's network interface
func (v *VM) SetNetwork(tapDevice string, ip string) {
}

// GetPID returns the VM process ID
func (v *VM) GetPID() int {
	return 0
}

// SetConsoleOutput sets the writer for capturing console/serial output
func (v *VM) SetConsoleOutput(w io.Writer) {
}

// Cleanup releases all VM resources
func (v *VM) Cleanup() error {
	return nil
}
