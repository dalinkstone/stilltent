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

// FilterByProfiles returns a new ComposeConfig containing only sandboxes that
// match the given active profiles. A sandbox with no profiles is always included.
// A sandbox with profiles is included only if at least one of its profiles is active.
// If no active profiles are specified, only sandboxes with no profiles are included.
func (c *ComposeConfig) FilterByProfiles(activeProfiles []string) *ComposeConfig {
	if len(activeProfiles) == 0 {
		// No profiles active: include only sandboxes with no profiles set
		filtered := &ComposeConfig{
			Sandboxes: make(map[string]*SandboxConfig),
			Volumes:   c.Volumes,
		}
		for name, sb := range c.Sandboxes {
			if len(sb.Profiles) == 0 {
				filtered.Sandboxes[name] = sb
			}
		}
		return filtered
	}

	activeSet := make(map[string]bool, len(activeProfiles))
	for _, p := range activeProfiles {
		activeSet[p] = true
	}

	filtered := &ComposeConfig{
		Sandboxes: make(map[string]*SandboxConfig),
		Volumes:   c.Volumes,
	}
	for name, sb := range c.Sandboxes {
		// No profiles means always included
		if len(sb.Profiles) == 0 {
			filtered.Sandboxes[name] = sb
			continue
		}
		// Include if any of the sandbox's profiles are active
		for _, p := range sb.Profiles {
			if activeSet[p] {
				filtered.Sandboxes[name] = sb
				break
			}
		}
	}
	return filtered
}

// ListProfiles returns all unique profile names referenced by sandboxes in the config.
func (c *ComposeConfig) ListProfiles() []string {
	seen := make(map[string]bool)
	for _, sb := range c.Sandboxes {
		for _, p := range sb.Profiles {
			seen[p] = true
		}
	}
	profiles := make([]string, 0, len(seen))
	for p := range seen {
		profiles = append(profiles, p)
	}
	// Sort for deterministic output
	sortStrings(profiles)
	return profiles
}

// sortStrings sorts a slice of strings in place.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
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
	EnvFile       []string          `yaml:"env_file,omitempty"` // paths to .env files loaded before inline env
	DependsOn     []string          `yaml:"depends_on,omitempty"`
	Profiles      []string          `yaml:"profiles,omitempty"`
	HealthCheck   *HealthCheckConf  `yaml:"health_check,omitempty"`
	RestartPolicy string            `yaml:"restart,omitempty"` // "no", "on-failure", "always"
	Hooks         *LifecycleHooks   `yaml:"hooks,omitempty"`
	Watch         *WatchConfig      `yaml:"watch,omitempty"`
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
