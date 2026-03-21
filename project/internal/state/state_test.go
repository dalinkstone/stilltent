package state

import (
	"testing"
)

func TestStateManager_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	
	sm, err := NewStateManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create state manager: %v", err)
	}
	
	vm := &VMState{
		Name:     "test-vm",
		Status:   VMStatusCreated,
		CreatedAt: 1234567890,
	}
	
	if err := sm.StoreVM(vm); err != nil {
		t.Fatalf("Failed to store VM: %v", err)
	}
	
	retrieved, err := sm.GetVM("test-vm")
	if err != nil {
		t.Fatalf("Failed to retrieve VM: %v", err)
	}
	
	if retrieved.Name != "test-vm" {
		t.Errorf("Expected name 'test-vm', got '%s'", retrieved.Name)
	}
	
	if err := sm.DeleteVM("test-vm"); err != nil {
		t.Fatalf("Failed to delete VM: %v", err)
	}
	
	_, err = sm.GetVM("test-vm")
	if err == nil {
		t.Error("Expected VM not found error after deletion")
	}
}

func TestStateManager_ListVMs(t *testing.T) {
	tmpDir := t.TempDir()
	sm, err := NewStateManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create state manager: %v", err)
	}
	
	for i := 0; i < 3; i++ {
		vm := &VMState{
			Name:      vmName(i),
			Status:    VMStatusCreated,
			CreatedAt: 1234567890,
		}
		if err := sm.StoreVM(vm); err != nil {
			t.Fatalf("Failed to store VM %d: %v", i, err)
		}
	}
	
	vms, err := sm.ListVMs()
	if err != nil {
		t.Fatalf("Failed to list VMs: %v", err)
	}
	
	if len(vms) != 3 {
		t.Errorf("Expected 3 VMs, got %d", len(vms))
	}
}

func TestStateManager_UpdateVM(t *testing.T) {
	tmpDir := t.TempDir()
	sm, err := NewStateManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create state manager: %v", err)
	}
	
	vm := &VMState{
		Name:      "test-vm",
		Status:    VMStatusCreated,
		CreatedAt: 1234567890,
	}
	
	if err := sm.StoreVM(vm); err != nil {
		t.Fatalf("Failed to store VM: %v", err)
	}
	
	if err := sm.UpdateVM("test-vm", func(v *VMState) error {
		v.Status = VMStatusRunning
		return nil
	}); err != nil {
		t.Fatalf("Failed to update VM: %v", err)
	}
	
	retrieved, err := sm.GetVM("test-vm")
	if err != nil {
		t.Fatalf("Failed to retrieve VM: %v", err)
	}
	
	if retrieved.Status != VMStatusRunning {
		t.Errorf("Expected status 'running', got '%s'", retrieved.Status)
	}
}

func vmName(i int) string {
	return "test-vm-" + string(rune('0'+i))
}
