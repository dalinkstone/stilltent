//go:build !linux

package firecracker

import (
	"github.com/dalinkstone/tent/internal/hypervisor"
)

// NewBackend returns an error on non-Linux platforms since Firecracker
// requires KVM which is only available on Linux.
func NewBackend(baseDir string) (*Backend, error) {
	return nil, hypervisor.ErrUnsupportedPlatform
}

// Backend is a stub on non-Linux platforms.
type Backend struct{}
