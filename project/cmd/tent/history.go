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

func historyCmd() *cobra.Command {
	var (
		limit      int
		since      string
		until      string
		eventType  string
		outputFmt  string
		noHeader   bool
		reverse    bool
		quiet      bool
	)

	cmd := &cobra.Command{
		Use:   "history [name]",
		Short: "Show lifecycle event history for sandboxes",
		Long: `Display the history of lifecycle events for one or all sandboxes.

Shows timestamped records of sandbox creation, start, stop, destroy,
snapshot, network changes, and other lifecycle events.

Without a sandbox name, shows events across all sandboxes.
With a name, filters to that sandbox only.

Examples:
  tent history
  tent history mybox
  tent history --limit 20
  tent history --since 1h
  tent history --type start,stop
  tent history mybox --output json
  tent history --until 2024-01-01
  tent history --reverse`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			logger := vm.NewEventLogger(baseDir)

			filter := vm.EventFilter{}

			if len(args) > 0 {
				filter.Sandbox = args[0]
			}

			if eventType != "" {
				filter.Type = vm.EventType(eventType)
			}

			if since != "" {
				t, err := parseSinceTime(since)
				if err != nil {
					return fmt.Errorf("invalid --since value %q: %w", since, err)
				}
				filter.Since = t
			}

			if limit > 0 {
				filter.Limit = limit
			}

			events, err := logger.Query(filter)
			if err != nil {
				return fmt.Errorf("failed to query events: %w", err)
			}

			// Apply --until filter
			if until != "" {
				untilTime, err := parseUntilTime(until)
				if err != nil {
					return fmt.Errorf("invalid --until value %q: %w", until, err)
				}
				filtered := events[:0]
				for _, ev := range events {
					if ev.Timestamp.Before(untilTime) || ev.Timestamp.Equal(untilTime) {
						filtered = append(filtered, ev)
					}
				}
				events = filtered
			}

			// Apply multi-type filter (comma-separated)
			if eventType != "" && strings.Contains(eventType, ",") {
				types := make(map[string]bool)
				for _, t := range strings.Split(eventType, ",") {
					types[strings.TrimSpace(t)] = true
				}
				// Re-query without type filter, then apply multi-type
				filter.Type = ""
				allEvents, err := logger.Query(filter)
				if err != nil {
					return fmt.Errorf("failed to query events: %w", err)
				}
				events = events[:0]
				for _, ev := range allEvents {
					if types[string(ev.Type)] {
						events = append(events, ev)
					}
				}
				if limit > 0 && len(events) > limit {
					events = events[len(events)-limit:]
				}
			}

			if reverse {
				for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
					events[i], events[j] = events[j], events[i]
				}
			}

			if len(events) == 0 {
				if quiet {
					return nil
				}
				fmt.Println("No events found.")
				return nil
			}

			switch outputFmt {
			case "json":
				return printHistoryJSON(events)
			case "jsonl":
				return printHistoryJSONL(events)
			default:
				return printHistoryTable(events, noHeader, quiet)
			}
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum number of events to show")
	cmd.Flags().StringVar(&since, "since", "", "Show events since duration (e.g., 1h, 30m, 7d) or RFC3339 time")
	cmd.Flags().StringVar(&until, "until", "", "Show events until RFC3339 time or date (e.g., 2024-01-01)")
	cmd.Flags().StringVarP(&eventType, "type", "t", "", "Filter by event type (comma-separated: start,stop,create)")
	cmd.Flags().StringVarP(&outputFmt, "output", "o", "table", "Output format: table, json, jsonl")
	cmd.Flags().BoolVar(&noHeader, "no-header", false, "Hide table header")
	cmd.Flags().BoolVar(&reverse, "reverse", false, "Show oldest events first")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only show event types and sandbox names")

	return cmd
}

func printHistoryTable(events []vm.Event, noHeader, quiet bool) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)

	if quiet {
		for _, ev := range events {
			fmt.Fprintf(w, "%s\t%s\n", ev.Type, ev.Sandbox)
		}
		w.Flush()
		return nil
	}

	if !noHeader {
		fmt.Fprintf(w, "TIMESTAMP\tTYPE\tSANDBOX\tDETAILS\n")
	}

	for _, ev := range events {
		ts := ev.Timestamp.Local().Format("2006-01-02 15:04:05")
		details := formatEventDetails(ev.Details)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", ts, ev.Type, ev.Sandbox, details)
	}

	w.Flush()
	return nil
}

func printHistoryJSON(events []vm.Event) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(events)
}

func printHistoryJSONL(events []vm.Event) error {
	enc := json.NewEncoder(os.Stdout)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
	return nil
}

func formatEventDetails(details map[string]string) string {
	if len(details) == 0 {
		return "-"
	}

	parts := make([]string, 0, len(details))
	for k, v := range details {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ", ")
}

// parseSinceTime parses a human-friendly duration string or absolute timestamp
// and returns the corresponding time.Time. Supports: 30s, 5m, 1h, 7d, or RFC3339/date.
func parseSinceTime(s string) (time.Time, error) {
	// Try RFC3339 first
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	// Try date-only format
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}

	// Try as a duration relative to now using the shared parseDuration
	d, err := parseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected duration (e.g., 1h, 7d) or RFC3339/date: %w", err)
	}

	return time.Now().UTC().Add(-d), nil
}

// parseUntilTime parses RFC3339 or date-only timestamps
func parseUntilTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expected RFC3339 or YYYY-MM-DD format")
}
