// Package network provides cross-platform networking for microVMs
package network

import (
	"fmt"
	"net"
	"strings"
	"sync"
)

// EgressRule represents a single egress firewall rule
type EgressRule struct {
	// Endpoint is the original allowlist entry (IP, CIDR, or hostname)
	Endpoint string
	// ResolvedIPs are the IPs resolved from the endpoint
	ResolvedIPs []net.IP
	// CIDR is set if the endpoint is a CIDR block
	CIDR *net.IPNet
	// Port is the allowed port (0 means all ports)
	Port int
	// Proto is "tcp", "udp", or "" (both)
	Proto string
}

// SandboxRules holds all egress rules for a single sandbox
type SandboxRules struct {
	SandboxName string
	SandboxIP   string
	Rules       []*EgressRule
	BlockAll    bool // true = default deny (always true in tent)
}

// EgressFirewall manages egress network filtering for all sandboxes.
// Default policy: block all outbound traffic. Per-sandbox allowlists
// open specific endpoints.
type EgressFirewall struct {
	mu          sync.Mutex
	initialized bool
	sandboxes   map[string]*SandboxRules
	backend     EgressBackend
}

// EgressBackend is the platform-specific firewall rule applicator.
// Implementations exist for PF (macOS) and iptables (Linux).
type EgressBackend interface {
	// Init initializes the firewall backend (creates chains/anchors)
	Init() error
	// ApplyRules writes rules for a sandbox, replacing any existing ones
	ApplyRules(rules *SandboxRules) error
	// RemoveRules removes all rules for a sandbox
	RemoveRules(sandboxName string) error
	// Cleanup tears down all tent firewall state
	Cleanup() error
}

// NewEgressFirewall creates a new egress firewall with the platform-specific backend
func NewEgressFirewall() *EgressFirewall {
	return &EgressFirewall{
		sandboxes: make(map[string]*SandboxRules),
		backend:   newEgressBackend(),
	}
}

// Initialize sets up the firewall subsystem
func (f *EgressFirewall) Initialize() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.initialized {
		return nil
	}

	if f.backend != nil {
		if err := f.backend.Init(); err != nil {
			return fmt.Errorf("failed to initialize firewall backend: %w", err)
		}
	}

	f.sandboxes = make(map[string]*SandboxRules)
	f.initialized = true
	return nil
}

// ApplyPolicy parses a network Policy into firewall rules and applies them
// for the given sandbox. The sandbox's traffic IP must be set via SetSandboxIP
// before calling this, or provided in the policy itself.
func (f *EgressFirewall) ApplyPolicy(name string, policy *Policy) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.initialized {
		if err := f.initLocked(); err != nil {
			return err
		}
	}

	sr, exists := f.sandboxes[name]
	if !exists {
		sr = &SandboxRules{
			SandboxName: name,
			BlockAll:    true,
		}
		f.sandboxes[name] = sr
	}

	// Parse allowed endpoints into rules
	rules, err := parseEndpoints(policy.Allowed)
	if err != nil {
		return fmt.Errorf("failed to parse allowed endpoints: %w", err)
	}
	sr.Rules = rules

	// Apply via platform backend
	if f.backend != nil {
		if err := f.backend.ApplyRules(sr); err != nil {
			return fmt.Errorf("failed to apply firewall rules: %w", err)
		}
	}

	return nil
}

// SetSandboxIP associates a sandbox with its network IP so rules
// can be scoped to that source address.
func (f *EgressFirewall) SetSandboxIP(name string, ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	sr, exists := f.sandboxes[name]
	if !exists {
		sr = &SandboxRules{
			SandboxName: name,
			BlockAll:    true,
		}
		f.sandboxes[name] = sr
	}
	sr.SandboxIP = ip
}

// RemovePolicy removes firewall rules for a sandbox
func (f *EgressFirewall) RemovePolicy(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.backend != nil {
		if err := f.backend.RemoveRules(name); err != nil {
			return fmt.Errorf("failed to remove firewall rules: %w", err)
		}
	}

	delete(f.sandboxes, name)
	return nil
}

// Reset clears all firewall rules
func (f *EgressFirewall) Reset() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.backend != nil {
		if err := f.backend.Cleanup(); err != nil {
			return fmt.Errorf("failed to cleanup firewall: %w", err)
		}
	}

	f.sandboxes = make(map[string]*SandboxRules)
	f.initialized = false
	return nil
}

// GetSandboxRules returns the current rules for a sandbox (for inspection/debugging)
func (f *EgressFirewall) GetSandboxRules(name string) *SandboxRules {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.sandboxes[name]
}

// GetAllowedIPs returns all allowed sandbox IPs (for backward compat)
func (f *EgressFirewall) GetAllowedIPs() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := make(map[string]string)
	for name, sr := range f.sandboxes {
		result[name] = sr.SandboxIP
	}
	return result
}

// IsAllowed checks whether traffic from a sandbox to dest:port is permitted
func (f *EgressFirewall) IsAllowed(sandboxName string, destIP net.IP, destPort int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	sr, exists := f.sandboxes[sandboxName]
	if !exists {
		return false // no rules = block all
	}

	for _, rule := range sr.Rules {
		if rule.Port != 0 && rule.Port != destPort {
			continue
		}
		if rule.CIDR != nil && rule.CIDR.Contains(destIP) {
			return true
		}
		for _, rip := range rule.ResolvedIPs {
			if rip.Equal(destIP) {
				return true
			}
		}
	}

	return false
}

func (f *EgressFirewall) initLocked() error {
	if f.backend != nil {
		if err := f.backend.Init(); err != nil {
			return fmt.Errorf("failed to initialize firewall backend: %w", err)
		}
	}
	f.sandboxes = make(map[string]*SandboxRules)
	f.initialized = true
	return nil
}

// parseEndpoints converts a list of endpoint strings into EgressRules.
// Supported formats:
//   - "1.2.3.4"          — single IP, all ports
//   - "1.2.3.0/24"       — CIDR block
//   - "example.com"      — hostname (resolved to IPs)
//   - "example.com:443"  — hostname with port
//   - "tcp:1.2.3.4:80"   — protocol-specific
func parseEndpoints(endpoints []string) ([]*EgressRule, error) {
	var rules []*EgressRule
	for _, ep := range endpoints {
		rule, err := parseEndpoint(ep)
		if err != nil {
			return nil, fmt.Errorf("invalid endpoint %q: %w", ep, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func parseEndpoint(ep string) (*EgressRule, error) {
	rule := &EgressRule{Endpoint: ep}

	// Check for protocol prefix
	if strings.HasPrefix(ep, "tcp:") || strings.HasPrefix(ep, "udp:") {
		rule.Proto = ep[:3]
		ep = ep[4:]
	}

	// Check for CIDR
	if strings.Contains(ep, "/") {
		_, cidr, err := net.ParseCIDR(ep)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR: %w", err)
		}
		rule.CIDR = cidr
		return rule, nil
	}

	// Split host:port
	host, portStr, err := splitHostPort(ep)
	if err != nil {
		host = ep
	}
	if portStr != "" {
		port := 0
		for _, c := range portStr {
			if c < '0' || c > '9' {
				return nil, fmt.Errorf("invalid port: %s", portStr)
			}
			port = port*10 + int(c-'0')
		}
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("port out of range: %d", port)
		}
		rule.Port = port
	}

	// Try as IP first
	if ip := net.ParseIP(host); ip != nil {
		rule.ResolvedIPs = []net.IP{ip}
		return rule, nil
	}

	// Resolve hostname
	ips, err := net.LookupIP(host)
	if err != nil {
		// Store hostname even if resolution fails — will be retried at enforcement time
		rule.ResolvedIPs = nil
		return rule, nil
	}

	rule.ResolvedIPs = ips
	return rule, nil
}

// splitHostPort wraps net.SplitHostPort but handles bare hosts (no port)
func splitHostPort(s string) (string, string, error) {
	// net.SplitHostPort requires a port; detect bare host
	if !strings.Contains(s, ":") {
		return s, "", nil
	}
	// Could be IPv6 without port — check for brackets
	if strings.HasPrefix(s, "[") {
		host, port, err := net.SplitHostPort(s)
		return host, port, err
	}
	// Single colon = host:port
	if strings.Count(s, ":") == 1 {
		host, port, err := net.SplitHostPort(s)
		return host, port, err
	}
	// Multiple colons without brackets = bare IPv6
	return s, "", nil
}
