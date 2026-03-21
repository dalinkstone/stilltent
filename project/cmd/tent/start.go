package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/vm"
)

// ConfigureStartCmd creates a new start command with optional dependencies
func ConfigureStartCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	cmd := &cobra.Command{
		Use:   "start <name>",
		Short: "Boot a stopped microVM",
		Long:  `Boot a stopped microVM.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

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

			// Start the VM
			if err := manager.Start(name); err != nil {
				return fmt.Errorf("failed to start VM: %w", err)
			}

			fmt.Printf("Successfully started VM: %s\n", name)
			return nil
		},
	}

	return cmd
}

// startCmd is a convenience function that uses default dependencies
func startCmd() *cobra.Command {
	return ConfigureStartCmd()
}
