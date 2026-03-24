package config

import (
	"os"

	"gopkg.in/yaml.v3"
	"github.com/dalinkstone/tent/pkg/models"
)

// Config represents the main configuration
type Config struct {
	VM       VMConfig       `yaml:"vm"`
	Storage  StorageConfig  `yaml:"storage"`
	Network  NetworkConfig  `yaml:"network"`
}

// VMConfig represents VM-related configuration
type VMConfig struct {
	DefaultVCPUs     int `yaml:"default_vcpus"`
	DefaultMemoryMB  int `yaml:"default_memory_mb"`
	DefaultDiskGB    int `yaml:"default_disk_gb"`
}

// StorageConfig represents storage configuration
type StorageConfig struct {
	DataDir string `yaml:"data_dir"`
}

// NetworkConfig represents network configuration
type NetworkConfig struct {
	DefaultBridge string `yaml:"default_bridge"`
	Subnet        string `yaml:"subnet"`
	DHCPRange     string `yaml:"dhcp_range"`
}

// Load loads configuration from a YAML file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Expand environment variable references (e.g., ${VAR}, ${VAR:-default})
	data = ExpandEnvBytes(data)

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	
	return &config, nil
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		VM: VMConfig{
			DefaultVCPUs:    2,
			DefaultMemoryMB: 1024,
			DefaultDiskGB:   10,
		},
		Storage: StorageConfig{
			DataDir: "~/.tent",
		},
		Network: NetworkConfig{
			DefaultBridge: "tent0",
			Subnet:        "172.16.0.0/24",
			DHCPRange:     "172.16.0.100-172.16.0.200",
		},
	}
}

// ToVMConfig converts Config to VMConfig with defaults
func (c *Config) ToVMConfig(name string) *models.VMConfig {
	return &models.VMConfig{
		Name:     name,
		VCPUs:    c.VM.DefaultVCPUs,
		MemoryMB: c.VM.DefaultMemoryMB,
		DiskGB:   c.VM.DefaultDiskGB,
		Kernel:   "default",
		RootFS:   "default",
		Network: models.NetworkConfig{
			Mode:   "bridge",
			Bridge: c.Network.DefaultBridge,
			Ports:  nil,
		},
		Mounts: nil,
		Env:    nil,
	}
}
