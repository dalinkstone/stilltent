package models

// VMStatus represents the current state of a VM
type VMStatus string

const (
	VMStatusCreated VMStatus = "created"
	VMStatusRunning VMStatus = "running"
	VMStatusStopped VMStatus = "stopped"
	VMStatusError   VMStatus = "error"
)

// VMConfig represents the configuration for a VM
type VMConfig struct {
	Name      string   `yaml:"name"`
	VCPUs     int      `yaml:"vcpus"`
	MemoryMB  int      `yaml:"memory_mb"`
	Kernel    string   `yaml:"kernel"`
	RootFS    string   `yaml:"rootfs"`
	DiskGB    int      `yaml:"disk_gb"`
	Network   NetworkConfig `yaml:"network"`
	Mounts    []MountConfig `yaml:"mounts"`
	Env       map[string]string `yaml:"env"`
}

// NetworkConfig represents network configuration for a VM
type NetworkConfig struct {
	Mode   string   `yaml:"mode"`
	Bridge string   `yaml:"bridge"`
	Ports  []PortForward `yaml:"ports"`
}

// PortForward represents a port forwarding rule
type PortForward struct {
	Host   int `yaml:"host"`
	Guest  int `yaml:"guest"`
}

// MountConfig represents a directory mount configuration
type MountConfig struct {
	Host     string `yaml:"host"`
	Guest    string `yaml:"guest"`
	Readonly bool   `yaml:"readonly"`
}

// VMState represents the runtime state of a VM
type VMState struct {
	Name        string     `json:"name"`
	Status      VMStatus   `json:"status"`
	PID         int        `json:"pid,omitempty"`
	SocketPath  string     `json:"socket_path,omitempty"`
	IP          string     `json:"ip,omitempty"`
	CreatedAt   int64      `json:"created_at"`
	UpdatedAt   int64      `json:"updated_at"`
	RootFSPath  string     `json:"rootfs_path,omitempty"`
	TAPDevice   string     `json:"tap_device,omitempty"`
}

// Snapshot represents a VM snapshot
type Snapshot struct {
	Name      string `json:"name"`
	Tag       string `json:"tag"`
	CreatedAt int64  `json:"created_at"`
	SizeMB    int    `json:"size_mb"`
}

// ImageInfo represents information about a base image
type ImageInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	SizeMB      int    `json:"size_mb"`
	CreatedAt   int64  `json:"created_at"`
	SourceURL   string `json:"source_url,omitempty"`
}

// String returns the string representation of VMStatus
func (s VMStatus) String() string {
	return string(s)
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
