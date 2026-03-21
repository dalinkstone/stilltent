package vm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/pkg/models"
	"github.com/dalinkstone/tent/internal/state"
	"github.com/dalinkstone/tent/internal/firecracker"
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

// FirecrackerClient defines the interface for Firecracker API operations
type FirecrackerClient interface {
	ConfigureVM(socketPath string, config *models.VMConfig) error
	StartVM(socketPath string) error
	ShutdownVM(socketPath string) error
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

// VMManager manages microVM lifecycle operations
type VMManager struct {
	stateManager  StateManager
	firecracker   FirecrackerClient
	networkMgr    NetworkManager
	storageMgr    StorageManager
	baseDir       string
	execCommand   func(cmd string, args ...string) *exec.Cmd
}

// NewManager creates a new VM manager
func NewManager(baseDir string, stateManager StateManager, fc FirecrackerClient, networkMgr NetworkManager, storageMgr StorageManager) (*VMManager, error) {
	if stateManager == nil {
		sm, err := state.NewStateManager(filepath.Join(baseDir, "state.json"))
		if err != nil {
			return nil, fmt.Errorf("failed to create state manager: %w", err)
		}
		stateManager = sm
	}

	if fc == nil {
		c, err := firecracker.NewClient("")
		if err != nil {
			return nil, fmt.Errorf("failed to create firecracker client: %w", err)
		}
		fc = c
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
		stateManager: stateManager,
		firecracker:  fc,
		networkMgr:   networkMgr,
		storageMgr:   storageMgr,
		baseDir:      baseDir,
		execCommand:  exec.Command,
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

	// Create firecracker socket path
	socketPath := filepath.Join(m.baseDir, "sockets", fmt.Sprintf("%s.sock", name))

	// Create VM state
	vmState := &models.VMState{
		Name:        name,
		Status:      models.VMStatusCreated,
		RootFSPath:  rootfsPath,
		TAPDevice:   tapDevice,
		SocketPath:  socketPath,
		CreatedAt:   time.Now().Unix(),
		UpdatedAt:   time.Now().Unix(),
	}

	// Save state
	if err := m.stateManager.StoreVM(vmState); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
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

	// Spawn Firecracker process
	cmd := m.execCommand("firecracker", "--api-sock", vmState.SocketPath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start firecracker: %w", err)
	}

	vmState.PID = cmd.Process.Pid
	vmState.Status = models.VMStatusRunning
	vmState.UpdatedAt = time.Now().Unix()

	// Update state
	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.PID = vmState.PID
		s.Status = vmState.Status
		s.UpdatedAt = vmState.UpdatedAt
		return nil
	}); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("failed to update state: %w", err)
	}

	// Configure VM via Firecracker API
	if err := m.firecracker.ConfigureVM(vmState.SocketPath, config); err != nil {
		m.Stop(name)
		return fmt.Errorf("failed to configure VM: %w", err)
	}

	// Start the VM
	if err := m.firecracker.StartVM(vmState.SocketPath); err != nil {
		m.Stop(name)
		return fmt.Errorf("failed to start VM: %w", err)
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

	// Try graceful shutdown first via Firecracker API
	if err := m.firecracker.ShutdownVM(vmState.SocketPath); err != nil {
		// If API fails, try sending SIGTERM to the process
		if vmState.PID > 0 {
			proc, err := os.FindProcess(vmState.PID)
			if err == nil {
				proc.Signal(os.Signal(syscall.SIGTERM))
				// Give it a moment to shut down
				time.Sleep(100 * time.Millisecond)
				// If still running, kill it
				proc.Kill()
			}
		}
	}

	// Cleanup network resources
	if vmState.TAPDevice != "" {
		if err := m.networkMgr.CleanupVMNetwork(name); err != nil {
			return fmt.Errorf("failed to cleanup network: %w", err)
		}
	}

	// Update state
	vmState.Status = models.VMStatusStopped
	vmState.PID = 0
	vmState.IP = ""
	vmState.UpdatedAt = time.Now().Unix()

	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.Status = vmState.Status
		s.PID = vmState.PID
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
	if vmState.Status == models.VMStatusRunning && vmState.PID > 0 {
		proc, err := os.FindProcess(vmState.PID)
		if err == nil {
			// Check if process is still running
			if err := proc.Signal(os.Signal(nil)); err != nil {
				vmState.Status = models.VMStatusStopped
				vmState.PID = 0
				vmState.UpdatedAt = time.Now().Unix()
				_ = m.stateManager.UpdateVM(name, func(s *models.VMState) error {
					s.Status = vmState.Status
					s.PID = vmState.PID
					s.UpdatedAt = vmState.UpdatedAt
					return nil
				})
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
	// from the firecracker log file or capture output
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
