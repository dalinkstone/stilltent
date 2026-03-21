//go:build linux
// +build linux

// Package kvm provides a KVM backend implementation for Linux.
// It uses the kernel's /dev/kvm interface via the hype library.
package kvm

import (
	"fmt"
	"os"

	"github.com/dalinkstone/tent/pkg/models"
	"github.com/dalinkstone/tent/internal/hypervisor"
)

// Backend implements hypervisor.Backend for Linux/KVM
type Backend struct {
	baseDir string
}

// VM represents a KVM virtual machine
type VM struct {
	config *models.VMConfig
	running bool
}

// NewBackend creates a new KVM backend
func NewBackend(baseDir string) (*Backend, error) {
	// Check if KVM is available
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return nil, fmt.Errorf("KVM not available: /dev/kvm not found")
	}

	return &Backend{
		baseDir: baseDir,
	}, nil
}

// CreateVM creates a new KVM virtual machine
func (b *Backend) CreateVM(config *models.VMConfig) (hypervisor.VM, error) {
	// TODO: Implement KVM VM creation using hype library
	// For now, return a stub VM that tracks state
	return &VM{
		config: config,
	}, nil
}

// ListVMs returns all active VMs (not implemented for KVM backend)
func (b *Backend) ListVMs() ([]hypervisor.VM, error) {
	// KVM doesn't provide a way to list VMs from userspace
	// This would require tracking VMs in a state file
	return nil, fmt.Errorf("ListVMs not implemented for KVM backend")
}

// DestroyVM releases all resources for a VM
func (b *Backend) DestroyVM(vm hypervisor.VM) error {
	kvmVM, ok := vm.(*VM)
	if !ok {
		return fmt.Errorf("invalid VM type")
	}

	// Stop the VM if running
	if kvmVM.running {
		_ = kvmVM.Stop()
	}

	return nil
}

// Start boots the VM
func (v *VM) Start() error {
	if v.running {
		return fmt.Errorf("VM is already running")
	}

	// TODO: Implement KVM VM start using hype library
	v.running = true
	return nil
}

// Stop gracefully shuts down the VM
func (v *VM) Stop() error {
	if !v.running {
		return fmt.Errorf("VM is not running")
	}

	v.running = false
	return nil
}

// Kill forcefully terminates the VM
func (v *VM) Kill() error {
	v.running = false
	return nil
}

// Status returns the current VM state
func (v *VM) Status() (models.VMStatus, error) {
	if v.running {
		return models.VMStatusRunning, nil
	}
	return models.VMStatusStopped, nil
}

// GetConfig returns the VM's configuration
func (v *VM) GetConfig() *models.VMConfig {
	return v.config
}

// GetIP returns the VM's network IP address
func (v *VM) GetIP() string {
	// IP would be tracked by the network manager
	return ""
}

// GetPID returns the VM process ID (KVM runs in-process)
func (v *VM) GetPID() int {
	// KVM doesn't create separate processes - returns 0
	return 0
}

// Cleanup releases all VM resources
func (v *VM) Cleanup() error {
	return nil
}
