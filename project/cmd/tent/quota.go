package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/internal/state"
)

func quotaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quota",
		Short: "Manage global resource quotas for sandboxes",
		Long: `Set, view, and enforce resource quotas that limit total resource usage
across all sandboxes. Quotas help prevent resource exhaustion on the host.

Examples:
  tent quota set --max-sandboxes 10 --max-vcpus 16 --max-memory 32768
  tent quota get
  tent quota status
  tent quota reset`,
	}

	cmd.AddCommand(quotaSetCmd())
	cmd.AddCommand(quotaGetCmd())
	cmd.AddCommand(quotaStatusCmd())
	cmd.AddCommand(quotaResetCmd())

	return cmd
}

func quotaSetCmd() *cobra.Command {
	var (
		maxSandboxes         int
		maxTotalVCPUs        int
		maxTotalMemoryMB     int
		maxTotalDiskGB       int
		maxVCPUsPerSandbox   int
		maxMemoryPerSandbox  int
		maxDiskPerSandbox    int
		outputJSON           bool
	)

	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set global resource quotas",
		Long: `Set resource quotas that limit total resource usage across all sandboxes.
Set a value to 0 to remove that specific limit.

Global limits apply to the sum of all sandboxes. Per-sandbox limits apply
to each individual sandbox.

Examples:
  tent quota set --max-sandboxes 10
  tent quota set --max-vcpus 16 --max-memory 32768 --max-disk 500
  tent quota set --max-vcpus-per-sandbox 4 --max-memory-per-sandbox 8192
  tent quota set --max-sandboxes 0    # remove sandbox count limit`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			qm := vm.NewQuotaManager(baseDir)

			// Load existing config to merge with
			cfg, err := qm.Get()
			if err != nil {
				return fmt.Errorf("failed to load quota config: %w", err)
			}

			// Apply only explicitly set flags
			if cmd.Flags().Changed("max-sandboxes") {
				cfg.MaxSandboxes = maxSandboxes
			}
			if cmd.Flags().Changed("max-vcpus") {
				cfg.MaxTotalVCPUs = maxTotalVCPUs
			}
			if cmd.Flags().Changed("max-memory") {
				cfg.MaxTotalMemoryMB = maxTotalMemoryMB
			}
			if cmd.Flags().Changed("max-disk") {
				cfg.MaxTotalDiskGB = maxTotalDiskGB
			}
			if cmd.Flags().Changed("max-vcpus-per-sandbox") {
				cfg.MaxVCPUsPerSandbox = maxVCPUsPerSandbox
			}
			if cmd.Flags().Changed("max-memory-per-sandbox") {
				cfg.MaxMemoryPerSandboxMB = maxMemoryPerSandbox
			}
			if cmd.Flags().Changed("max-disk-per-sandbox") {
				cfg.MaxDiskPerSandboxGB = maxDiskPerSandbox
			}

			if err := qm.Set(cfg); err != nil {
				return fmt.Errorf("failed to save quota config: %w", err)
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(cfg)
			}

			fmt.Println("Quota configuration updated.")
			printQuotaConfig(cfg)
			return nil
		},
	}

	cmd.Flags().IntVar(&maxSandboxes, "max-sandboxes", 0, "Maximum number of sandboxes (0 = unlimited)")
	cmd.Flags().IntVar(&maxTotalVCPUs, "max-vcpus", 0, "Maximum total vCPUs across all sandboxes (0 = unlimited)")
	cmd.Flags().IntVar(&maxTotalMemoryMB, "max-memory", 0, "Maximum total memory in MB (0 = unlimited)")
	cmd.Flags().IntVar(&maxTotalDiskGB, "max-disk", 0, "Maximum total disk in GB (0 = unlimited)")
	cmd.Flags().IntVar(&maxVCPUsPerSandbox, "max-vcpus-per-sandbox", 0, "Maximum vCPUs per sandbox (0 = unlimited)")
	cmd.Flags().IntVar(&maxMemoryPerSandbox, "max-memory-per-sandbox", 0, "Maximum memory per sandbox in MB (0 = unlimited)")
	cmd.Flags().IntVar(&maxDiskPerSandbox, "max-disk-per-sandbox", 0, "Maximum disk per sandbox in GB (0 = unlimited)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")

	return cmd
}

func quotaGetCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show current quota configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			qm := vm.NewQuotaManager(baseDir)

			cfg, err := qm.Get()
			if err != nil {
				return fmt.Errorf("failed to load quota config: %w", err)
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(cfg)
			}

			if isQuotaEmpty(cfg) {
				fmt.Println("No quotas configured. Use 'tent quota set' to define limits.")
				return nil
			}

			printQuotaConfig(cfg)
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

func quotaStatusCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show current resource usage against quota limits",
		Long: `Display current resource usage for all sandboxes alongside configured quota
limits. Shows whether usage is within limits, approaching limits (warning),
or has exceeded limits.

Examples:
  tent quota status
  tent quota status --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			qm := vm.NewQuotaManager(baseDir)

			cfg, err := qm.Get()
			if err != nil {
				return fmt.Errorf("failed to load quota config: %w", err)
			}

			// Get sandbox stats
			sm, err := state.NewStateManager("")
			if err != nil {
				return fmt.Errorf("failed to initialize state manager: %w", err)
			}

			vms, err := sm.ListVMs()
			if err != nil {
				return fmt.Errorf("failed to list sandboxes: %w", err)
			}

			var totalVCPUs, totalMemoryMB, totalDiskGB int
			for _, v := range vms {
				totalVCPUs += v.VCPUs
				totalMemoryMB += v.MemoryMB
				totalDiskGB += v.DiskGB
			}

			usage := qm.ComputeUsage(cfg, len(vms), totalVCPUs, totalMemoryMB, totalDiskGB)

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(usage)
			}

			printQuotaStatus(usage)
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

func quotaResetCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Remove all quota limits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			qm := vm.NewQuotaManager(baseDir)

			if !force {
				fmt.Println("This will remove all resource quotas. Use --force to confirm.")
				return nil
			}

			if err := qm.Reset(); err != nil {
				return fmt.Errorf("failed to reset quotas: %w", err)
			}

			fmt.Println("All quota limits have been removed.")
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	return cmd
}

func printQuotaConfig(cfg *vm.QuotaConfig) {
	fmt.Println("\nGlobal Limits:")
	fmt.Printf("  Max sandboxes:        %s\n", formatLimit(cfg.MaxSandboxes))
	fmt.Printf("  Max total vCPUs:      %s\n", formatLimit(cfg.MaxTotalVCPUs))
	fmt.Printf("  Max total memory:     %s\n", formatLimitMB(cfg.MaxTotalMemoryMB))
	fmt.Printf("  Max total disk:       %s\n", formatLimitGB(cfg.MaxTotalDiskGB))
	fmt.Println("\nPer-Sandbox Limits:")
	fmt.Printf("  Max vCPUs/sandbox:    %s\n", formatLimit(cfg.MaxVCPUsPerSandbox))
	fmt.Printf("  Max memory/sandbox:   %s\n", formatLimitMB(cfg.MaxMemoryPerSandboxMB))
	fmt.Printf("  Max disk/sandbox:     %s\n", formatLimitGB(cfg.MaxDiskPerSandboxGB))
}

func printQuotaStatus(usage *vm.QuotaUsage) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "RESOURCE\tCURRENT\tLIMIT\tSTATUS\n")

	printStatusRow(w, "Sandboxes", usage.Sandboxes, "")
	printStatusRow(w, "Total vCPUs", usage.TotalVCPUs, "")
	printStatusRow(w, "Total Memory", usage.TotalMemory, " MB")
	printStatusRow(w, "Total Disk", usage.TotalDisk, " GB")

	w.Flush()
}

func printStatusRow(w *tabwriter.Writer, name string, item vm.QuotaItem, suffix string) {
	limitStr := "unlimited"
	if item.Limit > 0 {
		limitStr = fmt.Sprintf("%d%s", item.Limit, suffix)
	}

	statusStr := item.Status
	switch item.Status {
	case "exceeded":
		statusStr = "EXCEEDED"
	case "warning":
		statusStr = "WARNING (>80%)"
	case "ok":
		statusStr = "OK"
	case "unlimited":
		statusStr = "-"
	}

	fmt.Fprintf(w, "%s\t%d%s\t%s\t%s\n", name, item.Current, suffix, limitStr, statusStr)
}

func formatLimit(v int) string {
	if v <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", v)
}

func formatLimitMB(v int) string {
	if v <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d MB", v)
}

func formatLimitGB(v int) string {
	if v <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d GB", v)
}

func isQuotaEmpty(cfg *vm.QuotaConfig) bool {
	return cfg.MaxSandboxes == 0 &&
		cfg.MaxTotalVCPUs == 0 &&
		cfg.MaxTotalMemoryMB == 0 &&
		cfg.MaxTotalDiskGB == 0 &&
		cfg.MaxVCPUsPerSandbox == 0 &&
		cfg.MaxMemoryPerSandboxMB == 0 &&
		cfg.MaxDiskPerSandboxGB == 0
}
