// Package boot provides Linux boot protocol support.
// This package handles kernel loading and boot parameter setup.
package boot

import (
	"os"
)

// KernelInfo contains information about a kernel image
type KernelInfo struct {
	KernelPath string
	InitrdPath string
	Cmdline    string
}

// Loader handles loading kernel and initrd into guest memory
type Loader struct {
	Kernel []byte
	Initrd []byte
	Cmdline string
}

// BootConfig holds boot configuration
type BootConfig struct {
	KernelPath   string
	InitrdPath   string
	Cmdline      string
	MemoryOffset uint64
}

// LoadKernel loads kernel from file
func LoadKernel(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// LoadInitrd loads initrd from file
func LoadInitrd(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// SetupBootParams sets up boot parameters for the kernel
func SetupBootParams(config *BootConfig) (*Loader, error) {
	loader := &Loader{
		Cmdline: config.Cmdline,
	}

	if config.KernelPath != "" {
		kernel, err := LoadKernel(config.KernelPath)
		if err != nil {
			return nil, err
		}
		loader.Kernel = kernel
	}

	if config.InitrdPath != "" {
		initrd, err := LoadInitrd(config.InitrdPath)
		if err != nil {
			return nil, err
		}
		loader.Initrd = initrd
	}

	return loader, nil
}

// LoadFromImage extracts kernel info from a disk image
func LoadFromImage(imagePath string) (*KernelInfo, error) {
	// This is a stub implementation
	// In production, this would extract kernel/initrd from the image
	return &KernelInfo{}, nil
}
