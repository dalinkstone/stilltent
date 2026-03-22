// Package hypervisor provides a cross-platform interface for VM management
// using the host hypervisor directly (KVM on Linux, Hypervisor.framework on macOS).
package hypervisor

import (
	"io"

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

	// Pause freezes vCPU execution without tearing down the VM
	Pause() error

	// Unpause resumes vCPU execution after a pause
	Unpause() error

	// Kill forcefully terminates the VM
	Kill() error

	// Status returns the current VM state
	Status() (models.VMStatus, error)

	// GetConfig returns the VM's configuration
	GetConfig() *models.VMConfig

	// GetIP returns the VM's network IP address
	GetIP() string

	// SetIP sets the VM's network IP address
	SetIP(ip string)

	// SetNetwork configures the VM's network interface
	SetNetwork(tapDevice string, ip string)

	// GetPID returns the VM process ID (if applicable)
	GetPID() int

	// SetConsoleOutput sets the writer for capturing console/serial output
	SetConsoleOutput(w io.Writer)

	// AddMounts attaches host-to-guest directory shares via virtio-9p.
	// Each MountTag maps a 9p tag to a host directory path.
	AddMounts(mounts []MountTag)

	// Cleanup releases all VM resources
	Cleanup() error
}

// MountTag represents a virtio-9p mount share descriptor.
type MountTag struct {
	// Tag is the 9p tag the guest uses to mount this share
	Tag string
	// HostPath is the absolute path on the host to share
	HostPath string
	// ReadOnly indicates whether the share is read-only
	ReadOnly bool
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
