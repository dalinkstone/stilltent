//go:build darwin
// +build darwin

package network

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/dalinkstone/tent/pkg/models"
)

// VMNetManager handles macOS vmnet.framework networking
type VMNetManager struct {
	bridgeName  string
	ipRange     string
	dhcpRange   string
	ifaceName   string
	firewall    *EgressFirewall
}

// NewManager creates a new network manager for macOS (overrides the Linux version)
func NewManager() (*VMNetManager, error) {
	return &VMNetManager{
		bridgeName: "vmnet",
		ipRange:    "172.16.0.1/24",
		dhcpRange:  "172.16.0.2,172.16.0.254",
		ifaceName:  "vmnet0",
	}, nil
}

// NewVMNetManager creates a new network manager for macOS
func NewVMNetManager() (*VMNetManager, error) {
	return &VMNetManager{
		bridgeName: "vmnet",
		ipRange:    "172.16.0.1/24",
		dhcpRange:  "172.16.0.2,172.16.0.254",
		ifaceName:  "vmnet0",
	}, nil
}

// SetupVMNetwork sets up network for a new VM on macOS
func (m *VMNetManager) SetupVMNetwork(vmName string, config *models.VMConfig) (string, error) {
	// On macOS, vmnet.framework is handled by the hypervisor backend
	// The VM creates its own vmnet interface via Hypervisor framework APIs
	// We just need to return a virtual device name that the hypervisor can use
	return fmt.Sprintf("vmnet-%s", vmName), nil
}

// CleanupVMNetwork cleans up network resources for a VM on macOS
func (m *VMNetManager) CleanupVMNetwork(vmName string) error {
	// On macOS with vmnet.framework, no cleanup is needed
	// The VM's vmnet interface is destroyed when the VM stops
	return nil
}

// ListNetworkResources lists all network resources on macOS
func (m *VMNetManager) ListNetworkResources() ([]*NetworkResource, error) {
	resources := []*NetworkResource{}

	// Check if vmnet interface exists
	iface, err := m.getVMNetInterface()
	if err == nil && iface != nil {
		resources = append(resources, iface)
	}

	return resources, nil
}

// getVMNetInterface returns information about the vmnet interface
func (m *VMNetManager) getVMNetInterface() (*NetworkResource, error) {
	// Check if vmnet0 exists using networksetup
	cmd := exec.Command("networksetup", "-listallhardwareports")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list hardware ports: %w", err)
	}

	// Parse output to find vmnet interface
	lines := strings.Split(string(output), "\n")
	for i, line := range lines {
		if strings.Contains(line, "Device:") && strings.Contains(line, "vmnet") {
			// Found vmnet interface
			ifaceName := strings.TrimSpace(strings.Split(line, ":")[1])
			
			// Get IP address for this interface
			if i+1 < len(lines) {
				nextLine := lines[i+1]
				if strings.Contains(nextLine, "Hardware Port:") {
					portName := strings.TrimSpace(strings.Split(nextLine, ":")[1])
					
					// Try to get IP using ifconfig
					ipCmd := exec.Command("ipconfig", "getifaddr", ifaceName)
					ipOutput, _ := ipCmd.Output()
					ip := strings.TrimSpace(string(ipOutput))
					
					return &NetworkResource{
						Name:       ifaceName,
						Type:       "vmnet",
						IP:         ip,
						Interfaces: []string{portName},
					}, nil
				}
			}
		}
	}

	return nil, nil
}

// EnsureVMNetHelperAvailable checks if required macOS networking tools are available
func EnsureVMNetHelperAvailable() error {
	// vmnet.framework is part of macOS, no additional tools required
	// But vmnet-helper can be installed for additional functionality
	return nil
}

// GetVMNetPath returns the path to any vmnet-related binaries
func GetVMNetPath() string {
	// vmnet.framework is built into macOS
	// No external binary required
	return ""
}

// ApplyNetworkPolicy applies egress firewall rules for a sandbox on macOS
// using the PF-based egress firewall engine
func (m *VMNetManager) ApplyNetworkPolicy(vmName string, policy *Policy) error {
	if m.firewall == nil {
		m.firewall = NewEgressFirewall()
	}
	return m.firewall.ApplyPolicy(vmName, policy)
}

// RemoveNetworkPolicy removes egress firewall rules for a sandbox on macOS
func (m *VMNetManager) RemoveNetworkPolicy(vmName string) error {
	if m.firewall == nil {
		return nil
	}
	return m.firewall.RemovePolicy(vmName)
}

// SetSandboxIP sets the IP address for a sandbox in the egress firewall
func (m *VMNetManager) SetSandboxIP(vmName string, ip string) {
	if m.firewall == nil {
		m.firewall = NewEgressFirewall()
	}
	m.firewall.SetSandboxIP(vmName, ip)
}
