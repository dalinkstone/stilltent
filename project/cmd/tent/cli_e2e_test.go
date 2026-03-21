//go:build integration

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/vm"
	"github.com/dalinkstone/tent/pkg/models"
)

// TestCLI_E2E_CommandStructure verifies command structure with mocks
func TestCLI_E2E_CommandStructure(t *testing.T) {
	// Build the command tree using the main.go pattern
	cmd := &cobra.Command{
		Use:   "tent",
		Short: "tent - MicroVM management tool",
		Long:  `tent is a command-line tool for creating, managing, and destroying microVMs as lightweight, isolated development environments.`,
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Usage()
		},
	}

	// Add all commands - use the main.go pattern
	cmd.AddCommand(createCmd())
	cmd.AddCommand(startCmd())
	cmd.AddCommand(stopCmd())
	cmd.AddCommand(destroyCmd())
	cmd.AddCommand(listCmd())
	cmd.AddCommand(sshCmd())
	cmd.AddCommand(statusCmd())
	cmd.AddCommand(logsCmd())
	cmd.AddCommand(snapshotCmd())
	cmd.AddCommand(networkCmd())
	cmd.AddCommand(imageCmd())

	// Verify root command
	assert.Equal(t, "tent", cmd.Use)

	// Log subcommands for debugging
	subCmds := cmd.Commands()
	t.Logf("Found %d subcommands", len(subCmds))
	for _, c := range subCmds {
		t.Logf("  - %s", c.Use)
	}

	// Verify subcommands are registered
	subCmdMap := make(map[string]bool)
	for _, c := range subCmds {
		if c != nil {
			// Extract the command name from Use (e.g., "create <name>" -> "create")
			cmdName := c.Use
			for i, ch := range c.Use {
				if ch == ' ' {
					cmdName = c.Use[:i]
					break
				}
			}
			subCmdMap[cmdName] = true
		}
	}

	expectedCommands := []string{
		"create", "start", "stop", "destroy", "list",
		"ssh", "status", "logs", "snapshot", "network", "image",
	}

	for _, expected := range expectedCommands {
		assert.True(t, subCmdMap[expected], "Expected subcommand %s to be registered (found: %v)", expected, subCmdMap)
	}
}

// TestCLI_E2E_CreateCommandWithMockedDependencies tests create command with mock dependencies
func TestCLI_E2E_CreateCommandWithMockedDependencies(t *testing.T) {
	tmpDir := t.TempDir()

	// Set environment variable for base directory
	os.Setenv("TENT_BASE_DIR", tmpDir)
	defer os.Unsetenv("TENT_BASE_DIR")

	// Create a test config file
	configPath := filepath.Join(tmpDir, "config.yaml")
	configContent := `name: test-vm
vcpus: 2
memory_mb: 1024
disk_gb: 10
kernel: default
network:
  mode: bridge
  bridge: tent0
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// Create mocks
	mockState := &MockStateManager{
		VMs: make(map[string]*models.VMState),
	}
	mockFC := &MockFirecrackerClient{}
	mockNet := &MockNetworkManager{TAPDevice: "tap0"}
	mockStorage := &MockStorageManager{}

	// Test the create function with mocked dependencies
	cfg, err := loadConfigFromFile(configPath)
	require.NoError(t, err)

	err = cfg.Validate()
	require.NoError(t, err)

	// Create manager with mocks
	manager, err := vm.NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	require.NoError(t, err)
	require.NotNil(t, manager)

	// Create the VM
	err = manager.Create("test-vm", cfg)
	assert.NoError(t, err)

	// Verify VM was stored
	storedVM, err := mockState.GetVM("test-vm")
	assert.NoError(t, err)
	assert.Equal(t, "test-vm", storedVM.Name)
	assert.Equal(t, models.VMStatusCreated, storedVM.Status)

	// Verify storage was called
	assert.True(t, mockStorage.CreateRootFSCalled, "CreateRootFS should have been called")
}

// TestCLI_E2E_CreateCommandWithMockedDependencies_NoConfig tests create with default config
func TestCLI_E2E_CreateCommandWithMockedDependencies_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Set environment variable for base directory
	os.Setenv("TENT_BASE_DIR", tmpDir)
	defer os.Unsetenv("TENT_BASE_DIR")

	// Create mocks
	mockState := &MockStateManager{
		VMs: make(map[string]*models.VMState),
	}
	mockFC := &MockFirecrackerClient{}
	mockNet := &MockNetworkManager{TAPDevice: "tap0"}
	mockStorage := &MockStorageManager{}

	// Create manager with mocks
	manager, err := vm.NewManager(tmpDir, mockState, mockFC, mockNet, mockStorage)
	require.NoError(t, err)
	require.NotNil(t, manager)

	// Create default config
	cfg := &models.VMConfig{
		Name:     "default-vm",
		VCPUs:    2,
		MemoryMB: 1024,
		DiskGB:   10,
		Kernel:   "default",
		RootFS:   "",
		Network: models.NetworkConfig{
			Mode:   "bridge",
			Bridge: "tent0",
		},
	}

	err = cfg.Validate()
	require.NoError(t, err)

	// Create the VM
	err = manager.Create("default-vm", cfg)
	assert.NoError(t, err)

	// Verify VM was stored
	storedVM, err := mockState.GetVM("default-vm")
	assert.NoError(t, err)
	assert.Equal(t, "default-vm", storedVM.Name)
	assert.Equal(t, models.VMStatusCreated, storedVM.Status)
}

// TestCLI_E2E_MockStateManager tests the mock state manager
func TestCLI_E2E_MockStateManager(t *testing.T) {
	mockState := &MockStateManager{
		VMs: make(map[string]*models.VMState),
	}

	// Test StoreVM
	vm1 := &models.VMState{Name: "vm1", Status: models.VMStatusCreated}
	err := mockState.StoreVM(vm1)
	assert.NoError(t, err)

	// Test GetVM
	storedVM, err := mockState.GetVM("vm1")
	assert.NoError(t, err)
	assert.Equal(t, "vm1", storedVM.Name)

	// Test ListVMs
	vms, err := mockState.ListVMs()
	assert.NoError(t, err)
	assert.Equal(t, 1, len(vms))

	// Test UpdateVM
	err = mockState.UpdateVM("vm1", func(v *models.VMState) error {
		v.Status = models.VMStatusRunning
		return nil
	})
	assert.NoError(t, err)

	storedVM, err = mockState.GetVM("vm1")
	assert.NoError(t, err)
	assert.Equal(t, models.VMStatusRunning, storedVM.Status)

	// Test DeleteVM
	err = mockState.DeleteVM("vm1")
	assert.NoError(t, err)

	_, err = mockState.GetVM("vm1")
	assert.Error(t, err)
	assert.Equal(t, os.ErrNotExist, err)
}

// TestCLI_E2E_MockFirecrackerClient tests the mock firecracker client
func TestCLI_E2E_MockFirecrackerClient(t *testing.T) {
	mockFC := &MockFirecrackerClient{}

	// Test ConfigureVM
	err := mockFC.ConfigureVM("/tmp/test.sock", &models.VMConfig{Name: "test-vm"})
	assert.NoError(t, err)
	assert.True(t, mockFC.ConfigureCalled)

	// Test StartVM
	err = mockFC.StartVM("/tmp/test.sock")
	assert.NoError(t, err)
	assert.True(t, mockFC.StartVMCalled)

	// Test ShutdownVM
	err = mockFC.ShutdownVM("/tmp/test.sock")
	assert.NoError(t, err)
	assert.True(t, mockFC.ShutdownVMCalled)
}

// TestCLI_E2E_MockNetworkManager tests the mock network manager
func TestCLI_E2E_MockNetworkManager(t *testing.T) {
	mockNet := &MockNetworkManager{TAPDevice: "tap0"}

	// Test SetupVMNetwork
	device, err := mockNet.SetupVMNetwork("test-vm", &models.VMConfig{Name: "test-vm"})
	assert.NoError(t, err)
	assert.Equal(t, "tap0", device)
	assert.True(t, mockNet.SetupCalled)

	// Test CleanupVMNetwork
	err = mockNet.CleanupVMNetwork("test-vm")
	assert.NoError(t, err)
	assert.True(t, mockNet.CleanupCalled)
}

// TestCLI_E2E_MockStorageManager tests the mock storage manager
func TestCLI_E2E_MockStorageManager(t *testing.T) {
	mockStorage := &MockStorageManager{
		SnapshotPath: "/tmp/snapshot.img",
		Snapshots: []*models.Snapshot{
			{Tag: "snap1", SizeMB: 100, Timestamp: "2009-02-13 23:31:30"},
		},
	}

	// Test CreateRootFS
	path, err := mockStorage.CreateRootFS("test-vm", &models.VMConfig{Name: "test-vm", DiskGB: 10})
	assert.NoError(t, err)
	assert.Contains(t, path, "test-vm.img")
	assert.True(t, mockStorage.CreateRootFSCalled)

	// Test DestroyVMStorage
	err = mockStorage.DestroyVMStorage("test-vm")
	assert.NoError(t, err)
	assert.True(t, mockStorage.DestroyVMCalled)

	// Test CreateSnapshot
	path, err = mockStorage.CreateSnapshot("test-vm", "test-tag")
	assert.NoError(t, err)
	assert.Equal(t, "/tmp/snapshot.img", path)

	// Test ListSnapshots
	snapshots, err := mockStorage.ListSnapshots("test-vm")
	assert.NoError(t, err)
	assert.Equal(t, 1, len(snapshots))
	assert.Equal(t, "snap1", snapshots[0].Tag)
}

// TestCLI_E2E_InvalidConfigValidation tests config validation
func TestCLI_E2E_InvalidConfigValidation(t *testing.T) {
	tests := []struct {
		name  string
		config *models.VMConfig
		valid bool
	}{
		{
			name: "valid config",
			config: &models.VMConfig{
				Name:     "valid-vm",
				VCPUs:    2,
				MemoryMB: 1024,
				DiskGB:   10,
			},
			valid: true,
		},
		{
			name: "invalid - empty name",
			config: &models.VMConfig{
				Name:     "",
				VCPUs:    2,
				MemoryMB: 1024,
				DiskGB:   10,
			},
			valid: false,
		},
		{
			name: "invalid - zero vcpus",
			config: &models.VMConfig{
				Name:     "test-vm",
				VCPUs:    0,
				MemoryMB: 1024,
				DiskGB:   10,
			},
			valid: false,
		},
		{
			name: "invalid - zero memory",
			config: &models.VMConfig{
				Name:     "test-vm",
				VCPUs:    2,
				MemoryMB: 0,
				DiskGB:   10,
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
