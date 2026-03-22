//go:build darwin && cgo
// +build darwin,cgo

// Package hvf provides a macOS Hypervisor.framework backend for tent.
// This implementation uses CGO to interface with Apple's Hypervisor.framework
// on ARM64 (Apple Silicon).
package hvf

/*
#cgo darwin LDFLAGS: -framework Hypervisor

#include <Hypervisor/Hypervisor.h>
#include <sys/mman.h>
#include <stdlib.h>
#include <string.h>
*/
import "C"
import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/dalinkstone/tent/internal/hypervisor"
	"github.com/dalinkstone/tent/pkg/models"
)

// Backend implements hypervisor.Backend for macOS Hypervisor.framework (ARM64)
type Backend struct {
	baseDir   string
	vmConfigs map[string]*models.VMConfig
	vmMutex   sync.Mutex
}

// VM represents a Hypervisor.framework virtual machine
type VM struct {
	config     *models.VMConfig
	backend    *Backend
	vcpuID     C.hv_vcpu_t
	vcpuExit   *C.hv_vcpu_exit_t
	running    bool
	ip         string
	tapDevice  string
	memoryPtr  unsafe.Pointer
	memorySize uint64
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

	if _, exists := b.vmConfigs[config.Name]; exists {
		return nil, fmt.Errorf("VM %s already exists", config.Name)
	}

	b.vmConfigs[config.Name] = config

	vm := &VM{
		config:  config,
		backend: b,
		running: false,
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
			running: false,
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

	if hvmVM.running {
		_ = hvmVM.Stop()
	}

	b.vmMutex.Lock()
	delete(b.vmConfigs, hvmVM.config.Name)
	b.vmMutex.Unlock()

	return nil
}

// Start boots the VM using Hypervisor.framework (ARM64 API)
func (v *VM) Start() error {
	if v.running {
		return fmt.Errorf("VM %s is already running", v.config.Name)
	}

	memorySize := uint64(v.config.MemoryMB) * 1024 * 1024

	// Create VM — ARM64 API takes hv_vm_config_t (NULL for default)
	ret := C.hv_vm_create(nil)
	if ret != C.HV_SUCCESS {
		return fmt.Errorf("hv_vm_create failed: %d", ret)
	}

	// Allocate guest memory via mmap
	memoryPtr := C.mmap(nil, C.size_t(memorySize),
		C.PROT_READ|C.PROT_WRITE,
		C.MAP_ANON|C.MAP_PRIVATE,
		C.int(-1), 0)
	if memoryPtr == C.MAP_FAILED {
		C.hv_vm_destroy()
		return fmt.Errorf("mmap failed: could not allocate %d MB of guest memory", v.config.MemoryMB)
	}

	// Map host memory into guest physical address space
	// ARM64 API: hv_vm_map(void *addr, hv_ipa_t ipa, size_t size, hv_memory_flags_t flags)
	ret = C.hv_vm_map(memoryPtr,
		C.hv_ipa_t(0),
		C.size_t(memorySize),
		C.HV_MEMORY_READ|C.HV_MEMORY_WRITE|C.HV_MEMORY_EXEC)
	if ret != C.HV_SUCCESS {
		C.munmap(memoryPtr, C.size_t(memorySize))
		C.hv_vm_destroy()
		return fmt.Errorf("hv_vm_map failed: %d", ret)
	}

	v.memoryPtr = memoryPtr
	v.memorySize = memorySize

	// Create vCPU — ARM64 API: hv_vcpu_create(hv_vcpu_t *, hv_vcpu_exit_t **, hv_vcpu_config_t)
	var vcpuID C.hv_vcpu_t
	var vcpuExit *C.hv_vcpu_exit_t
	ret = C.hv_vcpu_create(&vcpuID, &vcpuExit, nil)
	if ret != C.HV_SUCCESS {
		C.munmap(memoryPtr, C.size_t(memorySize))
		C.hv_vm_destroy()
		return fmt.Errorf("hv_vcpu_create failed: %d", ret)
	}
	v.vcpuID = vcpuID
	v.vcpuExit = vcpuExit

	// Set initial ARM64 registers for kernel boot
	// PC = entry point (0x80000 is standard for ARM64 Linux Image format)
	entryPoint := C.uint64_t(0x80000)
	ret = C.hv_vcpu_set_reg(vcpuID, C.HV_REG_PC, entryPoint)
	if ret != C.HV_SUCCESS {
		v.cleanup()
		return fmt.Errorf("failed to set PC register: %d", ret)
	}

	// X0 = address of DTB (device tree blob) — 0 for now
	ret = C.hv_vcpu_set_reg(vcpuID, C.HV_REG_X0, C.uint64_t(0))
	if ret != C.HV_SUCCESS {
		v.cleanup()
		return fmt.Errorf("failed to set X0 register: %d", ret)
	}

	// X1, X2, X3 = 0 (reserved, must be zero per ARM64 boot protocol)
	C.hv_vcpu_set_reg(vcpuID, C.HV_REG_X1, C.uint64_t(0))
	C.hv_vcpu_set_reg(vcpuID, C.HV_REG_X2, C.uint64_t(0))
	C.hv_vcpu_set_reg(vcpuID, C.HV_REG_X3, C.uint64_t(0))

	// CPSR = EL1h (exception level 1, handler mode) = 0x3c5
	ret = C.hv_vcpu_set_reg(vcpuID, C.HV_REG_CPSR, C.uint64_t(0x3c5))
	if ret != C.HV_SUCCESS {
		v.cleanup()
		return fmt.Errorf("failed to set CPSR register: %d", ret)
	}

	v.running = true
	v.ip = "172.16.0.2" // Placeholder — real IP comes from vmnet DHCP

	return nil
}

// RunVCPU executes the vCPU in a loop, handling exits.
// This is the core execution loop — call from a goroutine.
func (v *VM) RunVCPU() error {
	if !v.running {
		return fmt.Errorf("VM is not running")
	}

	for v.running {
		ret := C.hv_vcpu_run(v.vcpuID)
		if ret != C.HV_SUCCESS {
			return fmt.Errorf("hv_vcpu_run failed: %d", ret)
		}

		// Check exit reason from the exit info structure
		reason := v.vcpuExit.reason
		switch reason {
		case C.HV_EXIT_REASON_CANCELED:
			// VM was asked to stop
			v.running = false
			return nil
		case C.HV_EXIT_REASON_EXCEPTION:
			// Guest exception — for now, stop the VM
			// A full implementation would decode the exception syndrome (ESR)
			// and handle MMIO, HVC calls, etc.
			v.running = false
			return fmt.Errorf("guest exception at PC (check ESR for details)")
		case C.HV_EXIT_REASON_VTIMER_ACTIVATED:
			// Virtual timer fired — inject timer interrupt and continue
			continue
		case C.HV_EXIT_REASON_UNKNOWN:
			v.running = false
			return fmt.Errorf("unknown exit reason")
		default:
			continue
		}
	}

	return nil
}

// Stop gracefully shuts down the VM
func (v *VM) Stop() error {
	if !v.running {
		return fmt.Errorf("VM is not running")
	}
	v.running = false

	// Force the vCPU out of hv_vcpu_run if it's blocked
	vcpus := []C.hv_vcpu_t{v.vcpuID}
	C.hv_vcpus_exit(&vcpus[0], 1)

	v.cleanup()
	return nil
}

// cleanup releases hypervisor resources
func (v *VM) cleanup() {
	if v.vcpuID != 0 {
		C.hv_vcpu_destroy(v.vcpuID)
		v.vcpuID = 0
	}
	v.vcpuExit = nil

	if v.memoryPtr != nil && v.memorySize > 0 {
		C.hv_vm_unmap(C.hv_ipa_t(0), C.size_t(v.memorySize))
		C.munmap(v.memoryPtr, C.size_t(v.memorySize))
		v.memoryPtr = nil
		v.memorySize = 0
	}

	C.hv_vm_destroy()
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
	return 0
}

// Cleanup releases all VM resources (implements hypervisor.VM interface)
func (v *VM) Cleanup() error {
	if v.running {
		return v.Stop()
	}
	v.cleanup()
	return nil
}
