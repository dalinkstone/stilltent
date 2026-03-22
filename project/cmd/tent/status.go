package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/sandbox"
)

// ConfigureStatusCmd creates a new status command with optional dependencies
func ConfigureStatusCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Detailed status of a specific microVM",
		Long:  `Detailed status of a specific microVM.`,
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

			// Get VM status
			vmState, err := manager.Status(name)
			if err != nil {
				return fmt.Errorf("failed to get VM status: %w", err)
			}

			fmt.Printf("VM: %s\n", name)
			fmt.Printf("  Status:    %s\n", vmState.Status)
			fmt.Printf("  Image:     %s\n", vmState.ImageRef)
			fmt.Printf("  VCPUs:     %d\n", vmState.VCPUs)
			fmt.Printf("  Memory:    %d MB\n", vmState.MemoryMB)
			fmt.Printf("  Disk:      %d GB\n", vmState.DiskGB)
			fmt.Printf("  PID:       %d\n", vmState.PID)
			fmt.Printf("  IP:        %s\n", vmState.IP)
			fmt.Printf("  RootFS:    %s\n", vmState.RootFSPath)
			fmt.Printf("  TAP:       %s\n", vmState.TAPDevice)
			fmt.Printf("  Socket:    %s\n", vmState.SocketPath)
			fmt.Printf("  SSH Key:   %s\n", vmState.SSHKeyPath)
			if vmState.RestartPolicy != "" {
				fmt.Printf("  Restart:   %s (count: %d)\n", vmState.RestartPolicy, vmState.RestartCount)
			}
			fmt.Printf("  Created:   %d\n", vmState.CreatedAt)
			fmt.Printf("  Updated:   %d\n", vmState.UpdatedAt)

			return nil
		},
	}

	return cmd
}

// statusCmd is a convenience function that uses default dependencies
func statusCmd() *cobra.Command {
	return ConfigureStatusCmd()
}
