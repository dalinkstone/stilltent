package compose

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

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
	startOrder := config.TopologicalOrder()
	status := &ComposeStatus{
		Name:       name,
		Sandboxes:  make(map[string]*SandboxStatus),
		StartOrder: startOrder,
	}

	// Start a DNS server for service discovery within this compose group.
	// Sandboxes can reach each other by name (e.g., "agent", "shared-db").
	dns, err := network.NewDNSServer(network.DefaultDNSConfig())
	if err == nil {
		if startErr := dns.Start(); startErr == nil {
			m.dnsServers[name] = dns
		}
	}

	// Start sandboxes in dependency order (dependencies first)
	for _, sandboxName := range startOrder {
		sandboxConfig := config.Sandboxes[sandboxName]
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

	// Stop sandboxes in reverse dependency order (dependents first)
	stopOrder := reverseOrder(status.StartOrder, status.Sandboxes)

	var errors []string
	for _, sandboxName := range stopOrder {
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

// ServiceLog holds log output for a single service in a compose group.
type ServiceLog struct {
	Service string
	Logs    string
}

// Logs returns logs for all sandboxes in a compose group, optionally filtered
// by service names. Each returned ServiceLog contains the service name and its
// console output. Services are sorted alphabetically.
func (m *ComposeManager) Logs(name string, services []string, tail int) ([]ServiceLog, error) {
	status, err := m.Status(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get compose status: %w", err)
	}

	if len(status.Sandboxes) == 0 {
		return nil, fmt.Errorf("no sandboxes found for compose group %q", name)
	}

	// Build target list
	targets := make(map[string]bool)
	if len(services) > 0 {
		for _, s := range services {
			if _, ok := status.Sandboxes[s]; !ok {
				return nil, fmt.Errorf("service %q not found in compose group %q", s, name)
			}
			targets[s] = true
		}
	} else {
		for s := range status.Sandboxes {
			targets[s] = true
		}
	}

	// Collect logs in parallel
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make([]ServiceLog, 0, len(targets))
	var firstErr error

	for svc := range targets {
		wg.Add(1)
		go func(service string) {
			defer wg.Done()
			var logs string
			var err error
			if tail > 0 {
				logs, err = m.vmManager.TailLogs(service, tail)
			} else {
				logs, err = m.vmManager.Logs(service)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("failed to get logs for %s: %w", service, err)
				}
				return
			}
			results = append(results, ServiceLog{Service: service, Logs: logs})
		}(svc)
	}
	wg.Wait()

	if firstErr != nil && len(results) == 0 {
		return nil, firstErr
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Service < results[j].Service
	})

	return results, nil
}

// FollowComposeLogs streams logs from all sandboxes in a compose group to the
// given writer. Each line is prefixed with the service name. The caller should
// close the done channel to stop streaming.
func (m *ComposeManager) FollowComposeLogs(name string, services []string, tail int, out io.Writer, done <-chan struct{}) error {
	status, err := m.Status(name)
	if err != nil {
		return fmt.Errorf("failed to get compose status: %w", err)
	}

	if len(status.Sandboxes) == 0 {
		return fmt.Errorf("no sandboxes found for compose group %q", name)
	}

	// Build target list
	targets := make([]string, 0)
	if len(services) > 0 {
		for _, s := range services {
			if _, ok := status.Sandboxes[s]; !ok {
				return fmt.Errorf("service %q not found in compose group %q", s, name)
			}
			targets = append(targets, s)
		}
	} else {
		for s := range status.Sandboxes {
			targets = append(targets, s)
		}
	}
	sort.Strings(targets)

	// Create a prefixed writer for each service and follow logs concurrently
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, svc := range targets {
		wg.Add(1)
		go func(service string) {
			defer wg.Done()
			pw := &prefixWriter{
				prefix: service,
				out:    out,
				mu:     &mu,
			}
			// Best-effort: if a service has no logs or isn't running, skip silently
			_ = m.vmManager.FollowLogs(service, tail, pw, done)
		}(svc)
	}
	wg.Wait()
	return nil
}

// prefixWriter wraps an io.Writer and prepends each line with a service name prefix.
type prefixWriter struct {
	prefix string
	out    io.Writer
	mu     *sync.Mutex
	buf    []byte
}

func (pw *prefixWriter) Write(p []byte) (int, error) {
	pw.buf = append(pw.buf, p...)
	for {
		idx := -1
		for i, b := range pw.buf {
			if b == '\n' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		line := pw.buf[:idx]
		pw.buf = pw.buf[idx+1:]
		pw.mu.Lock()
		fmt.Fprintf(pw.out, "%s | %s\n", pw.prefix, string(line))
		pw.mu.Unlock()
	}
	return len(p), nil
}

// reverseOrder returns the start order reversed. If startOrder is empty or nil
// (e.g. from older state), it falls back to iterating the sandboxes map.
func reverseOrder(startOrder []string, sandboxes map[string]*SandboxStatus) []string {
	if len(startOrder) > 0 {
		reversed := make([]string, len(startOrder))
		for i, name := range startOrder {
			reversed[len(startOrder)-1-i] = name
		}
		return reversed
	}
	// Fallback: no ordering info, just iterate the map
	names := make([]string, 0, len(sandboxes))
	for name := range sandboxes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
