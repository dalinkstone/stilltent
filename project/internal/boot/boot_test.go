package boot

import (
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
		Kernel: []byte("test-kernel"),
		Initrd: []byte("test-initrd"),
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
