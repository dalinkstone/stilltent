package network

import (
	"testing"
)

func TestEgressFirewall_Initialize(t *testing.T) {
	fw := NewEgressFirewall()
	
	if err := fw.Initialize(); err != nil {
		t.Errorf("Initialize() error = %v", err)
	}
	
	if !fw.initialized {
		t.Error("Initialize() did not set initialized flag")
	}
}

func TestEgressFirewall_ApplyPolicy(t *testing.T) {
	fw := NewEgressFirewall()
	
	if err := fw.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	
	policy := &Policy{
		Name:      "test-sandbox",
		Allowed:   []string{"api.anthropic.com", "openrouter.ai"},
		Denied:    []string{},
		CreatedAt: 0,
		UpdatedAt: 0,
	}
	
	if err := fw.ApplyPolicy("test-sandbox", policy); err != nil {
		t.Errorf("ApplyPolicy() error = %v", err)
	}
	
	ips := fw.GetAllowedIPs()
	if _, exists := ips["test-sandbox"]; !exists {
		t.Error("ApplyPolicy() did not add sandbox to allowedIPs")
	}
}

func TestEgressFirewall_RemovePolicy(t *testing.T) {
	fw := NewEgressFirewall()
	
	if err := fw.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	
	fw.ApplyPolicy("test-sandbox", &Policy{Name: "test-sandbox"})
	
	if err := fw.RemovePolicy("test-sandbox"); err != nil {
		t.Errorf("RemovePolicy() error = %v", err)
	}
	
	ips := fw.GetAllowedIPs()
	if _, exists := ips["test-sandbox"]; exists {
		t.Error("RemovePolicy() did not remove sandbox from allowedIPs")
	}
}

func TestEgressFirewall_Reset(t *testing.T) {
	fw := NewEgressFirewall()
	
	if err := fw.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	
	fw.ApplyPolicy("sandbox1", &Policy{Name: "sandbox1"})
	fw.ApplyPolicy("sandbox2", &Policy{Name: "sandbox2"})
	
	if err := fw.Reset(); err != nil {
		t.Errorf("Reset() error = %v", err)
	}
	
	ips := fw.GetAllowedIPs()
	if len(ips) != 0 {
		t.Errorf("Reset() did not clear allowedIPs, got %d entries", len(ips))
	}
}

func TestEgressFirewall_UninitializedPolicy(t *testing.T) {
	fw := NewEgressFirewall()
	
	// Should be no-op when not initialized
	policy := &Policy{Name: "test-sandbox"}
	if err := fw.ApplyPolicy("test-sandbox", policy); err != nil {
		t.Errorf("ApplyPolicy() on uninitialized firewall error = %v", err)
	}
}
