package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dalinkstone/tent/pkg/models"
)

// DeviceManager handles device passthrough for sandbox VMs
type DeviceManager struct {
	baseDir string
}

// NewDeviceManager creates a new device manager
func NewDeviceManager(baseDir string) *DeviceManager {
	return &DeviceManager{baseDir: baseDir}
}

// HostDevice represents a device discovered on the host system
type HostDevice struct {
	Address     string `json:"address"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Driver      string `json:"driver,omitempty"`
	IOMMU       string `json:"iommu_group,omitempty"`
	InUse       bool   `json:"in_use"`
}

// ListHostDevices discovers passthrough-capable devices on the host
func (dm *DeviceManager) ListHostDevices(deviceType string) ([]HostDevice, error) {
	var devices []HostDevice

	switch runtime.GOOS {
	case "linux":
		devices = dm.discoverLinuxDevices(deviceType)
	case "darwin":
		devices = dm.discoverDarwinDevices(deviceType)
	default:
		return nil, fmt.Errorf("device passthrough not supported on %s", runtime.GOOS)
	}

	return devices, nil
}

// discoverLinuxDevices scans sysfs for PCI/USB devices on Linux
func (dm *DeviceManager) discoverLinuxDevices(deviceType string) []HostDevice {
	var devices []HostDevice

	switch deviceType {
	case "pci", "gpu", "vfio", "":
		devices = append(devices, dm.scanPCIDevices()...)
	}
	switch deviceType {
	case "usb", "":
		devices = append(devices, dm.scanUSBDevices()...)
	}

	// Filter for specific type if requested
	if deviceType != "" && deviceType != "pci" && deviceType != "usb" {
		var filtered []HostDevice
		for _, d := range devices {
			if d.Type == deviceType {
				filtered = append(filtered, d)
			}
		}
		return filtered
	}

	return devices
}

// scanPCIDevices reads PCI device info from sysfs
func (dm *DeviceManager) scanPCIDevices() []HostDevice {
	var devices []HostDevice
	pciPath := "/sys/bus/pci/devices"

	entries, err := os.ReadDir(pciPath)
	if err != nil {
		return devices
	}

	for _, entry := range entries {
		addr := entry.Name()
		devPath := filepath.Join(pciPath, addr)

		dev := HostDevice{
			Address: addr,
			Type:    "pci",
		}

		// Read device class to identify GPUs
		classBytes, err := os.ReadFile(filepath.Join(devPath, "class"))
		if err == nil {
			classStr := strings.TrimSpace(string(classBytes))
			if strings.HasPrefix(classStr, "0x03") {
				dev.Type = "gpu"
			}
		}

		// Read vendor/device description
		vendorBytes, _ := os.ReadFile(filepath.Join(devPath, "vendor"))
		deviceBytes, _ := os.ReadFile(filepath.Join(devPath, "device"))
		vendor := strings.TrimSpace(string(vendorBytes))
		device := strings.TrimSpace(string(deviceBytes))
		if vendor != "" && device != "" {
			dev.Description = fmt.Sprintf("PCI %s:%s", vendor, device)
		}

		// Check driver binding
		driverLink, err := os.Readlink(filepath.Join(devPath, "driver"))
		if err == nil {
			dev.Driver = filepath.Base(driverLink)
			dev.InUse = dev.Driver != "vfio-pci"
		}

		// Check IOMMU group
		iommuLink, err := os.Readlink(filepath.Join(devPath, "iommu_group"))
		if err == nil {
			dev.IOMMU = filepath.Base(iommuLink)
		}

		devices = append(devices, dev)
	}

	return devices
}

// scanUSBDevices reads USB device info from sysfs
func (dm *DeviceManager) scanUSBDevices() []HostDevice {
	var devices []HostDevice
	usbPath := "/sys/bus/usb/devices"

	entries, err := os.ReadDir(usbPath)
	if err != nil {
		return devices
	}

	for _, entry := range entries {
		devPath := filepath.Join(usbPath, entry.Name())

		vendorBytes, err := os.ReadFile(filepath.Join(devPath, "idVendor"))
		if err != nil {
			continue // Skip non-device entries (interfaces, etc.)
		}
		productBytes, _ := os.ReadFile(filepath.Join(devPath, "idProduct"))
		vendor := strings.TrimSpace(string(vendorBytes))
		product := strings.TrimSpace(string(productBytes))

		dev := HostDevice{
			Address: fmt.Sprintf("%s:%s", vendor, product),
			Type:    "usb",
		}

		// Read manufacturer/product strings
		mfgBytes, _ := os.ReadFile(filepath.Join(devPath, "manufacturer"))
		prodBytes, _ := os.ReadFile(filepath.Join(devPath, "product"))
		mfg := strings.TrimSpace(string(mfgBytes))
		prod := strings.TrimSpace(string(prodBytes))
		if mfg != "" || prod != "" {
			dev.Description = strings.TrimSpace(mfg + " " + prod)
		}

		// Check if driver is bound
		driverLink, err := os.Readlink(filepath.Join(devPath, "driver"))
		if err == nil {
			dev.Driver = filepath.Base(driverLink)
			dev.InUse = true
		}

		devices = append(devices, dev)
	}

	return devices
}

// discoverDarwinDevices lists devices available for passthrough on macOS
func (dm *DeviceManager) discoverDarwinDevices(deviceType string) []HostDevice {
	// macOS uses Virtualization.framework which supports USB and
	// specific device passthrough via entitlements
	var devices []HostDevice

	// On macOS, device enumeration requires IOKit framework access.
	// For the CLI, we return a placeholder that describes the capability.
	if deviceType == "" || deviceType == "usb" {
		devices = append(devices, HostDevice{
			Address:     "usb-host",
			Type:        "usb",
			Description: "USB host controller (Virtualization.framework)",
		})
	}
	if deviceType == "" || deviceType == "gpu" {
		devices = append(devices, HostDevice{
			Address:     "gpu-metal",
			Type:        "gpu",
			Description: "Metal GPU (Virtualization.framework)",
		})
	}

	return devices
}

// AttachDevice adds a device to a sandbox's configuration
func (m *VMManager) AttachDevice(name string, dev models.DeviceConfig) error {
	if err := dev.Validate(); err != nil {
		return fmt.Errorf("invalid device config: %w", err)
	}

	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("sandbox %q not found: %w", name, err)
	}

	// Check for duplicate device address
	for _, existing := range vmState.Devices {
		if existing.Address == dev.Address && existing.Type == models.DeviceType(dev.Type) {
			return fmt.Errorf("device %s (%s) is already attached to sandbox %q", dev.Address, dev.Type, name)
		}
	}

	// Add device to state
	deviceState := models.DeviceState{
		Name:     dev.Name,
		Type:     dev.Type,
		Address:  dev.Address,
		Readonly: dev.Readonly,
		Options:  dev.Options,
	}

	if vmState.Status == models.VMStatusRunning {
		// Hot-attach: device will be available after next restart
		deviceState.Status = "pending"
	} else {
		deviceState.Status = "attached"
	}

	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.Devices = append(s.Devices, deviceState)
		s.UpdatedAt = time.Now().Unix()
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update sandbox state: %w", err)
	}

	m.logEvent(EventUpdate, name, map[string]string{
		"action":  "device_attach",
		"device":  dev.Address,
		"type":    string(dev.Type),
	})

	return nil
}

// DetachDevice removes a device from a sandbox's configuration
func (m *VMManager) DetachDevice(name string, address string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("sandbox %q not found: %w", name, err)
	}

	found := false
	for _, d := range vmState.Devices {
		if d.Address == address {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("device %s not found on sandbox %q", address, name)
	}

	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		var updated []models.DeviceState
		for _, d := range s.Devices {
			if d.Address != address {
				updated = append(updated, d)
			}
		}
		s.Devices = updated
		s.UpdatedAt = time.Now().Unix()
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update sandbox state: %w", err)
	}

	m.logEvent(EventUpdate, name, map[string]string{
		"action": "device_detach",
		"device": address,
	})

	return nil
}

// ListDevices returns all devices attached to a sandbox
func (m *VMManager) ListDevices(name string) ([]models.DeviceState, error) {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return nil, fmt.Errorf("sandbox %q not found: %w", name, err)
	}

	return vmState.Devices, nil
}

