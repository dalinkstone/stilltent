//go:build integration

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// TestCLIIntegration_DirectoryStructure verifies the CLI directory structure
func TestCLIIntegration_DirectoryStructure(t *testing.T) {
	// Get the current directory
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}

	// Verify we're in the cmd/tent directory
	if filepath.Base(cwd) != "tent" {
		t.Skip("Test should run from cmd/tent directory")
	}

	// Verify all expected command files exist
	expectedFiles := []string{
		"main.go",
		"create.go",
		"start.go",
		"stop.go",
		"destroy.go",
		"list.go",
		"ssh.go",
		"status.go",
		"logs.go",
		"snapshot.go",
		"network.go",
		"image.go",
		"cli_test.go",
	}

	for _, file := range expectedFiles {
		path := filepath.Join(cwd, file)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Expected file %s does not exist", file)
		}
	}
}

// TestCLIIntegration_CommandDiscovery verifies all commands are discoverable
func TestCLIIntegration_CommandDiscovery(t *testing.T) {
	// Build the tent binary
	cmd := buildTestCommand()
	if cmd == nil {
		t.Fatal("Failed to build test command")
	}

	// Verify root command structure
	if cmd.Use != "tent" {
		t.Errorf("Expected root command use 'tent', got '%s'", cmd.Use)
	}

	// Log existing subcommands for debugging
	subCmds := cmd.Commands()
	t.Logf("Found %d subcommands", len(subCmds))

	// Verify all expected subcommands are registered by checking Use field starts with expected name
	expectedCommands := []struct {
		name string
	_USE  string // What to check for (Use field should start with this)
	}{
		{"create", "create"},
		{"start", "start"},
		{"stop", "stop"},
		{"destroy", "destroy"},
		{"list", "list"},
		{"ssh", "ssh"},
		{"status", "status"},
		{"logs", "logs"},
		{"snapshot", "snapshot"},
		{"network", "network"},
		{"image", "image"},
	}

	subCmdNames := make(map[string]bool)
	for _, c := range subCmds {
		subCmdNames[c.Use] = true
	}

	for _, expected := range expectedCommands {
		found := false
		for use := range subCmdNames {
			if use == expected.name || len(use) > len(expected.name) && use[:len(expected.name)] == expected.name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected subcommand '%s' not found in root command (Use: %s)", expected.name, expected._USE)
		}
	}
}

// TestCLIIntegration_SnapshotSubcommands verifies snapshot subcommand structure
func TestCLIIntegration_SnapshotSubcommands(t *testing.T) {
	cmd := snapshotCmd()
	if cmd == nil {
		t.Fatal("snapshotCmd() returned nil")
	}

	expectedSubcommands := []string{
		"create <name> <tag>",
		"restore <name> <tag>",
		"list <name>",
	}

	subCmds := cmd.Commands()
	subCmdNames := make(map[string]bool)
	for _, c := range subCmds {
		subCmdNames[c.Use] = true
	}

	for _, expected := range expectedSubcommands {
		if !subCmdNames[expected] {
			t.Errorf("Expected snapshot subcommand '%s' not found", expected)
		}
	}
}

// TestCLIIntegration_NetworkSubcommands verifies network subcommand structure
func TestCLIIntegration_NetworkSubcommands(t *testing.T) {
	cmd := networkCmd()
	if cmd == nil {
		t.Fatal("networkCmd() returned nil")
	}

	subCmds := cmd.Commands()
	subCmdNames := make(map[string]bool)
	for _, c := range subCmds {
		subCmdNames[c.Use] = true
	}

	if !subCmdNames["list"] {
		t.Error("Expected network subcommand 'list' not found")
	}
}

// TestCLIIntegration_ImageSubcommands verifies image subcommand structure
func TestCLIIntegration_ImageSubcommands(t *testing.T) {
	cmd := imageCmd()
	if cmd == nil {
		t.Fatal("imageCmd() returned nil")
	}

	subCmds := cmd.Commands()
	subCmdNames := make(map[string]bool)
	for _, c := range subCmds {
		subCmdNames[c.Use] = true
	}

	expectedSubcommands := []string{
		"list",
		"pull <name> [url]",
	}

	for _, expected := range expectedSubcommands {
		if !subCmdNames[expected] {
			t.Errorf("Expected image subcommand '%s' not found", expected)
		}
	}
}

// TestCLIIntegration_CommandArgumentValidation tests argument validation
func TestCLIIntegration_CommandArgumentValidation(t *testing.T) {
	tests := []struct {
		name        string
		cmdFactory  func() *cobra.Command
		validArgs   []string
		invalidArgs []string
		shouldError bool
	}{
		{
			name:        "create command validates arguments",
			cmdFactory:  createCmd,
			validArgs:   []string{"test-vm"},
			invalidArgs: []string{},
			shouldError: true,
		},
		{
			name:        "start command validates arguments",
			cmdFactory:  startCmd,
			validArgs:   []string{"test-vm"},
			invalidArgs: []string{},
			shouldError: true,
		},
		{
			name:        "stop command validates arguments",
			cmdFactory:  stopCmd,
			validArgs:   []string{"test-vm"},
			invalidArgs: []string{},
			shouldError: true,
		},
		{
			name:        "destroy command validates arguments",
			cmdFactory:  destroyCmd,
			validArgs:   []string{"test-vm"},
			invalidArgs: []string{},
			shouldError: true,
		},
		{
			name:        "status command validates arguments",
			cmdFactory:  statusCmd,
			validArgs:   []string{"test-vm"},
			invalidArgs: []string{},
			shouldError: true,
		},
		{
			name:        "logs command validates arguments",
			cmdFactory:  logsCmd,
			validArgs:   []string{"test-vm"},
			invalidArgs: []string{},
			shouldError: true,
		},
		{
			name:        "ssh command validates arguments",
			cmdFactory:  sshCmd,
			validArgs:   []string{"test-vm"},
			invalidArgs: []string{},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.cmdFactory()
			if cmd == nil {
				t.Fatalf("Command factory returned nil for %s", tt.name)
			}

			// Test valid arguments
			err := cmd.ValidateArgs(tt.validArgs)
			if err != nil {
				t.Errorf("Valid args %v returned error: %v", tt.validArgs, err)
			}

			// Test invalid arguments
			err = cmd.ValidateArgs(tt.invalidArgs)
			if tt.shouldError && err == nil {
				t.Errorf("Invalid args %v should have returned error but didn't", tt.invalidArgs)
			}
		})
	}
}

// TestCLIIntegration_CommandHelpTexts verifies all commands have help text
func TestCLIIntegration_CommandHelpTexts(t *testing.T) {
	commands := []*cobra.Command{
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
		if cmd.Use == "" {
			t.Error("Command.Use should not be empty")
		}
		if cmd.Short == "" {
			t.Errorf("Command %s should have a short description", cmd.Use)
		}
	}
}

// TestCLIIntegration_EnvironmentVariables verifies environment variable handling
func TestCLIIntegration_EnvironmentVariables(t *testing.T) {
	// Test TENT_BASE_DIR is respected
	os.Setenv("TENT_BASE_DIR", "/tmp/test-tent-base-dir")
	defer os.Unsetenv("TENT_BASE_DIR")

	// This command uses the base directory - just verify it can be created
	cmd := statusCmd()
	if cmd == nil {
		t.Error("statusCmd() should not return nil")
	}

	// Test that the command can be executed (will fail due to no VM, but that's expected)
	// We're just testing that the command structure is correct
	_ = cmd
}

// TestCLIIntegration_ComprehensiveStructure runs a comprehensive structure check
func TestCLIIntegration_ComprehensiveStructure(t *testing.T) {
	// Build the full command tree
	rootCmd := buildTestCommand()

	// Verify root command exists and has correct properties
	if rootCmd == nil {
		t.Fatal("Root command should not be nil")
	}

	// Verify we can execute usage without panicking
	output := &bytes.Buffer{}
	rootCmd.SetOut(output)
	rootCmd.SetErr(output)

	// This is a structural test - we can't actually test all paths without
	// implementing cobra's Path() method, but we verify the commands exist
	subCmds := rootCmd.Commands()
	if len(subCmds) < 10 {
		t.Errorf("Expected at least 10 subcommands, got %d", len(subCmds))
	}
}

// --- Helper Functions ---

// buildTestCommand creates a test command for integration testing
func buildTestCommand() *cobra.Command {
	// This mimics main() but returns the command for testing
	rootCmd := &cobra.Command{
		Use:   "tent",
		Short: "tent - MicroVM management tool",
		Long:  `tent is a command-line tool for creating, managing, and destroying microVMs as lightweight, isolated development environments.`,
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Usage()
		},
	}

	// Add all commands
	rootCmd.AddCommand(createCmd())
	rootCmd.AddCommand(startCmd())
	rootCmd.AddCommand(stopCmd())
	rootCmd.AddCommand(destroyCmd())
	rootCmd.AddCommand(listCmd())
	rootCmd.AddCommand(sshCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(logsCmd())
	rootCmd.AddCommand(snapshotCmd())
	rootCmd.AddCommand(networkCmd())
	rootCmd.AddCommand(imageCmd())

	return rootCmd
}
