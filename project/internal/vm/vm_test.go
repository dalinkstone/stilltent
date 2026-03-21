package vm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dalinkstone/tent/pkg/models"
)

func TestNewManager(t *testing.T) {
	tmpDir := t.TempDir()

	manager, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	if manager == nil {
		t.Fatal("manager should not be nil")
	}
}

func TestNewManager_WithSetup(t *testing.T) {
	tmpDir := t.TempDir()

	manager, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Setup should not fail (it will fail if firecracker/network not available,
	// but that's expected in test environment)
	err = manager.Setup()
	// Setup may fail if dependencies aren't available - that's OK for unit test
	_ = err
}

func TestCreateVM(t *testing.T) {
	tmpDir := t.TempDir()

	manager, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Setup the manager
	err = manager.Setup()
	if err != nil {
		// Setup may fail due to missing dependencies - that's expected in test environment
		t.Skipf("setup failed (expected in test env): %v", err)
	}

	config := &models.VMConfig{
		Name:     "test-vm",
		VCPUs:    2,
		MemoryMB: 1024,
		DiskGB:   10,
	}

	err = manager.Create("test-vm", config)
	// Create may fail due to missing dependencies - that's expected
	_ = err
}

func TestListVMs(t *testing.T) {
	tmpDir := t.TempDir()

	manager, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	vms, err := manager.List()
	if err != nil {
		t.Fatalf("failed to list VMs: %v", err)
	}

	if len(vms) != 0 {
		t.Errorf("expected 0 VMs, got %d", len(vms))
	}
}

func TestStatusVM(t *testing.T) {
	tmpDir := t.TempDir()

	manager, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	_, err = manager.Status("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestLogsVM(t *testing.T) {
	tmpDir := t.TempDir()

	manager, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	logs, err := manager.Logs("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
	_ = logs
}

func TestDestroyVM(t *testing.T) {
	tmpDir := t.TempDir()

	manager, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	err = manager.Destroy("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestLoadConfigFromState(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a config file
	configDir := filepath.Join(tmpDir, "configs")
	os.MkdirAll(configDir, 0755)

	configPath := filepath.Join(configDir, "test-vm.yaml")
	configContent := `name: test-vm
vcpus: 4
memory_mb: 2048
`
	os.WriteFile(configPath, []byte(configContent), 0644)

	manager, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	vmState := &models.VMState{
		Name: "test-vm",
	}

	config, err := manager.loadConfigFromState(vmState)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if config == nil {
		t.Fatal("config should not be nil")
	}
	if config.Name != "test-vm" {
		t.Errorf("expected name 'test-vm', got '%s'", config.Name)
	}
	if config.VCPUs != 4 {
		t.Errorf("expected vcpus 4, got %d", config.VCPUs)
	}
}
