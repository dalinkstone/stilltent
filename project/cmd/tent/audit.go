package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/state"
)

func auditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "View and manage the sandbox operation audit log",
		Long: `Query, inspect, and manage the audit trail of all sandbox operations.

The audit log records every sandbox lifecycle action (create, start, stop, destroy,
exec, snapshot, etc.) with timestamps, user info, and success/failure status.

Examples:
  tent audit list
  tent audit list --sandbox mybox
  tent audit list --action create --since 24h
  tent audit list --limit 50 --json
  tent audit stats
  tent audit clear`,
	}

	cmd.AddCommand(auditListCmd())
	cmd.AddCommand(auditStatsCmd())
	cmd.AddCommand(auditClearCmd())

	return cmd
}

func auditListCmd() *cobra.Command {
	var (
		sandbox   string
		action    string
		since     string
		until     string
		limit     int
		onlyFails bool
		jsonOut   bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List audit log entries",
		Long: `Display audit log entries with optional filtering.

Examples:
  tent audit list
  tent audit list --sandbox mybox
  tent audit list --action create
  tent audit list --since 1h
  tent audit list --since 24h --action exec
  tent audit list --fails
  tent audit list --limit 20 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			auditLog, err := state.NewAuditLog("")
			if err != nil {
				return fmt.Errorf("failed to open audit log: %w", err)
			}

			filter := state.AuditFilter{
				Sandbox: sandbox,
				Action:  action,
				Limit:   limit,
			}

			if onlyFails {
				f := false
				filter.Success = &f
			}

			if since != "" {
				d, err := parseDuration(since)
				if err != nil {
					return fmt.Errorf("invalid --since value: %w", err)
				}
				filter.Since = time.Now().UTC().Add(-d)
			}

			if until != "" {
				d, err := parseDuration(until)
				if err != nil {
					return fmt.Errorf("invalid --until value: %w", err)
				}
				filter.Until = time.Now().UTC().Add(-d)
			}

			entries, err := auditLog.Query(filter)
			if err != nil {
				return fmt.Errorf("failed to query audit log: %w", err)
			}

			if len(entries) == 0 {
				fmt.Println("No audit entries found.")
				return nil
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "TIMESTAMP\tACTION\tSANDBOX\tUSER\tSTATUS\tDETAILS\n")
			for _, e := range entries {
				status := "ok"
				if !e.Success {
					status = "FAIL"
				}
				details := formatDetails(e)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					e.Timestamp.Format("2006-01-02 15:04:05"),
					e.Action,
					e.Sandbox,
					e.User,
					status,
					details,
				)
			}
			w.Flush()

			fmt.Fprintf(os.Stderr, "\nShowing %d entries\n", len(entries))
			return nil
		},
	}

	cmd.Flags().StringVar(&sandbox, "sandbox", "", "Filter by sandbox name")
	cmd.Flags().StringVar(&action, "action", "", "Filter by action (create, start, stop, destroy, exec, snapshot, etc.)")
	cmd.Flags().StringVar(&since, "since", "", "Show entries since duration ago (e.g. 1h, 24h, 7d)")
	cmd.Flags().StringVar(&until, "until", "", "Show entries until duration ago")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum number of entries to show")
	cmd.Flags().BoolVar(&onlyFails, "fails", false, "Show only failed operations")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")

	return cmd
}

func auditStatsCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show audit log statistics",
		Long: `Display aggregate statistics from the audit log.

Shows the count of each operation type and overall totals.

Examples:
  tent audit stats
  tent audit stats --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			auditLog, err := state.NewAuditLog("")
			if err != nil {
				return fmt.Errorf("failed to open audit log: %w", err)
			}

			counts, total, err := auditLog.Stats()
			if err != nil {
				return fmt.Errorf("failed to compute stats: %w", err)
			}

			if total == 0 {
				fmt.Println("No audit entries recorded yet.")
				return nil
			}

			if jsonOut {
				data := map[string]interface{}{
					"total":   total,
					"actions": counts,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(data)
			}

			// Sort actions by count (descending)
			type actionCount struct {
				action string
				count  int
			}
			var sorted []actionCount
			for a, c := range counts {
				sorted = append(sorted, actionCount{a, c})
			}
			sort.Slice(sorted, func(i, j int) bool {
				return sorted[i].count > sorted[j].count
			})

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "ACTION\tCOUNT\n")
			for _, ac := range sorted {
				fmt.Fprintf(w, "%s\t%d\n", ac.action, ac.count)
			}
			fmt.Fprintf(w, "---\t---\n")
			fmt.Fprintf(w, "TOTAL\t%d\n", total)
			w.Flush()

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")

	return cmd
}

func auditClearCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear the audit log",
		Long: `Remove all entries from the audit log.

Examples:
  tent audit clear --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				return fmt.Errorf("this will delete all audit log entries; use --force to confirm")
			}

			auditLog, err := state.NewAuditLog("")
			if err != nil {
				return fmt.Errorf("failed to open audit log: %w", err)
			}

			if err := auditLog.Clear(); err != nil {
				if os.IsNotExist(err) {
					fmt.Println("Audit log is already empty.")
					return nil
				}
				return fmt.Errorf("failed to clear audit log: %w", err)
			}

			fmt.Println("Audit log cleared.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Confirm clearing the audit log")

	return cmd
}

func formatDetails(e state.AuditEntry) string {
	if e.Error != "" && !e.Success {
		return e.Error
	}
	if len(e.Details) == 0 {
		return ""
	}
	var parts []string
	for k, v := range e.Details {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// parseDuration handles standard Go durations plus "d" for days.
func parseDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		s = strings.TrimSuffix(s, "d")
		var days int
		if _, err := fmt.Sscanf(s, "%d", &days); err != nil {
			return 0, fmt.Errorf("invalid duration: %sd", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
