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
	Name      string            `yaml:"name,omitempty"`
	From      string            `yaml:"from"`
	VCPUs     int               `yaml:"vcpus,omitempty"`
	MemoryMB  int               `yaml:"memory_mb,omitempty"`
	DiskGB    int               `yaml:"disk_gb,omitempty"`
	Network   *NetworkConf      `yaml:"network,omitempty"`
	Mounts    []Mount           `yaml:"mounts,omitempty"`
	Volumes   []VolumeMount     `yaml:"volumes,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	DependsOn []string          `yaml:"depends_on,omitempty"`
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
	Name   string `yaml:"name"`
	Status string `yaml:"status"`
	IP     string `yaml:"ip,omitempty"`
	PID    int    `yaml:"pid,omitempty"`
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
