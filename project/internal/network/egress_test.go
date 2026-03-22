package network

import (
	"net"
	"testing"
)

func TestParseEndpoint_IP(t *testing.T) {
	rule, err := parseEndpoint("1.2.3.4")
	if err != nil {
		t.Fatalf("parseEndpoint() error = %v", err)
	}
	if len(rule.ResolvedIPs) != 1 {
		t.Fatalf("expected 1 resolved IP, got %d", len(rule.ResolvedIPs))
	}
	if rule.ResolvedIPs[0].String() != "1.2.3.4" {
		t.Errorf("expected 1.2.3.4, got %s", rule.ResolvedIPs[0])
	}
	if rule.Port != 0 {
		t.Errorf("expected port 0, got %d", rule.Port)
	}
}

func TestParseEndpoint_IPWithPort(t *testing.T) {
	rule, err := parseEndpoint("1.2.3.4:443")
	if err != nil {
		t.Fatalf("parseEndpoint() error = %v", err)
	}
	if rule.Port != 443 {
		t.Errorf("expected port 443, got %d", rule.Port)
	}
	if rule.ResolvedIPs[0].String() != "1.2.3.4" {
		t.Errorf("expected 1.2.3.4, got %s", rule.ResolvedIPs[0])
	}
}

func TestParseEndpoint_CIDR(t *testing.T) {
	rule, err := parseEndpoint("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parseEndpoint() error = %v", err)
	}
	if rule.CIDR == nil {
		t.Fatal("expected CIDR to be set")
	}
	if rule.CIDR.String() != "10.0.0.0/8" {
		t.Errorf("expected 10.0.0.0/8, got %s", rule.CIDR)
	}
}

func TestParseEndpoint_Proto(t *testing.T) {
	rule, err := parseEndpoint("tcp:1.2.3.4:80")
	if err != nil {
		t.Fatalf("parseEndpoint() error = %v", err)
	}
	if rule.Proto != "tcp" {
		t.Errorf("expected proto tcp, got %s", rule.Proto)
	}
	if rule.Port != 80 {
		t.Errorf("expected port 80, got %d", rule.Port)
	}
}

func TestEgressFirewall_IsAllowed(t *testing.T) {
	fw := &EgressFirewall{
		sandboxes: make(map[string]*SandboxRules),
		// No backend — pure in-memory check
	}

	_, cidr, _ := net.ParseCIDR("10.0.0.0/8")

	fw.sandboxes["box1"] = &SandboxRules{
		SandboxName: "box1",
		SandboxIP:   "172.16.0.2",
		BlockAll:    true,
		Rules: []*EgressRule{
			{ResolvedIPs: []net.IP{net.ParseIP("1.2.3.4")}, Port: 443},
			{CIDR: cidr},
		},
	}

	tests := []struct {
		name    string
		sandbox string
		destIP  string
		port    int
		want    bool
	}{
		{"allowed IP+port", "box1", "1.2.3.4", 443, true},
		{"wrong port", "box1", "1.2.3.4", 80, false},
		{"CIDR match", "box1", "10.5.6.7", 8080, true},
		{"blocked IP", "box1", "9.9.9.9", 443, false},
		{"unknown sandbox", "box2", "1.2.3.4", 443, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fw.IsAllowed(tt.sandbox, net.ParseIP(tt.destIP), tt.port)
			if got != tt.want {
				t.Errorf("IsAllowed(%s, %s, %d) = %v, want %v",
					tt.sandbox, tt.destIP, tt.port, got, tt.want)
			}
		})
	}
}

func TestEgressFirewall_ApplyAndRemove(t *testing.T) {
	fw := &EgressFirewall{
		sandboxes: make(map[string]*SandboxRules),
		// No backend — tests run without root
	}
	fw.initialized = true

	policy := &Policy{
		Name:    "test-sandbox",
		Allowed: []string{"1.2.3.4:443", "10.0.0.0/8"},
	}

	if err := fw.ApplyPolicy("test-sandbox", policy); err != nil {
		t.Fatalf("ApplyPolicy() error = %v", err)
	}

	sr := fw.GetSandboxRules("test-sandbox")
	if sr == nil {
		t.Fatal("expected sandbox rules to exist")
	}
	if len(sr.Rules) != 2 {
		t.Errorf("expected 2 rules, got %d", len(sr.Rules))
	}

	if err := fw.RemovePolicy("test-sandbox"); err != nil {
		t.Fatalf("RemovePolicy() error = %v", err)
	}

	sr = fw.GetSandboxRules("test-sandbox")
	if sr != nil {
		t.Error("expected sandbox rules to be removed")
	}
}
