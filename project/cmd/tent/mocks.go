//go:build integration

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dalinkstone/tent/internal/hypervisor"
	"github.com/dalinkstone/tent/internal/storage"
	"github.com/dalinkstone/tent/pkg/models"
)

// Mock implementations for testing CLI e2e tests

type MockStateManager struct {
	VMs  map[string]*models.VMState
	Err  error
}

func (m *MockStateManager) GetVM(name string) (*models.VMState, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	vm, exists := m.VMs[name]
	if !exists {
		return nil, os.ErrNotExist
	}
	return vm, nil
}

func (m *MockStateManager) StoreVM(vm *models.VMState) error {
	if m.Err != nil {
		return m.Err
	}
	if m.VMs == nil {
		m.VMs = make(map[string]*models.VMState)
	}
	m.VMs[vm.Name] = vm
	return nil
}

func (m *MockStateManager) UpdateVM(name string, updateFn func(*models.VMState) error) error {
	if m.Err != nil {
		return m.Err
	}
	v, exists := m.VMs[name]
	if !exists {
		return os.ErrNotExist
	}
	return updateFn(v)
}

func (m *MockStateManager) DeleteVM(name string) error {
	if m.Err != nil {
		return m.Err
	}
	if _, exists := m.VMs[name]; !exists {
		return os.ErrNotExist
	}
	delete(m.VMs, name)
	return nil
}

func (m *MockStateManager) ListVMs() ([]*models.VMState, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	var result []*models.VMState
	for _, vm := range m.VMs {
		result = append(result, vm)
	}
	return result, nil
}

// MockVMInstance implements hypervisor.VM for testing
type MockVMInstance struct {
	config  *models.VMConfig
	running bool
	errStop error
}

func (v *MockVMInstance) Start() error {
	if v.running {
		return fmt.Errorf("VM already running")
	}
	v.running = true
	return nil
}

func (v *MockVMInstance) Stop() error {
	if v.errStop != nil {
		return v.errStop
	}
	if !v.running {
		return fmt.Errorf("VM not running")
	}
	v.running = false
	return nil
}

func (v *MockVMInstance) Kill() error {
	v.running = false
	return nil
}

func (v *MockVMInstance) Status() (models.VMStatus, error) {
	if v.running {
		return models.VMStatusRunning, nil
	}
	return models.VMStatusStopped, nil
}

func (v *MockVMInstance) GetConfig() *models.VMConfig {
	return v.config
}

func (v *MockVMInstance) GetIP() string {
	return "172.16.0.1"
}

func (v *MockVMInstance) GetPID() int {
	return 0
}

func (v *MockVMInstance) SetIP(ip string) {
}

func (v *MockVMInstance) SetNetwork(tapDevice string, ip string) {
}

func (v *MockVMInstance) SetConsoleOutput(w io.Writer) {
}

func (v *MockVMInstance) Cleanup() error {
	return nil
}

// MockHypervisorBackend implements vm.HypervisorBackend for testing
type MockHypervisorBackend struct {
	ErrCreate error
	ErrList   error
	ErrDestroy error
	CreatedVM *MockVMInstance
}

func (m *MockHypervisorBackend) CreateVM(config *models.VMConfig) (hypervisor.VM, error) {
	if m.ErrCreate != nil {
		return nil, m.ErrCreate
	}
	vm := &MockVMInstance{config: config}
	m.CreatedVM = vm
	return vm, nil
}

func (m *MockHypervisorBackend) ListVMs() ([]hypervisor.VM, error) {
	if m.ErrList != nil {
		return nil, m.ErrList
	}
	var vms []hypervisor.VM
	if m.CreatedVM != nil {
		vms = append(vms, m.CreatedVM)
	}
	return vms, nil
}

func (m *MockHypervisorBackend) DestroyVM(vm hypervisor.VM) error {
	if m.ErrDestroy != nil {
		return m.ErrDestroy
	}
	m.CreatedVM = nil
	return nil
}

type MockNetworkManager struct {
	ErrSetup      error
	ErrCleanup    error
	TAPDevice     string
	SetupCalled   bool
	CleanupCalled bool
}

func (m *MockNetworkManager) SetupVMNetwork(name string, config *models.VMConfig) (string, error) {
	m.SetupCalled = true
	if m.ErrSetup != nil {
		return "", m.ErrSetup
	}
	return m.TAPDevice, nil
}

func (m *MockNetworkManager) CleanupVMNetwork(name string) error {
	m.CleanupCalled = true
	if m.ErrCleanup != nil {
		return m.ErrCleanup
	}
	return nil
}

type MockStorageManager struct {
	ErrCreateRootFS    error
	ErrDestroyVM       error
	ErrCreateSnapshot  error
	ErrRestoreSnapshot error
	ErrListSnapshots   error
	CreateRootFSCalled bool
	DestroyVMCalled    bool
	SnapshotPath       string
	Snapshots          []*models.Snapshot
}

func (m *MockStorageManager) CreateRootFS(name string, config *models.VMConfig) (string, error) {
	m.CreateRootFSCalled = true
	if m.ErrCreateRootFS != nil {
		return "", m.ErrCreateRootFS
	}
	return filepath.Join("/tmp", name+".img"), nil
}

func (m *MockStorageManager) DestroyVMStorage(name string) error {
	m.DestroyVMCalled = true
	if m.ErrDestroyVM != nil {
		return m.ErrDestroyVM
	}
	return nil
}

func (m *MockStorageManager) CreateSnapshot(name string, tag string) (string, error) {
	if m.ErrCreateSnapshot != nil {
		return "", m.ErrCreateSnapshot
	}
	return m.SnapshotPath, nil
}

func (m *MockStorageManager) RestoreSnapshot(name string, tag string) error {
	if m.ErrRestoreSnapshot != nil {
		return m.ErrRestoreSnapshot
	}
	return nil
}

func (m *MockStorageManager) ListSnapshots(name string) ([]*storage.SnapshotInfo, error) {
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

// MockVMManager is a mock VM manager for testing CLI commands
type MockVMManager struct {
	SetupCalled        bool
	CreateCalled       bool
	StartCalled        bool
	StopCalled         bool
	DestroyCalled      bool
	StatusCalled       bool
	LogsCalled         bool
	ListCalled         bool
	SnapshotCalled     bool
	RestoreCalled      bool
	ListSnapshotsCalled bool
	CreateSnapshotPath string
	RestoreTag         string
	ErrSetup           error
	ErrCreate          error
	ErrStart           error
	ErrStop            error
	ErrDestroy         error
	ErrStatus          error
	ErrLogs            error
	ErrList            error
	ErrSnapshot        error
	ErrRestore         error
	ErrListSnapshots   error
	VMLastCreated      *models.VMConfig
	VMState            *models.VMState
}

func (m *MockVMManager) Setup() error {
	m.SetupCalled = true
	return m.ErrSetup
}

func (m *MockVMManager) Create(name string, config *models.VMConfig) error {
	m.CreateCalled = true
	m.VMLastCreated = config
	return m.ErrCreate
}

func (m *MockVMManager) Start(name string) error {
	m.StartCalled = true
	return m.ErrStart
}

func (m *MockVMManager) Stop(name string) error {
	m.StopCalled = true
	return m.ErrStop
}

func (m *MockVMManager) Destroy(name string) error {
	m.DestroyCalled = true
	return m.ErrDestroy
}

func (m *MockVMManager) Status(name string) (*models.VMState, error) {
	m.StatusCalled = true
	if m.VMState != nil {
		return m.VMState, nil
	}
	return nil, m.ErrStatus
}

func (m *MockVMManager) Logs(name string) (string, error) {
	m.LogsCalled = true
	return "VM logs placeholder", m.ErrLogs
}

func (m *MockVMManager) List() ([]*models.VMState, error) {
	m.ListCalled = true
	if m.VMState != nil {
		return []*models.VMState{m.VMState}, m.ErrList
	}
	return nil, m.ErrList
}

func (m *MockVMManager) CreateSnapshot(name string, tag string) (string, error) {
	m.SnapshotCalled = true
	m.CreateSnapshotPath = tag
	return m.CreateSnapshotPath, m.ErrSnapshot
}

func (m *MockVMManager) RestoreSnapshot(name string, tag string) error {
	m.RestoreCalled = true
	m.RestoreTag = tag
	return m.ErrRestore
}

func (m *MockVMManager) ListSnapshots(name string) ([]*storage.SnapshotInfo, error) {
	m.ListSnapshotsCalled = true
	return nil, m.ErrListSnapshots
}

// NewMockVMManager creates a new MockVMManager with default values
func NewMockVMManager() *MockVMManager {
	return &MockVMManager{
		VMState: &models.VMState{
			Name:   "test-vm",
			Status: models.VMStatusCreated,
		},
	}
}
