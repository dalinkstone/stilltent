package kvm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dalinkstone/tent/pkg/models"
	"github.com/dalinkstone/tent/internal/hypervisor"
	"github.com/dalinkstone/tent/internal/storage"
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

// SetNetwork configures the VM's network interface
func (v *VM) SetNetwork(tapDevice string, ip string) {
	v.tapDevice = tapDevice
	v.ip = ip
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

	// Note: The hype library doesn't support virtio-net devices.
	// Network is handled externally via TAP devices and the network manager.
	// The VM will use the kernel's network stack with the TAP device configured
	// by the network manager before VM start.

	// Set up kernel loader using storage manager's ExtractKernel
	// This extracts kernel/initrd from the rootfs image
	storageMgr, err := storage.NewManager(v.backend.baseDir)
	if err != nil {
		return fmt.Errorf("failed to create storage manager: %w", err)
	}

	// Find the rootfs path
	rootfsPath := filepath.Join(v.backend.baseDir, "storage", "images", v.config.RootFS+".img")
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		// Try alternative path (VM-specific rootfs)
		rootfsPath = filepath.Join(v.backend.baseDir, "storage", "rootfs", v.config.Name, "rootfs.img")
	}

	// Extract kernel information from the rootfs
	kernelInfo, err := storageMgr.ExtractKernel(rootfsPath)
	if err != nil {
		return fmt.Errorf("failed to extract kernel: %w", err)
	}

	// Read kernel bytes
	kernelBytes, err := os.ReadFile(kernelInfo.KernelPath)
	if err != nil {
		return fmt.Errorf("failed to read kernel file %s: %w", kernelInfo.KernelPath, err)
	}

	// Read initrd bytes if available
	var initrdBytes []byte
	if kernelInfo.InitrdPath != "" {
		if initrdBytes, err = os.ReadFile(kernelInfo.InitrdPath); err != nil {
			return fmt.Errorf("failed to read initrd file %s: %w", kernelInfo.InitrdPath, err)
		}
	}

	// Create a loader with the kernel and initrd
	loader := &linux.Loader{
		Kernel:  kernelBytes,
		Initrd:  initrdBytes,
		Cmdline: kernelInfo.Cmdline,
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
