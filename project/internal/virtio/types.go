// Package virtio provides virtio device emulation types.
// This package defines the interface for virtio devices used in microVMs.
package virtio

// DeviceType represents the type of virtio device
type DeviceType uint32

const (
	DeviceTypeInvalid DeviceType = 0
	DeviceTypeNet     DeviceType = 1
	DeviceTypeBlock   DeviceType = 2
	DeviceTypeConsole DeviceType = 3
	DeviceTypeRng     DeviceType = 4
)

// VirtioDevice defines the interface for a virtio device
type VirtioDevice interface {
	// Type returns the device type
	Type() DeviceType
	// ID returns the device ID
	ID() string
	// Start initializes and starts the device
	Start() error
	// Stop shuts down the device
	Stop() error
	// Configure configures the device with the given parameters
	Configure(config map[string]string) error
}

// BlockDevice represents a virtio block device
type BlockDevice struct {
	deviceID string
	Path     string
	SizeMB   int
	ReadOnly bool
}

func (d *BlockDevice) Type() DeviceType {
	return DeviceTypeBlock
}

func (d *BlockDevice) ID() string {
	return d.deviceID
}

func (d *BlockDevice) Start() error {
	// Implement block device start
	return nil
}

func (d *BlockDevice) Stop() error {
	// Implement block device stop
	return nil
}

func (d *BlockDevice) Configure(config map[string]string) error {
	// Implement block device configuration
	return nil
}

// NetworkDevice represents a virtio network device
type NetworkDevice struct {
	deviceID  string
	TAPDevice string
	MAC       string
}

func (d *NetworkDevice) Type() DeviceType {
	return DeviceTypeNet
}

func (d *NetworkDevice) ID() string {
	return d.deviceID
}

func (d *NetworkDevice) Start() error {
	// Implement network device start
	return nil
}

func (d *NetworkDevice) Stop() error {
	// Implement network device stop
	return nil
}

func (d *NetworkDevice) Configure(config map[string]string) error {
	// Implement network device configuration
	return nil
}

// ConsoleDevice represents a virtio console device
type ConsoleDevice struct {
	deviceID string
	TTY      string
	LogFile  string
}

func (d *ConsoleDevice) Type() DeviceType {
	return DeviceTypeConsole
}

func (d *ConsoleDevice) ID() string {
	return d.deviceID
}

func (d *ConsoleDevice) Start() error {
	// Implement console device start
	return nil
}

func (d *ConsoleDevice) Stop() error {
	// Implement console device stop
	return nil
}

func (d *ConsoleDevice) Configure(config map[string]string) error {
	// Implement console device configuration
	return nil
}
