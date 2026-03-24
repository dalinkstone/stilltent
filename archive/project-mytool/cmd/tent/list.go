package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

// ConfigureListCmd creates a new list command with optional dependencies
func ConfigureListCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	var filterLabels []string

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "ps"},
		Short:   "List all microVM sandboxes",
		Long: `List all microVM sandboxes with their status, IP address, resource
allocation, and source image.

Output columns: NAME, STATUS, IP, VCPUS, MEM, DISK, IMAGE.
Status values: running, stopped, paused, creating, error.

Use --filter to show only sandboxes with matching labels. Multiple
filters are ANDed together (all must match).

See also: tent status, tent inspect, tent top`,
		Example: `  # List all sandboxes
  tent list

  # Filter by label
  tent list --filter project=api
  tent list --filter env=staging --filter team=ml

  # Filter by label key only (any value matches)
  tent list --filter project`,
		Args: cobra.NoArgs,
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

			// Parse label filters
			labelFilters := make(map[string]string)
			for _, f := range filterLabels {
				parts := strings.SplitN(f, "=", 2)
				if len(parts) == 2 {
					labelFilters[parts[0]] = parts[1]
				} else {
					labelFilters[parts[0]] = ""
				}
			}

			// Apply label filters
			if len(labelFilters) > 0 {
				var filtered []*models.VMState
				for _, v := range vms {
					if matchVMLabels(v.Labels, labelFilters) {
						filtered = append(filtered, v)
					}
				}
				vms = filtered
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

	cmd.Flags().StringSliceVar(&filterLabels, "filter", nil, "Filter by label (key=value or key)")

	return cmd
}

// matchVMLabels checks if VM labels match all filter criteria.
func matchVMLabels(labels map[string]string, filters map[string]string) bool {
	for k, v := range filters {
		labelVal, ok := labels[k]
		if !ok {
			return false
		}
		if v != "" && labelVal != v {
			return false
		}
	}
	return true
}

// listCmd is a convenience function that uses default dependencies
func listCmd() *cobra.Command {
	return ConfigureListCmd()
}
