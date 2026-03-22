// Package vm provides cross-platform VM management operations.
// This file contains the shared VM manager implementation used on all platforms.
package vm

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/internal/console"
	"github.com/dalinkstone/tent/internal/hypervisor"
	"github.com/dalinkstone/tent/internal/network"
	"github.com/dalinkstone/tent/internal/state"
	"github.com/dalinkstone/tent/internal/storage"
	"github.com/dalinkstone/tent/pkg/models"
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
	consoleMgr     *console.Manager
	policyMgr      *network.PolicyManager
	egressFirewall *network.EgressFirewall
	mountMgr       *MountManager
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

	consoleMgr, err := console.NewManager(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create console manager: %w", err)
	}

	policyMgr, err := network.NewPolicyManager(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create policy manager: %w", err)
	}

	egressFw := network.NewEgressFirewall()

	return &VMManager{
		stateManager:   stateManager,
		hypervisor:     hv,
		networkMgr:     networkMgr,
		storageMgr:     storageMgr,
		portForwarder:  network.NewPortForwarder(),
		consoleMgr:     consoleMgr,
		policyMgr:      policyMgr,
		egressFirewall: egressFw,
		mountMgr:       NewMountManager(baseDir),
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

	// Initialize egress policy — use config-specified allowlist or fall back
	// to the default AI allowlist (AI-native defaults).
	if len(config.Network.Allow) > 0 || len(config.Network.Deny) > 0 {
		policy, err := m.policyMgr.SetPolicy(name, config.Network.Allow, config.Network.Deny)
		if err != nil {
			return fmt.Errorf("failed to set network policy: %w", err)
		}
		if err := m.policyMgr.SavePolicy(policy); err != nil {
			return fmt.Errorf("failed to save network policy: %w", err)
		}
	} else {
		policy, err := m.policyMgr.EnsureDefaultPolicy(name)
		if err != nil {
			return fmt.Errorf("failed to create default network policy: %w", err)
		}
		if err := m.policyMgr.SavePolicy(policy); err != nil {
			return fmt.Errorf("failed to save default network policy: %w", err)
		}
	}

	// Generate SSH keypair for this sandbox
	keyPair, err := GenerateSSHKeys(m.baseDir, name)
	if err != nil {
		return fmt.Errorf("failed to generate SSH keys: %w", err)
	}

	// Validate and persist mount shares if configured
	if len(config.Mounts) > 0 {
		shares, err := m.mountMgr.PrepareMounts(name, config.Mounts)
		if err != nil {
			return fmt.Errorf("failed to prepare mounts: %w", err)
		}
		if err := m.mountMgr.SaveMounts(name, shares); err != nil {
			return fmt.Errorf("failed to save mount state: %w", err)
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
		SSHKeyPath:  keyPair.PrivateKeyPath,
		ImageRef:    config.From,
		VCPUs:       config.VCPUs,
		MemoryMB:    config.MemoryMB,
		DiskGB:      config.DiskGB,
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

	// Attach host-to-guest mounts via virtio-9p
	if shares, err := m.mountMgr.LoadMounts(name); err == nil && len(shares) > 0 {
		mountTags := make([]hypervisor.MountTag, len(shares))
		for i, s := range shares {
			mountTags[i] = hypervisor.MountTag{
				Tag:      s.Tag,
				HostPath: s.HostPath,
				ReadOnly: s.ReadOnly,
			}
		}
		vm.AddMounts(mountTags)
	}

	// Set up console log capture
	consoleLogger, err := m.consoleMgr.CreateLogger(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to create console logger for %s: %v\n", name, err)
	} else {
		vm.SetConsoleOutput(consoleLogger)
	}

	// Start the VM
	if err := vm.Start(); err != nil {
		if consoleLogger != nil {
			consoleLogger.Close()
		}
		return fmt.Errorf("failed to start VM: %w", err)
	}

	// Track running VM
	m.runningVMs[name] = vm

	// Update state
	vmState.Status = models.VMStatusRunning
	vmState.IP = vm.GetIP()
	vmState.UpdatedAt = time.Now().Unix()

	// Apply egress firewall rules now that VM has an IP
	if vmState.IP != "" {
		m.egressFirewall.SetSandboxIP(name, vmState.IP)
		policy, err := m.policyMgr.GetPolicy(name)
		if err == nil {
			if fwErr := m.egressFirewall.ApplyPolicy(name, policy); fwErr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to apply egress firewall for %s: %v\n", name, fwErr)
			}
		}
	}

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

	// Close console logger
	m.consoleMgr.CloseLogger(name)

	// Remove egress firewall rules
	if err := m.egressFirewall.RemovePolicy(name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to remove egress firewall rules for %s: %v\n", name, err)
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

// Restart stops and starts a running microVM, incrementing the restart count
func (m *VMManager) Restart(name string, timeoutSec int) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	if vmState.Status != models.VMStatusRunning {
		return fmt.Errorf("VM %s is not running (status: %s)", name, vmState.Status)
	}

	// Stop the VM
	if err := m.Stop(name); err != nil {
		return fmt.Errorf("failed to stop VM during restart: %w", err)
	}

	// Wait briefly for clean shutdown if timeout specified
	if timeoutSec > 0 {
		time.Sleep(time.Duration(timeoutSec) * time.Second)
	}

	// Start the VM again
	if err := m.Start(name); err != nil {
		return fmt.Errorf("failed to start VM during restart: %w", err)
	}

	// Update restart count
	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.RestartCount++
		s.UpdatedAt = time.Now().Unix()
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update restart count: %w", err)
	}

	return nil
}

// SetRestartPolicy updates the restart policy for a sandbox
func (m *VMManager) SetRestartPolicy(name string, policy models.RestartPolicy) error {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	// Validate policy
	switch policy {
	case models.RestartPolicyNever, models.RestartPolicyAlways, models.RestartPolicyOnFailure:
		// valid
	default:
		return fmt.Errorf("invalid restart policy %q: must be never, always, or on-failure", policy)
	}

	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.RestartPolicy = policy
		s.UpdatedAt = time.Now().Unix()
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update restart policy: %w", err)
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

	// Cleanup SSH keys
	if err := RemoveSSHKeys(m.baseDir, name); err != nil {
		// Non-fatal — keys might not exist for older sandboxes
		fmt.Fprintf(os.Stderr, "warning: failed to remove SSH keys for %s: %v\n", name, err)
	}

	// Cleanup mount state
	if err := m.mountMgr.RemoveMounts(name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to remove mount state for %s: %v\n", name, err)
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

// Logs returns console logs for a microVM.
func (m *VMManager) Logs(name string) (string, error) {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return "", fmt.Errorf("VM not found: %w", err)
	}

	logs, err := m.consoleMgr.ReadLogs(name)
	if err != nil {
		return "", err
	}
	if logs == "" {
		return fmt.Sprintf("No logs available for VM %s", name), nil
	}
	return logs, nil
}

// TailLogs returns the last n lines of console logs for a VM.
func (m *VMManager) TailLogs(name string, n int) (string, error) {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return "", fmt.Errorf("VM not found: %w", err)
	}

	logs, err := m.consoleMgr.TailLogs(name, n)
	if err != nil {
		return "", err
	}
	if logs == "" {
		return fmt.Sprintf("No logs available for VM %s", name), nil
	}
	return logs, nil
}

// FollowLogs streams console logs to the given writer until done is closed.
func (m *VMManager) FollowLogs(name string, tailLines int, out io.Writer, done <-chan struct{}) error {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	return m.consoleMgr.FollowLogs(name, tailLines, out, done)
}

// ClearLogs removes console logs for a VM.
func (m *VMManager) ClearLogs(name string) error {
	return m.consoleMgr.ClearLogs(name)
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

	return m.execSSH(name, vmIP, command)
}

// execSSH runs a command inside the guest VM over SSH and returns
// the combined output and exit code.
func (m *VMManager) execSSH(vmName, ip string, command []string) (string, int, error) {
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
	}

	// Use per-sandbox SSH key if available
	keyPath := SSHPrivateKeyPath(m.baseDir, vmName)
	if _, err := os.Stat(keyPath); err == nil {
		sshArgs = append(sshArgs, "-i", keyPath)
	}

	sshArgs = append(sshArgs, "root@"+ip, "--")
	sshArgs = append(sshArgs, command...)
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

// GetSSHArgs returns SSH arguments (including identity key) for connecting to a sandbox.
// This is used by the CLI's `tent ssh` command.
func (m *VMManager) GetSSHArgs(name string) ([]string, error) {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return nil, fmt.Errorf("VM not found: %w", err)
	}

	if vmState.Status != models.VMStatusRunning {
		return nil, fmt.Errorf("VM %s is not running", name)
	}

	if vmState.IP == "" {
		return nil, fmt.Errorf("VM %s has no IP address assigned", name)
	}

	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
	}

	keyPath := SSHPrivateKeyPath(m.baseDir, name)
	if _, err := os.Stat(keyPath); err == nil {
		args = append(args, "-i", keyPath)
	}

	args = append(args, "root@"+vmState.IP)
	return args, nil
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
