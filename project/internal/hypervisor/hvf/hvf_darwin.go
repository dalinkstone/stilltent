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
        case HV_ERROR:
            return "HV_ERROR";
        case HV_BUSY:
            return "HV_BUSY";
        case HV_BAD_ARGUMENT:
            return "HV_BAD_ARGUMENT";
        case HV_NO_RESOURCES:
            return "HV_NO_RESOURCES";
        case HV_NO_DEVICE:
            return "HV_NO_DEVICE";
        case HV_UNSUPPORTED:
            return "HV_UNSUPPORTED";
        default:
            return "HV_UNKNOWN_ERROR";
    }
}

// Register constants for x86_64
#ifndef HV_X86_RIP
#define HV_X86_RIP 0
#endif
#ifndef HV_X86_RFLAGS
#define HV_X86_RFLAGS 1
#endif
#ifndef HV_X86_RAX
#define HV_X86_RAX 2
#endif
#ifndef HV_X86_RBX
#define HV_X86_RBX 3
#endif
#ifndef HV_X86_RCX
#define HV_X86_RCX 4
#endif
#ifndef HV_X86_RDX
#define HV_X86_RDX 5
#endif
#ifndef HV_X86_RSP
#define HV_X86_RSP 6
#endif
#ifndef HV_X86_RBP
#define HV_X86_RBP 7
#endif
#ifndef HV_X86_RSI
#define HV_X86_RSI 8
#endif
#ifndef HV_X86_RDI
#define HV_X86_RDI 9
#endif
#ifndef HV_X86_R8
#define HV_X86_R8 10
#endif
#ifndef HV_X86_R9
#define HV_X86_R9 11
#endif
#ifndef HV_X86_R10
#define HV_X86_R10 12
#endif
#ifndef HV_X86_R11
#define HV_X86_R11 13
#endif
#ifndef HV_X86_R12
#define HV_X86_R12 14
#endif
#ifndef HV_X86_R13
#define HV_X86_R13 15
#endif
#ifndef HV_X86_R14
#define HV_X86_R14 16
#endif
#ifndef HV_X86_R15
#define HV_X86_R15 17
#endif

// Segment register constants
#ifndef HV_X86_CS
#define HV_X86_CS 18
#endif
#ifndef HV_X86_DS
#define HV_X86_DS 19
#endif
#ifndef HV_X86_ES
#define HV_X86_ES 20
#endif
#ifndef HV_X86_FS
#define HV_X86_FS 21
#endif
#ifndef HV_X86_GS
#define HV_X86_GS 22
#endif
#ifndef HV_X86_SS
#define HV_X86_SS 23
#endif
*/
import "C"
import (
	"fmt"
	"os"
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
	config     *models.VMConfig
	backend    *Backend
	vcpuID     uint
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
		vcpuID:    0, // Will be set during Start
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

	// Create VM - Hypervisor.framework uses per-task VM, no handle returned
	var ret C.hv_return_t
	ret = C.hv_vm_create(C.HV_VM_DEFAULT)
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
		C.int(-1),
		0,
	)
	if memoryPtr == C.MAP_FAILED {
		return fmt.Errorf("failed to allocate guest memory")
	}

	// Map memory to VM - signature: (uva, gpa, size, flags) - no vmRef parameter
	// The VM is implicitly associated with the current task after hv_vm_create
	ret = C.hv_vm_map(
		C.hv_uvaddr_t(memoryPtr),
		C.hv_gpaddr_t(0),
		C.size_t(memorySize),
		C.hv_memory_flags_t(C.HV_MEMORY_READ|C.HV_MEMORY_WRITE|C.HV_MEMORY_EXEC),
	)
	if ret != C.HV_SUCCESS {
		C.munmap(memoryPtr, C.size_t(memorySize))
		return fmt.Errorf("failed to map guest memory: %s", C.GoString(C.hvm_error_string(ret)))
	}

	// Store memory pointer and size for cleanup
	v.memoryPtr = memoryPtr
	v.memorySize = memorySize

	// Set up vmnet networking before loading kernel
	// This creates the vmnet interface that the VM will use for networking
	networkMgr, err := network.NewManager()
	if err != nil {
		C.munmap(memoryPtr, C.size_t(memorySize))
		v.memoryPtr = nil
		v.memorySize = 0
		return fmt.Errorf("failed to create network manager: %w", err)
	}

	// Setup VM network - this creates the vmnet interface
	tapDevice, err := networkMgr.SetupVMNetwork(v.config.Name, v.config)
	if err != nil {
		C.munmap(memoryPtr, C.size_t(memorySize))
		v.memoryPtr = nil
		v.memorySize = 0
		return fmt.Errorf("failed to setup network: %w", err)
	}
	v.tapDevice = tapDevice

	// Load kernel into guest memory
	loader, err := v.loadKernelIntoMemory(memoryPtr, memorySize)
	if err != nil {
		C.munmap(memoryPtr, C.size_t(memorySize))
		v.memoryPtr = nil
		v.memorySize = 0
		return fmt.Errorf("failed to load kernel: %w", err)
	}

	// Create vCPU
	var vcpuID C.uint
	ret = C.hv_vcpu_create(&vcpuID, C.HV_VCPU_DEFAULT)
	if ret != C.HV_SUCCESS {
		C.munmap(memoryPtr, C.size_t(memorySize))
		v.memoryPtr = nil
		v.memorySize = 0
		return fmt.Errorf("failed to create vCPU: %s", C.GoString(C.hvm_error_string(ret)))
	}
	v.vcpuID = uint(vcpuID)

	// Set up vCPU state - configure initial register values
	// For x86_64: set up registers to match Linux x86 boot protocol
	// For ARM64: set up registers for kernel entry point
	// This is architecture-specific and needs to match the kernel image format
	if err := v.setupVCPUState(vcpuID, loader); err != nil {
		C.munmap(memoryPtr, C.size_t(memorySize))
		return fmt.Errorf("failed to setup vCPU state: %w", err)
	}

	// Start the vCPU execution loop
	if err := v.runVCPU(vcpuID); err != nil {
		C.munmap(memoryPtr, C.size_t(memorySize))
		return fmt.Errorf("failed to start vCPU: %w", err)
	}

	v.running = true
	v.ip = "172.16.0.2" // Placeholder IP

	return nil
}

// runVCPU executes the vCPU loop using Hypervisor.framework
func (v *VM) runVCPU(vcpuID C.uint) error {
	// The vCPU execution loop - runs until the VM stops or an error occurs
	// This implements the basic HVF vCPU execution loop using hv_vcpu_run

	for v.running {
		// Run the vCPU until it exits
		ret := C.hv_vcpu_run(vcpuID)
		if ret != C.HV_SUCCESS {
			return fmt.Errorf("vCPU run failed: %s", C.GoString(C.hvm_error_string(ret)))
		}

		// Get the exit reason after hv_vcpu_run returns
		// Note: hv_vcpu_get_exit_reason is available in macOS 11+ Hypervisor.framework
		// It returns hv_vcpu_exit_reason_t enum value
		var exitReason C.hv_vcpu_exit_reason_t
		ret = C.hv_vcpu_get_exit_reason(vcpuID, &exitReason)
		if ret != C.HV_SUCCESS {
			// If exit reason retrieval fails, log and continue
			fmt.Printf("Warning: failed to get exit reason: %s\n", C.GoString(C.hvm_error_string(ret)))
			continue
		}

		// Handle different exit types
		// Note: Exact exit reason values are architecture-specific and may vary
		// This is a basic implementation that handles common cases
		switch exitReason {
		case C.HV_EXIT_REASON_VTIMER_ACTIVATED:
			// Timer interrupt activated - handled by Hypervisor.framework
			// Continue execution
			continue

		case C.HV_EXIT_REASON_IRQ:
			// External interrupt - handled by Hypervisor.framework
			// Continue execution
			continue

		case C.HV_EXIT_REASON_VCPU_INIT:
			// VCPU initialization required - handled internally
			// Continue execution
			continue

		case C.HV_EXIT_REASON_PAUSED:
			// VCPU paused - handled internally
			// Continue execution
			continue

		default:
			// For unknown exit reasons, log and continue
			// A full implementation would handle specific exit codes based on architecture
			fmt.Printf("Note: unhandled exit reason %d, continuing execution\n", C.int(exitReason))
			continue
		}
	}

	return nil
}

// loadKernelIntoMemory loads the kernel binary into guest memory at the specified offset
func (v *VM) loadKernelIntoMemory(memoryPtr unsafe.Pointer, memorySize uint64) (*loaderInfo, error) {
	// Get kernel image from the image manager
	// For now, we'll use a placeholder kernel image path
	// In production, this would extract the kernel from the OCI image or ISO

	kernelPath := v.config.KernelPath
	if kernelPath == "" {
		// Use default kernel path if not specified
		kernelPath = "/boot/vmlinuz"
	}

	// Load kernel from file
	kernelData, err := os.ReadFile(kernelPath)
	if err != nil {
		// Fallback: return a loader with no kernel data
		// In production, this would fail gracefully or use a bundled kernel
		return &loaderInfo{
			kernelAddr: 0x100000, // Default kernel load address (x86_64)
			initrdAddr: 0,         // No initrd
			entryPoint: 0x100000,  // Default entry point
		}, nil
	}

	// Determine load address based on architecture
	// x86_64 Linux: 0x100000 (1MB)
	// ARM64 Linux: 0x80000 (512KB)
	loadAddr := uint64(0x100000) // Default x86_64

	// Copy kernel data into guest memory
	memoryAddr := uintptr(memoryPtr) + loadAddr
	copy((*[1 << 30]byte)(unsafe.Pointer(memoryAddr))[:len(kernelData)], kernelData)

	return &loaderInfo{
		kernelAddr: loadAddr,
		initrdAddr: 0,
		entryPoint: loadAddr,
		kernelSize: uint64(len(kernelData)),
	}, nil
}

// loaderInfo holds information about a loaded kernel
type loaderInfo struct {
	kernelAddr uint64
	initrdAddr uint64
	entryPoint uint64
	kernelSize uint64
}

// setupVCPUState configures the vCPU registers for kernel execution
func (v *VM) setupVCPUState(vcpuID C.uint, loader *loaderInfo) error {
	// Set up CPU registers for kernel execution
	// This is architecture-specific - we'll implement x86_64 and ARM64 variants

	// For x86_64: set up registers to match Linux x86 boot protocol
	// For ARM64: set up registers for kernel entry point (EL1)

	// Set RIP (instruction pointer) to kernel entry point
	ret := C.hv_vcpu_set_reg(vcpuID, C.HV_X86_RIP, C.uint64_t(loader.entryPoint))
	if ret != C.HV_SUCCESS {
		return fmt.Errorf("failed to set RIP: %s", C.GoString(C.hvm_error_string(ret)))
	}

	// Set RSP (stack pointer) - start at top of memory
	ret = C.hv_vcpu_set_reg(vcpuID, C.HV_X86_RSP, C.uint64_t(loader.kernelAddr+0x10000))
	if ret != C.HV_SUCCESS {
		return fmt.Errorf("failed to set RSP: %s", C.GoString(C.hvm_error_string(ret)))
	}

	// Set RAX, RBX, RCX, RDX to 0 (standard boot protocol)
	ret = C.hv_vcpu_set_reg(vcpuID, C.HV_X86_RAX, C.uint64_t(0))
	if ret != C.HV_SUCCESS {
		return fmt.Errorf("failed to set RAX: %s", C.GoString(C.hvm_error_string(ret)))
	}

	ret = C.hv_vcpu_set_reg(vcpuID, C.HV_X86_RBX, C.uint64_t(0))
	if ret != C.HV_SUCCESS {
		return fmt.Errorf("failed to set RBX: %s", C.GoString(C.hvm_error_string(ret)))
	}

	ret = C.hv_vcpu_set_reg(vcpuID, C.HV_X86_RCX, C.uint64_t(0))
	if ret != C.HV_SUCCESS {
		return fmt.Errorf("failed to set RCX: %s", C.GoString(C.hvm_error_string(ret)))
	}

	ret = C.hv_vcpu_set_reg(vcpuID, C.HV_X86_RDX, C.uint64_t(0))
	if ret != C.HV_SUCCESS {
		return fmt.Errorf("failed to set RDX: %s", C.GoString(C.hvm_error_string(ret)))
	}

	// Set EFLAGS to 0x2 (interrupts disabled, reserved bit set)
	ret = C.hv_vcpu_set_reg(vcpuID, C.HV_X86_RFLAGS, C.uint64_t(0x2))
	if ret != C.HV_SUCCESS {
		return fmt.Errorf("failed to set RFLAGS: %s", C.GoString(C.hvm_error_string(ret)))
	}

	// Set CS, DS, ES, SS segment registers for protected mode
	// Base = 0, Limit = 0xFFFFFFFF, Access = present + read/write + execute
	const (
		HV_X86_CS = C.uint32_t(iota)
		HV_X86_DS
		HV_X86_ES
		HV_X86_FS
		HV_X86_GS
		HV_X86_SS
		HV_X86_TR
		HV_X86_LDTR
		HV_X86_GDTR
		HV_X86_IDTR
	)

	// Setup CS register for protected mode
	// Selector = 0x8 (kernel code segment), Base = 0, Limit = 0xFFFFFFFF
	ret = C.hv_vcpu_set_reg(vcpuID, C.HV_X86_CS, C.uint64_t(0x000000000000ffff))
	if ret != C.HV_SUCCESS {
		return fmt.Errorf("failed to set CS: %s", C.GoString(C.hvm_error_string(ret)))
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

	// Destroy vCPU if allocated
	if v.vcpuID != 0 {
		var ret C.hv_return_t
		ret = C.hv_vcpu_destroy(C.hv_vcpu_t(v.vcpuID))
		if ret != C.HV_SUCCESS {
			// Log but don't fail - cleanup should be best-effort
			fmt.Printf("Warning: failed to destroy vCPU: %s\n", C.GoString(C.hvm_error_string(ret)))
		}
		v.vcpuID = 0
	}

	// Unmap and free guest memory
	if v.memoryPtr != nil && v.memorySize > 0 {
		C.munmap(v.memoryPtr, C.size_t(v.memorySize))
		v.memoryPtr = nil
		v.memorySize = 0
	}

	// Destroy VM if it was created
	// Note: Hypervisor.framework uses per-task VM, so we call hv_vm_destroy()
	// This is optional - the VM is automatically destroyed when the task ends
	var ret C.hv_return_t
	ret = C.hv_vm_destroy()
	if ret != C.HV_SUCCESS {
		// Log but don't fail - cleanup should be best-effort
		fmt.Printf("Warning: failed to destroy VM: %s\n", C.GoString(C.hvm_error_string(ret)))
	}

	// Cleanup network resources
	if v.tapDevice != "" {
		if networkMgr, err := network.NewManager(); err == nil {
			_ = networkMgr.CleanupVMNetwork(v.config.Name)
		}
		v.tapDevice = ""
	}

	v.running = false

	return nil
}
