package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/vm"
)

// ConfigureStopCmd creates a new stop command with optional dependencies
func ConfigureStopCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	cmd := &cobra.Command{
		Use:   "stop <name>",
		Short: "Gracefully shut down a running microVM",
		Long:  `Gracefully shut down a running microVM.`,
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

			// Stop the VM
			if err := manager.Stop(name); err != nil {
				return fmt.Errorf("failed to stop VM: %w", err)
			}

			fmt.Printf("Successfully stopped VM: %s\n", name)
			return nil
		},
	}

	return cmd
}

// stopCmd is a convenience function that uses default dependencies
func stopCmd() *cobra.Command {
	return ConfigureStopCmd()
}
