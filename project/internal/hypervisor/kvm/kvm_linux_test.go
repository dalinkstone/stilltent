//go:build linux
// +build linux

package kvm

import (
	"testing"

	"github.com/dalinkstone/tent/pkg/models"
)

func TestBackend_CreateVM(t *testing.T) {
	backend := &Backend{baseDir: "/tmp/test"}
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
	vm := &VM{
		config: &models.VMConfig{
			Name:     "test-vm",
			VCPUs:    2,
			MemoryMB: 1024,
		},
	}

	// Start should work
	if err := vm.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Start again should fail (already running)
	if err := vm.Start(); err == nil {
		t.Error("Expected error when starting already-running VM")
	}

	// Stop should work
	if err := vm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Stop again should fail (not running)
	if err := vm.Stop(); err == nil {
		t.Error("Expected error when stopping already-stopped VM")
	}
}
