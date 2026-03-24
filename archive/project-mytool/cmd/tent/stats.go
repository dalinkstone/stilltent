package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func statsCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "stats [name]",
		Short: "Show resource usage statistics for sandboxes",
		Long:  `Show detailed resource usage including CPU, memory, disk, uptime, and snapshot counts. If no name is given, shows stats for all sandboxes.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if len(args) == 1 {
				return showSandboxStats(manager, args[0], jsonOutput)
			}
			return showAllStats(manager, jsonOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func showSandboxStats(manager *vm.VMManager, name string, jsonOut bool) error {
	stats, err := manager.GetStats(name)
	if err != nil {
		return err
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(stats)
	}

	fmt.Printf("Sandbox: %s\n", stats.Name)
	fmt.Printf("  Status:       %s\n", stats.Status)
	if stats.IP != "" {
		fmt.Printf("  IP:           %s\n", stats.IP)
	}
	if stats.ImageRef != "" {
		fmt.Printf("  Image:        %s\n", stats.ImageRef)
	}
	if stats.PID > 0 {
		fmt.Printf("  PID:          %d\n", stats.PID)
	}
	fmt.Printf("  vCPUs:        %d\n", stats.VCPUs)
	fmt.Printf("  Memory:       %d MB\n", stats.MemoryMB)
	fmt.Printf("  Disk Config:  %d GB\n", stats.DiskGB)
	fmt.Printf("  Disk Used:    %d MB\n", stats.DiskUsedMB)
	fmt.Printf("  RootFS Size:  %d MB\n", stats.RootFSSizeMB)
	if stats.UptimeSeconds > 0 {
		fmt.Printf("  Uptime:       %s\n", formatUptime(stats.UptimeSeconds))
	}
	fmt.Printf("  Snapshots:    %d\n", stats.SnapshotCount)

	return nil
}

func showAllStats(manager *vm.VMManager, jsonOut bool) error {
	allStats, err := manager.GetAllStats()
	if err != nil {
		return err
	}

	if len(allStats) == 0 {
		fmt.Println("No sandboxes found.")
		return nil
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(allStats)
	}

	fmt.Printf("%-20s %-10s %-6s %-8s %-10s %-10s %-12s %-5s\n",
		"NAME", "STATUS", "VCPUS", "MEMORY", "DISK USED", "ROOTFS", "UPTIME", "SNAPS")
	for _, s := range allStats {
		uptime := "-"
		if s.UptimeSeconds > 0 {
			uptime = formatUptime(s.UptimeSeconds)
		}
		fmt.Printf("%-20s %-10s %-6d %-8s %-10s %-10s %-12s %-5d\n",
			s.Name,
			s.Status,
			s.VCPUs,
			fmt.Sprintf("%dMB", s.MemoryMB),
			fmt.Sprintf("%dMB", s.DiskUsedMB),
			fmt.Sprintf("%dMB", s.RootFSSizeMB),
			uptime,
			s.SnapshotCount,
		)
	}

	return nil
}

func formatUptime(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm%ds", seconds/60, seconds%60)
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	if hours < 24 {
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
	days := hours / 24
	hours = hours % 24
	return fmt.Sprintf("%dd%dh%dm", days, hours, minutes)
}
