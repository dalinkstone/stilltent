package models

// VMStatus represents the current state of a microVM
type VMStatus string

const (
	VMStatusStopped VMStatus = "stopped"
	VMStatusRunning VMStatus = "running"
	VMStatusCreated VMStatus = "created"
	VMStatusError   VMStatus = "error"
)

// String returns the string representation of VMStatus
func (s VMStatus) String() string {
	return string(s)
}

// VMConfig represents the configuration for a microVM
type VMConfig struct {
	Name      string        `yaml:"name"`
	VCPUs     int           `yaml:"vcpus"`
	MemoryMB  int           `yaml:"memory_mb"`
	Kernel    string        `yaml:"kernel"`
	RootFS    string        `yaml:"rootfs"`
	DiskGB    int           `yaml:"disk_gb"`
	Network   NetworkConfig `yaml:"network"`
	Mounts    []MountConfig `yaml:"mounts"`
	Env       map[string]string `yaml:"env"`
}

// NetworkConfig represents network configuration for a VM
type NetworkConfig struct {
	Mode   string        `yaml:"mode"`
	Bridge string        `yaml:"bridge"`
	Ports  []PortForward `yaml:"ports"`
}

// PortForward represents port forwarding configuration
type PortForward struct {
	Host  int `yaml:"host"`
	Guest int `yaml:"guest"`
}

// MountConfig represents a host-to-guest directory mount
type MountConfig struct {
	Host     string `yaml:"host"`
	Guest    string `yaml:"guest"`
	Readonly bool   `yaml:"readonly"`
}

// VMState represents the runtime state of a microVM
type VMState struct {
	Name        string     `json:"name"`
	Status      VMStatus   `json:"status"`
	PID         int        `json:"pid,omitempty"`
	IP          string     `json:"ip,omitempty"`
	SocketPath  string     `json:"socket_path,omitempty"`
	RootFSPath  string     `json:"rootfs_path,omitempty"`
	TAPDevice   string     `json:"tap_device,omitempty"`
	CreatedAt   int64      `json:"created_at"`
	UpdatedAt   int64      `json:"updated_at"`
}

// Snapshot represents a VM snapshot
type Snapshot struct {
	Tag       string `json:"tag"`
	Timestamp string `json:"timestamp"`
	SizeMB    int    `json:"size_mb"`
}

// ImageInfo represents a base image
type ImageInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeMB    int    `json:"size_mb"`
	CreatedAt string `json:"created_at"`
}

// BridgeInfo represents network bridge state
type BridgeInfo struct {
	Name       string   `json:"name"`
	Interface  string   `json:"interface"`
	IP         string   `json:"ip"`
	TAPDevices []string `json:"tap_devices"`
}

// ValidateVMConfig validates a VM configuration
func ValidateVMConfig(cfg *VMConfig) error {
	if cfg == nil {
		return &ValidationError{Errors: []ConfigError{
			{Field: "config", Message: "config cannot be nil"},
		}}
	}

	var errors []ConfigError

	if cfg.Name == "" {
		errors = append(errors, ConfigError{Field: "name", Message: "name is required"})
	}

	if cfg.VCPUs <= 0 {
		errors = append(errors, ConfigError{Field: "vcpus", Message: "vcpus must be positive"})
	}

	if cfg.MemoryMB <= 0 {
		errors = append(errors, ConfigError{Field: "memory_mb", Message: "memory_mb must be positive"})
	}

	if cfg.DiskGB <= 0 {
		errors = append(errors, ConfigError{Field: "disk_gb", Message: "disk_gb must be positive"})
	}

	if len(errors) > 0 {
		return &ValidationError{Errors: errors}
	}

	return nil
}

// Validate validates the VMConfig
func (cfg *VMConfig) Validate() error {
	return ValidateVMConfig(cfg)
}
