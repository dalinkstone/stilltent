// Package vm provides cross-platform VM management operations.
// This file contains the shared VM manager implementation used on all platforms.
package vm

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/internal/boot"
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
	RenameVM(oldName, newName string) error
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
	CloneRootFS(srcName string, dstName string) (string, error)
	DestroyVMStorage(name string) error
	CreateSnapshot(name string, tag string) (string, error)
	RestoreSnapshot(name string, tag string) error
	ListSnapshots(name string) ([]*storage.SnapshotInfo, error)
	DeleteSnapshot(vmName string, tag string) error
	DeleteAllSnapshots(vmName string) (int, error)
}

// PortForwarder defines the interface for port forwarding operations
type PortForwarder interface {
	SetupForwards(vmName string, ports []models.PortForward) error
	ActivateForwards(vmName string, guestIP string) error
	RemoveForwards(vmName string) error
	AddForward(vmName string, hostPort, guestPort int, guestIP string) error
	RemoveForward(vmName string, hostPort int) error
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
	eventLogger     *EventLogger
	webhookMgr      *WebhookManager
	resourceLimiter *ResourceLimiter
	accounting      *AccountingManager
	baseDir        string
	execCommand    func(cmd string, args ...string) *exec.Cmd
	mu             sync.Mutex               // Protects runningVMs
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

	acctMgr, _ := NewAccountingManager(baseDir)

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
		eventLogger:     NewEventLogger(baseDir),
		webhookMgr:      NewWebhookManager(baseDir),
		resourceLimiter: NewResourceLimiter(baseDir),
		accounting:      acctMgr,
		baseDir:        baseDir,
		execCommand:    exec.Command,
		runningVMs:     make(map[string]hypervisor.VM),
	}, nil
}

// Setup initializes the VM manager components
func (m *VMManager) Setup() error {
	return nil // Components are initialized in NewManager
}

// logEvent records a lifecycle event (best-effort, errors are ignored)
func (m *VMManager) logEvent(eventType EventType, sandbox string, details map[string]string) {
	if m.eventLogger != nil {
		_ = m.eventLogger.Log(eventType, sandbox, details)
	}
	if m.webhookMgr != nil {
		m.webhookMgr.Deliver(Event{
			Timestamp: time.Now().UTC(),
			Type:      eventType,
			Sandbox:   sandbox,
			Details:   details,
		})
	}
}

// EventLogger returns the event logger for external use
func (m *VMManager) EventLog() *EventLogger {
	return m.eventLogger
}

// WebhookManager returns the webhook manager for external use.
func (m *VMManager) WebhookMgr() *WebhookManager {
	return m.webhookMgr
}

// GetResourceLimits returns the applied resource limits for a sandbox.
func (m *VMManager) GetResourceLimits(name string) (*AppliedLimits, error) {
	return m.resourceLimiter.GetLimits(name)
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
		Labels:      config.Labels,
		CreatedAt:   time.Now().Unix(),
		UpdatedAt:   time.Now().Unix(),
	}

	// Save state
	if err := m.stateManager.StoreVM(vmState); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}

	m.logEvent(EventCreate, name, map[string]string{
		"image": config.From,
		"vcpus": fmt.Sprintf("%d", config.VCPUs),
		"memory": fmt.Sprintf("%dMB", config.MemoryMB),
	})

	return nil
}

// Clone creates a new sandbox by cloning an existing one's rootfs and config.
// The source sandbox must be stopped. The new sandbox inherits the source's
// configuration but gets its own rootfs copy, network, and SSH keys.
func (m *VMManager) Clone(srcName, dstName string) error {
	// Verify source exists and is stopped
	srcState, err := m.stateManager.GetVM(srcName)
	if err != nil {
		return fmt.Errorf("source VM not found: %w", err)
	}
	if srcState.Status == models.VMStatusRunning {
		return fmt.Errorf("cannot clone running VM %q — stop it first", srcName)
	}

	// Verify destination doesn't already exist
	if _, err := m.stateManager.GetVM(dstName); err == nil {
		return fmt.Errorf("VM %q already exists", dstName)
	}

	// Load source config
	srcConfig, err := m.LoadConfig(srcName)
	if err != nil {
		return fmt.Errorf("failed to load source config: %w", err)
	}

	// Clone the rootfs
	rootfsPath, err := m.storageMgr.CloneRootFS(srcName, dstName)
	if err != nil {
		return fmt.Errorf("failed to clone rootfs: %w", err)
	}

	// Setup network for the new sandbox
	dstConfig := *srcConfig
	dstConfig.Name = dstName
	dstConfig.RootFS = rootfsPath

	tapDevice, err := m.networkMgr.SetupVMNetwork(dstName, &dstConfig)
	if err != nil {
		// Cleanup rootfs on failure
		m.storageMgr.DestroyVMStorage(dstName)
		return fmt.Errorf("failed to setup network: %w", err)
	}

	// Setup port forwarding if configured
	if len(dstConfig.Network.Ports) > 0 {
		if err := m.portForwarder.SetupForwards(dstName, dstConfig.Network.Ports); err != nil {
			m.networkMgr.CleanupVMNetwork(dstName)
			m.storageMgr.DestroyVMStorage(dstName)
			return fmt.Errorf("failed to setup port forwarding: %w", err)
		}
	}

	// Clone network policy from source
	if srcPolicy, err := m.policyMgr.GetPolicy(srcName); err == nil {
		policy, err := m.policyMgr.SetPolicy(dstName, srcPolicy.Allowed, srcPolicy.Denied)
		if err == nil {
			m.policyMgr.SavePolicy(policy)
		}
	} else {
		policy, _ := m.policyMgr.EnsureDefaultPolicy(dstName)
		if policy != nil {
			m.policyMgr.SavePolicy(policy)
		}
	}

	// Generate fresh SSH keys for the clone
	keyPair, err := GenerateSSHKeys(m.baseDir, dstName)
	if err != nil {
		m.networkMgr.CleanupVMNetwork(dstName)
		m.storageMgr.DestroyVMStorage(dstName)
		return fmt.Errorf("failed to generate SSH keys: %w", err)
	}

	// Clone mounts if configured
	if len(dstConfig.Mounts) > 0 {
		shares, err := m.mountMgr.PrepareMounts(dstName, dstConfig.Mounts)
		if err == nil {
			m.mountMgr.SaveMounts(dstName, shares)
		}
	}

	// Save config for the new sandbox
	if err := m.saveConfig(&dstConfig); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Create state for the clone
	vmState := &models.VMState{
		Name:       dstName,
		Status:     models.VMStatusCreated,
		RootFSPath: rootfsPath,
		TAPDevice:  tapDevice,
		SSHKeyPath: keyPair.PrivateKeyPath,
		ImageRef:   srcState.ImageRef,
		VCPUs:      dstConfig.VCPUs,
		MemoryMB:   dstConfig.MemoryMB,
		DiskGB:     dstConfig.DiskGB,
		CreatedAt:  time.Now().Unix(),
		UpdatedAt:  time.Now().Unix(),
	}

	if err := m.stateManager.StoreVM(vmState); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}

	m.logEvent(EventClone, dstName, map[string]string{
		"source": srcName,
		"image":  srcState.ImageRef,
	})

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
	if err := os.WriteFile(configPath, data, 0600); err != nil {
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

	// Resolve kernel path — "default" or empty means use the kernel store's default.
	// If no kernel is installed, auto-download one.
	if config.Kernel == "" || config.Kernel == "default" {
		kernelStore, err := boot.NewKernelStore(m.baseDir)
		if err != nil {
			return fmt.Errorf("failed to open kernel store: %w", err)
		}
		entry, err := kernelStore.GetDefault()
		if err != nil {
			// No kernel available — auto-download one
			fmt.Println("No kernel installed. Downloading Linux kernel for this platform...")
			entry, err = m.autoProvisionKernel(kernelStore)
			if err != nil {
				return fmt.Errorf("failed to auto-provision kernel: %w\n\nYou can also add one manually:\n  tent kernel add /path/to/vmlinuz", err)
			}
		}
		config.Kernel = entry.Path
		if config.Initrd == "" && entry.InitrdPath != "" {
			config.Initrd = entry.InitrdPath
		}
	}

	// Run pre-start lifecycle hooks
	if config.Hooks != nil {
		if _, err := m.RunHooks(name, config.Hooks, HookPreStart); err != nil {
			return fmt.Errorf("pre-start hook failed: %w", err)
		}
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
	m.mu.Lock()
	m.runningVMs[name] = vm
	m.mu.Unlock()

	// Apply resource limits
	if config.Resources != nil {
		applied, err := m.resourceLimiter.ApplyLimits(name, config)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to apply resource limits for %s: %v\n", name, err)
		} else if applied != nil && vmState.PID > 0 {
			// Assign VM process to the cgroup
			if err := m.resourceLimiter.AssignProcess(name, vmState.PID); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to assign process to cgroup for %s: %v\n", name, err)
			}
		}
	}

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
		m.mu.Lock()
		delete(m.runningVMs, name)
		m.mu.Unlock()
		return fmt.Errorf("failed to update state: %w", err)
	}

	m.logEvent(EventStart, name, nil)

	// Record start in accounting
	if m.accounting != nil {
		_ = m.accounting.RecordStart(name, config.VCPUs, config.MemoryMB)
	}

	// Run post-start lifecycle hooks (non-fatal)
	if config.Hooks != nil {
		if _, err := m.RunHooks(name, config.Hooks, HookPostStart); err != nil {
			fmt.Fprintf(os.Stderr, "warning: post-start hook failed for %s: %v\n", name, err)
		}
	}

	return nil
}

// Stop gracefully shuts down a running microVM
func (m *VMManager) Stop(name string) error {
	if err := m.checkLock(name); err != nil {
		return err
	}

	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	if vmState.Status != models.VMStatusRunning {
		return fmt.Errorf("VM %s is not running", name)
	}

	// Load config to check for lifecycle hooks
	config, _ := m.loadConfigFromState(vmState)

	// Run pre-stop lifecycle hooks
	if config != nil && config.Hooks != nil {
		if _, err := m.RunHooks(name, config.Hooks, HookPreStop); err != nil {
			fmt.Fprintf(os.Stderr, "warning: pre-stop hook failed for %s: %v\n", name, err)
		}
	}

	// Get running VM instance
	m.mu.Lock()
	vm, ok := m.runningVMs[name]
	m.mu.Unlock()
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

	// Remove resource limits
	if err := m.resourceLimiter.RemoveLimits(name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to remove resource limits for %s: %v\n", name, err)
	}

	// Cleanup network resources
	if vmState.TAPDevice != "" {
		if err := m.networkMgr.CleanupVMNetwork(name); err != nil {
			return fmt.Errorf("failed to cleanup network: %w", err)
		}
	}

	// Remove from running VMs
	m.mu.Lock()
	delete(m.runningVMs, name)
	m.mu.Unlock()

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

	m.logEvent(EventStop, name, nil)

	// Record stop in accounting
	if m.accounting != nil {
		_ = m.accounting.RecordStop(name)
	}

	// Run post-stop lifecycle hooks (non-fatal)
	if config != nil && config.Hooks != nil {
		if _, err := m.RunHooks(name, config.Hooks, HookPostStop); err != nil {
			fmt.Fprintf(os.Stderr, "warning: post-stop hook failed for %s: %v\n", name, err)
		}
	}

	return nil
}

// Accounting returns the accounting manager for external use.
func (m *VMManager) Accounting() *AccountingManager {
	return m.accounting
}

// Pause freezes a running sandbox's vCPUs without tearing it down.
// The sandbox retains its memory, network, and disk state.
func (m *VMManager) Pause(name string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	if vmState.Status != models.VMStatusRunning && vmState.Status != models.VMStatusPaused {
		return fmt.Errorf("VM %s is not running (status: %s)", name, vmState.Status)
	}

	m.mu.Lock()
	vm, ok := m.runningVMs[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("VM %s not found in running VMs", name)
	}

	if err := vm.Pause(); err != nil {
		return fmt.Errorf("failed to pause VM: %w", err)
	}

	vmState.Status = models.VMStatusPaused
	vmState.UpdatedAt = time.Now().Unix()

	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.Status = vmState.Status
		s.UpdatedAt = vmState.UpdatedAt
		return nil
	}); err != nil {
		// Best-effort rollback
		vm.Unpause()
		return fmt.Errorf("failed to update state: %w", err)
	}

	m.logEvent(EventPause, name, nil)
	return nil
}

// Unpause resumes a paused sandbox's vCPU execution.
func (m *VMManager) Unpause(name string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	if vmState.Status != models.VMStatusPaused {
		return fmt.Errorf("VM %s is not paused (status: %s)", name, vmState.Status)
	}

	m.mu.Lock()
	vm, ok := m.runningVMs[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("VM %s not found in running VMs", name)
	}

	if err := vm.Unpause(); err != nil {
		return fmt.Errorf("failed to unpause VM: %w", err)
	}

	vmState.Status = models.VMStatusRunning
	vmState.UpdatedAt = time.Now().Unix()

	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.Status = vmState.Status
		s.UpdatedAt = vmState.UpdatedAt
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	m.logEvent(EventUnpause, name, nil)
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

	m.logEvent(EventRestart, name, nil)

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
	if err := m.checkLock(name); err != nil {
		return err
	}

	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	// Stop if running — Stop() handles its own network/port cleanup
	wasStopped := false
	if vmState.Status == models.VMStatusRunning {
		if err := m.Stop(name); err != nil {
			return fmt.Errorf("failed to stop VM before destroy: %w", err)
		}
		wasStopped = true
	}

	// Remove port forwarding rules (for created/never-started VMs;
	// Stop() already handles this for running VMs)
	if !wasStopped {
		m.portForwarder.RemoveForwards(name)
	}

	// Cleanup network resources (for created/never-started VMs;
	// Stop() already handles this for running VMs)
	if !wasStopped && vmState.TAPDevice != "" {
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

	m.logEvent(EventDestroy, name, nil)

	return nil
}

// RenameVM renames a sandbox. The sandbox must be stopped.
func (m *VMManager) RenameVM(oldName, newName string) error {
	// Validate the sandbox exists and is stopped
	vmState, err := m.stateManager.GetVM(oldName)
	if err != nil {
		return fmt.Errorf("sandbox %q not found: %w", oldName, err)
	}

	if vmState.Status == models.VMStatusRunning {
		return fmt.Errorf("cannot rename running sandbox %q — stop it first", oldName)
	}

	// Check that the new name doesn't already exist
	if _, err := m.stateManager.GetVM(newName); err == nil {
		return fmt.Errorf("sandbox %q already exists", newName)
	}

	// Rename config file if it exists
	oldConfigPath := filepath.Join(m.baseDir, "configs", oldName+".yaml")
	newConfigPath := filepath.Join(m.baseDir, "configs", newName+".yaml")
	if _, err := os.Stat(oldConfigPath); err == nil {
		// Read and update the config name field
		data, err := os.ReadFile(oldConfigPath)
		if err == nil {
			var config models.VMConfig
			if err := yaml.Unmarshal(data, &config); err == nil {
				config.Name = newName
				if newData, err := yaml.Marshal(&config); err == nil {
					_ = os.WriteFile(newConfigPath, newData, 0600)
					_ = os.Remove(oldConfigPath)
				}
			}
		}
	}

	// Rename console log file if it exists
	if m.consoleMgr != nil {
		oldLogPath := filepath.Join(m.baseDir, "logs", oldName+".log")
		newLogPath := filepath.Join(m.baseDir, "logs", newName+".log")
		if _, err := os.Stat(oldLogPath); err == nil {
			_ = os.Rename(oldLogPath, newLogPath)
		}
	}

	// Rename mount state if it exists
	if m.mountMgr != nil {
		oldMountPath := filepath.Join(m.baseDir, "mounts", oldName+".json")
		newMountPath := filepath.Join(m.baseDir, "mounts", newName+".json")
		if _, err := os.Stat(oldMountPath); err == nil {
			_ = os.Rename(oldMountPath, newMountPath)
		}
	}

	// Rename in state store (this updates the name and persists)
	if err := m.stateManager.RenameVM(oldName, newName); err != nil {
		return fmt.Errorf("failed to rename sandbox state: %w", err)
	}

	m.logEvent(EventRename, newName, map[string]string{"from": oldName})

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
		m.mu.Lock()
		vm, ok := m.runningVMs[name]
		m.mu.Unlock()
		if ok {
			status, _ := vm.Status()
			if status == models.VMStatusStopped {
				vmState.Status = models.VMStatusStopped
				vmState.IP = ""
				vmState.UpdatedAt = time.Now().Unix()
				m.mu.Lock()
				delete(m.runningVMs, name)
				m.mu.Unlock()
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

	path, err := m.storageMgr.CreateSnapshot(name, tag)
	if err != nil {
		return "", err
	}
	m.logEvent(EventSnapshotCreate, name, map[string]string{"tag": tag})
	return path, nil
}

// RestoreSnapshot restores a VM's rootfs from a snapshot
func (m *VMManager) RestoreSnapshot(name string, tag string) error {
	// Check if VM exists
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	if err := m.storageMgr.RestoreSnapshot(name, tag); err != nil {
		return err
	}
	m.logEvent(EventSnapshotRestore, name, map[string]string{"tag": tag})
	return nil
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

// DeleteSnapshot deletes a specific snapshot of a VM's rootfs
func (m *VMManager) DeleteSnapshot(name string, tag string) error {
	// Check if VM exists
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	return m.storageMgr.DeleteSnapshot(name, tag)
}

// DeleteAllSnapshots deletes all snapshots for a VM
func (m *VMManager) DeleteAllSnapshots(name string) (int, error) {
	// Check if VM exists
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return 0, fmt.Errorf("VM not found: %w", err)
	}

	return m.storageMgr.DeleteAllSnapshots(name)
}

// CreateCheckpoint saves a full VM checkpoint (memory + CPU + disk state).
func (m *VMManager) CreateCheckpoint(name, tag, description string, includeDisk bool) (*models.CheckpointInfo, error) {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return nil, fmt.Errorf("VM not found: %w", err)
	}

	config, err := m.LoadConfig(name)
	if err != nil {
		// Fallback to state-derived config
		config = &models.VMConfig{
			Name:     vmState.Name,
			VCPUs:    vmState.VCPUs,
			MemoryMB: vmState.MemoryMB,
			DiskGB:   vmState.DiskGB,
		}
	}

	cpMgr := NewCheckpointManager(m.baseDir)
	info, err := cpMgr.CreateCheckpoint(name, tag, description, includeDisk, vmState, config)
	if err != nil {
		return nil, err
	}

	m.logEvent(EventCheckpointCreate, name, map[string]string{
		"tag":          tag,
		"disk_included": fmt.Sprintf("%v", includeDisk),
	})

	return info, nil
}

// RestoreCheckpoint restores a VM from a full checkpoint.
func (m *VMManager) RestoreCheckpoint(name, tag string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	if vmState.Status == models.VMStatusRunning {
		return fmt.Errorf("VM %s must be stopped before restoring a checkpoint", name)
	}

	cpMgr := NewCheckpointManager(m.baseDir)
	meta, err := cpMgr.RestoreCheckpoint(name, tag)
	if err != nil {
		return err
	}

	// If the checkpoint includes a disk image, restore it
	if meta.DiskIncluded {
		diskPath, err := cpMgr.GetCheckpointDiskPath(name, tag)
		if err != nil {
			return fmt.Errorf("failed to get checkpoint disk: %w", err)
		}

		if vmState.RootFSPath != "" {
			srcFile, err := os.Open(diskPath)
			if err != nil {
				return fmt.Errorf("failed to open checkpoint disk: %w", err)
			}
			defer srcFile.Close()

			dstFile, err := os.Create(vmState.RootFSPath)
			if err != nil {
				return fmt.Errorf("failed to open VM disk for restore: %w", err)
			}
			defer dstFile.Close()

			if _, err := io.Copy(dstFile, srcFile); err != nil {
				return fmt.Errorf("failed to restore disk image: %w", err)
			}
		}
	}

	// Update VM config if checkpoint has config
	if meta.VMConfig != nil {
		_ = m.UpdateConfig(name, meta.VMConfig)
	}

	m.logEvent(EventCheckpointRestore, name, map[string]string{
		"tag": tag,
	})

	return nil
}

// ListCheckpoints returns all checkpoints for a VM.
func (m *VMManager) ListCheckpoints(name string) ([]*models.CheckpointInfo, error) {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return nil, fmt.Errorf("VM not found: %w", err)
	}

	cpMgr := NewCheckpointManager(m.baseDir)
	return cpMgr.ListCheckpoints(name)
}

// DeleteCheckpoint removes a specific checkpoint.
func (m *VMManager) DeleteCheckpoint(name, tag string) error {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	cpMgr := NewCheckpointManager(m.baseDir)
	if err := cpMgr.DeleteCheckpoint(name, tag); err != nil {
		return err
	}

	m.logEvent(EventCheckpointDelete, name, map[string]string{
		"tag": tag,
	})

	return nil
}

// DeleteAllCheckpoints removes all checkpoints for a VM.
func (m *VMManager) DeleteAllCheckpoints(name string) (int, error) {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return 0, fmt.Errorf("VM not found: %w", err)
	}

	cpMgr := NewCheckpointManager(m.baseDir)
	return cpMgr.DeleteAllCheckpoints(name)
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
	m.mu.Lock()
	vm, ok := m.runningVMs[name]
	m.mu.Unlock()
	if ok {
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

// AddPortForward adds a single port forwarding rule to a running sandbox
func (m *VMManager) AddPortForward(name string, hostPort, guestPort int) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}
	if vmState.Status != models.VMStatusRunning {
		return fmt.Errorf("VM %s is not running (status: %s)", name, vmState.Status)
	}
	return m.portForwarder.AddForward(name, hostPort, guestPort, vmState.IP)
}

// RemovePortForward removes a single port forwarding rule from a sandbox
func (m *VMManager) RemovePortForward(name string, hostPort int) error {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}
	return m.portForwarder.RemoveForward(name, hostPort)
}

// ExecInVM runs a single command string inside a sandbox and returns the output.
// Used by health checks to execute commands inside the guest.
func (m *VMManager) ExecInVM(name string, command string, timeoutSec int) (string, error) {
	output, exitCode, err := m.Exec(name, []string{"sh", "-c", command})
	if err != nil {
		return output, err
	}
	if exitCode != 0 {
		return output, fmt.Errorf("command exited with code %d", exitCode)
	}
	return output, nil
}

// UpdateHealth persists the health state for a sandbox.
func (m *VMManager) UpdateHealth(name string, health *models.HealthState) error {
	return m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.Health = health
		s.UpdatedAt = time.Now().Unix()
		return nil
	})
}

// LoadConfig loads the persisted VMConfig for a sandbox.
func (m *VMManager) LoadConfig(name string) (*models.VMConfig, error) {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return nil, fmt.Errorf("VM not found: %w", err)
	}
	return m.loadConfigFromState(vmState)
}

// UpdateConfig updates the persisted VMConfig and state for a stopped sandbox.
func (m *VMManager) UpdateConfig(name string, config *models.VMConfig) error {
	if err := m.checkLock(name); err != nil {
		return err
	}

	// Save the updated config to disk
	config.Name = name
	if err := m.saveConfig(config); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Update state to reflect resource changes
	if err := m.stateManager.UpdateVM(name, func(vmState *models.VMState) error {
		vmState.VCPUs = config.VCPUs
		vmState.MemoryMB = config.MemoryMB
		vmState.DiskGB = config.DiskGB
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	return nil
}

// CopyToGuest copies a file or directory from the host to a running sandbox using SCP.
func (m *VMManager) CopyToGuest(name, hostPath, guestPath string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}
	if vmState.Status != models.VMStatusRunning {
		return fmt.Errorf("VM %s is not running", name)
	}
	ip := vmState.IP
	m.mu.Lock()
	vm, ok := m.runningVMs[name]
	m.mu.Unlock()
	if ok {
		if vmIP := vm.GetIP(); vmIP != "" {
			ip = vmIP
		}
	}
	if ip == "" {
		return fmt.Errorf("VM %s has no IP address", name)
	}

	info, err := os.Stat(hostPath)
	if err != nil {
		return fmt.Errorf("host path not found: %w", err)
	}

	args := m.scpBaseArgs(name)
	if info.IsDir() {
		args = append(args, "-r")
	}
	args = append(args, hostPath, fmt.Sprintf("root@%s:%s", ip, guestPath))

	cmd := m.execCommand("scp", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp to guest failed: %s: %w", string(output), err)
	}
	return nil
}

// CopyFromGuest copies a file or directory from a running sandbox to the host using SCP.
func (m *VMManager) CopyFromGuest(name, guestPath, hostPath string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}
	if vmState.Status != models.VMStatusRunning {
		return fmt.Errorf("VM %s is not running", name)
	}
	ip := vmState.IP
	m.mu.Lock()
	vm, ok := m.runningVMs[name]
	m.mu.Unlock()
	if ok {
		if vmIP := vm.GetIP(); vmIP != "" {
			ip = vmIP
		}
	}
	if ip == "" {
		return fmt.Errorf("VM %s has no IP address", name)
	}

	args := m.scpBaseArgs(name)
	// Always use -r for directories; SCP handles files fine with -r too
	args = append(args, "-r")
	args = append(args, fmt.Sprintf("root@%s:%s", ip, guestPath), hostPath)

	cmd := m.execCommand("scp", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp from guest failed: %s: %w", string(output), err)
	}
	return nil
}

// scpBaseArgs returns the common SCP arguments for a sandbox, including the identity key.
func (m *VMManager) scpBaseArgs(vmName string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
	}
	keyPath := SSHPrivateKeyPath(m.baseDir, vmName)
	if _, err := os.Stat(keyPath); err == nil {
		args = append(args, "-i", keyPath)
	}
	return args
}

// GetStats returns resource statistics for a specific sandbox
func (m *VMManager) GetStats(name string) (*models.ResourceStats, error) {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return nil, fmt.Errorf("sandbox '%s' not found: %w", name, err)
	}

	stats := &models.ResourceStats{
		Name:     vmState.Name,
		Status:   string(vmState.Status),
		VCPUs:    vmState.VCPUs,
		MemoryMB: vmState.MemoryMB,
		DiskGB:   vmState.DiskGB,
		IP:       vmState.IP,
		ImageRef: vmState.ImageRef,
		PID:      vmState.PID,
	}

	// Calculate uptime for running sandboxes
	if vmState.Status == models.VMStatusRunning && vmState.UpdatedAt > 0 {
		stats.UptimeSeconds = time.Now().Unix() - vmState.UpdatedAt
	}

	// Get disk usage from rootfs
	if vmState.RootFSPath != "" {
		if fi, err := os.Stat(vmState.RootFSPath); err == nil {
			stats.RootFSSizeMB = fi.Size() / (1024 * 1024)
		}
	}

	// Calculate total disk used in the sandbox directory
	sandboxDir := filepath.Join(m.baseDir, "rootfs", name)
	stats.DiskUsedMB = dirSizeMB(sandboxDir)

	// Count snapshots
	snapshotDir := filepath.Join(m.baseDir, "snapshots", name)
	if entries, err := os.ReadDir(snapshotDir); err == nil {
		stats.SnapshotCount = len(entries)
	}

	return stats, nil
}

// GetAllStats returns resource statistics for all sandboxes
func (m *VMManager) GetAllStats() ([]*models.ResourceStats, error) {
	vms, err := m.stateManager.ListVMs()
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}

	var allStats []*models.ResourceStats
	for _, vmState := range vms {
		stats, err := m.GetStats(vmState.Name)
		if err != nil {
			continue
		}
		allStats = append(allStats, stats)
	}
	return allStats, nil
}

// dirSizeMB returns total size of files in a directory in MB
func dirSizeMB(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total / (1024 * 1024)
}

// ExportArchive represents the metadata stored in an export archive
type ExportArchive struct {
	Version   int              `json:"version"`
	Name      string           `json:"name"`
	State     *models.VMState  `json:"state"`
	Config    *models.VMConfig `json:"config,omitempty"`
	ExportedAt int64           `json:"exported_at"`
}

// Export exports a stopped sandbox to a tar.gz archive at the given output path.
// The archive contains the sandbox config, state metadata, and rootfs.
func (m *VMManager) Export(name string, outputPath string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("sandbox not found: %w", err)
	}

	if vmState.Status == models.VMStatusRunning {
		return fmt.Errorf("cannot export running sandbox %q — stop it first", name)
	}

	// Load config if available
	config, _ := m.loadConfigFromState(vmState)

	archive := &ExportArchive{
		Version:    1,
		Name:       name,
		State:      vmState,
		Config:     config,
		ExportedAt: time.Now().Unix(),
	}

	metaJSON, err := json.Marshal(archive)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	// Write metadata
	if err := writeTarEntry(tw, "metadata.json", metaJSON); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	// Write config YAML if present
	configPath := filepath.Join(m.baseDir, "configs", fmt.Sprintf("%s.yaml", name))
	if data, err := os.ReadFile(configPath); err == nil {
		if err := writeTarEntry(tw, "config.yaml", data); err != nil {
			return fmt.Errorf("failed to write config: %w", err)
		}
	}

	// Write rootfs if it exists
	if vmState.RootFSPath != "" {
		if info, err := os.Stat(vmState.RootFSPath); err == nil {
			if err := writeTarFile(tw, "rootfs.img", vmState.RootFSPath, info.Size()); err != nil {
				return fmt.Errorf("failed to write rootfs: %w", err)
			}
		}
	}

	return nil
}

// Import imports a sandbox from a tar.gz archive. If newName is non-empty it overrides the original name.
func (m *VMManager) Import(archivePath string, newName string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to read gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	var archive ExportArchive
	var configData []byte
	var rootfsData []byte

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read archive entry: %w", err)
		}

		switch hdr.Name {
		case "metadata.json":
			data, err := io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("failed to read metadata: %w", err)
			}
			if err := json.Unmarshal(data, &archive); err != nil {
				return fmt.Errorf("failed to parse metadata: %w", err)
			}
		case "config.yaml":
			configData, err = io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("failed to read config: %w", err)
			}
		case "rootfs.img":
			rootfsData, err = io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("failed to read rootfs: %w", err)
			}
		}
	}

	if archive.Version == 0 {
		return fmt.Errorf("invalid archive: missing metadata")
	}

	name := archive.Name
	if newName != "" {
		name = newName
	}

	// Check if sandbox already exists
	if _, err := m.stateManager.GetVM(name); err == nil {
		return fmt.Errorf("sandbox %q already exists", name)
	}

	// Restore config file
	if configData != nil {
		configDir := filepath.Join(m.baseDir, "configs")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return fmt.Errorf("failed to create config dir: %w", err)
		}

		// Update name in config if renamed
		if newName != "" {
			var cfg models.VMConfig
			if err := yaml.Unmarshal(configData, &cfg); err == nil {
				cfg.Name = newName
				if updated, err := yaml.Marshal(&cfg); err == nil {
					configData = updated
				}
			}
		}

		if err := os.WriteFile(filepath.Join(configDir, name+".yaml"), configData, 0600); err != nil {
			return fmt.Errorf("failed to write config: %w", err)
		}
	}

	// Restore rootfs
	var rootfsPath string
	if rootfsData != nil {
		rootfsDir := filepath.Join(m.baseDir, "vms", name)
		if err := os.MkdirAll(rootfsDir, 0755); err != nil {
			return fmt.Errorf("failed to create VM directory: %w", err)
		}
		rootfsPath = filepath.Join(rootfsDir, "rootfs.img")
		if err := os.WriteFile(rootfsPath, rootfsData, 0600); err != nil {
			return fmt.Errorf("failed to write rootfs: %w", err)
		}
	}

	// Restore state
	vmState := archive.State
	vmState.Name = name
	vmState.Status = models.VMStatusStopped
	vmState.PID = 0
	vmState.IP = ""
	vmState.UpdatedAt = time.Now().Unix()
	if rootfsPath != "" {
		vmState.RootFSPath = rootfsPath
	}

	if err := m.stateManager.StoreVM(vmState); err != nil {
		return fmt.Errorf("failed to store state: %w", err)
	}

	return nil
}

// Commit saves a sandbox's current rootfs as a reusable image.
// The sandbox can be running or stopped. The resulting image can be used
// with "tent create --from <image-name>" to create new sandboxes.
func (m *VMManager) Commit(name string, imageName string, message string) (string, error) {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return "", fmt.Errorf("sandbox not found: %w", err)
	}

	if vmState.RootFSPath == "" {
		return "", fmt.Errorf("sandbox %q has no rootfs", name)
	}

	if _, err := os.Stat(vmState.RootFSPath); err != nil {
		return "", fmt.Errorf("rootfs not found at %s: %w", vmState.RootFSPath, err)
	}

	// Create the images directory
	imagesDir := filepath.Join(m.baseDir, "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create images directory: %w", err)
	}

	// Destination path for the committed image
	destPath := filepath.Join(imagesDir, fmt.Sprintf("%s.img", imageName))

	// Check if image already exists
	if _, err := os.Stat(destPath); err == nil {
		return "", fmt.Errorf("image %q already exists — use a different name or remove it first", imageName)
	}

	// Copy rootfs to images directory
	srcFile, err := os.Open(vmState.RootFSPath)
	if err != nil {
		return "", fmt.Errorf("failed to open rootfs: %w", err)
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat rootfs: %w", err)
	}

	dstFile, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create image file: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("failed to copy rootfs to image: %w", err)
	}

	// Write commit metadata alongside the image
	meta := CommitMetadata{
		ImageName:   imageName,
		SourceName:  name,
		SourceImage: vmState.ImageRef,
		Message:     message,
		SizeBytes:   srcInfo.Size(),
		CreatedAt:   time.Now().Unix(),
		VCPUs:       vmState.VCPUs,
		MemoryMB:    vmState.MemoryMB,
		Labels:      vmState.Labels,
	}

	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err == nil {
		metaPath := filepath.Join(imagesDir, fmt.Sprintf("%s.meta.json", imageName))
		_ = os.WriteFile(metaPath, metaJSON, 0600)
	}

	m.logEvent(EventCommit, name, map[string]string{
		"image":   imageName,
		"message": message,
		"size_mb": fmt.Sprintf("%d", srcInfo.Size()/(1024*1024)),
	})

	return destPath, nil
}

// CommitMetadata stores metadata about a committed image
type CommitMetadata struct {
	ImageName   string            `json:"image_name"`
	SourceName  string            `json:"source_sandbox"`
	SourceImage string            `json:"source_image,omitempty"`
	Message     string            `json:"message,omitempty"`
	SizeBytes   int64             `json:"size_bytes"`
	CreatedAt   int64             `json:"created_at"`
	VCPUs       int               `json:"vcpus,omitempty"`
	MemoryMB    int               `json:"memory_mb,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

func writeTarEntry(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name: name,
		Mode: 0644,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func writeTarFile(tw *tar.Writer, name string, srcPath string, size int64) error {
	hdr := &tar.Header{
		Name: name,
		Mode: 0644,
		Size: size,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

// autoProvisionKernel downloads a Linux kernel appropriate for the current platform.
func (m *VMManager) autoProvisionKernel(kernelStore *boot.KernelStore) (*boot.KernelEntry, error) {
	kernelURL := ""
	arch := ""

	switch runtime.GOARCH {
	case "arm64":
		kernelURL = "https://cloud-images.ubuntu.com/releases/22.04/release/unpacked/ubuntu-22.04-server-cloudimg-arm64-vmlinuz-generic"
		arch = "arm64"
	case "amd64":
		kernelURL = "https://cloud-images.ubuntu.com/releases/22.04/release/unpacked/ubuntu-22.04-server-cloudimg-amd64-vmlinuz-generic"
		arch = "amd64"
	default:
		return nil, fmt.Errorf("no pre-built kernel available for architecture %s", runtime.GOARCH)
	}

	fmt.Printf("Downloading Linux kernel for %s...\n", arch)

	tmpFile := filepath.Join(m.baseDir, "kernel-download-tmp")
	defer os.Remove(tmpFile)

	resp, err := http.Get(kernelURL)
	if err != nil {
		return nil, fmt.Errorf("download kernel: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download kernel: HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}

	n, err := io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("download kernel: %w", err)
	}

	fmt.Printf("Downloaded %d MB. Adding to kernel store...\n", n/(1024*1024))

	entry, err := kernelStore.Add(tmpFile, "ubuntu-22.04-"+arch, nil)
	if err != nil {
		return nil, fmt.Errorf("add kernel to store: %w", err)
	}

	fmt.Printf("Kernel ready: %s (%s, %s)\n", entry.Version, entry.Format, entry.Arch)
	return entry, nil
}

func (m *VMManager) loadConfigFromState(vmState *models.VMState) (*models.VMConfig, error) {
	// Load config from state file if present
	configPath := filepath.Join(m.baseDir, "configs", fmt.Sprintf("%s.yaml", vmState.Name))
	if data, err := os.ReadFile(configPath); err == nil {
		var config models.VMConfig
		if err := yaml.Unmarshal(data, &config); err == nil {
			return &config, nil
		}
	}

	// Return minimal config based on state — use actual VM resource values
	vcpus := vmState.VCPUs
	if vcpus < 1 {
		vcpus = 2
	}
	memoryMB := vmState.MemoryMB
	if memoryMB < 1 {
		memoryMB = 1024
	}
	return &models.VMConfig{
		Name:     vmState.Name,
		From:     vmState.ImageRef,
		VCPUs:    vcpus,
		MemoryMB: memoryMB,
		DiskGB:   vmState.DiskGB,
		RootFS:   vmState.RootFSPath,
	}, nil
}

// FollowConsoleLogs streams console logs to the writer until done is closed.
// This is the method used by the attach command.
func (m *VMManager) FollowConsoleLogs(name string, tailLines int, out io.Writer, done <-chan struct{}) error {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	return m.consoleMgr.FollowLogs(name, tailLines, out, done)
}

// WriteToConsole writes data to a running VM's console input.
// This allows the attach command to forward stdin to the guest.
func (m *VMManager) WriteToConsole(name string, data []byte) error {
	_, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}

	// Write to the console log file so it appears in the log stream.
	// In a real hypervisor scenario, this would inject keystrokes via
	// the virtio-console rx queue. For now, we append to the log.
	logPath := m.consoleMgr.GetLogPath(name)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("failed to open console log: %w", err)
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}

// SetLabels sets labels on a sandbox, merging with existing labels.
func (m *VMManager) SetLabels(name string, labels map[string]string) error {
	if err := m.stateManager.UpdateVM(name, func(vmState *models.VMState) error {
		if vmState.Labels == nil {
			vmState.Labels = make(map[string]string)
		}
		for k, v := range labels {
			vmState.Labels[k] = v
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update labels: %w", err)
	}

	// Also update saved config
	cfg, err := m.LoadConfig(name)
	if err == nil {
		if cfg.Labels == nil {
			cfg.Labels = make(map[string]string)
		}
		for k, v := range labels {
			cfg.Labels[k] = v
		}
		_ = m.saveConfig(cfg)
	}

	return nil
}

// RemoveLabels removes labels from a sandbox by key.
func (m *VMManager) RemoveLabels(name string, keys []string) error {
	if err := m.stateManager.UpdateVM(name, func(vmState *models.VMState) error {
		for _, k := range keys {
			delete(vmState.Labels, k)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to update labels: %w", err)
	}

	cfg, err := m.LoadConfig(name)
	if err == nil {
		for _, k := range keys {
			delete(cfg.Labels, k)
		}
		_ = m.saveConfig(cfg)
	}

	return nil
}

// GetLabels returns the labels for a sandbox.
func (m *VMManager) GetLabels(name string) (map[string]string, error) {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return nil, fmt.Errorf("VM not found: %w", err)
	}
	if vmState.Labels == nil {
		return map[string]string{}, nil
	}
	return vmState.Labels, nil
}

// Lock prevents a sandbox from being stopped, destroyed, or modified
func (m *VMManager) Lock(name string, reason string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}
	if vmState.Locked {
		return fmt.Errorf("sandbox %q is already locked", name)
	}

	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.Locked = true
		s.LockedReason = reason
		s.LockedAt = time.Now().Unix()
		return nil
	}); err != nil {
		return fmt.Errorf("failed to lock sandbox: %w", err)
	}

	m.logEvent(EventType("lock"), name, map[string]string{"reason": reason})
	return nil
}

// Unlock removes the lock from a sandbox, allowing modifications again
func (m *VMManager) Unlock(name string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return fmt.Errorf("VM not found: %w", err)
	}
	if !vmState.Locked {
		return fmt.Errorf("sandbox %q is not locked", name)
	}

	if err := m.stateManager.UpdateVM(name, func(s *models.VMState) error {
		s.Locked = false
		s.LockedReason = ""
		s.LockedAt = 0
		return nil
	}); err != nil {
		return fmt.Errorf("failed to unlock sandbox: %w", err)
	}

	m.logEvent(EventType("unlock"), name, nil)
	return nil
}

// checkLock returns an error if the sandbox is locked
func (m *VMManager) checkLock(name string) error {
	vmState, err := m.stateManager.GetVM(name)
	if err != nil {
		return nil // will fail later with proper error
	}
	if vmState.Locked {
		reason := ""
		if vmState.LockedReason != "" {
			reason = fmt.Sprintf(" (reason: %s)", vmState.LockedReason)
		}
		return fmt.Errorf("sandbox %q is locked%s — use 'tent unlock %s' to unlock it first", name, reason, name)
	}
	return nil
}
