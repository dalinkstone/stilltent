//go:build linux
// +build linux

package kvm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dalinkstone/tent/pkg/models"
)

func TestBackend_CreateVM(t *testing.T) {
	// Create a temporary base directory
	tempDir, err := os.MkdirTemp("", "kvm-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a mock /dev/kvm for testing
	// We can't actually open /dev/kvm in tests, so we'll create a test that doesn't require it
	backend := &Backend{
		baseDir:   tempDir,
		vmConfigs: make(map[string]*models.VMConfig),
	}
	config := &models.VMConfig{
		Name:     "test-vm",
		VCPUs:    2,
		MemoryMB: 1024,
	}

	vm, err := backend.CreateVM(config)
	if err != nil {
		t.Fatalf("CreateVM failed: %v", err)
	}

	if vm.GetConfig().Name != "test-vm" {
		t.Errorf("Expected VM name 'test-vm', got '%s'", vm.GetConfig().Name)
	}
}

func TestVM_Status(t *testing.T) {
	vm := &VM{
		config: &models.VMConfig{
			Name:     "test-vm",
			VCPUs:    2,
			MemoryMB: 1024,
		},
		running: false,
	}

	status, err := vm.Status()
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status != models.VMStatusStopped {
		t.Errorf("Expected VMStatusStopped, got %v", status)
	}

	vm.running = true
	status, err = vm.Status()
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status != models.VMStatusRunning {
		t.Errorf("Expected VMStatusRunning, got %v", status)
	}
}

func TestVM_StartStop(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "kvm-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a fake rootfs image for the test
	rootfsDir := filepath.Join(tempDir, "storage", "images")
	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		t.Fatalf("Failed to create rootfs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootfsDir, "test.img"), []byte("fake"), 0644); err != nil {
		t.Fatalf("Failed to create fake rootfs: %v", err)
	}

	vm := &VM{
		config: &models.VMConfig{
			Name:     "test-vm",
			VCPUs:    2,
			MemoryMB: 1024,
			RootFS:   "test",
		},
		backend: &Backend{
			baseDir:   tempDir,
			vmConfigs: make(map[string]*models.VMConfig),
		},
	}

	// Start should fail because we don't have a real kernel
	// but the VM should at least be created
	if err := vm.Start(); err == nil {
		t.Log("VM started (kernel may be available in test environment)")
		// Stop should work if we started
		if err := vm.Stop(); err != nil {
			t.Fatalf("Stop failed: %v", err)
		}
	} else {
		// Expected - no kernel available in test environment
		t.Logf("Start failed as expected (no kernel): %v", err)
	}

	// Test Stop on non-running VM
	if err := vm.Stop(); err == nil {
		t.Error("Expected error when stopping already-stopped VM")
	}
}
