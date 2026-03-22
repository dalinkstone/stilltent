//go:build darwin && cgo
// +build darwin,cgo

// Package hvf provides a macOS Hypervisor.framework backend for tent.
// This implementation uses CGO to interface with Apple's Hypervisor.framework.
package hvf

/*
#cgo darwin CFLAGS: -framework Hypervisor
#cgo darwin LDFLAGS: -framework Hypervisor

#include <Hypervisor/Hypervisor.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>

// Helper function to convert macOS error codes to human-readable strings
static const char* hvm_error_string(hv_return_t ret) {
    switch (ret) {
        case HV_SUCCESS:
            return "HV_SUCCESS";
        case HV_NO_RESOURCE:
            return "HV_NO_RESOURCE";
        case HV_NO_SPACE:
            return "HV_NO_SPACE";
        case HV_BAD_ARGUMENT:
            return "HV_BAD_ARGUMENT";
        case HV_IN_USE:
            return "HV_IN_USE";
        case HV_NO_PRIVILEGE:
            return "HV_NO_PRIVILEGE";
        case HV_IO_ERROR:
            return "HV_IO_ERROR";
        case HV_VM_CONFIG_ERROR:
            return "HV_VM_CONFIG_ERROR";
        default:
            return "HV_UNKNOWN_ERROR";
    }
}
*/
import "C"
import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/dalinkstone/tent/pkg/models"
	"github.com/dalinkstone/tent/internal/hypervisor"
)

// Backend implements hypervisor.Backend for macOS/Hypervisor.framework
type Backend struct {
	baseDir   string
	vmConfigs map[string]*models.VMConfig
	vmMutex   sync.Mutex
}

// VM represents a Hypervisor.framework virtual machine
type VM struct {
	config   *models.VMConfig
	backend  *Backend
	vmRef    C.hv_vm_t
	running  bool
	ip       string
	tapDevice string
}

// NewBackend creates a new Hypervisor.framework backend
func NewBackend(baseDir string) (*Backend, error) {
	return &Backend{
		baseDir:   baseDir,
		vmConfigs: make(map[string]*models.VMConfig),
	}, nil
}

// CreateVM creates a new Hypervisor.framework virtual machine
func (b *Backend) CreateVM(config *models.VMConfig) (hypervisor.VM, error) {
	b.vmMutex.Lock()
	defer b.vmMutex.Unlock()

	// Check if VM already exists
	if _, exists := b.vmConfigs[config.Name]; exists {
		return nil, fmt.Errorf("VM %s already exists", config.Name)
	}

	// Store config
	b.vmConfigs[config.Name] = config

	// Create the VM instance
	vm := &VM{
		config:    config,
		backend:   b,
		vmRef:     nil, // Will be set during Start
		running:   false,
		tapDevice: "", // Will be set during Start
	}

	return vm, nil
}

// ListVMs returns all active VMs
func (b *Backend) ListVMs() ([]hypervisor.VM, error) {
	b.vmMutex.Lock()
	defer b.vmMutex.Unlock()

	var vms []hypervisor.VM
	for _, config := range b.vmConfigs {
		vm := &VM{
			config:  config,
			backend: b,
			running: false, // Status tracked separately
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

// DestroyVM releases all resources for a VM
func (b *Backend) DestroyVM(vm hypervisor.VM) error {
	hvmVM, ok := vm.(*VM)
	if !ok {
		return fmt.Errorf("invalid VM type")
	}

	// Stop the VM if running
	if hvmVM.running {
		_ = hvmVM.Stop()
	}

	// Clean up config
	b.vmMutex.Lock()
	delete(b.vmConfigs, hvmVM.config.Name)
	b.vmMutex.Unlock()

	return nil
}

// Start boots the VM using Hypervisor.framework
func (v *VM) Start() error {
	if v.running {
		return fmt.Errorf("VM %s is already running", v.config.Name)
	}

	// Allocate VM memory
	memorySize := uint64(v.config.MemoryMB * 1024 * 1024) // Convert MB to bytes

	// Create VM
	var ret C.hv_return_t
	ret = C.hv_vm_create(&v.vmRef)
	if ret != C.HV_SUCCESS {
		return fmt.Errorf("failed to create VM: %s", C.GoString(C.hvm_error_string(ret)))
	}

	// Map guest memory
	// Allocate memory for the guest
	memoryPtr := C.mmap(
		C.NULL,
		C.size_t(memorySize),
		C.PROT_READ|C.PROT_WRITE,
		C.MAP_ANON|C.MAP_PRIVATE,
		-1,
		0,
	)
	if memoryPtr == C.MAP_FAILED {
		return fmt.Errorf("failed to allocate guest memory")
	}

	// Map memory to VM
	// Note: hv_vm_map signature is (uva, gpa, size, flags) - no vmRef parameter
	// The VM is implicitly associated with the current task after hv_vm_create
	ret = C.hv_vm_map(
		C.hv_uvaddr_t(unsafe.Pointer(memoryPtr)),
		C.hv_gpaddr_t(0),
		C.size_t(memorySize),
		C.HV_MEMORY_READ|C.HV_MEMORY_WRITE|C.HV_MEMORY_EXEC,
	)
	if ret != C.HV_SUCCESS {
		C.munmap(memoryPtr, C.size_t(memorySize))
		return fmt.Errorf("failed to map guest memory: %s", C.GoString(C.hvm_error_string(ret)))
	}

	// Create vCPU
	var vcpuRef C.hv_vcpu_t
	ret = C.hv_vcpu_create(&vcpuRef, nil)
	if ret != C.HV_SUCCESS {
		C.munmap(memoryPtr, C.size_t(memorySize))
		return fmt.Errorf("failed to create vCPU: %s", C.GoString(C.hvm_error_string(ret)))
	}

	// Set up vCPU state - configure the vCPU with initial registers
	// For simplicity, we'll set up a minimal configuration
	// In production, this would set up proper x86/ARM registers for the guest kernel

	// Start the vCPU execution loop
	if err := v.runVCPU(vcpuRef); err != nil {
		C.munmap(memoryPtr, C.size_t(memorySize))
		return fmt.Errorf("failed to start vCPU: %w", err)
	}

	v.running = true
	v.ip = "172.16.0.2" // Placeholder IP

	return nil
}

// runVCPU executes the vCPU loop using Hypervisor.framework
func (v *VM) runVCPU(vcpuRef C.hv_vcpu_t) error {
	// The vCPU execution loop - runs until the VM stops or an error occurs
	// This implements the basic HVF vCPU execution loop using hv_vcpu_run

	for v.running {
		// Run the vCPU until it exits
		ret := C.hv_vcpu_run(vcpuRef)
		if ret != C.HV_SUCCESS {
			return fmt.Errorf("vCPU run failed: %s", C.GoString(C.hvm_error_string(ret)))
		}

		// In a full implementation, we would:
		// 1. Check the exit reason from hv_vcpu_run
		// 2. Handle different exit types (memory access, I/O, interrupts, etc.)
		// 3. Update guest state based on exit reasons
		// 4. Resume execution

		// For now, we just continue the loop
		// A complete implementation would handle exits properly
	}

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
	return v.Stop()
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

// GetPID returns the VM process ID (HVF runs in-process)
func (v *VM) GetPID() int {
	// HVF doesn't create separate processes - returns 0
	return 0
}

// Cleanup releases all VM resources
func (v *VM) Cleanup() error {
	if v.running {
		_ = v.Stop()
	}

	// Unmap memory if allocated
	if v.vmRef != nil {
		// In a full implementation, we would unmap memory and destroy the VM
		// This is a simplified version
	}

	v.running = false
	v.vmRef = nil

	return nil
}
