//go:build darwin
// +build darwin

// Package hvf provides a Hypervisor.framework backend implementation for macOS.
// It uses Apple's Hypervisor.framework for VM management.
package hvf

import (
	"fmt"

	"github.com/dalinkstone/tent/pkg/models"
	"github.com/dalinkstone/tent/internal/hypervisor"
)

// Backend implements hypervisor.Backend for macOS/Hypervisor.framework
type Backend struct {
	baseDir string
}

// VM represents a Hypervisor.framework virtual machine
type VM struct {
	config *models.VMConfig
}

// NewBackend creates a new Hypervisor.framework backend
func NewBackend(baseDir string) (*Backend, error) {
	// Check if Hypervisor.framework is available
	// This would require CGO to check at runtime
	
	return &Backend{
		baseDir: baseDir,
	}, nil
}

// CreateVM creates a new Hypervisor.framework virtual machine
func (b *Backend) CreateVM(config *models.VMConfig) (hypervisor.VM, error) {
	// TODO: Implement Hypervisor.framework VM creation
	return nil, fmt.Errorf("CreateVM not implemented for Hypervisor.framework backend")
}

// ListVMs returns all active VMs (not implemented)
func (b *Backend) ListVMs() ([]hypervisor.VM, error) {
	return nil, fmt.Errorf("ListVMs not implemented for Hypervisor.framework backend")
}

// DestroyVM releases all resources for a VM
func (b *Backend) DestroyVM(vm hypervisor.VM) error {
	return nil
}

// Start boots the VM
func (v *VM) Start() error {
	return fmt.Errorf("Start not implemented for Hypervisor.framework backend")
}

// Stop gracefully shuts down the VM
func (v *VM) Stop() error {
	return fmt.Errorf("Stop not implemented for Hypervisor.framework backend")
}

// Kill forcefully terminates the VM
func (v *VM) Kill() error {
	return fmt.Errorf("Kill not implemented for Hypervisor.framework backend")
}

// Status returns the current VM state
func (v *VM) Status() (models.VMStatus, error) {
	return models.VMStatusUnknown, fmt.Errorf("Status not implemented for Hypervisor.framework backend")
}

// GetConfig returns the VM's configuration
func (v *VM) GetConfig() *models.VMConfig {
	return v.config
}

// GetIP returns the VM's network IP address
func (v *VM) GetIP() string {
	return ""
}

// GetPID returns the VM process ID
func (v *VM) GetPID() int {
	return 0
}

// Cleanup releases all VM resources
func (v *VM) Cleanup() error {
	return nil
}
