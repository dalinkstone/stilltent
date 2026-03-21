//go:build integration

package main

import (
	"os"
	"path/filepath"

	"github.com/dalinkstone/tent/pkg/models"
	"github.com/dalinkstone/tent/internal/storage"
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

type MockFirecrackerClient struct {
	ConfigureCalled   bool
	StartVMCalled     bool
	ShutdownVMCalled  bool
	ErrConfigure      error
	ErrStart          error
	ErrShutdown       error
}

func (m *MockFirecrackerClient) ConfigureVM(socketPath string, config *models.VMConfig) error {
	m.ConfigureCalled = true
	if m.ErrConfigure != nil {
		return m.ErrConfigure
	}
	return nil
}

func (m *MockFirecrackerClient) StartVM(socketPath string) error {
	m.StartVMCalled = true
	if m.ErrStart != nil {
		return m.ErrStart
	}
	return nil
}

func (m *MockFirecrackerClient) ShutdownVM(socketPath string) error {
	m.ShutdownVMCalled = true
	if m.ErrShutdown != nil {
		return m.ErrShutdown
	}
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
