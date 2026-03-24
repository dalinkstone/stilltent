package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

func resourcesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resources",
		Short: "Manage sandbox resource limits",
		Long: `View and configure resource limits for sandboxes.

Resource limits control CPU, memory, I/O, and process constraints.
On Linux, limits are enforced via cgroups v2. On macOS, limits are
advisory and enforced at the hypervisor level where supported.`,
	}

	cmd.AddCommand(resourcesShowCmd())
	cmd.AddCommand(resourcesSetCmd())

	return cmd
}

func resourcesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show resource limits for a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return err
			}

			// Verify sandbox exists
			if _, err := manager.Status(name); err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}

			applied, err := manager.GetResourceLimits(name)
			if err != nil {
				return fmt.Errorf("failed to get resource limits: %w", err)
			}

			fmt.Printf("Resource limits for %q:\n", name)
			fmt.Print(vm.FormatLimits(applied))
			return nil
		},
	}
}

func resourcesSetCmd() *cobra.Command {
	var (
		cpuWeight    int
		cpuMax       int
		memMax       int
		swapMax      int
		ioReadBPS    string
		ioWriteBPS   string
		ioReadIOPS   int64
		ioWriteIOPS  int64
		netBandwidth int
		pidsMax      int
	)

	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Set resource limits for a sandbox",
		Long: `Configure resource limits for a sandbox. The sandbox must be stopped.

Examples:
  tent resources set mybox --cpu-weight 2048 --cpu-max 200
  tent resources set mybox --memory-max 4096 --swap-max 1024
  tent resources set mybox --io-read-bps 50M --io-write-bps 25M
  tent resources set mybox --pids-max 1000
  tent resources set mybox --net-bandwidth 100`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return err
			}

			// Check sandbox exists and is stopped
			state, err := manager.Status(name)
			if err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}
			if state.Status == models.VMStatusRunning {
				return fmt.Errorf("cannot set resource limits on running sandbox %q — stop it first", name)
			}

			// Load config
			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if config.Resources == nil {
				config.Resources = &models.ResourceLimits{}
			}

			changed := false

			if cmd.Flags().Changed("cpu-weight") {
				config.Resources.CPUWeight = cpuWeight
				changed = true
			}
			if cmd.Flags().Changed("cpu-max") {
				config.Resources.CPUMaxPercent = cpuMax
				changed = true
			}
			if cmd.Flags().Changed("memory-max") {
				config.Resources.MemoryMaxMB = memMax
				changed = true
			}
			if cmd.Flags().Changed("swap-max") {
				config.Resources.MemorySwapMaxMB = swapMax
				changed = true
			}
			if cmd.Flags().Changed("io-read-bps") {
				v, err := parseByteRate(ioReadBPS)
				if err != nil {
					return fmt.Errorf("invalid --io-read-bps: %w", err)
				}
				config.Resources.IOReadBPS = v
				changed = true
			}
			if cmd.Flags().Changed("io-write-bps") {
				v, err := parseByteRate(ioWriteBPS)
				if err != nil {
					return fmt.Errorf("invalid --io-write-bps: %w", err)
				}
				config.Resources.IOWriteBPS = v
				changed = true
			}
			if cmd.Flags().Changed("io-read-iops") {
				config.Resources.IOReadIOPS = ioReadIOPS
				changed = true
			}
			if cmd.Flags().Changed("io-write-iops") {
				config.Resources.IOWriteIOPS = ioWriteIOPS
				changed = true
			}
			if cmd.Flags().Changed("net-bandwidth") {
				config.Resources.NetworkBandwidthMbps = netBandwidth
				changed = true
			}
			if cmd.Flags().Changed("pids-max") {
				config.Resources.PidsMax = pidsMax
				changed = true
			}

			if !changed {
				fmt.Println("No changes specified.")
				return nil
			}

			// Validate
			if err := config.Resources.Validate(); err != nil {
				return fmt.Errorf("invalid resource limits: %w", err)
			}

			// Save updated config
			if err := manager.UpdateConfig(name, config); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Printf("Updated resource limits for %q:\n", name)
			if config.Resources.CPUWeight > 0 {
				fmt.Printf("  CPU weight:  %d\n", config.Resources.CPUWeight)
			}
			if config.Resources.CPUMaxPercent > 0 {
				fmt.Printf("  CPU max:     %d%%\n", config.Resources.CPUMaxPercent)
			}
			if config.Resources.MemoryMaxMB > 0 {
				fmt.Printf("  Memory max:  %d MB\n", config.Resources.MemoryMaxMB)
			}
			if config.Resources.MemorySwapMaxMB > 0 {
				fmt.Printf("  Swap max:    %d MB\n", config.Resources.MemorySwapMaxMB)
			}
			if config.Resources.IOReadBPS > 0 {
				fmt.Printf("  IO read:     %s/s\n", formatBytes(config.Resources.IOReadBPS))
			}
			if config.Resources.IOWriteBPS > 0 {
				fmt.Printf("  IO write:    %s/s\n", formatBytes(config.Resources.IOWriteBPS))
			}
			if config.Resources.IOReadIOPS > 0 {
				fmt.Printf("  IO read:     %d IOPS\n", config.Resources.IOReadIOPS)
			}
			if config.Resources.IOWriteIOPS > 0 {
				fmt.Printf("  IO write:    %d IOPS\n", config.Resources.IOWriteIOPS)
			}
			if config.Resources.NetworkBandwidthMbps > 0 {
				fmt.Printf("  Network:     %d Mbps\n", config.Resources.NetworkBandwidthMbps)
			}
			if config.Resources.PidsMax > 0 {
				fmt.Printf("  PIDs max:    %d\n", config.Resources.PidsMax)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&cpuWeight, "cpu-weight", 0, "CPU scheduling weight (1-10000, default 1024)")
	cmd.Flags().IntVar(&cpuMax, "cpu-max", 0, "Maximum CPU usage as percentage (e.g., 200 = 2 cores)")
	cmd.Flags().IntVar(&memMax, "memory-max", 0, "Hard memory limit in MB")
	cmd.Flags().IntVar(&swapMax, "swap-max", 0, "Maximum swap usage in MB")
	cmd.Flags().StringVar(&ioReadBPS, "io-read-bps", "", "Disk read bandwidth limit (e.g., 50M, 1G)")
	cmd.Flags().StringVar(&ioWriteBPS, "io-write-bps", "", "Disk write bandwidth limit (e.g., 50M, 1G)")
	cmd.Flags().Int64Var(&ioReadIOPS, "io-read-iops", 0, "Disk read IOPS limit")
	cmd.Flags().Int64Var(&ioWriteIOPS, "io-write-iops", 0, "Disk write IOPS limit")
	cmd.Flags().IntVar(&netBandwidth, "net-bandwidth", 0, "Network bandwidth limit in Mbps")
	cmd.Flags().IntVar(&pidsMax, "pids-max", 0, "Maximum number of processes")

	return cmd
}

// parseByteRate parses a byte rate string like "50M", "1G", "500K" into bytes.
func parseByteRate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}

	multiplier := int64(1)
	suffix := s[len(s)-1]
	switch suffix {
	case 'K', 'k':
		multiplier = 1024
		s = s[:len(s)-1]
	case 'M', 'm':
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	case 'G', 'g':
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}

	val, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid rate %q: %w", s, err)
	}
	if val < 0 {
		return 0, fmt.Errorf("rate must be non-negative")
	}
	return val * multiplier, nil
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(b int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
