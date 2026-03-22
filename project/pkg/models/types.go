package models

// VMStatus represents the current state of a microVM
type VMStatus string

const (
	VMStatusUnknown VMStatus = "unknown"
	VMStatusStopped VMStatus = "stopped"
	VMStatusRunning VMStatus = "running"
	VMStatusPaused  VMStatus = "paused"
	VMStatusCreated VMStatus = "created"
	VMStatusError   VMStatus = "error"
)

// String returns the string representation of VMStatus
func (s VMStatus) String() string {
	return string(s)
}

// VMConfig represents the configuration for a microVM
type VMConfig struct {
	Name      string        `yaml:"name" json:"name"`
	From      string        `yaml:"from" json:"from,omitempty"`
	VCPUs     int           `yaml:"vcpus" json:"vcpus"`
	MemoryMB  int           `yaml:"memory_mb" json:"memory_mb"`
	Kernel    string        `yaml:"kernel" json:"kernel,omitempty"`
	RootFS    string        `yaml:"rootfs" json:"rootfs,omitempty"`
	DiskGB    int           `yaml:"disk_gb" json:"disk_gb"`
	Network   NetworkConfig `yaml:"network" json:"network"`
	Mounts    []MountConfig `yaml:"mounts" json:"mounts,omitempty"`
	Env            map[string]string  `yaml:"env" json:"env,omitempty"`
	Labels         map[string]string  `yaml:"labels" json:"labels,omitempty"`
	RestartPolicy  RestartPolicy      `yaml:"restart_policy" json:"restart_policy,omitempty"`
	HealthCheck    *HealthCheckConfig `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
}

// RestartPolicy defines the restart behavior for a sandbox
type RestartPolicy string

const (
	// RestartPolicyNever never auto-restarts the sandbox (default)
	RestartPolicyNever RestartPolicy = "never"
	// RestartPolicyAlways always restarts the sandbox when it stops
	RestartPolicyAlways RestartPolicy = "always"
	// RestartPolicyOnFailure restarts the sandbox only on non-zero exit
	RestartPolicyOnFailure RestartPolicy = "on-failure"
)

// NetworkConfig represents network configuration for a VM
type NetworkConfig struct {
	Mode   string        `yaml:"mode" json:"mode"`
	Bridge string        `yaml:"bridge" json:"bridge,omitempty"`
	Allow  []string      `yaml:"allow" json:"allow,omitempty"`
	Deny   []string      `yaml:"deny" json:"deny,omitempty"`
	Ports  []PortForward `yaml:"ports" json:"ports,omitempty"`
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

// HealthCheckConfig defines how to check if a sandbox is healthy
type HealthCheckConfig struct {
	// Type is the check type: "exec", "http", or "agent" (ping via guest agent)
	Type string `yaml:"type" json:"type"`
	// Command to execute inside the sandbox (for type=exec)
	Command string `yaml:"command,omitempty" json:"command,omitempty"`
	// URL to check (for type=http, checked from inside the guest)
	URL string `yaml:"url,omitempty" json:"url,omitempty"`
	// IntervalSec is seconds between checks (default: 30)
	IntervalSec int `yaml:"interval_sec,omitempty" json:"interval_sec,omitempty"`
	// TimeoutSec is seconds before a check is considered failed (default: 5)
	TimeoutSec int `yaml:"timeout_sec,omitempty" json:"timeout_sec,omitempty"`
	// Retries is the number of consecutive failures before marking unhealthy (default: 3)
	Retries int `yaml:"retries,omitempty" json:"retries,omitempty"`
	// StartPeriodSec is grace period after start before health checks count (default: 0)
	StartPeriodSec int `yaml:"start_period_sec,omitempty" json:"start_period_sec,omitempty"`
}

// HealthCheckDefaults fills in zero-value fields with defaults
func (h *HealthCheckConfig) HealthCheckDefaults() {
	if h.IntervalSec <= 0 {
		h.IntervalSec = 30
	}
	if h.TimeoutSec <= 0 {
		h.TimeoutSec = 5
	}
	if h.Retries <= 0 {
		h.Retries = 3
	}
	if h.Type == "" {
		h.Type = "agent"
	}
}

// HealthStatus represents the current health of a sandbox
type HealthStatus string

const (
	HealthStatusUnknown   HealthStatus = "unknown"
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
	HealthStatusStarting  HealthStatus = "starting"
)

// HealthState tracks the health check state for a sandbox
type HealthState struct {
	Status           HealthStatus `json:"status"`
	FailCount        int          `json:"fail_count"`
	SuccessCount     int          `json:"success_count"`
	LastCheckAt      int64        `json:"last_check_at,omitempty"`
	LastOutput       string       `json:"last_output,omitempty"`
	LastError        string       `json:"last_error,omitempty"`
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
	SSHKeyPath  string     `json:"ssh_key_path,omitempty"`
	ImageRef    string     `json:"image_ref,omitempty"`
	VCPUs       int        `json:"vcpus,omitempty"`
	MemoryMB    int        `json:"memory_mb,omitempty"`
	DiskGB      int        `json:"disk_gb,omitempty"`
	Labels         map[string]string    `json:"labels,omitempty"`
	CreatedAt      int64         `json:"created_at"`
	UpdatedAt      int64         `json:"updated_at"`
	RestartCount   int              `json:"restart_count,omitempty"`
	RestartPolicy  RestartPolicy    `json:"restart_policy,omitempty"`
	Health         *HealthState     `json:"health,omitempty"`
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

// ResourceStats represents runtime resource statistics for a sandbox
type ResourceStats struct {
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	VCPUs         int     `json:"vcpus"`
	MemoryMB      int     `json:"memory_mb"`
	DiskGB        int     `json:"disk_gb"`
	DiskUsedMB    int64   `json:"disk_used_mb"`
	RootFSSizeMB  int64   `json:"rootfs_size_mb"`
	UptimeSeconds int64   `json:"uptime_seconds,omitempty"`
	IP            string  `json:"ip,omitempty"`
	ImageRef      string  `json:"image_ref,omitempty"`
	PID           int     `json:"pid,omitempty"`
	SnapshotCount int     `json:"snapshot_count"`
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
