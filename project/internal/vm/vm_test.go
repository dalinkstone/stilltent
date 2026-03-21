package vm

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dalinkstone/tent/pkg/models"
	"github.com/dalinkstone/tent/internal/storage"
)

// Mock implementations for testing
type mockStateManager struct {
	vms        map[string]*models.VMState
	ErrGet     error
	ErrStore   error
	ErrUpdate  error
	ErrDelete  error
	ErrList    error
}

func (m *mockStateManager) GetVM(name string) (*models.VMState, error) {
	if m.ErrGet != nil {
		return nil, m.ErrGet
	}
	vm, exists := m.vms[name]
	if !exists {
		return nil, os.ErrNotExist
	}
	return vm, nil
}

func (m *mockStateManager) StoreVM(vm *models.VMState) error {
	if m.ErrStore != nil {
		return m.ErrStore
	}
	if m.vms == nil {
		m.vms = make(map[string]*models.VMState)
	}
	m.vms[vm.Name] = vm
	return nil
}

func (m *mockStateManager) UpdateVM(name string, updateFn func(*models.VMState) error) error {
	if m.ErrUpdate != nil {
		return m.ErrUpdate
	}
	vm, exists := m.vms[name]
	if !exists {
		return os.ErrNotExist
	}
	return updateFn(vm)
}

func (m *mockStateManager) DeleteVM(name string) error {
	if m.ErrDelete != nil {
		return m.ErrDelete
	}
	if _, exists := m.vms[name]; !exists {
		return os.ErrNotExist
	}
	delete(m.vms, name)
	return nil
}

func (m *mockStateManager) ListVMs() ([]*models.VMState, error) {
	if m.ErrList != nil {
		return nil, m.ErrList
	}
	var result []*models.VMState
	for _, vm := range m.vms {
		result = append(result, vm)
	}
	return result, nil
}

type mockFirecrackerClient struct {
	ErrConfigure error
	ErrStart     error
	ErrShutdown  error
}

func (m *mockFirecrackerClient) ConfigureVM(socketPath string, config *models.VMConfig) error {
	if m.ErrConfigure != nil {
		return m.ErrConfigure
	}
	return nil
}

func (m *mockFirecrackerClient) StartVM(socketPath string) error {
	if m.ErrStart != nil {
		return m.ErrStart
	}
	return nil
}

func (m *mockFirecrackerClient) ShutdownVM(socketPath string) error {
	if m.ErrShutdown != nil {
		return m.ErrShutdown
	}
	return nil
}

type mockNetworkManager struct {
	ErrSetup      error
	ErrCleanup    error
	TAPDevice     string
}

func (m *mockNetworkManager) SetupVMNetwork(name string, config *models.VMConfig) (string, error) {
	if m.ErrSetup != nil {
		return "", m.ErrSetup
	}
	return m.TAPDevice, nil
}

func (m *mockNetworkManager) CleanupVMNetwork(name string) error {
	if m.ErrCleanup != nil {
		return m.ErrCleanup
	}
	return nil
}

type mockStorageManager struct {
	ErrCreateRootFS    error
	ErrDestroyVM       error
	ErrCreateSnapshot  error
	ErrRestoreSnapshot error
	ErrListSnapshots   error
	SnapshotPath       string
	Snapshots          []*models.Snapshot
}

func (m *mockStorageManager) CreateRootFS(name string, config *models.VMConfig) (string, error) {
	if m.ErrCreateRootFS != nil {
		return "", m.ErrCreateRootFS
	}
	return filepath.Join("/tmp", name+".img"), nil
}

func (m *mockStorageManager) DestroyVMStorage(name string) error {
	if m.ErrDestroyVM != nil {
		return m.ErrDestroyVM
	}
	return nil
}

func (m *mockStorageManager) CreateSnapshot(name string, tag string) (string, error) {
	if m.ErrCreateSnapshot != nil {
		return "", m.ErrCreateSnapshot
	}
	return m.SnapshotPath, nil
}

func (m *mockStorageManager) RestoreSnapshot(name string, tag string) error {
	if m.ErrRestoreSnapshot != nil {
		return m.ErrRestoreSnapshot
	}
	return nil
}

func (m *mockStorageManager) ListSnapshots(name string) ([]*storage.SnapshotInfo, error) {
	if m.ErrListSnapshots != nil {
		return nil, m.ErrListSnapshots
	}
	var result []*storage.SnapshotInfo
	for _, snap := range m.Snapshots {
		result = append(result, &storage.SnapshotInfo{
			Tag:       snap.Tag,
			SizeMB:    snap.SizeMB,
			CreatedAt: snap.Timestamp,
		})
	}
	return result, nil
}

func TestNewManager(t *testing.T) {
	tmpDir := t.TempDir()

	manager, err := NewManager(tmpDir, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	if manager == nil {
		t.Fatal("manager should not be nil")
	}
	if manager.baseDir != tmpDir {
		t.Errorf("expected baseDir '%s', got '%s'", tmpDir, manager.baseDir)
	}
}

func TestNewManager_WithCustomDependencies(t *testing.T) {
	tmpDir := t.TempDir()
	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	if manager.stateManager != mockState {
		t.Error("manager should use custom state manager")
	}
	if manager.firecracker != mockFC {
		t.Error("manager should use custom firecracker client")
	}
	if manager.networkMgr != mockNet {
		t.Error("manager should use custom network manager")
	}
	if manager.storageMgr != mockStorage {
		t.Error("manager should use custom storage manager")
	}
}

func TestCreateVM(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{TAPDevice: "tap0"}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	config := &models.VMConfig{
		Name:     "test-vm",
		VCPUs:    2,
		MemoryMB: 1024,
		DiskGB:   10,
	}

	err = manager.Create("test-vm", config)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify VM was stored
	vm, err := mockState.GetVM("test-vm")
	if err != nil {
		t.Fatalf("VM should exist: %v", err)
	}
	if vm.Name != "test-vm" {
		t.Errorf("expected name 'test-vm', got '%s'", vm.Name)
	}
	if vm.Status != models.VMStatusCreated {
		t.Errorf("expected status 'created', got '%s'", vm.Status)
	}
}

func TestCreateVM_AlreadyExists(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {Name: "test-vm", Status: models.VMStatusCreated},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	config := &models.VMConfig{
		Name:     "test-vm",
		VCPUs:    2,
		MemoryMB: 1024,
		DiskGB:   10,
	}

	err = manager.Create("test-vm", config)
	if err == nil {
		t.Error("expected error when creating existing VM")
	}
}

func TestCreateVM_InvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Invalid config - empty name
	config := &models.VMConfig{
		Name:     "",
		VCPUs:    2,
		MemoryMB: 1024,
		DiskGB:   10,
	}

	err = manager.Create("test-vm", config)
	if err == nil {
		t.Error("expected error for invalid config")
	}
}

func TestCreateVM_StorageError(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{
		ErrCreateRootFS: os.ErrPermission,
	}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	config := &models.VMConfig{
		Name:     "test-vm",
		VCPUs:    2,
		MemoryMB: 1024,
		DiskGB:   10,
	}

	err = manager.Create("test-vm", config)
	if err == nil {
		t.Error("expected error from storage manager")
	}
}

func TestCreateVM_NetworkError(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{
		ErrSetup: os.ErrPermission,
	}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	config := &models.VMConfig{
		Name:     "test-vm",
		VCPUs:    2,
		MemoryMB: 1024,
		DiskGB:   10,
	}

	err = manager.Create("test-vm", config)
	if err == nil {
		t.Error("expected error from network manager")
	}
}

func TestListVMs(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"vm1": {Name: "vm1", Status: models.VMStatusCreated},
			"vm2": {Name: "vm2", Status: models.VMStatusRunning},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	vms, err := manager.List()
	if err != nil {
		t.Fatalf("failed to list VMs: %v", err)
	}

	if len(vms) != 2 {
		t.Errorf("expected 2 VMs, got %d", len(vms))
	}
}

func TestListVMs_Empty(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: make(map[string]*models.VMState),
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	vms, err := manager.List()
	if err != nil {
		t.Fatalf("failed to list VMs: %v", err)
	}

	if len(vms) != 0 {
		t.Errorf("expected 0 VMs, got %d", len(vms))
	}
}

func TestStatusVM(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {Name: "test-vm", Status: models.VMStatusRunning, PID: 1234},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	vmState, err := manager.Status("test-vm")
	if err != nil {
		t.Fatalf("failed to get status: %v", err)
	}
	if vmState.Name != "test-vm" {
		t.Errorf("expected name 'test-vm', got '%s'", vmState.Name)
	}
}

func TestStatusVM_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	_, err = manager.Status("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestLogsVM(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {Name: "test-vm", Status: models.VMStatusCreated},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Test with no log file
	logs, err := manager.Logs("test-vm")
	if err != nil {
		t.Fatalf("failed to get logs: %v", err)
	}
	if logs == "" {
		t.Error("expected non-empty logs string")
	}
}

func TestLogsVM_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	_, err = manager.Logs("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestDestroyVM(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {Name: "test-vm", Status: models.VMStatusRunning, PID: 1234},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	err = manager.Destroy("test-vm")
	if err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}

	// Verify VM was deleted from state
	_, err = mockState.GetVM("test-vm")
	if err == nil {
		t.Error("VM should be deleted from state")
	}
}

func TestDestroyVM_NotRunning(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {Name: "test-vm", Status: models.VMStatusStopped},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	err = manager.Destroy("test-vm")
	if err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}
}

func TestDestroyVM_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	err = manager.Destroy("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestStartVM(t *testing.T) {
	tmpDir := t.TempDir()

	// Setup state with created VM
	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {
				Name:       "test-vm",
				Status:     models.VMStatusCreated,
				SocketPath: filepath.Join(tmpDir, "test-vm.sock"),
			},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Mock exec.Command to avoid spawning a real process
	var spawnCount int
	manager.execCommand = func(cmd string, args ...string) *exec.Cmd {
		spawnCount++
		// Return a fake command that succeeds
		return exec.Command("true") // no-op command that succeeds
	}

	err = manager.Start("test-vm")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify VM status changed to running
	vm, err := mockState.GetVM("test-vm")
	if err != nil {
		t.Fatalf("VM should exist: %v", err)
	}
	if vm.Status != models.VMStatusRunning {
		t.Errorf("expected status 'running', got '%s'", vm.Status)
	}
	if spawnCount != 1 {
		t.Errorf("expected exec.Command to be called once, got %d times", spawnCount)
	}
}

func TestStartVM_AlreadyRunning(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {Name: "test-vm", Status: models.VMStatusRunning, PID: 1234},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	err = manager.Start("test-vm")
	if err == nil {
		t.Error("expected error when starting already running VM")
	}
}

func TestStartVM_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	err = manager.Start("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestStopVM(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {Name: "test-vm", Status: models.VMStatusRunning, PID: 1234},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	err = manager.Stop("test-vm")
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Verify VM status changed to stopped
	vm, err := mockState.GetVM("test-vm")
	if err != nil {
		t.Fatalf("VM should exist: %v", err)
	}
	if vm.Status != models.VMStatusStopped {
		t.Errorf("expected status 'stopped', got '%s'", vm.Status)
	}
}

func TestStopVM_NotRunning(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {Name: "test-vm", Status: models.VMStatusStopped},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	err = manager.Stop("test-vm")
	if err == nil {
		t.Error("expected error when stopping non-running VM")
	}
}

func TestStopVM_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	err = manager.Stop("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestCreateSnapshot(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {Name: "test-vm", Status: models.VMStatusCreated},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{
		SnapshotPath: "/tmp/snapshot.img",
	}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	snapshotPath, err := manager.CreateSnapshot("test-vm", "test-tag")
	if err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}
	if snapshotPath != "/tmp/snapshot.img" {
		t.Errorf("expected snapshot path '/tmp/snapshot.img', got '%s'", snapshotPath)
	}
}

func TestCreateSnapshot_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	_, err = manager.CreateSnapshot("nonexistent", "test-tag")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestRestoreSnapshot(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {Name: "test-vm", Status: models.VMStatusCreated},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	err = manager.RestoreSnapshot("test-vm", "test-tag")
	if err != nil {
		t.Fatalf("RestoreSnapshot failed: %v", err)
	}
}

func TestRestoreSnapshot_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	err = manager.RestoreSnapshot("nonexistent", "test-tag")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestListSnapshots(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{
		vms: map[string]*models.VMState{
			"test-vm": {Name: "test-vm", Status: models.VMStatusCreated},
		},
	}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{
		Snapshots: []*models.Snapshot{
			{Tag: "snap1", SizeMB: 100, Timestamp: "2009-02-13 23:31:30"},
			{Tag: "snap2", SizeMB: 150, Timestamp: "2009-02-13 23:31:40"},
		},
	}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	snapshots, err := manager.ListSnapshots("test-vm")
	if err != nil {
		t.Fatalf("ListSnapshots failed: %v", err)
	}
	if len(snapshots) != 2 {
		t.Errorf("expected 2 snapshots, got %d", len(snapshots))
	}
	if snapshots[0].Tag != "snap1" {
		t.Errorf("expected tag 'snap1', got '%s'", snapshots[0].Tag)
	}
}

func TestListSnapshots_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	mockState := &mockStateManager{}
	mockFC := &mockFirecrackerClient{}
	mockNet := &mockNetworkManager{}
	mockStorage := &mockStorageManager{}

	manager, err := NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	_, err = manager.ListSnapshots("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestLoadConfigFromState(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a config file
	configDir := filepath.Join(tmpDir, "configs")
	os.MkdirAll(configDir, 0755)

	configPath := filepath.Join(configDir, "test-vm.yaml")
	configContent := `name: test-vm
vcpus: 4
memory_mb: 2048
`
	os.WriteFile(configPath, []byte(configContent), 0644)

	manager, err := NewManager(tmpDir, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	vmState := &models.VMState{
		Name: "test-vm",
	}

	config, err := manager.loadConfigFromState(vmState)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if config == nil {
		t.Fatal("config should not be nil")
	}
	if config.Name != "test-vm" {
		t.Errorf("expected name 'test-vm', got '%s'", config.Name)
	}
	if config.VCPUs != 4 {
		t.Errorf("expected vcpus 4, got %d", config.VCPUs)
	}
}

func TestLoadConfigFromState_DefaultValues(t *testing.T) {
	tmpDir := t.TempDir()

	// No config file exists
	configDir := filepath.Join(tmpDir, "configs")
	os.MkdirAll(configDir, 0755)

	manager, err := NewManager(tmpDir, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	vmState := &models.VMState{
		Name: "test-vm",
	}

	config, err := manager.loadConfigFromState(vmState)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if config == nil {
		t.Fatal("config should not be nil")
	}
	if config.Name != "test-vm" {
		t.Errorf("expected name 'test-vm', got '%s'", config.Name)
	}
	if config.VCPUs != 2 {
		t.Errorf("expected default vcpus 2, got %d", config.VCPUs)
	}
	if config.MemoryMB != 1024 {
		t.Errorf("expected default memory 1024, got %d", config.MemoryMB)
	}
}
