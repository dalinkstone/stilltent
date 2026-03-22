package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/internal/hypervisor"
)

// ConfigureExecCmd creates a new exec command with optional dependencies
func ConfigureExecCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	cmd := &cobra.Command{
		Use:   "exec <name> <command> [args...]",
		Short: "Execute a command inside a running microVM",
		Long:  `Execute a command inside a running microVM. The command runs in the VM's default shell.`,
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			command := args[1:]
			// Join all remaining args into a single command string
			cmdStr := fmt.Sprintf("%s", command)

			// Create VM manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			// Get platform-specific hypervisor backend if not provided
			var hvBackend hypervisor.Backend
			if opts.Hypervisor != nil {
				hvBackend = opts.Hypervisor
			} else {
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
			output, err := manager.Exec(name, cmdStr)
			if err != nil {
				return fmt.Errorf("failed to execute command: %w", err)
			}

			fmt.Print(output)
			return nil
		},
	}

	return cmd
}

// execCmd is a convenience function that uses default dependencies
func execCmd() *cobra.Command {
	return ConfigureExecCmd()
}
