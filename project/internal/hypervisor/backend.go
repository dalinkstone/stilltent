// Package hypervisor provides a cross-platform interface for VM management
// using the host hypervisor directly (KVM on Linux, Hypervisor.framework on macOS).
package hypervisor

import (
	"github.com/dalinkstone/tent/pkg/models"
)

// Backend defines the interface for hypervisor-specific VM operations.
// Each platform (Linux/KVM, macOS/Hypervisor.framework) provides its own implementation.
type Backend interface {
	// CreateVM allocates VM resources and returns a VM handle
	CreateVM(config *models.VMConfig) (VM, error)

	// ListVMs returns all active VMs on the system
	ListVMs() ([]VM, error)

	// DestroyVM releases all resources associated with a VM
	DestroyVM(vm VM) error
}

// VM represents a managed virtual machine instance
type VM interface {
	// Start boots the VM
	Start() error

	// Stop gracefully shuts down the VM
	Stop() error

	// Kill forcefully terminates the VM
	Kill() error

	// Status returns the current VM state
	Status() (models.VMStatus, error)

	// GetConfig returns the VM's configuration
	GetConfig() *models.VMConfig

	// GetIP returns the VM's network IP address
	GetIP() string

	// GetPID returns the VM process ID (if applicable)
	GetPID() int

	// Cleanup releases all VM resources
	Cleanup() error
}

// ErrUnsupportedPlatform indicates the current OS is not supported
var ErrUnsupportedPlatform = &Error{msg: "hypervisor backend not supported on this platform"}

// Error represents a hypervisor backend error
type Error struct {
	msg string
}

func (e *Error) Error() string {
	return e.msg
}
