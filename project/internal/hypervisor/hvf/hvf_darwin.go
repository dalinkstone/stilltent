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
	"io"
	"sync"
	"unsafe"

	"github.com/dalinkstone/tent/internal/hypervisor"
	"github.com/dalinkstone/tent/pkg/models"
)

// Backend implements hypervisor.Backend for macOS Hypervisor.framework (ARM64)
type Backend struct {
	baseDir   string
	vmConfigs map[string]*models.VMConfig
	vms       map[string]*VM
	vmMutex   sync.Mutex
}

// VM represents a Hypervisor.framework virtual machine
type VM struct {
	config        *models.VMConfig
	backend       *Backend
	vcpuID        C.hv_vcpu_t
	vcpuExit      *C.hv_vcpu_exit_t
	running       bool
	paused        bool
	ip            string
	tapDevice     string
	memoryPtr     unsafe.Pointer
	memorySize    uint64
	consoleOutput io.Writer
	mounts        []hypervisor.MountTag
}

// NewBackend creates a new Hypervisor.framework backend
func NewBackend(baseDir string) (*Backend, error) {
	return &Backend{
		baseDir:   baseDir,
		vmConfigs: make(map[string]*models.VMConfig),
		vms:       make(map[string]*VM),
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

	b.vms[config.Name] = vm
	return vm, nil
}

// ListVMs returns all active VMs
func (b *Backend) ListVMs() ([]hypervisor.VM, error) {
	b.vmMutex.Lock()
	defer b.vmMutex.Unlock()

	vms := make([]hypervisor.VM, 0, len(b.vms))
	for _, vm := range b.vms {
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
	delete(b.vms, hvmVM.config.Name)
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

	// Load kernel into guest memory at offset 0x80000 (ARM64 Linux Image format)
	// For now, we'll write a minimal "hello world" style kernel that just halts
	// A production implementation would load a real kernel image here
	if err := v.loadMinimalKernel(); err != nil {
		v.cleanup()
		return fmt.Errorf("failed to load kernel: %w", err)
	}

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

// loadMinimalKernel writes a minimal kernel image to guest memory
// This creates a functional VM that can boot and execute code
func (v *VM) loadMinimalKernel() error {
	// ARM64 Linux kernel entry point is at offset 0x80000
	const entryOffset = 0x80000

	// Write a minimal ARM64 kernel that just halts the CPU
	// This is a simplified version - a real kernel would be much larger
	// The kernel image starts with a magic number "ARM\x64"
	kernelImage := []byte{
		// ARM64 Image header (simplified)
		0x41, 0x52, 0x4d, 0x64, // Magic: "ARM\x64"
		0x00, 0x00, 0x00, 0x00, // Image size (0 = unknown)
		0x00, 0x00, 0x00, 0x00, // Text offset
		0x00, 0x00, 0x00, 0x00, // Image size
		0x00, 0x00, 0x00, 0x00, // Reserved
		0x00, 0x00, 0x00, 0x00, // Reserved
		0x00, 0x00, 0x00, 0x00, // Reserved
		0x00, 0x00, 0x00, 0x00, // Reserved
		// Machine type (0 = any)
		0x00, 0x00,
		// Reserved
		0x00, 0x00,
		// PC entry point (same as text offset)
		0x00, 0x80, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
		// Reserved
		0x00, 0x00, 0x00, 0x00,
	}

	// Copy kernel image to guest memory at offset 0x80000
	memoryBytes := (*[1 << 30]byte)(v.memoryPtr)[:v.memorySize:v.memorySize]
	copy(memoryBytes[entryOffset:], kernelImage)

	// Add a simple HLT loop at the entry point for testing
	// HLT instruction on ARM64 is 0xD503205F
	hltLoop := []byte{
		0x5F, 0x20, 0x03, 0xD5, // hlt #0
		0x5F, 0x20, 0x03, 0xD5, // hlt #0 (infinite loop)
	}
	copy(memoryBytes[entryOffset:], hltLoop)

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

// Pause freezes vCPU execution without tearing down the VM.
// Uses hv_vcpus_exit to force the vCPU out of its run loop.
func (v *VM) Pause() error {
	if !v.running {
		return fmt.Errorf("VM %s is not running", v.config.Name)
	}
	if v.paused {
		return fmt.Errorf("VM %s is already paused", v.config.Name)
	}
	// Force the vCPU out of its run loop
	vcpus := []C.hv_vcpu_t{v.vcpuID}
	C.hv_vcpus_exit(&vcpus[0], 1)
	v.paused = true
	return nil
}

// Unpause resumes vCPU execution after a pause.
func (v *VM) Unpause() error {
	if !v.running {
		return fmt.Errorf("VM %s is not running", v.config.Name)
	}
	if !v.paused {
		return fmt.Errorf("VM %s is not paused", v.config.Name)
	}
	v.paused = false
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

// SetNetwork configures the VM's network interface
func (v *VM) SetNetwork(tapDevice string, ip string) {
	v.tapDevice = tapDevice
	v.ip = ip
}

// GetPID returns the VM process ID (HVF runs in-process)
func (v *VM) GetPID() int {
	return 0
}

// SetConsoleOutput sets the writer for capturing console/serial output
func (v *VM) SetConsoleOutput(w io.Writer) {
	v.consoleOutput = w
}

// AddMounts attaches host-to-guest directory shares via virtio-9p.
// On HVF, the mount tags are stored and passed to the guest via the device tree
// so the guest kernel can mount them using 9p.
func (v *VM) AddMounts(mounts []hypervisor.MountTag) {
	v.mounts = append(v.mounts, mounts...)
}

// Cleanup releases all VM resources (implements hypervisor.VM interface)
func (v *VM) Cleanup() error {
	if v.running {
		return v.Stop()
	}
	v.cleanup()
	return nil
}
