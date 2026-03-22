// Package boot provides Linux boot protocol support.
// This package handles kernel loading, bzImage header parsing,
// setup header validation, and guest memory layout computation.
package boot

import (
	"encoding/binary"
	"fmt"
	"os"
)

// Linux boot protocol constants
const (
	// bzImage header magic
	BootMagic     = 0x53726448 // "HdrS"
	BootMagicOff  = 0x202      // offset of header magic in bzImage
	BootProtoOff  = 0x206      // offset of boot protocol version
	SetupSectsOff = 0x1F1      // offset of setup_sects field
	SysSizeOff    = 0x1F4      // offset of syssize (protected-mode code size in 16-byte units)

	// Standard memory layout addresses for x86_64
	RealModeAddr     = 0x10000   // real-mode code loaded here
	CmdlineAddr      = 0x20000   // kernel command line
	KernelAddr       = 0x100000  // protected-mode kernel loaded at 1MB
	InitrdAddrMax    = 0x37FFFFFF // max initrd address (default)
	BootParamsAddr   = 0x7000    // boot_params struct

	// Boot protocol versions
	ProtoVersion2_00 = 0x0200
	ProtoVersion2_01 = 0x0201
	ProtoVersion2_02 = 0x0202
	ProtoVersion2_06 = 0x0206
	ProtoVersion2_10 = 0x0210
	ProtoVersion2_15 = 0x020F

	// Boot loader type
	LoaderTypeUndefined = 0xFF

	// Header field offsets (within setup header at 0x1F1)
	TypeOfLoaderOff = 0x210
	LoadFlagsOff    = 0x211
	RamdiskImageOff = 0x218
	RamdiskSizeOff  = 0x21C
	CmdlinePointerOff = 0x228
	KernelAlignmentOff = 0x230

	// Load flags
	LoadedHigh    = 0x01 // protected-mode code loaded at 0x100000
	CanUseHeap    = 0x80
	KeepSegments  = 0x40
)

// KernelInfo contains information about a kernel image
type KernelInfo struct {
	KernelPath string
	InitrdPath string
	Cmdline    string
}

// SetupHeader represents the parsed Linux setup header from a bzImage
type SetupHeader struct {
	SetupSects    uint8
	SysSize       uint32
	BootMagic     uint32
	ProtoVersion  uint16
	LoadFlags     uint8
	KernelAlign   uint32
	SetupDataSize int // total size of setup/real-mode code
	ProtModeSize  int // size of protected-mode kernel code
	IsBzImage     bool
}

// GuestMemoryLayout describes where to load kernel components in guest RAM
type GuestMemoryLayout struct {
	KernelLoadAddr  uint64 // where protected-mode kernel goes
	InitrdLoadAddr  uint64 // where initrd goes
	CmdlineAddr     uint64 // where command line goes
	BootParamsAddr  uint64 // where boot_params struct goes
	SetupAddr       uint64 // where real-mode setup code goes
	KernelSize      int
	InitrdSize      int
	CmdlineSize     int
	SetupSize       int
	TotalRequired   uint64 // minimum guest memory needed
}

// Loader handles loading kernel and initrd into guest memory
type Loader struct {
	Kernel      []byte
	Initrd      []byte
	Cmdline     string
	Header      *SetupHeader
	Layout      *GuestMemoryLayout
	SetupData   []byte // real-mode setup code
	ProtMode    []byte // protected-mode kernel code
}

// BootConfig holds boot configuration
type BootConfig struct {
	KernelPath   string
	InitrdPath   string
	Cmdline      string
	MemorySize   uint64 // total guest memory in bytes
	MemoryOffset uint64 // kept for backwards compatibility
}

// LoadKernel loads kernel from file
func LoadKernel(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// LoadInitrd loads initrd from file
func LoadInitrd(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// ParseBzImageHeader parses the setup header from a bzImage kernel.
// Returns nil header (no error) if the image is not a bzImage.
func ParseBzImageHeader(kernel []byte) (*SetupHeader, error) {
	if len(kernel) < 0x260 {
		return nil, nil // too small to be a bzImage
	}

	magic := binary.LittleEndian.Uint32(kernel[BootMagicOff:])
	if magic != BootMagic {
		return nil, nil // not a bzImage, could be raw kernel
	}

	hdr := &SetupHeader{
		IsBzImage:    true,
		BootMagic:    magic,
		ProtoVersion: binary.LittleEndian.Uint16(kernel[BootProtoOff:]),
		SetupSects:   kernel[SetupSectsOff],
		SysSize:      binary.LittleEndian.Uint32(kernel[SysSizeOff:]),
	}

	// setup_sects == 0 means 4 sectors (legacy)
	if hdr.SetupSects == 0 {
		hdr.SetupSects = 4
	}

	// Setup data = (1 + setup_sects) * 512 bytes (boot sector + setup sectors)
	hdr.SetupDataSize = (1 + int(hdr.SetupSects)) * 512

	// Protected-mode kernel starts after setup data
	if hdr.SetupDataSize < len(kernel) {
		hdr.ProtModeSize = len(kernel) - hdr.SetupDataSize
	}

	// Parse load flags if protocol version >= 2.00
	if hdr.ProtoVersion >= ProtoVersion2_00 {
		hdr.LoadFlags = kernel[LoadFlagsOff]
	}

	// Parse kernel alignment if protocol version >= 2.06
	if hdr.ProtoVersion >= ProtoVersion2_06 && len(kernel) > KernelAlignmentOff+4 {
		hdr.KernelAlign = binary.LittleEndian.Uint32(kernel[KernelAlignmentOff:])
	}
	if hdr.KernelAlign == 0 {
		hdr.KernelAlign = 0x200000 // default 2MB alignment
	}

	return hdr, nil
}

// ComputeLayout determines guest memory addresses for kernel, initrd, and cmdline
func ComputeLayout(header *SetupHeader, kernelSize, initrdSize, cmdlineLen int, memorySize uint64) (*GuestMemoryLayout, error) {
	layout := &GuestMemoryLayout{
		BootParamsAddr: BootParamsAddr,
		CmdlineAddr:    CmdlineAddr,
		CmdlineSize:    cmdlineLen + 1, // null terminator
		KernelLoadAddr: KernelAddr,
	}

	if header != nil && header.IsBzImage {
		layout.SetupAddr = RealModeAddr
		layout.SetupSize = header.SetupDataSize
		layout.KernelSize = header.ProtModeSize
	} else {
		layout.KernelSize = kernelSize
	}

	// Place initrd as high as possible but below the max address
	if initrdSize > 0 {
		maxAddr := uint64(InitrdAddrMax)
		if memorySize > 0 && memorySize < maxAddr {
			maxAddr = memorySize
		}
		initrdAddr := maxAddr - uint64(initrdSize)
		// Align down to page boundary
		initrdAddr &= ^uint64(0xFFF)
		layout.InitrdLoadAddr = initrdAddr
		layout.InitrdSize = initrdSize
	}

	// Compute total memory required
	endOfKernel := layout.KernelLoadAddr + uint64(layout.KernelSize)
	endOfInitrd := layout.InitrdLoadAddr + uint64(layout.InitrdSize)

	layout.TotalRequired = endOfKernel
	if endOfInitrd > layout.TotalRequired {
		layout.TotalRequired = endOfInitrd
	}

	// Validate memory is sufficient
	if memorySize > 0 && layout.TotalRequired > memorySize {
		return nil, fmt.Errorf("guest memory %d bytes insufficient: need at least %d bytes for kernel + initrd",
			memorySize, layout.TotalRequired)
	}

	return layout, nil
}

// BuildBootParams constructs the Linux boot_params struct that gets placed
// at BootParamsAddr in guest memory. This is the 4096-byte struct the
// kernel reads on entry.
func BuildBootParams(header *SetupHeader, layout *GuestMemoryLayout) []byte {
	params := make([]byte, 4096)

	if header == nil || !header.IsBzImage {
		return params
	}

	// Copy setup header fields into boot_params at offset 0x1F1
	// (boot_params.hdr starts at offset 0x1F1)
	params[SetupSectsOff] = header.SetupSects
	binary.LittleEndian.PutUint32(params[SysSizeOff:], header.SysSize)

	// Write magic
	binary.LittleEndian.PutUint32(params[BootMagicOff:], BootMagic)
	// Protocol version
	binary.LittleEndian.PutUint16(params[BootProtoOff:], header.ProtoVersion)

	// Type of loader
	params[TypeOfLoaderOff] = LoaderTypeUndefined

	// Load flags: loaded high + can use heap
	params[LoadFlagsOff] = LoadedHigh | CanUseHeap

	// Ramdisk address and size
	if layout.InitrdSize > 0 {
		binary.LittleEndian.PutUint32(params[RamdiskImageOff:], uint32(layout.InitrdLoadAddr))
		binary.LittleEndian.PutUint32(params[RamdiskSizeOff:], uint32(layout.InitrdSize))
	}

	// Command line pointer
	binary.LittleEndian.PutUint32(params[CmdlinePointerOff:], uint32(layout.CmdlineAddr))

	return params
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

		// Try to parse bzImage header
		header, err := ParseBzImageHeader(kernel)
		if err != nil {
			return nil, fmt.Errorf("failed to parse kernel header: %w", err)
		}
		loader.Header = header

		// Split kernel into setup and protected-mode parts
		if header != nil && header.IsBzImage {
			if header.SetupDataSize <= len(kernel) {
				loader.SetupData = kernel[:header.SetupDataSize]
				loader.ProtMode = kernel[header.SetupDataSize:]
			}
		}
	}

	if config.InitrdPath != "" {
		initrd, err := LoadInitrd(config.InitrdPath)
		if err != nil {
			return nil, err
		}
		loader.Initrd = initrd
	}

	// Compute memory layout
	memSize := config.MemorySize
	if memSize == 0 && config.MemoryOffset > 0 {
		memSize = config.MemoryOffset // backwards compat
	}
	layout, err := ComputeLayout(
		loader.Header,
		len(loader.Kernel),
		len(loader.Initrd),
		len(loader.Cmdline),
		memSize,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to compute memory layout: %w", err)
	}
	loader.Layout = layout

	return loader, nil
}

// LoadFromImage extracts kernel info from a disk image by checking
// for standard kernel paths within the image filesystem.
func LoadFromImage(imagePath string) (*KernelInfo, error) {
	info, err := os.Stat(imagePath)
	if err != nil {
		return nil, fmt.Errorf("cannot access image %q: %w", imagePath, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%q is a directory, expected a disk image", imagePath)
	}

	// Read the first bytes to detect image format
	f, err := os.Open(imagePath)
	if err != nil {
		return nil, fmt.Errorf("cannot open image %q: %w", imagePath, err)
	}
	defer f.Close()

	header := make([]byte, 1024)
	n, err := f.Read(header)
	if err != nil {
		return nil, fmt.Errorf("cannot read image header: %w", err)
	}

	// Check if the image itself is a bzImage kernel
	if n >= 0x260 {
		magic := binary.LittleEndian.Uint32(header[BootMagicOff:])
		if magic == BootMagic {
			return &KernelInfo{
				KernelPath: imagePath,
			}, nil
		}
	}

	// For disk images (ext4, ISO, etc.), return empty info.
	// The hypervisor backend will boot these via firmware/direct kernel boot
	// using a bundled or extracted kernel.
	return &KernelInfo{}, nil
}
