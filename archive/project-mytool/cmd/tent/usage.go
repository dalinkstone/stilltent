package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func usageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "View sandbox resource usage and accounting",
		Long: `Track and report cumulative resource usage for sandboxes including
CPU time, memory hours, network I/O, and disk usage.

Usage data is recorded automatically as sandboxes start, stop, and run.
Use the report subcommand to generate summaries for specific time periods.

Examples:
  tent usage show mybox
  tent usage list
  tent usage report --since 24h
  tent usage reset mybox`,
	}

	cmd.AddCommand(usageShowCmd())
	cmd.AddCommand(usageListCmd())
	cmd.AddCommand(usageReportCmd())
	cmd.AddCommand(usageResetCmd())

	return cmd
}

func usageShowCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "show <sandbox>",
		Short: "Show resource usage for a specific sandbox",
		Long: `Display detailed cumulative resource usage for a sandbox including
CPU seconds, memory hours, network I/O, disk I/O, uptime, and session count.

Running sandboxes include in-flight usage up to the current moment.

Examples:
  tent usage show mybox
  tent usage show mybox --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			acct, err := vm.NewAccountingManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create accounting manager: %w", err)
			}

			rec, err := acct.GetRecord(name)
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rec)
			}

			fmt.Printf("Resource usage for %q\n\n", name)
			fmt.Printf("  CPU time:       %s (%.1f vCPU-seconds)\n", vm.FormatDuration(rec.CPUSeconds), rec.CPUSeconds)
			fmt.Printf("  Memory hours:   %.2f MB-hours (%d MB allocated)\n", rec.MemoryMBHrs, rec.MemoryMB)
			fmt.Printf("  Network TX:     %s\n", vm.FormatBytes(rec.NetTxBytes))
			fmt.Printf("  Network RX:     %s\n", vm.FormatBytes(rec.NetRxBytes))
			fmt.Printf("  Disk reads:     %d ops\n", rec.DiskReadOps)
			fmt.Printf("  Disk writes:    %d ops\n", rec.DiskWriteOps)
			fmt.Printf("  Disk used:      %d MB\n", rec.DiskUsedMB)
			fmt.Printf("  Total uptime:   %s\n", vm.FormatDuration(rec.UptimeSec))
			fmt.Printf("  Sessions:       %d\n", rec.Sessions)
			fmt.Printf("  vCPUs:          %d\n", rec.VCPUs)

			if rec.LastStart != nil {
				fmt.Printf("  Last start:     %s\n", rec.LastStart.Format(time.RFC3339))
			}
			if rec.LastStop != nil {
				fmt.Printf("  Last stop:      %s\n", rec.LastStop.Format(time.RFC3339))
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func usageListCmd() *cobra.Command {
	var (
		jsonOutput bool
		sortBy     string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List resource usage for all sandboxes",
		Long: `Display a summary table of resource usage across all tracked sandboxes.

Examples:
  tent usage list
  tent usage list --sort cpu
  tent usage list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			acct, err := vm.NewAccountingManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create accounting manager: %w", err)
			}

			records := acct.ListRecords()
			if len(records) == 0 {
				fmt.Println("No usage records found.")
				return nil
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(records)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SANDBOX\tCPU-SEC\tMEM-MBHR\tNET-TX\tNET-RX\tUPTIME\tSESSIONS")

			for _, rec := range records {
				fmt.Fprintf(w, "%s\t%.1f\t%.2f\t%s\t%s\t%s\t%d\n",
					rec.Sandbox,
					rec.CPUSeconds,
					rec.MemoryMBHrs,
					vm.FormatBytes(rec.NetTxBytes),
					vm.FormatBytes(rec.NetRxBytes),
					vm.FormatDuration(rec.UptimeSec),
					rec.Sessions,
				)
			}

			_ = sortBy // reserved for future sorting
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().StringVar(&sortBy, "sort", "name", "Sort by: name, cpu, memory, uptime")
	return cmd
}

func usageReportCmd() *cobra.Command {
	var (
		since      string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Generate a usage report for a time period",
		Long: `Generate a summary report of resource usage across all sandboxes
for a specified time period. Useful for cost allocation and capacity planning.

Examples:
  tent usage report --since 24h
  tent usage report --since 7d
  tent usage report --since 30d --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			acct, err := vm.NewAccountingManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create accounting manager: %w", err)
			}

			end := time.Now().UTC()
			start, err := parseSinceDuration(since, end)
			if err != nil {
				return err
			}

			report := acct.GenerateReport(start, end)

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}

			fmt.Printf("Usage Report: %s to %s\n\n",
				report.PeriodStart.Format("2006-01-02 15:04"),
				report.PeriodEnd.Format("2006-01-02 15:04"))

			if len(report.Sandboxes) == 0 {
				fmt.Println("No usage recorded in this period.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SANDBOX\tCPU-SEC\tMEM-MBHR\tNET-TX\tNET-RX\tUPTIME")

			for name, rec := range report.Sandboxes {
				fmt.Fprintf(w, "%s\t%.1f\t%.2f\t%s\t%s\t%s\n",
					name,
					rec.CPUSeconds,
					rec.MemoryMBHrs,
					vm.FormatBytes(rec.NetTxBytes),
					vm.FormatBytes(rec.NetRxBytes),
					vm.FormatDuration(rec.UptimeSec),
				)
			}
			_ = w.Flush()

			fmt.Printf("\nTotals:\n")
			fmt.Printf("  CPU time:      %.1f vCPU-seconds\n", report.TotalCPUSec)
			fmt.Printf("  Memory hours:  %.2f MB-hours\n", report.TotalMemMBHrs)
			fmt.Printf("  Network TX:    %s\n", vm.FormatBytes(report.TotalNetTx))
			fmt.Printf("  Network RX:    %s\n", vm.FormatBytes(report.TotalNetRx))
			fmt.Printf("  Disk used:     %d MB\n", report.TotalDiskMB)

			return nil
		},
	}

	cmd.Flags().StringVar(&since, "since", "24h", "Report period (e.g. 1h, 24h, 7d, 30d)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func usageResetCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "reset [sandbox]",
		Short: "Reset usage counters for a sandbox",
		Long: `Clear cumulative usage counters for a sandbox. This zeroes out CPU time,
memory hours, network I/O, disk I/O, and session count. The record itself is
preserved so future usage continues to be tracked.

Use --all to reset all sandbox records.

Examples:
  tent usage reset mybox
  tent usage reset --all`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			acct, err := vm.NewAccountingManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create accounting manager: %w", err)
			}

			if all {
				records := acct.ListRecords()
				for _, rec := range records {
					if err := acct.ResetRecord(rec.Sandbox); err != nil {
						return fmt.Errorf("failed to reset %s: %w", rec.Sandbox, err)
					}
				}
				fmt.Printf("Reset usage counters for %d sandbox(es).\n", len(records))
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("specify a sandbox name or use --all")
			}

			name := args[0]
			if err := acct.ResetRecord(name); err != nil {
				return err
			}

			fmt.Printf("Reset usage counters for %q.\n", name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Reset all sandbox records")
	return cmd
}

// parseSinceDuration parses a human-friendly duration like "24h", "7d", "30d".
func parseSinceDuration(since string, now time.Time) (time.Time, error) {
	since = strings.TrimSpace(since)
	if since == "" {
		since = "24h"
	}

	// Handle day suffix
	if strings.HasSuffix(since, "d") {
		numStr := strings.TrimSuffix(since, "d")
		var days int
		if _, err := fmt.Sscanf(numStr, "%d", &days); err != nil {
			return time.Time{}, fmt.Errorf("invalid duration %q: %w", since, err)
		}
		if days <= 0 {
			return time.Time{}, fmt.Errorf("duration must be positive: %q", since)
		}
		return now.AddDate(0, 0, -days), nil
	}

	// Fall back to Go time.ParseDuration (supports h, m, s)
	d, err := time.ParseDuration(since)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid duration %q: must be like 1h, 24h, 7d, 30d", since)
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("duration must be positive: %q", since)
	}
	return now.Add(-d), nil
}
