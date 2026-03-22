package compose

import (
	"github.com/dalinkstone/tent/internal/network"
	"github.com/dalinkstone/tent/internal/sandbox"
)

// ComposeConfig represents the top-level compose file structure
type ComposeConfig struct {
	Sandboxes map[string]*SandboxConfig `yaml:"sandboxes,omitempty"`
	Volumes   map[string]*VolumeConfig  `yaml:"volumes,omitempty"`
}

// VolumeConfig defines a named volume in a compose file
type VolumeConfig struct {
	Driver string            `yaml:"driver,omitempty"` // "local" (default)
	SizeMB int               `yaml:"size_mb,omitempty"`
	Labels map[string]string `yaml:"labels,omitempty"`
}

// SandboxConfig represents a single sandbox definition in the compose file
type SandboxConfig struct {
	Name          string            `yaml:"name,omitempty"`
	From          string            `yaml:"from"`
	VCPUs         int               `yaml:"vcpus,omitempty"`
	MemoryMB      int               `yaml:"memory_mb,omitempty"`
	DiskGB        int               `yaml:"disk_gb,omitempty"`
	Network       *NetworkConf      `yaml:"network,omitempty"`
	Mounts        []Mount           `yaml:"mounts,omitempty"`
	Volumes       []VolumeMount     `yaml:"volumes,omitempty"`
	Env           map[string]string `yaml:"env,omitempty"`
	DependsOn     []string          `yaml:"depends_on,omitempty"`
	HealthCheck   *HealthCheckConf  `yaml:"health_check,omitempty"`
	RestartPolicy string            `yaml:"restart,omitempty"` // "no", "on-failure", "always"
	Hooks         *LifecycleHooks   `yaml:"hooks,omitempty"`
}

// LifecycleHooks defines commands to run at various stages of sandbox lifecycle.
// Each hook is a list of shell commands executed inside the sandbox.
type LifecycleHooks struct {
	// PostStart runs inside the sandbox after it has started and is reachable.
	// Use for initialization: warming caches, running migrations, seeding data.
	PostStart []string `yaml:"post_start,omitempty"`
	// PreStop runs inside the sandbox before it is stopped.
	// Use for graceful shutdown: draining connections, flushing buffers.
	PreStop []string `yaml:"pre_stop,omitempty"`
	// PostCreate runs inside the sandbox after creation but before first start.
	// Use for one-time setup: installing packages, configuring services.
	PostCreate []string `yaml:"post_create,omitempty"`
	// PreDestroy runs inside the sandbox before it is destroyed.
	// Use for cleanup: exporting data, sending notifications.
	PreDestroy []string `yaml:"pre_destroy,omitempty"`
}

// HealthCheckConf defines a health check for a sandbox service
type HealthCheckConf struct {
	// Command to run inside the sandbox to check health (exit 0 = healthy)
	Command []string `yaml:"command"`
	// IntervalSec is how often to run the check (default: 30)
	IntervalSec int `yaml:"interval_sec,omitempty"`
	// TimeoutSec is how long to wait for the check to complete (default: 10)
	TimeoutSec int `yaml:"timeout_sec,omitempty"`
	// Retries is how many consecutive failures before marking unhealthy (default: 3)
	Retries int `yaml:"retries,omitempty"`
	// StartPeriodSec is grace period after start before checks begin (default: 0)
	StartPeriodSec int `yaml:"start_period_sec,omitempty"`
}

// VolumeMount maps a named volume to a guest path
type VolumeMount struct {
	Name     string `yaml:"name"`
	Guest    string `yaml:"guest"`
	Readonly bool   `yaml:"readonly,omitempty"`
}

// NetworkConf defines network policy for a sandbox
type NetworkConf struct {
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
}

// Mount represents a host-to-guest filesystem mount
type Mount struct {
	Host     string `yaml:"host"`
	Guest    string `yaml:"guest"`
	Readonly bool   `yaml:"readonly,omitempty"`
}

// ComposeStatus represents the status of all sandboxes in a compose group
type ComposeStatus struct {
	Name       string                    `yaml:"name"`
	Sandboxes  map[string]*SandboxStatus `yaml:"sandboxes"`
	StartOrder []string                  `yaml:"start_order,omitempty"`
}

// SandboxStatus represents the status of a single sandbox in a compose group
type SandboxStatus struct {
	Name         string `yaml:"name"`
	Status       string `yaml:"status"`
	IP           string `yaml:"ip,omitempty"`
	PID          int    `yaml:"pid,omitempty"`
	Health       string `yaml:"health,omitempty"`        // "healthy", "unhealthy", "starting", ""
	HealthDetail string `yaml:"health_detail,omitempty"` // last check output
	Restarts     int    `yaml:"restarts,omitempty"`
}

// ComposeManager manages multi-sandbox orchestration
type ComposeManager struct {
	vmManager     *vm.VMManager
	baseDir       string
	stateManager  StateManager
	dnsServers    map[string]*network.DNSServer // group name -> DNS server
	volumeManager *VolumeManager
}

// StateManager manages compose state persistence
type StateManager interface {
	SaveComposeState(name string, state *ComposeStatus) error
	LoadComposeState(name string) (*ComposeStatus, error)
	DeleteComposeState(name string) error
}
