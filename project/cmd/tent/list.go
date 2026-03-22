package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/sandbox"
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

			fmt.Printf("%-20s %-10s %-16s %-6s %-6s %-8s %-20s\n",
				"NAME", "STATUS", "IP", "VCPUS", "MEM", "DISK", "IMAGE")
			for _, vm := range vms {
				mem := ""
				if vm.MemoryMB > 0 {
					mem = fmt.Sprintf("%dMB", vm.MemoryMB)
				}
				disk := ""
				if vm.DiskGB > 0 {
					disk = fmt.Sprintf("%dGB", vm.DiskGB)
				}
				vcpus := ""
				if vm.VCPUs > 0 {
					vcpus = fmt.Sprintf("%d", vm.VCPUs)
				}
				image := vm.ImageRef
				if image == "" {
					image = "-"
				}
				fmt.Printf("%-20s %-10s %-16s %-6s %-6s %-8s %-20s\n",
					vm.Name, vm.Status, vm.IP, vcpus, mem, disk, image)
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
