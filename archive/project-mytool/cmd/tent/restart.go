package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

// ConfigureRestartCmd creates a new restart command with optional dependencies
func ConfigureRestartCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	var timeout int
	var policy string

	cmd := &cobra.Command{
		Use:   "restart <name>",
		Short: "Restart a running microVM",
		Long: `Stop and start a running microVM. Optionally set a restart policy for automatic restarts.

Restart policies:
  never       - Never auto-restart (default)
  always      - Always restart when the sandbox stops
  on-failure  - Restart only on non-zero exit`,
		Args: cobra.ExactArgs(1),
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

			// Set restart policy if specified
			if policy != "" {
				p := models.RestartPolicy(policy)
				if err := manager.SetRestartPolicy(name, p); err != nil {
					return fmt.Errorf("failed to set restart policy: %w", err)
				}
				fmt.Printf("Set restart policy for %s: %s\n", name, policy)
			}

			// Restart the VM
			fmt.Printf("Restarting VM %s...\n", name)
			if err := manager.Restart(name, timeout); err != nil {
				return fmt.Errorf("failed to restart VM: %w", err)
			}

			fmt.Printf("Successfully restarted VM: %s\n", name)
			return nil
		},
	}

	cmd.Flags().IntVar(&timeout, "timeout", 0, "Seconds to wait between stop and start for graceful shutdown")
	cmd.Flags().StringVar(&policy, "policy", "", "Set restart policy: never, always, on-failure")

	return cmd
}

// restartCmd is a convenience function that uses default dependencies
func restartCmd() *cobra.Command {
	return ConfigureRestartCmd()
}
