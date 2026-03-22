package compose

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dalinkstone/tent/internal/network"
	"github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

// envVarPattern matches ${VAR} and $VAR references in strings
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// expandEnvVars expands environment variable references in a string.
// Supports ${VAR}, ${VAR:-default}, and $VAR syntax.
// Unset variables without defaults expand to empty string.
func expandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Strip ${ } or $ prefix
		var varExpr string
		if strings.HasPrefix(match, "${") {
			varExpr = match[2 : len(match)-1]
		} else {
			varExpr = match[1:]
		}

		// Check for default value syntax: VAR:-default
		if idx := strings.Index(varExpr, ":-"); idx >= 0 {
			name := varExpr[:idx]
			defaultVal := varExpr[idx+2:]
			if val, ok := os.LookupEnv(name); ok {
				return val
			}
			return defaultVal
		}

		return os.Getenv(varExpr)
	})
}

// expandSandboxEnv expands all environment variable references in a
// sandbox's env map, returning a new map with resolved values.
func expandSandboxEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return env
	}
	expanded := make(map[string]string, len(env))
	for k, v := range env {
		expanded[k] = expandEnvVars(v)
	}
	return expanded
}

// NewComposeManager creates a new compose manager
func NewComposeManager(baseDir string, vmManager *vm.VMManager, stateManager StateManager) *ComposeManager {
	return &ComposeManager{
		vmManager:    vmManager,
		baseDir:      baseDir,
		stateManager: stateManager,
		dnsServers:   make(map[string]*network.DNSServer),
	}
}

// ParseConfig parses a compose YAML file
func (m *ComposeManager) ParseConfig(filePath string) (*ComposeConfig, error) {
	return ParseConfigFile(filePath)
}

// Up starts all sandboxes in a compose group
func (m *ComposeManager) Up(name string, config *ComposeConfig) (*ComposeStatus, error) {
	status := &ComposeStatus{
		Name:      name,
		Sandboxes: make(map[string]*SandboxStatus),
	}

	// Start a DNS server for service discovery within this compose group.
	// Sandboxes can reach each other by name (e.g., "agent", "shared-db").
	dns, err := network.NewDNSServer(network.DefaultDNSConfig())
	if err == nil {
		if startErr := dns.Start(); startErr == nil {
			m.dnsServers[name] = dns
		}
	}

	for sandboxName, sandboxConfig := range config.Sandboxes {
		// Expand environment variable references from host env
		expandedEnv := expandSandboxEnv(sandboxConfig.Env)

		// Build network config with allow/deny from compose
		netConfig := models.NetworkConfig{}
		if sandboxConfig.Network != nil {
			netConfig.Allow = sandboxConfig.Network.Allow
			netConfig.Deny = sandboxConfig.Network.Deny
		}

		// Create sandbox configuration
		vmConfig := &models.VMConfig{
			Name:     sandboxName,
			From:     sandboxConfig.From,
			VCPUs:    sandboxConfig.VCPUs,
			MemoryMB: sandboxConfig.MemoryMB,
			DiskGB:   sandboxConfig.DiskGB,
			Mounts:   make([]models.MountConfig, len(sandboxConfig.Mounts)),
			Env:      expandedEnv,
			Network:  netConfig,
		}

		// Convert mounts
		for i, m := range sandboxConfig.Mounts {
			vmConfig.Mounts[i] = models.MountConfig{
				Host:     m.Host,
				Guest:    m.Guest,
				Readonly: m.Readonly,
			}
		}

		// Create and start the sandbox
		if err := m.vmManager.Create(sandboxName, vmConfig); err != nil {
			return nil, fmt.Errorf("failed to create sandbox %s: %w", sandboxName, err)
		}

		if err := m.vmManager.Start(sandboxName); err != nil {
			return nil, fmt.Errorf("failed to start sandbox %s: %w", sandboxName, err)
		}

		// Get sandbox status
		vmState, err := m.vmManager.Status(sandboxName)
		if err != nil {
			return nil, fmt.Errorf("failed to get status for sandbox %s: %w", sandboxName, err)
		}

		status.Sandboxes[sandboxName] = &SandboxStatus{
			Name:   sandboxName,
			Status: vmState.Status.String(),
			IP:     vmState.IP,
			PID:    vmState.PID,
		}

		// Register sandbox name in DNS for service discovery
		if dnsServer, ok := m.dnsServers[name]; ok && vmState.IP != "" {
			if ip := net.ParseIP(vmState.IP); ip != nil {
				dnsServer.Register(sandboxName, ip)
			}
		}

		// Save compose state
		if err := m.stateManager.SaveComposeState(name, status); err != nil {
			return nil, fmt.Errorf("failed to save compose state: %w", err)
		}
	}

	return status, nil
}

// Down stops and destroys all sandboxes in a compose group
func (m *ComposeManager) Down(name string) error {
	// Stop the DNS server for this compose group
	if dns, ok := m.dnsServers[name]; ok {
		dns.Stop()
		delete(m.dnsServers, name)
	}

	status, err := m.stateManager.LoadComposeState(name)
	if err != nil {
		return fmt.Errorf("failed to load compose state: %w", err)
	}

	var errors []string
	for sandboxName := range status.Sandboxes {
		// Stop sandbox
		if err := m.vmManager.Stop(sandboxName); err != nil {
			errors = append(errors, fmt.Sprintf("failed to stop %s: %v", sandboxName, err))
		}

		// Destroy sandbox
		if err := m.vmManager.Destroy(sandboxName); err != nil {
			errors = append(errors, fmt.Sprintf("failed to destroy %s: %v", sandboxName, err))
		}
	}

	// Delete compose state
	if err := m.stateManager.DeleteComposeState(name); err != nil {
		errors = append(errors, fmt.Sprintf("failed to delete compose state: %v", err))
	}

	if len(errors) > 0 {
		return fmt.Errorf("errors during shutdown: %v", errors)
	}

	return nil
}

// Status returns the status of all sandboxes in a compose group
func (m *ComposeManager) Status(name string) (*ComposeStatus, error) {
	status, err := m.stateManager.LoadComposeState(name)
	if err != nil {
		// If state not found, query actual VM status
		status = &ComposeStatus{
			Name:      name,
			Sandboxes: make(map[string]*SandboxStatus),
		}

		// Query running VMs and build status
		// Load the compose config to get the list of sandboxes
		config, err := m.ParseConfig(filepath.Join(m.baseDir, "compose", name, "config.yaml"))
		if err == nil && config != nil {
			// Query running VMs
			allVMs, err := m.vmManager.List()
			if err == nil {
				// Build a map of running VM names for quick lookup
				runningVMs := make(map[string]*models.VMState)
				for _, vmState := range allVMs {
					runningVMs[vmState.Name] = vmState
				}

				// For each sandbox in the compose config, check if it's running
				for sandboxName := range config.Sandboxes {
					if vmState, exists := runningVMs[sandboxName]; exists {
						status.Sandboxes[sandboxName] = &SandboxStatus{
							Name:   sandboxName,
							Status: vmState.Status.String(),
							IP:     vmState.IP,
							PID:    vmState.PID,
						}
					} else {
						// Sandbox not running
						status.Sandboxes[sandboxName] = &SandboxStatus{
							Name:   sandboxName,
							Status: "stopped",
						}
					}
				}
			}
		}

		return status, nil
	}

	// Update status with current VM states
	for sandboxName := range status.Sandboxes {
		vmState, err := m.vmManager.Status(sandboxName)
		if err == nil {
			status.Sandboxes[sandboxName].Status = vmState.Status.String()
			status.Sandboxes[sandboxName].IP = vmState.IP
			status.Sandboxes[sandboxName].PID = vmState.PID
		}
	}

	return status, nil
}

// List returns all compose groups
func (m *ComposeManager) List() ([]string, error) {
	statesDir := filepath.Join(m.baseDir, "compose")
	entries, err := os.ReadDir(statesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read compose states: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	return names, nil
}
