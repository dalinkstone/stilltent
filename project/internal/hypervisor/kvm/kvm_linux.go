package kvm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dalinkstone/tent/pkg/models"
	"github.com/dalinkstone/tent/internal/hypervisor"
	"github.com/c35s/hype/virtio"
	"github.com/c35s/hype/vmm"
	"github.com/c35s/hype/os/linux"
)

// Backend implements hypervisor.Backend for Linux/KVM
type Backend struct {
	baseDir   string
	vmConfigs map[string]*models.VMConfig
}

// VM represents a KVM virtual machine managed by tent
type VM struct {
	config    *models.VMConfig
	backend   *Backend
	vm        *vmm.VM
	ctx       context.Context
	cancel    context.CancelFunc
	running   bool
	ip        string
	tapDevice string
}

// NewBackend creates a new KVM backend
func NewBackend(baseDir string) (*Backend, error) {
	// Check if KVM is available
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return nil, fmt.Errorf("KVM not available: /dev/kvm not found: %w", err)
	}

	// Verify we can open /dev/kvm
	kvmFile, err := os.Open("/dev/kvm")
	if err != nil {
		return nil, fmt.Errorf("failed to open /dev/kvm: %w", err)
	}
	kvmFile.Close()

	return &Backend{
		baseDir:   baseDir,
		vmConfigs: make(map[string]*models.VMConfig),
	}, nil
}

// CreateVM creates a new KVM virtual machine
func (b *Backend) CreateVM(config *models.VMConfig) (hypervisor.VM, error) {
	// Store config for later use
	b.vmConfigs[config.Name] = config

	// Create the VM instance
	vm := &VM{
		config:    config,
		backend:   b,
		running:   false,
		tapDevice: "", // Will be set during Start
	}

	return vm, nil
}

// ListVMs returns all active VMs
func (b *Backend) ListVMs() ([]hypervisor.VM, error) {
	// KVM doesn't provide a way to list VMs from userspace
	// We would need to track VMs in a state file or maintain a global registry
	// For now, return empty - the VM manager tracks running VMs separately
	return nil, nil
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

	// Clean up config
	delete(b.vmConfigs, kvmVM.config.Name)

	return nil
}

// Start boots the VM using the hype library's vmm.Run
func (v *VM) Start() error {
	if v.running {
		return fmt.Errorf("VM %s is already running", v.config.Name)
	}

	// Build the vmm.Config
	cfg := vmm.Config{
		MemSize: v.config.MemoryMB * 1024 * 1024, // Convert MB to bytes
		Devices: []virtio.DeviceConfig{},
	}

	// Add console device for serial output
	cfg.Devices = append(cfg.Devices, &virtio.ConsoleDevice{})

	// Set up kernel loader
	rootfsPath := filepath.Join(v.backend.baseDir, "storage", "images", v.config.RootFS+".img")
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		return fmt.Errorf("rootfs not found: %s", rootfsPath)
	}

	// For now, we'll use a placeholder kernel path
	// In production, this would extract the kernel from the rootfs image
	kernelPath := "/boot/vmlinuz"
	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		// Try alternative paths
		kernelPath = "/vmlinuz"
	}

	// Create a loader with the kernel
	loader := &linux.Loader{
		Kernel:  []byte{}, // Empty - would need actual kernel image
		Initrd:  []byte{}, // Empty - would need actual initrd
		Cmdline: fmt.Sprintf("root=/dev/vda console=hvc0 rw ip=dhcp init=/sbin/init"),
	}

	cfg.Loader = loader

	// Create the VM using hype
	vm, err := vmm.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	v.vm = vm
	v.ctx, v.cancel = context.WithCancel(context.Background())

	// Start the VM in a goroutine
	go func() {
		if err := vm.Run(v.ctx); err != nil {
			// VM exited - could be normal shutdown or error
			v.running = false
		}
	}()

	v.running = true
	return nil
}

// Stop gracefully shuts down the VM
func (v *VM) Stop() error {
	if !v.running {
		return fmt.Errorf("VM is not running")
	}

	// Cancel the context to signal VM shutdown
	if v.cancel != nil {
		v.cancel()
	}

	// Wait for VM to stop
	v.running = false
	return nil
}

// Kill forcefully terminates the VM
func (v *VM) Kill() error {
	if !v.running {
		return nil
	}

	// Cancel the context
	if v.cancel != nil {
		v.cancel()
	}

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
	return v.ip
}

// SetIP sets the VM's IP address
func (v *VM) SetIP(ip string) {
	v.ip = ip
}

// GetPID returns the VM process ID (KVM runs in-process)
func (v *VM) GetPID() int {
	// KVM doesn't create separate processes - returns 0
	return 0
}

// Cleanup releases all VM resources
func (v *VM) Cleanup() error {
	// Cancel context if running
	if v.running && v.cancel != nil {
		v.cancel()
	}

	v.running = false
	v.vm = nil
	v.ctx = nil
	v.cancel = nil

	return nil
}
