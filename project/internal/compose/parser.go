package compose

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"

	tentconfig "github.com/dalinkstone/tent/internal/config"
)

// ParseConfig parses a compose YAML file and returns a ComposeConfig
func ParseConfig(data []byte) (*ComposeConfig, error) {
	// Expand environment variable references before parsing
	data = tentconfig.ExpandEnvBytes(data)

	var cfg ComposeConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Validate the configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid compose config: %w", err)
	}

	return &cfg, nil
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

		// Validate health check
		if err := ValidateHealthCheck(sandbox.HealthCheck); err != nil {
			return fmt.Errorf("sandbox %s: %w", name, err)
		}

		// Validate restart policy
		if err := ValidateRestartPolicy(sandbox.RestartPolicy); err != nil {
			return fmt.Errorf("sandbox %s: %w", name, err)
		}

		// Validate depends_on references
		for _, dep := range sandbox.DependsOn {
			if _, ok := c.Sandboxes[dep]; !ok {
				return fmt.Errorf("sandbox %s: depends_on references unknown sandbox %q", name, dep)
			}
			if dep == name {
				return fmt.Errorf("sandbox %s: cannot depend on itself", name)
			}
		}
	}

	// Validate volume references
	for name, sandbox := range c.Sandboxes {
		for _, vol := range sandbox.Volumes {
			if vol.Name == "" {
				return fmt.Errorf("sandbox %s: volume mount must have a 'name' field", name)
			}
			if vol.Guest == "" {
				return fmt.Errorf("sandbox %s: volume %q must have a 'guest' mount path", name, vol.Name)
			}
			if c.Volumes == nil {
				return fmt.Errorf("sandbox %s: references volume %q but no volumes are defined", name, vol.Name)
			}
			if _, ok := c.Volumes[vol.Name]; !ok {
				return fmt.Errorf("sandbox %s: references undefined volume %q", name, vol.Name)
			}
		}
	}

	// Set volume defaults
	for vname, vol := range c.Volumes {
		if vol == nil {
			c.Volumes[vname] = &VolumeConfig{Driver: "local"}
		} else if vol.Driver == "" {
			vol.Driver = "local"
		}
	}

	// Detect dependency cycles
	if err := c.detectCycles(); err != nil {
		return err
	}

	return nil
}

// detectCycles checks for circular dependencies in the compose config
func (c *ComposeConfig) detectCycles() error {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)

	state := make(map[string]int)
	for name := range c.Sandboxes {
		state[name] = unvisited
	}

	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		if state[name] == visited {
			return nil
		}
		if state[name] == visiting {
			return fmt.Errorf("dependency cycle detected: %s -> %s", joinPath(path), name)
		}
		state[name] = visiting
		path = append(path, name)
		for _, dep := range c.Sandboxes[name].DependsOn {
			if err := visit(dep, path); err != nil {
				return err
			}
		}
		state[name] = visited
		return nil
	}

	for name := range c.Sandboxes {
		if state[name] == unvisited {
			if err := visit(name, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// TopologicalOrder returns sandbox names in dependency order (dependencies first).
func (c *ComposeConfig) TopologicalOrder() []string {
	var order []string
	visited := make(map[string]bool)

	var visit func(name string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		if sb, ok := c.Sandboxes[name]; ok {
			for _, dep := range sb.DependsOn {
				visit(dep)
			}
		}
		order = append(order, name)
	}

	// Sort keys for deterministic ordering of sandboxes at the same level
	names := make([]string, 0, len(c.Sandboxes))
	for name := range c.Sandboxes {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		visit(name)
	}
	return order
}

func joinPath(path []string) string {
	if len(path) == 0 {
		return ""
	}
	result := path[0]
	for i := 1; i < len(path); i++ {
		result += " -> " + path[i]
	}
	return result
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
