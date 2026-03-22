package boot

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestBootConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files
	kernelPath := filepath.Join(tmpDir, "vmlinuz")
	initrdPath := filepath.Join(tmpDir, "initrd.img")

	err := os.WriteFile(kernelPath, []byte("test-kernel"), 0644)
	if err != nil {
		t.Fatalf("failed to create kernel file: %v", err)
	}

	err = os.WriteFile(initrdPath, []byte("test-initrd"), 0644)
	if err != nil {
		t.Fatalf("failed to create initrd file: %v", err)
	}

	config := &BootConfig{
		KernelPath:   kernelPath,
		InitrdPath:   initrdPath,
		Cmdline:      "console=ttyS0",
		MemoryOffset: 0x1000000,
	}

	loader, err := SetupBootParams(config)
	if err != nil {
		t.Fatalf("SetupBootParams() returned error: %v", err)
	}

	if loader == nil {
		t.Fatal("expected loader, got nil")
	}

	if len(loader.Kernel) == 0 {
		t.Error("expected kernel data")
	}

	if len(loader.Initrd) == 0 {
		t.Error("expected initrd data")
	}

	if loader.Cmdline != "console=ttyS0" {
		t.Errorf("expected cmdline 'console=ttyS0', got '%s'", loader.Cmdline)
	}

	if loader.Layout == nil {
		t.Fatal("expected memory layout, got nil")
	}
}

func TestLoadKernel(t *testing.T) {
	tmpDir := t.TempDir()

	kernelPath := filepath.Join(tmpDir, "vmlinuz")
	testData := []byte("test-kernel-data")

	err := os.WriteFile(kernelPath, testData, 0644)
	if err != nil {
		t.Fatalf("failed to create kernel file: %v", err)
	}

	kernel, err := LoadKernel(kernelPath)
	if err != nil {
		t.Fatalf("LoadKernel() returned error: %v", err)
	}

	if string(kernel) != string(testData) {
		t.Error("kernel data mismatch")
	}
}

func TestLoadInitrd(t *testing.T) {
	tmpDir := t.TempDir()

	initrdPath := filepath.Join(tmpDir, "initrd.img")
	testData := []byte("test-initrd-data")

	err := os.WriteFile(initrdPath, testData, 0644)
	if err != nil {
		t.Fatalf("failed to create initrd file: %v", err)
	}

	initrd, err := LoadInitrd(initrdPath)
	if err != nil {
		t.Fatalf("LoadInitrd() returned error: %v", err)
	}

	if string(initrd) != string(testData) {
		t.Error("initrd data mismatch")
	}
}

func TestLoadKernelNotFound(t *testing.T) {
	_, err := LoadKernel("/nonexistent/path/vmlinuz")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestLoadInitrdNotFound(t *testing.T) {
	_, err := LoadInitrd("/nonexistent/path/initrd.img")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestBootConfigWithOnlyKernel(t *testing.T) {
	tmpDir := t.TempDir()

	kernelPath := filepath.Join(tmpDir, "vmlinuz")
	err := os.WriteFile(kernelPath, []byte("test-kernel"), 0644)
	if err != nil {
		t.Fatalf("failed to create kernel file: %v", err)
	}

	config := &BootConfig{
		KernelPath:   kernelPath,
		InitrdPath:   "", // No initrd
		Cmdline:      "console=ttyS0",
		MemoryOffset: 0x1000000,
	}

	loader, err := SetupBootParams(config)
	if err != nil {
		t.Fatalf("SetupBootParams() returned error: %v", err)
	}

	if len(loader.Kernel) == 0 {
		t.Error("expected kernel data")
	}

	// Initrd should be empty
	if len(loader.Initrd) != 0 {
		t.Error("expected empty initrd")
	}
}

func TestBootConfigWithOnlyInitrd(t *testing.T) {
	tmpDir := t.TempDir()

	initrdPath := filepath.Join(tmpDir, "initrd.img")
	err := os.WriteFile(initrdPath, []byte("test-initrd"), 0644)
	if err != nil {
		t.Fatalf("failed to create initrd file: %v", err)
	}

	config := &BootConfig{
		KernelPath:   "", // No kernel
		InitrdPath:   initrdPath,
		Cmdline:      "console=ttyS0",
		MemoryOffset: 0x1000000,
	}

	loader, err := SetupBootParams(config)
	if err != nil {
		t.Fatalf("SetupBootParams() returned error: %v", err)
	}

	// Kernel should be empty
	if len(loader.Kernel) != 0 {
		t.Error("expected empty kernel")
	}

	if len(loader.Initrd) == 0 {
		t.Error("expected initrd data")
	}
}

func TestLoaderStruct(t *testing.T) {
	loader := &Loader{
		Kernel:  []byte("test-kernel"),
		Initrd:  []byte("test-initrd"),
		Cmdline: "console=ttyS0",
	}

	if string(loader.Kernel) != "test-kernel" {
		t.Error("kernel data mismatch")
	}

	if string(loader.Initrd) != "test-initrd" {
		t.Error("initrd data mismatch")
	}

	if loader.Cmdline != "console=ttyS0" {
		t.Errorf("expected cmdline 'console=ttyS0', got '%s'", loader.Cmdline)
	}
}

// makeFakeBzImage creates a minimal bzImage-like kernel for testing
func makeFakeBzImage(setupSects uint8, protoVersion uint16, protModeSize int) []byte {
	setupSize := (1 + int(setupSects)) * 512
	total := setupSize + protModeSize
	if total < 0x260 {
		total = 0x260
	}
	img := make([]byte, total)

	// setup_sects
	img[SetupSectsOff] = setupSects
	// syssize (in 16-byte paragraphs)
	binary.LittleEndian.PutUint32(img[SysSizeOff:], uint32(protModeSize/16))
	// magic "HdrS"
	binary.LittleEndian.PutUint32(img[BootMagicOff:], BootMagic)
	// protocol version
	binary.LittleEndian.PutUint16(img[BootProtoOff:], protoVersion)
	// load flags
	img[LoadFlagsOff] = LoadedHigh | CanUseHeap

	return img
}

func TestParseBzImageHeader(t *testing.T) {
	kernel := makeFakeBzImage(4, 0x020F, 8192)

	hdr, err := ParseBzImageHeader(kernel)
	if err != nil {
		t.Fatalf("ParseBzImageHeader error: %v", err)
	}
	if hdr == nil {
		t.Fatal("expected header, got nil")
	}
	if !hdr.IsBzImage {
		t.Error("expected IsBzImage=true")
	}
	if hdr.ProtoVersion != 0x020F {
		t.Errorf("expected proto 0x020F, got 0x%04X", hdr.ProtoVersion)
	}
	if hdr.SetupSects != 4 {
		t.Errorf("expected 4 setup sects, got %d", hdr.SetupSects)
	}
	if hdr.SetupDataSize != (1+4)*512 {
		t.Errorf("expected setup data size %d, got %d", (1+4)*512, hdr.SetupDataSize)
	}
	if hdr.LoadFlags&LoadedHigh == 0 {
		t.Error("expected LoadedHigh flag set")
	}
}

func TestParseBzImageHeaderTooSmall(t *testing.T) {
	hdr, err := ParseBzImageHeader([]byte{1, 2, 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hdr != nil {
		t.Error("expected nil header for small input")
	}
}

func TestParseBzImageHeaderNoMagic(t *testing.T) {
	kernel := make([]byte, 0x300)
	hdr, err := ParseBzImageHeader(kernel)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hdr != nil {
		t.Error("expected nil header for non-bzImage")
	}
}

func TestParseBzImageHeaderZeroSetupSects(t *testing.T) {
	kernel := makeFakeBzImage(0, 0x0200, 4096)
	// Zero setup_sects should be treated as 4
	kernel[SetupSectsOff] = 0

	hdr, err := ParseBzImageHeader(kernel)
	if err != nil {
		t.Fatalf("ParseBzImageHeader error: %v", err)
	}
	if hdr.SetupSects != 4 {
		t.Errorf("expected 4 setup sects (legacy default), got %d", hdr.SetupSects)
	}
}

func TestComputeLayout(t *testing.T) {
	hdr := &SetupHeader{
		IsBzImage:     true,
		SetupSects:    4,
		SetupDataSize: 2560,
		ProtModeSize:  8192,
	}

	layout, err := ComputeLayout(hdr, 10752, 1024*1024, 20, 128*1024*1024)
	if err != nil {
		t.Fatalf("ComputeLayout error: %v", err)
	}
	if layout.KernelLoadAddr != KernelAddr {
		t.Errorf("expected kernel at 0x%X, got 0x%X", KernelAddr, layout.KernelLoadAddr)
	}
	if layout.KernelSize != 8192 {
		t.Errorf("expected kernel size 8192, got %d", layout.KernelSize)
	}
	if layout.InitrdSize != 1024*1024 {
		t.Errorf("expected initrd size %d, got %d", 1024*1024, layout.InitrdSize)
	}
	if layout.InitrdLoadAddr == 0 {
		t.Error("expected non-zero initrd address")
	}
	if layout.InitrdLoadAddr%4096 != 0 {
		t.Error("expected page-aligned initrd address")
	}
	if layout.SetupAddr != RealModeAddr {
		t.Errorf("expected setup at 0x%X, got 0x%X", RealModeAddr, layout.SetupAddr)
	}
}

func TestComputeLayoutInsufficientMemory(t *testing.T) {
	hdr := &SetupHeader{
		IsBzImage:     true,
		SetupDataSize: 2560,
		ProtModeSize:  8192,
	}

	_, err := ComputeLayout(hdr, 10752, 1024*1024, 20, 1024) // 1KB memory
	if err == nil {
		t.Error("expected error for insufficient memory")
	}
}

func TestComputeLayoutNoHeader(t *testing.T) {
	layout, err := ComputeLayout(nil, 4096, 0, 15, 64*1024*1024)
	if err != nil {
		t.Fatalf("ComputeLayout error: %v", err)
	}
	if layout.KernelSize != 4096 {
		t.Errorf("expected raw kernel size 4096, got %d", layout.KernelSize)
	}
	if layout.SetupSize != 0 {
		t.Error("expected no setup data for raw kernel")
	}
}

func TestBuildBootParams(t *testing.T) {
	hdr := &SetupHeader{
		IsBzImage:    true,
		SetupSects:   4,
		SysSize:      512,
		BootMagic:    BootMagic,
		ProtoVersion: 0x020F,
	}
	layout := &GuestMemoryLayout{
		KernelLoadAddr: KernelAddr,
		InitrdLoadAddr: 0x7000000,
		InitrdSize:     1024 * 1024,
		CmdlineAddr:    CmdlineAddr,
	}

	params := BuildBootParams(hdr, layout)
	if len(params) != 4096 {
		t.Fatalf("expected 4096 bytes, got %d", len(params))
	}

	// Verify magic
	magic := binary.LittleEndian.Uint32(params[BootMagicOff:])
	if magic != BootMagic {
		t.Errorf("expected magic 0x%08X, got 0x%08X", BootMagic, magic)
	}

	// Verify ramdisk address
	ramdiskAddr := binary.LittleEndian.Uint32(params[RamdiskImageOff:])
	if ramdiskAddr != 0x7000000 {
		t.Errorf("expected ramdisk addr 0x7000000, got 0x%08X", ramdiskAddr)
	}

	// Verify cmdline pointer
	cmdPtr := binary.LittleEndian.Uint32(params[CmdlinePointerOff:])
	if cmdPtr != uint32(CmdlineAddr) {
		t.Errorf("expected cmdline addr 0x%X, got 0x%X", CmdlineAddr, cmdPtr)
	}

	// Verify loader type
	if params[TypeOfLoaderOff] != LoaderTypeUndefined {
		t.Errorf("expected loader type 0xFF, got 0x%02X", params[TypeOfLoaderOff])
	}
}

func TestBuildBootParamsNilHeader(t *testing.T) {
	layout := &GuestMemoryLayout{}
	params := BuildBootParams(nil, layout)
	if len(params) != 4096 {
		t.Fatalf("expected 4096 bytes, got %d", len(params))
	}
	// Should be all zeros
	for i, b := range params {
		if b != 0 {
			t.Errorf("expected zero at offset %d, got 0x%02X", i, b)
			break
		}
	}
}

func TestSetupBootParamsWithBzImage(t *testing.T) {
	tmpDir := t.TempDir()

	kernel := makeFakeBzImage(4, 0x020F, 8192)
	kernelPath := filepath.Join(tmpDir, "vmlinuz")
	if err := os.WriteFile(kernelPath, kernel, 0644); err != nil {
		t.Fatal(err)
	}

	initrd := make([]byte, 4096)
	initrdPath := filepath.Join(tmpDir, "initrd.img")
	if err := os.WriteFile(initrdPath, initrd, 0644); err != nil {
		t.Fatal(err)
	}

	config := &BootConfig{
		KernelPath: kernelPath,
		InitrdPath: initrdPath,
		Cmdline:    "console=ttyS0 root=/dev/vda",
		MemorySize: 256 * 1024 * 1024,
	}

	loader, err := SetupBootParams(config)
	if err != nil {
		t.Fatalf("SetupBootParams error: %v", err)
	}

	if loader.Header == nil {
		t.Fatal("expected parsed bzImage header")
	}
	if !loader.Header.IsBzImage {
		t.Error("expected IsBzImage=true")
	}
	if loader.SetupData == nil {
		t.Error("expected setup data to be split out")
	}
	if loader.ProtMode == nil {
		t.Error("expected protected-mode code to be split out")
	}
	if loader.Layout == nil {
		t.Fatal("expected memory layout")
	}
	if loader.Layout.KernelLoadAddr != KernelAddr {
		t.Errorf("expected kernel at 0x%X", KernelAddr)
	}
}

func TestLoadFromImageBzImage(t *testing.T) {
	tmpDir := t.TempDir()

	kernel := makeFakeBzImage(4, 0x020F, 4096)
	path := filepath.Join(tmpDir, "vmlinuz")
	if err := os.WriteFile(path, kernel, 0644); err != nil {
		t.Fatal(err)
	}

	info, err := LoadFromImage(path)
	if err != nil {
		t.Fatalf("LoadFromImage error: %v", err)
	}
	if info.KernelPath != path {
		t.Errorf("expected kernel path %q, got %q", path, info.KernelPath)
	}
}

func TestLoadFromImageDisk(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake disk image (no bzImage magic)
	disk := make([]byte, 2048)
	path := filepath.Join(tmpDir, "disk.img")
	if err := os.WriteFile(path, disk, 0644); err != nil {
		t.Fatal(err)
	}

	info, err := LoadFromImage(path)
	if err != nil {
		t.Fatalf("LoadFromImage error: %v", err)
	}
	if info.KernelPath != "" {
		t.Error("expected empty kernel path for non-bzImage disk")
	}
}

func TestLoadFromImageNotFound(t *testing.T) {
	_, err := LoadFromImage("/nonexistent/image.img")
	if err == nil {
		t.Error("expected error for nonexistent image")
	}
}

func TestLoadFromImageDirectory(t *testing.T) {
	_, err := LoadFromImage(t.TempDir())
	if err == nil {
		t.Error("expected error for directory")
	}
}
