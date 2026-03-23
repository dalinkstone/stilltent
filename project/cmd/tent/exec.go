package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/sandbox"
)

// ConfigureExecCmd creates a new exec command with optional dependencies
func ConfigureExecCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	cmd := &cobra.Command{
		Use:   "exec <name> -- <command> [args...]",
		Short: "Execute a command inside a running microVM",
		Long: `Execute a command inside a running microVM sandbox via the vsock guest agent.

The sandbox must be running. The command and its arguments are passed
after the "--" separator. The command's stdout is printed to your
terminal, and its exit code is forwarded as the exit code of "tent exec".

The vsock agent communicates directly with the guest without requiring
SSH, IP discovery, or network configuration. If the agent is unavailable,
the command falls back to SSH.

For an interactive shell session, use "tent shell" instead.

See also: tent shell, tent run, tent attach`,
		Example: `  # Run a simple command
  tent exec mybox -- ls /

  # Run a command with arguments
  tent exec mybox -- cat /etc/os-release

  # Run a multi-word command
  tent exec mybox -- bash -c "echo hello && whoami"

  # Capture output in a script
  output=$(tent exec mybox -- hostname)`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			command := args[1:]

			// Create VM manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			// Get platform-specific hypervisor backend if not provided
			hvBackend := opts.Hypervisor
			if hvBackend == nil {
				var err error
				hvBackend, err = vm.NewPlatformBackend(baseDir)
				if err != nil {
					return fmt.Errorf("failed to create hypervisor backend: %w", err)
				}
			}

			// Create manager with dependencies
			manager, err := vm.NewManager(baseDir, opts.StateManager, hvBackend, opts.NetworkMgr, opts.StorageMgr)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Execute the command in the VM
			output, exitCode, err := manager.Exec(name, command)
			if err != nil {
				return fmt.Errorf("failed to execute command: %w", err)
			}

			fmt.Print(output)
			if exitCode != 0 {
				os.Exit(exitCode)
			}
			return nil
		},
	}

	return cmd
}

// execCmd is a convenience function that uses default dependencies
func execCmd() *cobra.Command {
	return ConfigureExecCmd()
}
