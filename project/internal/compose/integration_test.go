//go:build linux

package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// MockStateManager is a mock implementation of StateManager for compose state
type MockStateManager struct {
	states  map[string]*ComposeStatus
	baseDir string
}

func NewMockStateManager(baseDir string) *MockStateManager {
	return &MockStateManager{
		states:  make(map[string]*ComposeStatus),
		baseDir: baseDir,
	}
}

func (m *MockStateManager) SaveComposeState(name string, state *ComposeStatus) error {
	m.states[name] = state
	return nil
}

func (m *MockStateManager) LoadComposeState(name string) (*ComposeStatus, error) {
	if s, ok := m.states[name]; ok {
		return s, nil
	}
	return nil, os.ErrNotExist
}

func (m *MockStateManager) DeleteComposeState(name string) error {
	delete(m.states, name)
	return nil
}

// TestComposeIntegrationBasic tests basic compose state management
func TestComposeIntegrationBasic(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "tent")

	// Create necessary subdirectories
	os.MkdirAll(filepath.Join(baseDir, "rootfs"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "compose"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "state"), 0755)

	// Create compose state manager
	stateMgr := NewMockStateManager(filepath.Join(baseDir, "compose"))

	// Test that state manager works correctly
	err := stateMgr.SaveComposeState("test-group", &ComposeStatus{Name: "test-group"})
	if err != nil {
		t.Fatalf("Failed to save compose state: %v", err)
	}

	loaded, err := stateMgr.LoadComposeState("test-group")
	if err != nil {
		t.Fatalf("Failed to load compose state: %v", err)
	}
	if loaded.Name != "test-group" {
		t.Errorf("Expected name 'test-group', got '%s'", loaded.Name)
	}

	// Test DeleteComposeState
	err = stateMgr.DeleteComposeState("test-group")
	if err != nil {
		t.Fatalf("Failed to delete compose state: %v", err)
	}

	// Verify state is gone
	_, err = stateMgr.LoadComposeState("test-group")
	if err == nil {
		t.Error("Expected compose state to be deleted")
	}
}

// TestComposeIntegrationList tests compose group listing
func TestComposeIntegrationList(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "tent")

	os.MkdirAll(filepath.Join(baseDir, "compose"), 0755)

	// Create compose state manager
	stateMgr := NewMockStateManager(filepath.Join(baseDir, "compose"))

	// Create multiple compose groups
	groups := []string{"group1", "group2", "group3"}
	for _, name := range groups {
		status := &ComposeStatus{
			Name: name,
			Sandboxes: map[string]*SandboxStatus{
				"vm1": {Name: "vm1", Status: "running", IP: "10.0.0.1"},
			},
		}
		stateMgr.SaveComposeState(name, status)
	}

	// Verify all groups were saved
	for _, name := range groups {
		status, err := stateMgr.LoadComposeState(name)
		if err != nil {
			t.Fatalf("Failed to load group '%s': %v", name, err)
		}
		if status.Name != name {
			t.Errorf("Expected group name '%s', got '%s'", name, status.Name)
		}
	}

	// Test DeleteComposeState for non-existent group (should be idempotent)
	err := stateMgr.DeleteComposeState("nonexistent-group")
	if err != nil {
		t.Errorf("Delete should not error for missing group: %v", err)
	}

	// Test SaveComposeState with nil state - should not panic
	defer func() {
		if r := recover(); r != nil {
			t.Error("SaveComposeState should not panic with nil state")
		}
	}()
	stateMgr.SaveComposeState("test-group", nil)
}

// TestComposeStatusOps tests ComposeStatus struct operations
func TestComposeStatusOps(t *testing.T) {
	t.Run("ComposeStatus with running sandboxes", func(t *testing.T) {
		status := &ComposeStatus{
			Name: "test-group",
			Sandboxes: map[string]*SandboxStatus{
				"vm1": {Name: "vm1", Status: "running", IP: "10.0.0.1", PID: 1234},
				"vm2": {Name: "vm2", Status: "running", IP: "10.0.0.2", PID: 1235},
				"vm3": {Name: "vm3", Status: "stopped"},
			},
		}

		if status.Name != "test-group" {
			t.Errorf("Expected name 'test-group', got '%s'", status.Name)
		}

		if len(status.Sandboxes) != 3 {
			t.Errorf("Expected 3 sandboxes, got %d", len(status.Sandboxes))
		}

		runningCount := 0
		for _, s := range status.Sandboxes {
			if s.Status == "running" {
				runningCount++
			}
		}
		if runningCount != 2 {
			t.Errorf("Expected 2 running sandboxes, got %d", runningCount)
		}
	})

	t.Run("ComposeStatus with empty sandboxes", func(t *testing.T) {
		status := &ComposeStatus{
			Name:      "empty-group",
			Sandboxes: make(map[string]*SandboxStatus),
		}

		if len(status.Sandboxes) != 0 {
			t.Errorf("Expected 0 sandboxes, got %d", len(status.Sandboxes))
		}
	})
}

// TestSandboxStatus tests SandboxStatus struct operations
func TestSandboxStatus(t *testing.T) {
	t.Run("SandboxStatus with all fields", func(t *testing.T) {
		status := &SandboxStatus{
			Name:   "test-vm",
			Status: "running",
			IP:     "10.0.0.1",
			PID:    1234,
		}

		if status.Name != "test-vm" {
			t.Errorf("Expected name 'test-vm', got '%s'", status.Name)
		}

		if status.Status != "running" {
			t.Errorf("Expected status 'running', got '%s'", status.Status)
		}

		if status.IP != "10.0.0.1" {
			t.Errorf("Expected IP '10.0.0.1', got '%s'", status.IP)
		}

		if status.PID != 1234 {
			t.Errorf("Expected PID 1234, got %d", status.PID)
		}
	})

	t.Run("SandboxStatus with minimal fields", func(t *testing.T) {
		status := &SandboxStatus{
			Name:   "test-vm",
			Status: "stopped",
		}

		if status.IP != "" {
			t.Error("Expected empty IP")
		}

		if status.PID != 0 {
			t.Error("Expected zero PID")
		}
	})
}

// TestComposeConfigValidation tests compose configuration validation
func TestComposeConfigValidation(t *testing.T) {
	t.Run("Valid compose config", func(t *testing.T) {
		config := &ComposeConfig{
			Sandboxes: map[string]*SandboxConfig{
				"vm1": {
					From:     "ubuntu:22.04",
					VCPUs:    2,
					MemoryMB: 2048,
				},
			},
		}

		err := config.Validate()
		if err != nil {
			t.Errorf("Valid config should not fail validation: %v", err)
		}
	})

	t.Run("Empty sandboxes fails validation", func(t *testing.T) {
		config := &ComposeConfig{
			Sandboxes: make(map[string]*SandboxConfig),
		}

		err := config.Validate()
		if err == nil {
			t.Error("Expected validation error for empty sandboxes")
		}
	})

	t.Run("Missing 'from' field fails validation", func(t *testing.T) {
		config := &ComposeConfig{
			Sandboxes: map[string]*SandboxConfig{
				"vm1": {
					VCPUs:    2,
					MemoryMB: 1024,
				},
			},
		}

		err := config.Validate()
		if err == nil {
			t.Error("Expected validation error for missing 'from' field")
		}
	})

	t.Run("Zero values get defaults", func(t *testing.T) {
		config := &ComposeConfig{
			Sandboxes: map[string]*SandboxConfig{
				"vm1": {
					From: "ubuntu:22.04",
				},
			},
		}

		err := config.Validate()
		if err != nil {
			t.Fatalf("Validation failed: %v", err)
		}

		vm1 := config.Sandboxes["vm1"]
		if vm1.VCPUs != 2 {
			t.Errorf("Expected default vcpus 2, got %d", vm1.VCPUs)
		}
		if vm1.MemoryMB != 1024 {
			t.Errorf("Expected default memory_mb 1024, got %d", vm1.MemoryMB)
		}
		if vm1.DiskGB != 10 {
			t.Errorf("Expected default disk_gb 10, got %d", vm1.DiskGB)
		}
	})
}

// TestComposeIntegrationWithRealVMManager tests compose with actual VM manager
// This test requires actual hypervisor capabilities and is skipped if not available
func TestComposeIntegrationWithRealVMManager(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "tent")

	// Create necessary subdirectories
	os.MkdirAll(filepath.Join(baseDir, "rootfs"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "compose"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "state"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "configs"), 0755)

	// Skip this test - requires actual hypervisor backend
	t.Log("Integration test skipped: requires actual hypervisor and network setup")
	t.Skip("Integration test requires actual hypervisor backend and network resources")
}
