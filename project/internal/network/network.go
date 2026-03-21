package network

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/dalinkstone/tent/pkg/models"
)

// NetworkManager handles TAP devices, bridges, and port forwarding
type Manager struct {
	bridgeName string
	ipRange    string
	dhcpRange  string
}

// NewManager creates a new network manager
func NewManager() (*Manager, error) {
	return &Manager{
		bridgeName: "tent0",
		ipRange:    "172.16.0.1/24",
		dhcpRange:  "172.16.0.2,172.16.0.254",
	}, nil
}

// SetupVMNetwork sets up network for a new VM
func (m *Manager) SetupVMNetwork(vmName string, config *models.VMConfig) (string, error) {
	// Ensure bridge exists
	if err := m.ensureBridge(); err != nil {
		return "", fmt.Errorf("failed to ensure bridge: %w", err)
	}

	// Create TAP device for the VM
	tapDevice := fmt.Sprintf("tap-%s", vmName)
	if err := m.createTapDevice(tapDevice); err != nil {
		return "", fmt.Errorf("failed to create TAP device: %w", err)
	}

	// Add TAP device to bridge
	if err := m.addDeviceToBridge(tapDevice); err != nil {
		m.cleanupTapDevice(tapDevice)
		return "", fmt.Errorf("failed to add TAP to bridge: %w", err)
	}

	// Configure IP address for the TAP device
	// In a real implementation, we'd assign a static IP based on VM name
	// For now, return the TAP device name - DHCP will assign the IP
	return tapDevice, nil
}

// CleanupVMNetwork cleans up network resources for a VM
func (m *Manager) CleanupVMNetwork(vmName string) error {
	tapDevice := fmt.Sprintf("tap-%s", vmName)

	// Remove TAP device from bridge
	if err := m.removeDeviceFromBridge(tapDevice); err != nil {
		return fmt.Errorf("failed to remove TAP from bridge: %w", err)
	}

	// Delete TAP device
	if err := m.deleteTapDevice(tapDevice); err != nil {
		return fmt.Errorf("failed to delete TAP device: %w", err)
	}

	return nil
}

// ensureBridge ensures the bridge interface exists
func (m *Manager) ensureBridge() error {
	// Check if bridge exists
	if _, err := exec.Command("ip", "link", "show", m.bridgeName).Output(); err == nil {
		return nil // Bridge already exists
	}

	// Create bridge
	cmd := exec.Command("ip", "link", "add", m.bridgeName, "type", "bridge")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create bridge: %w", err)
	}

	// Bring bridge up
	cmd = exec.Command("ip", "link", "set", "dev", m.bridgeName, "up")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bring bridge up: %w", err)
	}

	// Assign IP address to bridge
	cmd = exec.Command("ip", "addr", "add", m.ipRange, "dev", m.bridgeName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to assign IP to bridge: %w", err)
	}

	return nil
}

// createTapDevice creates a new TAP device
func (m *Manager) createTapDevice(tapName string) error {
	// Check if TAP device already exists
	if _, err := exec.Command("ip", "link", "show", tapName).Output(); err == nil {
		return nil // Device already exists
	}

	// Create TAP device using ip command
	// Note: In a real implementation, we might use the tun/tap interface directly
	cmd := exec.Command("ip", "tuntap", "add", "mode", "tap", tapName)
	if err := cmd.Run(); err != nil {
		// Try alternative method using tunctl
		cmd = exec.Command("tunctl", "-t", tapName)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create TAP device: %w", err)
		}
	}

	// Bring TAP device up
	cmd = exec.Command("ip", "link", "set", "dev", tapName, "up")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bring TAP device up: %w", err)
	}

	return nil
}

// cleanupTapDevice cleans up a TAP device
func (m *Manager) cleanupTapDevice(tapName string) error {
	cmd := exec.Command("ip", "link", "delete", tapName, "type", "tap")
	return cmd.Run()
}

// deleteTapDevice deletes a TAP device
func (m *Manager) deleteTapDevice(tapName string) error {
	cmd := exec.Command("ip", "tuntap", "del", "mode", "tap", tapName)
	return cmd.Run()
}

// addDeviceToBridge adds a device to the bridge
func (m *Manager) addDeviceToBridge(deviceName string) error {
	cmd := exec.Command("ip", "link", "set", "master", m.bridgeName, "dev", deviceName)
	return cmd.Run()
}

// removeDeviceFromBridge removes a device from the bridge
func (m *Manager) removeDeviceFromBridge(deviceName string) error {
	cmd := exec.Command("ip", "link", "set", "nomaster", "dev", deviceName)
	return cmd.Run()
}

// ListNetworkResources lists all network resources
func (m *Manager) ListNetworkResources() ([]*NetworkResource, error) {
	resources := []*NetworkResource{}

	// List bridge info
	bridgeInfo, err := m.getBridgeInfo()
	if err == nil {
		resources = append(resources, bridgeInfo)
	}

	// List TAP devices
	tapDevices, err := m.listTapDevices()
	if err == nil {
		resources = append(resources, tapDevices...)
	}

	return resources, nil
}

// getBridgeInfo returns information about the bridge
func (m *Manager) getBridgeInfo() (*NetworkResource, error) {
	// Get bridge IP
	cmd := exec.Command("ip", "addr", "show", m.bridgeName)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get bridge info: %w", err)
	}

	// Parse output to extract IP
	var ip string
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "inet ") && strings.Contains(line, m.bridgeName) {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				ip = parts[1]
			}
			break
		}
	}

	return &NetworkResource{
		Name:       m.bridgeName,
		Type:       "bridge",
		IP:         ip,
		Interfaces: []string{}, // Would need to parse bridge interfaces
	}, nil
}

// listTapDevices lists all TAP devices
func (m *Manager) listTapDevices() ([]*NetworkResource, error) {
	cmd := exec.Command("ip", "link", "show", "type", "tap")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list TAP devices: %w", err)
	}

	var devices []*NetworkResource
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, ":") {
			parts := strings.Split(strings.TrimSpace(line), ":")
			if len(parts) >= 2 {
				deviceName := strings.TrimSpace(parts[1])
				devices = append(devices, &NetworkResource{
					Name:       deviceName,
					Type:       "tap",
					IP:         "", // Would need to query IP separately
					Interfaces: []string{},
				})
			}
		}
	}

	return devices, nil
}

// NetworkResource represents a network resource
type NetworkResource struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	IP         string   `json:"ip"`
	Interfaces []string `json:"interfaces,omitempty"`
}
