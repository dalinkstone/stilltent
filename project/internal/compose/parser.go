package compose

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ParseConfig parses a compose YAML file and returns a ComposeConfig
func ParseConfig(data []byte) (*ComposeConfig, error) {
	var config ComposeConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Validate the configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid compose config: %w", err)
	}

	return &config, nil
}

// Validate checks if the compose configuration is valid
func (c *ComposeConfig) Validate() error {
	if c.Sandboxes == nil || len(c.Sandboxes) == 0 {
		return fmt.Errorf("compose config must have at least one sandbox")
	}

	for name, sandbox := range c.Sandboxes {
		if sandbox.From == "" {
			return fmt.Errorf("sandbox %s: 'from' field is required", name)
		}

		if sandbox.VCPUs <= 0 {
			sandbox.VCPUs = 2 // Default
		}

		if sandbox.MemoryMB <= 0 {
			sandbox.MemoryMB = 1024 // Default
		}

		if sandbox.DiskGB <= 0 {
			sandbox.DiskGB = 10 // Default
		}
	}

	return nil
}

// ParseConfigFile parses a compose YAML file from disk
func ParseConfigFile(filePath string) (*ComposeConfig, error) {
	data, err := readFile(filePath)
	if err != nil {
		return nil, err
	}

	return ParseConfig(data)
}

// readFile reads a file from disk
func readFile(filePath string) ([]byte, error) {
	// This is a placeholder - actual implementation would use os.ReadFile
	// Using a separate function to make testing easier
	data, err := readFileImpl(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return data, nil
}

// readFileImpl is the actual file reading implementation
func readFileImpl(filePath string) ([]byte, error) {
	return os.ReadFile(filePath)
}
