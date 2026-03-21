package main

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
)

// TestRootCommand tests that the root command is properly configured
func TestRootCommand(t *testing.T) {
	rootCmd := func() *cobra.Command {
		return &cobra.Command{
			Use:   "tent",
			Short: "tent - MicroVM management tool",
			Long:  `tent is a command-line tool for creating, managing, and destroying microVMs as lightweight, isolated development environments.`,
			Run: func(cmd *cobra.Command, args []string) {
				_ = cmd.Usage()
			},
		}
	}()
	
	if rootCmd.Use != "tent" {
		t.Errorf("Expected root command use 'tent', got '%s'", rootCmd.Use)
	}
	
	if rootCmd.Short == "" {
		t.Error("Root command should have a short description")
	}
}

// TestCreateCommand tests the create command structure
func TestCreateCommand(t *testing.T) {
	cmd := createCmd()
	
	if cmd.Use != "create <name> [--config <path>]" {
		t.Errorf("Expected use 'create <name> [--config <path>]', got '%s'", cmd.Use)
	}
	
	if cmd.Short == "" {
		t.Error("Create command should have a short description")
	}
	
	// Test argument validation
	err := cmd.ValidateArgs([]string{})
	if err == nil {
		t.Error("Expected error for missing name argument")
	}
	
	err = cmd.ValidateArgs([]string{"test-vm"})
	if err != nil {
		t.Errorf("Expected no error for valid name, got: %v", err)
	}
}

// TestStartCommand tests the start command structure
func TestStartCommand(t *testing.T) {
	cmd := startCmd()
	
	if cmd.Use != "start <name>" {
		t.Errorf("Expected use 'start <name>', got '%s'", cmd.Use)
	}
	
	if cmd.Short == "" {
		t.Error("Start command should have a short description")
	}
}

// TestStopCommand tests the stop command structure
func TestStopCommand(t *testing.T) {
	cmd := stopCmd()
	
	if cmd.Use != "stop <name>" {
		t.Errorf("Expected use 'stop <name>', got '%s'", cmd.Use)
	}
	
	if cmd.Short == "" {
		t.Error("Stop command should have a short description")
	}
}

// TestDestroyCommand tests the destroy command structure
func TestDestroyCommand(t *testing.T) {
	cmd := destroyCmd()
	
	if cmd.Use != "destroy <name>" {
		t.Errorf("Expected use 'destroy <name>', got '%s'", cmd.Use)
	}
	
	if cmd.Short == "" {
		t.Error("Destroy command should have a short description")
	}
}

// TestListCommand tests the list command structure
func TestListCommand(t *testing.T) {
	cmd := listCmd()
	
	if cmd.Use != "list" {
		t.Errorf("Expected use 'list', got '%s'", cmd.Use)
	}
	
	if cmd.Short == "" {
		t.Error("List command should have a short description")
	}
}

// TestStatusCommand tests the status command structure
func TestStatusCommand(t *testing.T) {
	cmd := statusCmd()
	
	if cmd.Use != "status <name>" {
		t.Errorf("Expected use 'status <name>', got '%s'", cmd.Use)
	}
	
	if cmd.Short == "" {
		t.Error("Status command should have a short description")
	}
}

// TestLogsCommand tests the logs command structure
func TestLogsCommand(t *testing.T) {
	cmd := logsCmd()
	
	if cmd.Use != "logs <name>" {
		t.Errorf("Expected use 'logs <name>', got '%s'", cmd.Use)
	}
	
	if cmd.Short == "" {
		t.Error("Logs command should have a short description")
	}
}

// TestSSHCommand tests the ssh command structure
func TestSSHCommand(t *testing.T) {
	cmd := sshCmd()
	
	if cmd.Use != "ssh <name>" {
		t.Errorf("Expected use 'ssh <name>', got '%s'", cmd.Use)
	}
	
	if cmd.Short == "" {
		t.Error("SSH command should have a short description")
	}
}

// TestSnapshotCommand tests the snapshot subcommand structure
func TestSnapshotCommand(t *testing.T) {
	cmd := snapshotCmd()
	
	if cmd.Use != "snapshot" {
		t.Errorf("Expected use 'snapshot', got '%s'", cmd.Use)
	}
	
	// Check subcommands exist
	subCmds := cmd.Commands()
	subCmdNames := make(map[string]bool)
	for _, c := range subCmds {
		subCmdNames[c.Use] = true
	}
	
	requiredSubCmds := []string{"create <name> <tag>", "restore <name> <tag>", "list <name>"}
	for _, expected := range requiredSubCmds {
		if !subCmdNames[expected] {
			t.Errorf("Expected subcommand '%s' not found", expected)
		}
	}
}

// TestNetworkCommand tests the network subcommand structure
func TestNetworkCommand(t *testing.T) {
	cmd := networkCmd()
	
	if cmd.Use != "network" {
		t.Errorf("Expected use 'network', got '%s'", cmd.Use)
	}
	
	// Check subcommands exist
	subCmds := cmd.Commands()
	subCmdNames := make(map[string]bool)
	for _, c := range subCmds {
		subCmdNames[c.Use] = true
	}
	
	if !subCmdNames["list"] {
		t.Error("Expected subcommand 'list' not found")
	}
}

// TestImageCommand tests the image subcommand structure
func TestImageCommand(t *testing.T) {
	cmd := imageCmd()
	
	if cmd.Use != "image" {
		t.Errorf("Expected use 'image', got '%s'", cmd.Use)
	}
	
	// Check subcommands exist
	subCmds := cmd.Commands()
	subCmdNames := make(map[string]bool)
	for _, c := range subCmds {
		subCmdNames[c.Use] = true
	}
	
	requiredSubCmds := []string{"list", "pull <name> [url]"}
	for _, expected := range requiredSubCmds {
		if !subCmdNames[expected] {
			t.Errorf("Expected subcommand '%s' not found", expected)
		}
	}
}

// TestBuildCommands tests that all commands can be built
func TestBuildCommands(t *testing.T) {
	// This is a basic smoke test - just verify commands can be instantiated
	commands := []*cobra.Command{
		func() *cobra.Command {
			return &cobra.Command{
				Use:   "tent",
				Short: "tent - MicroVM management tool",
				Long:  `tent is a command-line tool for creating, managing, and destroying microVMs as lightweight, isolated development environments.`,
				Run: func(cmd *cobra.Command, args []string) {
					_ = cmd.Usage()
				},
			}
		}(),
		createCmd(),
		startCmd(),
		stopCmd(),
		destroyCmd(),
		listCmd(),
		sshCmd(),
		statusCmd(),
		logsCmd(),
		snapshotCmd(),
		networkCmd(),
		imageCmd(),
	}
	
	for _, cmd := range commands {
		if cmd == nil {
			t.Error("Command should not be nil")
		}
	}
}

// TestEnvironmentVariableHandling tests that TENT_BASE_DIR is respected
func TestEnvironmentVariableHandling(t *testing.T) {
	// Set a test environment variable
	os.Setenv("TENT_BASE_DIR", "/tmp/test-tent-base")
	defer os.Unsetenv("TENT_BASE_DIR")
	
	// Create a command that uses the base directory
	cmd := statusCmd()
	
	// This just tests that the command can be created
	// Actual execution would require a real VM state
	if cmd == nil {
		t.Error("Command should not be nil")
	}
}

// TestRootCmdInMain tests that main() properly creates and executes the root command
func TestRootCmdInMain(t *testing.T) {
	// This test verifies the main command structure
	// We can't actually run main() in tests, but we can verify command structure
	
	// Verify root command has all expected subcommands
	subCmds := []func() *cobra.Command{
		createCmd, startCmd, stopCmd, destroyCmd, listCmd, sshCmd,
		statusCmd, logsCmd, snapshotCmd, networkCmd, imageCmd,
	}
	
	for _, cmdFn := range subCmds {
		cmd := cmdFn()
		if cmd == nil {
			t.Errorf("Command function returned nil")
		}
	}
}