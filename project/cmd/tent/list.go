package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/vm"
)

// ConfigureListCmd creates a new list command with optional dependencies
func ConfigureListCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all microVMs",
		Long:  `List all microVMs with status, IP, resource usage.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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

			// List VMs
			vms, err := manager.List()
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			if len(vms) == 0 {
				fmt.Println("No VMs found.")
				return nil
			}

			fmt.Println("Listing VMs:")
			fmt.Println("NAME\tSTATUS\tIP\tPID\tROOTFS")
			for _, vm := range vms {
				fmt.Printf("%s\t%s\t%s\t%d\t%s\n",
					vm.Name, vm.Status, vm.IP, vm.PID, vm.RootFSPath)
			}

			return nil
		},
	}

	return cmd
}

// listCmd is a convenience function that uses default dependencies
func listCmd() *cobra.Command {
	return ConfigureListCmd()
}
