// Package vm provides cross-platform VM management operations.
// This file contains the shared VM manager implementation used on all platforms.
package vm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/pkg/models"
	"github.com/dalinkstone/tent/internal/state"
	"github.com/dalinkstone/tent/internal/hypervisor"
	"github.com/dalinkstone/tent/internal/network"
	"github.com/dalinkstone/tent/internal/storage"
)

// StateManager defines the interface for state persistence
type StateManager interface {
	GetVM(name string) (*models.VMState, error)
	StoreVM(vm *models.VMState) error
	UpdateVM(name string, updateFn func(*models.VMState) error) error
	DeleteVM(name string) error
	ListVMs() ([]*models.VMState, error)
}

// HypervisorBackend defines the interface for hypervisor VM operations
type HypervisorBackend interface {
	// CreateVM creates a new VM
	CreateVM(config *models.VMConfig) (hypervisor.VM, error)
	// ListVMs returns all active VMs
	ListVMs() ([]hypervisor.VM, error)
	// DestroyVM destroys a VM
	DestroyVM(vm hypervisor.VM) error
}

// NetworkManager defines the interface for network operations
type NetworkManager interface {
	SetupVMNetwork(name string, config *models.VMConfig) (string, error)
	CleanupVMNetwork(name string) error
}

// StorageManager defines the interface for storage operations
type StorageManager interface {
	CreateRootFS(name string, config *models.VMConfig) (string, error)
	DestroyVMStorage(name string) error
	CreateSnapshot(name string, tag string) (string, error)
	RestoreSnapshot(name string, tag string) error
	ListSnapshots(name string) ([]*storage.SnapshotInfo, error)
}

// PortForwarder defines the interface for port forwarding operations
type PortForwarder interface {
	SetupForwards(vmName string, ports []models.PortForward) error
	ActivateForwards(vmName string, guestIP string) error
	RemoveForwards(vmName string) error
	ListForwards(vmName string) []network.ForwardStatus
	ListAllForwards() []network.ForwardStatus
}

// VMManager manages microVM lifecycle operations
type VMManager struct {
	stateManager   StateManager
	hypervisor     HypervisorBackend
	networkMgr     NetworkManager
	storageMgr     StorageManager
	portForwarder  PortForwarder
	baseDir        string
	execCommand    func(cmd string, args ...string) *exec.Cmd
	runningVMs     map[string]hypervisor.VM // Track running VM instances
}

// NewManager creates a new VM manager
func NewManager(baseDir string, stateManager StateManager, hv HypervisorBackend, networkMgr NetworkManager, storageMgr StorageManager) (*VMManager, error) {
	if stateManager == nil {
		sm, err := state.NewStateManager(filepath.Join(baseDir, "state.json"))
		if err != nil {
			return nil, fmt.Errorf("failed to create state manager: %w", err)
		}
		stateManager = sm
	}

	if hv == nil {
		return nil, fmt.Errorf("hypervisor backend must be provided")
	}

	if networkMgr == nil {
		nm, err := network.NewManager()
		if err != nil {
			return nil, fmt.Errorf("failed to create network manager: %w", err)
		}
		networkMgr = nm
	}

	if storageMgr == nil {
		sm, err := storage.NewManager(baseDir)
		if err != nil {
			return nil, fmt.Errorf("failed to create storage manager: %w", err)
		}
		storageMgr = sm
	}

	return &VMManager{
		stateManager:   stateManager,
		hypervisor:     hv,
		networkMgr:     networkMgr,
		storageMgr:     storageMgr,
		portForwarder:  network.NewPortForwarder(),
		baseDir:        baseDir,
		execCommand:    exec.Command,
		runningVMs:     make(map[string]hypervisor.VM),
	}, nil
}

// Setup initializes the VM manager components
func (m *VMManager) Setup() error {
	return nil // Components are initialized in NewManager
}

// Create creates a new microVM
func (m *VMManager) Create(name string, config *models.VMConfig) error {
	// Validate config
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	// Check if VM already exists
	if _, err := m.stateManager.GetVM(name); err == nil {
		return fmt.Errorf("VM %s already exists", name)
	}

	// Create storage (rootfs)
	rootfsPath, err := m.storageMgr.CreateRootFS(name, config)
	if err != nil {
		return fmt.Errorf("failed to create rootfs: %w", err)
	}

	// Setup network
	tapDevice, err := m.networkMgr.SetupVMNetwork(name, config)
	if err != nil {
		return fmt.Errorf("failed to setup network: %w", err)
	}

	// Setup port forwarding rules (but don't activate until VM starts)
	if len(config.Network.Ports) > 0 {
		if err := m.portForwarder.SetupForwards(name, config.Network.Ports); err != nil {
			return fmt.Errorf("failed to setup port forwarding: %w", err)
		}
	}

	// Persist the full config so it survives stop/start cycles
	if err := m.saveConfig(config); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Create VM state
	vmState := &models.VMState{
		Name:        name,
		Status:      models.VMStatusCreated,
		RootFSPath:  rootfsPath,
		TAPDevice:   tapDevice,
		CreatedAt:   time.Now().Unix(),
		UpdatedAt:   time.Now().Unix(),
	}

	// Save state
	if err := m.stateManager.StoreVM(vmState); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}

	return nil
}

// saveConfig persists the full VMConfig to disk
func (m *VMManager) saveConfig(config *models.VMConfig) error {
	configDir := filepath.Join(m.baseDir, "configs")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	configPath := filepath.Join(configDir, fmt.Sprintf("%s.yaml", config.Name))
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// Start starts a stopped microVM
func (m *VMManager) Start(name string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	if vmState.Status == models.VMStatusRunning {
		return fmt.Errorf("VM %s is already running", name)
	}

	// Get VM config from state
	config, err := m.loadConfigFromState(vmState)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Use hypervisor backend to start VM
	vm, err := m.hypervisor.CreateVM(config)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	// Configure network for the VM
	vm.SetNetwork(vmState.TAPDevice, vmState.IP)

	// Start the VM
	if err := vm.Start(); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	// Track running VM
	m.runningVMs[name] = vm

	// Update state
	vmState.Status = models.VMStatusRunning
	vmState.IP = vm.GetIP()
	vmState.UpdatedAt = time.Now().Unix()

	// Activate port forwarding now that VM has an IP
	if vmState.IP != "" {
		if err := m.portForwarder.ActivateForwards(name, vmState.IP); err != nil {
			// Port forwarding failure is non-fatal - log but continue
			fmt.Fprintf(os.Stderr, "warning: failed to activate port forwarding for %s: %v\n", name, err)
		}
	}

	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.Status = vmState.Status
		s.IP = vmState.IP
		s.UpdatedAt = vmState.UpdatedAt
		return nil
	}); err != nil {
		vm.Stop()
		delete(m.runningVMs, name)
		return fmt.Errorf("failed to update state: %w", err)
	}

	return nil
}

// Stop gracefully shuts down a running microVM
func (m *VMManager) Stop(name string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	if vmState.Status != models.VMStatusRunning {
		return fmt.Errorf("VM %s is not running", name)
	}

	// Get running VM instance
	vm, ok := m.runningVMs[name]
	if !ok {
		return fmt.Errorf("VM %s not found in running VMs", name)
	}

	// Stop the VM
	if err := vm.Stop(); err != nil {
		return fmt.Errorf("failed to stop VM: %w", err)
	}

	// Remove port forwarding
	m.portForwarder.RemoveForwards(name)

	// Cleanup network resources
	if vmState.TAPDevice != "" {
		if err := m.networkMgr.CleanupVMNetwork(name); err != nil {
			return fmt.Errorf("failed to cleanup network: %w", err)
		}
	}

	// Remove from running VMs
	delete(m.runningVMs, name)

	// Update state
	vmState.Status = models.VMStatusStopped
	vmState.IP = ""
	vmState.UpdatedAt = time.Now().Unix()

	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.Status = vmState.Status
		s.IP = vmState.IP
		s.UpdatedAt = vmState.UpdatedAt
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	return nil
}

// Destroy removes a microVM and all its resources
func (m *VMManager) Destroy(name string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	// Stop if running
	if vmState.Status == models.VMStatusRunning {
		if err := m.Stop(name); err != nil {
			return fmt.Errorf("failed to stop VM before destroy: %w", err)
		}
	}

	// Remove port forwarding rules (for created/never-started VMs)
	m.portForwarder.RemoveForwards(name)

	// Cleanup network resources (for created/never-started VMs)
	if vmState.TAPDevice != "" {
		if err := m.networkMgr.CleanupVMNetwork(name); err != nil {
			return fmt.Errorf("failed to cleanup network: %w", err)
		}
	}

	// Cleanup storage
	if err := m.storageMgr.DestroyVMStorage(name); err != nil {
		return fmt.Errorf("failed to destroy storage: %w", err)
	}

	// Cleanup state
	if err := m.stateManager.DeleteVM(name); err != nil {
		return fmt.Errorf("failed to delete state: %w", err)
	}

	return nil
}

// Status returns detailed status of a microVM
func (m *VMManager) Status(name string) (*models.VMState, error) {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return nil, fmt.Errorf("VM not found: %w", err)
	}

	// Update status if running
	if vmState.Status == models.VMStatusRunning {
		vm, ok := m.runningVMs[name]
		if ok {
			status, _ := vm.Status()
			if status == models.VMStatusStopped {
				vmState.Status = models.VMStatusStopped
				vmState.IP = ""
				vmState.UpdatedAt = time.Now().Unix()
				delete(m.runningVMs, name)
				_ = m.stateManager.UpdateVM(name, func(s *models.VMState) error {
					s.Status = vmState.Status
					s.IP = vmState.IP
					s.UpdatedAt = vmState.UpdatedAt
					return nil
				})
			} else {
				vmState.IP = vm.GetIP()
			}
		}
	}

	return vmState, nil
}

// List returns all managed microVMs
func (m *VMManager) List() ([]*models.VMState, error) {
	return m.stateManager.ListVMs()
}

// Logs returns console logs for a microVM
func (m *VMManager) Logs(name string) (string, error) {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return "", fmt.Errorf("VM not found: %w", err)
	}

	// For now, return placeholder - in production, this would read
	// from the hypervisor log or capture output
	logPath := filepath.Join(m.baseDir, "logs", fmt.Sprintf("%s.log", name))
	if data, err := os.ReadFile(logPath); err == nil {
		return string(data), nil
	}

	return fmt.Sprintf("No logs available for VM %s", name), nil
}

// CreateSnapshot creates a snapshot of a VM's rootfs
func (m *VMManager) CreateSnapshot(name string, tag string) (string, error) {
	// Check if VM exists
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return "", fmt.Errorf("VM not found: %w", err)
	}

	return m.storageMgr.CreateSnapshot(name, tag)
}

// RestoreSnapshot restores a VM's rootfs from a snapshot
func (m *VMManager) RestoreSnapshot(name string, tag string) error {
	// Check if VM exists
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	return m.storageMgr.RestoreSnapshot(name, tag)
}

// ListSnapshots lists all snapshots for a VM
func (m *VMManager) ListSnapshots(name string) ([]*models.Snapshot, error) {
	// Check if VM exists
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return nil, fmt.Errorf("VM not found: %w", err)
	}

	snapshots, err := m.storageMgr.ListSnapshots(name)
	if err != nil {
		return nil, err
	}

	// Convert storage SnapshotInfo to models Snapshot
	var result []*models.Snapshot
	for _, snap := range snapshots {
		result = append(result, &models.Snapshot{
			Tag:       snap.Tag,
			SizeMB:    snap.SizeMB,
			Timestamp: snap.CreatedAt,
		})
	}

	return result, nil
}

// Exec executes a command inside a running microVM
func (m *VMManager) Exec(name string, command []string) (string, int, error) {
	// Check if VM exists
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return "", 1, fmt.Errorf("VM not found: %w", err)
	}

	if vmState.Status != models.VMStatusRunning {
		return "", 1, fmt.Errorf("VM %s is not running", name)
	}

	// Resolve the guest IP
	vmIP := vmState.IP
	if vm, ok := m.runningVMs[name]; ok {
		if ip := vm.GetIP(); ip != "" {
			vmIP = ip
		}
	}

	if vmIP == "" {
		return "", 1, fmt.Errorf("VM %s has no IP address", name)
	}

	return m.execSSH(vmIP, command)
}

// execSSH runs a command inside the guest VM over SSH and returns
// the combined output and exit code.
func (m *VMManager) execSSH(ip string, command []string) (string, int, error) {
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
		"root@" + ip,
		"--",
	}
	sshArgs = append(sshArgs, command...)

	cmd := m.execCommand("ssh", sshArgs...)
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return string(output), 1, fmt.Errorf("SSH exec failed: %w", err)
		}
	}

	return string(output), exitCode, nil
}

// ListPortForwards returns port forwarding status for a specific VM
func (m *VMManager) ListPortForwards(name string) ([]network.ForwardStatus, error) {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return nil, fmt.Errorf("VM not found: %w", err)
	}
	return m.portForwarder.ListForwards(name), nil
}

// ListAllPortForwards returns port forwarding status for all VMs
func (m *VMManager) ListAllPortForwards() []network.ForwardStatus {
	return m.portForwarder.ListAllForwards()
}

// loadConfigFromState loads the VM config from the state file
func (m *VMManager) loadConfigFromState(vmState *models.VMState) (*models.VMConfig, error) {
	// Load config from state file if present
	configPath := filepath.Join(m.baseDir, "configs", fmt.Sprintf("%s.yaml", vmState.Name))
	if data, err := os.ReadFile(configPath); err == nil {
		var config models.VMConfig
		if err := yaml.Unmarshal(data, &config); err == nil {
			return &config, nil
		}
	}

	// Return minimal config based on state
	return &models.VMConfig{
		Name:     vmState.Name,
		VCPUs:    2,  // default
		MemoryMB: 1024, // default
	}, nil
}
