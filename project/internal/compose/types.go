package compose

import (
	"github.com/dalinkstone/tent/internal/sandbox"
)

// ComposeConfig represents the top-level compose file structure
type ComposeConfig struct {
	Sandboxes map[string]*SandboxConfig `yaml:"sandboxes,omitempty"`
}

// SandboxConfig represents a single sandbox definition in the compose file
type SandboxConfig struct {
	Name      string       `yaml:"name,omitempty"`
	From      string       `yaml:"from"`
	VCPUs     int          `yaml:"vcpus,omitempty"`
	MemoryMB  int          `yaml:"memory_mb,omitempty"`
	DiskGB    int          `yaml:"disk_gb,omitempty"`
	Network   *NetworkConf `yaml:"network,omitempty"`
	Mounts    []Mount      `yaml:"mounts,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
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
	Name      string                    `yaml:"name"`
	Sandboxes map[string]*SandboxStatus `yaml:"sandboxes"`
}

// SandboxStatus represents the status of a single sandbox in a compose group
type SandboxStatus struct {
	Name   string `yaml:"name"`
	Status string `yaml:"status"`
	IP     string `yaml:"ip,omitempty"`
	PID    int    `yaml:"pid,omitempty"`
}

// ComposeManager manages multi-sandbox orchestration
type ComposeManager struct {
	vmManager    *vm.VMManager
	baseDir      string
	stateManager StateManager
}

// StateManager manages compose state persistence
type StateManager interface {
	SaveComposeState(name string, state *ComposeStatus) error
	LoadComposeState(name string) (*ComposeStatus, error)
	DeleteComposeState(name string) error
}
