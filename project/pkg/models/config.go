package models

// StorageConfig represents storage-related configuration
type StorageConfig struct {
	DataDir string `yaml:"data_dir" default:"~/.tent"`
}

// NetworkConfig represents network-wide configuration
type NetworkConfig struct {
	DefaultBridge string `yaml:"default_bridge" default:"tent0"`
	Subnet        string `yaml:"subnet" default:"172.16.0.0/24"`
	DHCPRange     string `yaml:"dhcp_range" default:"172.16.0.100-172.16.0.200"`
}
