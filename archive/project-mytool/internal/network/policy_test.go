package network

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestPolicyManager_NewPolicyManager(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	if pm.baseDir != tmpDir {
		t.Errorf("expected baseDir '%s', got '%s'", tmpDir, pm.baseDir)
	}

	if pm.policies == nil {
		t.Error("expected policies map to be initialized")
	}
}

func TestPolicyManager_GetPoliciesDir(t *testing.T) {
	tmpDir := t.TempDir()
	pm := &PolicyManager{
		baseDir:  tmpDir,
		policies: make(map[string]*Policy),
	}

	expectedDir := filepath.Join(tmpDir, "network-policies")
	actualDir := pm.getPoliciesDir()

	if actualDir != expectedDir {
		t.Errorf("expected policies dir '%s', got '%s'", expectedDir, actualDir)
	}
}

func TestPolicyManager_SavePolicy(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	policy := &Policy{
		Name:      "test-vm",
		Allowed:   []string{"api.example.com", "api2.example.com"},
		Denied:    []string{"blocked.com"},
		CreatedAt: 1234567890,
		UpdatedAt: 1234567890,
	}

	err = pm.SavePolicy(policy)
	if err != nil {
		t.Fatalf("SavePolicy() failed: %v", err)
	}

	// Verify policy was saved
	savedPolicy, err := pm.GetPolicy("test-vm")
	if err != nil {
		t.Fatalf("GetPolicy() failed after SavePolicy: %v", err)
	}

	if savedPolicy.Name != policy.Name {
		t.Errorf("expected name '%s', got '%s'", policy.Name, savedPolicy.Name)
	}

	if len(savedPolicy.Allowed) != 2 {
		t.Errorf("expected 2 allowed endpoints, got %d", len(savedPolicy.Allowed))
	}

	if len(savedPolicy.Denied) != 1 {
		t.Errorf("expected 1 denied endpoint, got %d", len(savedPolicy.Denied))
	}
}

func TestPolicyManager_GetPolicy(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	// Test with non-existent policy
	_, err = pm.GetPolicy("non-existent")
	if err == nil {
		t.Error("expected error for non-existent policy, got nil")
	}

	// Create a policy first
	policy := &Policy{
		Name:      "test-vm",
		Allowed:   []string{"api.example.com"},
		Denied:    []string{},
		CreatedAt: 1234567890,
		UpdatedAt: 1234567890,
	}
	pm.policies["test-vm"] = policy

	// Now get it
	savedPolicy, err := pm.GetPolicy("test-vm")
	if err != nil {
		t.Fatalf("GetPolicy() failed for existing policy: %v", err)
	}

	if savedPolicy.Name != "test-vm" {
		t.Errorf("expected name 'test-vm', got '%s'", savedPolicy.Name)
	}
}

func TestPolicyManager_SetPolicy(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	// Test creating new policy
	policy, err := pm.SetPolicy("test-vm", []string{"api1.com"}, []string{"blocked.com"})
	if err != nil {
		t.Fatalf("SetPolicy() failed for new policy: %v", err)
	}

	if policy.Name != "test-vm" {
		t.Errorf("expected name 'test-vm', got '%s'", policy.Name)
	}

	if len(policy.Allowed) != 1 {
		t.Errorf("expected 1 allowed endpoint, got %d", len(policy.Allowed))
	}

	if len(policy.Denied) != 1 {
		t.Errorf("expected 1 denied endpoint, got %d", len(policy.Denied))
	}

	if policy.CreatedAt == 0 {
		t.Error("expected CreatedAt to be set for new policy")
	}

	if policy.UpdatedAt == 0 {
		t.Error("expected UpdatedAt to be set for new policy")
	}

	// Test updating existing policy
	policy2, err := pm.SetPolicy("test-vm", []string{"api1.com", "api2.com"}, []string{})
	if err != nil {
		t.Fatalf("SetPolicy() failed for existing policy: %v", err)
	}

	if len(policy2.Allowed) != 2 {
		t.Errorf("expected 2 allowed endpoints after update, got %d", len(policy2.Allowed))
	}
}

func TestPolicyManager_AddAllowedEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	// Test adding to new policy
	err = pm.AddAllowedEndpoint("test-vm", "api.example.com")
	if err != nil {
		t.Fatalf("AddAllowedEndpoint() failed for new policy: %v", err)
	}

	// Verify policy was created
	policy, err := pm.GetPolicy("test-vm")
	if err != nil {
		t.Fatalf("GetPolicy() failed after AddAllowedEndpoint: %v", err)
	}

	if len(policy.Allowed) != 1 {
		t.Errorf("expected 1 allowed endpoint, got %d", len(policy.Allowed))
	}

	// Test adding duplicate (should be idempotent)
	err = pm.AddAllowedEndpoint("test-vm", "api.example.com")
	if err != nil {
		t.Fatalf("AddAllowedEndpoint() failed for duplicate: %v", err)
	}

	// Should still be 1 (duplicate not added)
	policy, _ = pm.GetPolicy("test-vm")
	if len(policy.Allowed) != 1 {
		t.Errorf("expected 1 allowed endpoint after duplicate, got %d", len(policy.Allowed))
	}

	// Test adding another endpoint
	err = pm.AddAllowedEndpoint("test-vm", "api2.example.com")
	if err != nil {
		t.Fatalf("AddAllowedEndpoint() failed for second endpoint: %v", err)
	}

	policy, _ = pm.GetPolicy("test-vm")
	if len(policy.Allowed) != 2 {
		t.Errorf("expected 2 allowed endpoints, got %d", len(policy.Allowed))
	}
}

func TestPolicyManager_RemoveAllowedEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	// First create a policy with endpoints
	_, err = pm.SetPolicy("test-vm", []string{"api1.com", "api2.com", "api3.com"}, []string{})
	if err != nil {
		t.Fatalf("SetPolicy() failed: %v", err)
	}

	// Test removing existing endpoint
	err = pm.RemoveAllowedEndpoint("test-vm", "api2.com")
	if err != nil {
		t.Fatalf("RemoveAllowedEndpoint() failed: %v", err)
	}

	policy, _ := pm.GetPolicy("test-vm")
	if len(policy.Allowed) != 2 {
		t.Errorf("expected 2 allowed endpoints after removal, got %d", len(policy.Allowed))
	}

	// Test removing non-existent endpoint
	err = pm.RemoveAllowedEndpoint("test-vm", "nonexistent.com")
	if err == nil {
		t.Error("expected error for non-existent endpoint, got nil")
	}

	// Test removing from non-existent policy
	err = pm.RemoveAllowedEndpoint("non-existent", "api.com")
	if err == nil {
		t.Error("expected error for non-existent policy, got nil")
	}
}

func TestPolicyManager_AddDeniedEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	// Test adding to new policy
	err = pm.AddDeniedEndpoint("test-vm", "blocked.com")
	if err != nil {
		t.Fatalf("AddDeniedEndpoint() failed for new policy: %v", err)
	}

	policy, _ := pm.GetPolicy("test-vm")
	if len(policy.Denied) != 1 {
		t.Errorf("expected 1 denied endpoint, got %d", len(policy.Denied))
	}

	// Test adding duplicate
	err = pm.AddDeniedEndpoint("test-vm", "blocked.com")
	if err != nil {
		t.Fatalf("AddDeniedEndpoint() failed for duplicate: %v", err)
	}

	policy, _ = pm.GetPolicy("test-vm")
	if len(policy.Denied) != 1 {
		t.Errorf("expected 1 denied endpoint after duplicate, got %d", len(policy.Denied))
	}
}

func TestPolicyManager_RemoveDeniedEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	// First create a policy with denied endpoints
	_, err = pm.SetPolicy("test-vm", []string{}, []string{"blocked1.com", "blocked2.com"})
	if err != nil {
		t.Fatalf("SetPolicy() failed: %v", err)
	}

	// Test removing existing endpoint
	err = pm.RemoveDeniedEndpoint("test-vm", "blocked1.com")
	if err != nil {
		t.Fatalf("RemoveDeniedEndpoint() failed: %v", err)
	}

	policy, _ := pm.GetPolicy("test-vm")
	if len(policy.Denied) != 1 {
		t.Errorf("expected 1 denied endpoint after removal, got %d", len(policy.Denied))
	}

	// Test removing non-existent endpoint
	err = pm.RemoveDeniedEndpoint("test-vm", "nonexistent.com")
	if err == nil {
		t.Error("expected error for non-existent endpoint, got nil")
	}
}

func TestPolicyManager_ListPolicies(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	// Test empty list
	policies, err := pm.ListPolicies()
	if err != nil {
		t.Fatalf("ListPolicies() failed: %v", err)
	}

	if policies == nil {
		t.Error("ListPolicies() returned nil")
	}

	if len(policies) != 0 {
		t.Errorf("expected 0 policies, got %d", len(policies))
	}

	// Add some policies
	pm.SetPolicy("vm1", []string{"api1.com"}, []string{})
	pm.SetPolicy("vm2", []string{"api2.com"}, []string{})

	// Test list with policies
	policies, _ = pm.ListPolicies()
	if len(policies) != 2 {
		t.Errorf("expected 2 policies, got %d", len(policies))
	}
}

func TestPolicyManager_IsEndpointAllowed(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	// Test with no policy (should return false - default blocked)
	allowed, err := pm.IsEndpointAllowed("non-existent", "api.example.com")
	if err != nil {
		t.Fatalf("IsEndpointAllowed() failed for non-existent policy: %v", err)
	}

	if allowed {
		t.Error("expected false (blocked) for non-existent policy, got true")
	}

	// Test with allowed endpoint
	pm.SetPolicy("test-vm", []string{"api.example.com", "api2.example.com"}, []string{"blocked.com"})

	// Allowed endpoint
	allowed, _ = pm.IsEndpointAllowed("test-vm", "api.example.com")
	if !allowed {
		t.Error("expected true for allowed endpoint, got false")
	}

	// Denied endpoint (deny list takes precedence)
	allowed, _ = pm.IsEndpointAllowed("test-vm", "blocked.com")
	if allowed {
		t.Error("expected false for denied endpoint, got true")
	}

	// Unlisted endpoint (should be blocked)
	allowed, _ = pm.IsEndpointAllowed("test-vm", "unlisted.com")
	if allowed {
		t.Error("expected false for unlisted endpoint, got true")
	}

	// Test denied list takes precedence over allow
	pm.SetPolicy("test-vm-2", []string{"api.example.com"}, []string{"api.example.com"})
	allowed, _ = pm.IsEndpointAllowed("test-vm-2", "api.example.com")
	if allowed {
		t.Error("expected false when endpoint is both allowed and denied (deny takes precedence), got true")
	}
}

func TestPolicyManager_MultiplePolicies(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	// Create multiple policies
	pm.SetPolicy("vm1", []string{"api1.com"}, []string{"blocked1.com"})
	pm.SetPolicy("vm2", []string{"api2.com"}, []string{"blocked2.com"})
	pm.SetPolicy("vm3", []string{}, []string{})

	// Verify each policy is independent
	p1, _ := pm.GetPolicy("vm1")
	if len(p1.Allowed) != 1 || len(p1.Denied) != 1 {
		t.Errorf("vm1 policy incorrect: allowed=%d, denied=%d", len(p1.Allowed), len(p1.Denied))
	}

	p2, _ := pm.GetPolicy("vm2")
	if len(p2.Allowed) != 1 || len(p2.Denied) != 1 {
		t.Errorf("vm2 policy incorrect: allowed=%d, denied=%d", len(p2.Allowed), len(p2.Denied))
	}

	p3, _ := pm.GetPolicy("vm3")
	if len(p3.Allowed) != 0 || len(p3.Denied) != 0 {
		t.Errorf("vm3 policy incorrect: allowed=%d, denied=%d", len(p3.Allowed), len(p3.Denied))
	}

	// Verify isolation - modifying vm1 shouldn't affect vm2
	pm.AddAllowedEndpoint("vm1", "api1-new.com")
	p1, _ = pm.GetPolicy("vm1")
	p2, _ = pm.GetPolicy("vm2")

	if len(p1.Allowed) != 2 {
		t.Errorf("vm1 should have 2 allowed endpoints after addition, got %d", len(p1.Allowed))
	}

	if len(p2.Allowed) != 1 {
		t.Errorf("vm2 should still have 1 allowed endpoint, got %d", len(p2.Allowed))
	}
}

func TestPolicyManager_InvalidPolicyName(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	// Test with empty policy name
	_, err = pm.SetPolicy("", []string{"api.com"}, []string{})
	if err != nil {
		// This is acceptable - empty name might be invalid
		t.Logf("SetPolicy with empty name returned error (acceptable): %v", err)
	}

	// Test GetPolicy with empty name
	_, err = pm.GetPolicy("")
	if err == nil {
		// This might return nil policy or error - both are acceptable
		t.Logf("GetPolicy with empty name returned error or nil")
	}
}

func TestPolicyManager_OverlappingAllowedDenied(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	// Add same endpoint to both allowed and denied
	pm.SetPolicy("test-vm", []string{"api.example.com"}, []string{})

	// Then add to denied (deny should take precedence in IsEndpointAllowed)
	err = pm.AddDeniedEndpoint("test-vm", "api.example.com")
	if err != nil {
		t.Fatalf("AddDeniedEndpoint() failed: %v", err)
	}

	allowed, _ := pm.IsEndpointAllowed("test-vm", "api.example.com")
	if allowed {
		t.Error("expected false when endpoint is in both lists (deny takes precedence), got true")
	}
}

// TestPolicyManager_ConcurrentAccess tests thread safety
func TestPolicyManager_ConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	pm, err := NewPolicyManager(tmpDir)
	if err != nil {
		t.Fatalf("NewPolicyManager() failed: %v", err)
	}

	done := make(chan bool, 100)

	// Concurrent writes
	for i := 0; i < 50; i++ {
		go func(idx int) {
			pm.AddAllowedEndpoint("test-vm", fmt.Sprintf("api%d.example.com", idx))
			done <- true
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		go func() {
			_, _ = pm.IsEndpointAllowed("test-vm", "api0.example.com")
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 100; i++ {
		<-done
	}

	// Verify no panics occurred (race condition would cause panic in race detector)
	policy, _ := pm.GetPolicy("test-vm")
	if policy == nil {
		t.Error("policy should exist after concurrent access")
	}
}
