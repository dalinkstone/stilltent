package compose

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dalinkstone/tent/internal/vm"
	"github.com/dalinkstone/tent/pkg/models"
)

// NewComposeManager creates a new compose manager
func NewComposeManager(baseDir string, vmManager *vm.VMManager, stateManager StateManager) *ComposeManager {
	return &ComposeManager{
		vmManager:    vmManager,
		baseDir:      baseDir,
		stateManager: stateManager,
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

	for sandboxName, sandboxConfig := range config.Sandboxes {
		// Create sandbox configuration
		vmConfig := &models.VMConfig{
			Name:     sandboxName,
			VCPUs:    sandboxConfig.VCPUs,
			MemoryMB: sandboxConfig.MemoryMB,
			DiskGB:   sandboxConfig.DiskGB,
			Mounts:   make([]models.MountConfig, len(sandboxConfig.Mounts)),
			Env:      sandboxConfig.Env,
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

		// Save compose state
		if err := m.stateManager.SaveComposeState(name, status); err != nil {
			return nil, fmt.Errorf("failed to save compose state: %w", err)
		}
	}

	return status, nil
}

// Down stops and destroys all sandboxes in a compose group
func (m *ComposeManager) Down(name string) error {
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

		// TODO: Query running VMs and build status
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
