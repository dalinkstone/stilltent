// Package network provides cross-platform networking for microVMs
package network

// EgressFirewall manages egress network filtering
type EgressFirewall struct {
	initialized bool
	allowedIPs  map[string]string // sandboxName -> allowed IPs
}

// NewEgressFirewall creates a new egress firewall
func NewEgressFirewall() *EgressFirewall {
	return &EgressFirewall{
		allowedIPs: make(map[string]string),
	}
}

// Initialize sets up the firewall subsystem
func (f *EgressFirewall) Initialize() error {
	f.allowedIPs = make(map[string]string)
	f.initialized = true
	return nil
}

// ApplyPolicy applies the network policy for a sandbox
// This is a no-op on platforms where the hypervisor handles firewalling
func (f *EgressFirewall) ApplyPolicy(name string, policy *Policy) error {
	if !f.initialized {
		return nil
	}
	
	// Get the sandbox's IP from the policy
	ip := f.getSandboxIP(name)
	f.allowedIPs[name] = ip
	
	return nil
}

// RemovePolicy removes firewall rules for a sandbox
func (f *EgressFirewall) RemovePolicy(name string) error {
	delete(f.allowedIPs, name)
	return nil
}

// Reset clears all firewall rules
func (f *EgressFirewall) Reset() error {
	f.allowedIPs = make(map[string]string)
	return nil
}

// getSandboxIP returns the IP for a sandbox
func (f *EgressFirewall) getSandboxIP(name string) string {
	return "172.16.0.2"
}

// GetAllowedIPs returns all allowed sandbox IPs
func (f *EgressFirewall) GetAllowedIPs() map[string]string {
	return f.allowedIPs
}
