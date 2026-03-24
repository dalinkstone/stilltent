package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func eventsCmd() *cobra.Command {
	var (
		sandboxName string
		eventType   string
		since       string
		limit       int
		quiet       bool
	)

	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show sandbox lifecycle events",
		Long:  `Display a log of sandbox lifecycle events such as create, start, stop, destroy, snapshot operations, and more.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			logger := vm.NewEventLogger(baseDir)

			filter := vm.EventFilter{
				Sandbox: sandboxName,
				Type:    vm.EventType(eventType),
				Limit:   limit,
			}

			if since != "" {
				d, err := time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("invalid duration %q: %w (use e.g. 1h, 30m, 24h)", since, err)
				}
				filter.Since = time.Now().UTC().Add(-d)
			}

			events, err := logger.Query(filter)
			if err != nil {
				return fmt.Errorf("failed to query events: %w", err)
			}

			if len(events) == 0 {
				fmt.Println("No events found.")
				return nil
			}

			for _, e := range events {
				ts := e.Timestamp.Local().Format("2006-01-02 15:04:05")
				if quiet {
					fmt.Printf("%s %s %s\n", ts, e.Type, e.Sandbox)
				} else {
					detail := ""
					for k, v := range e.Details {
						if detail != "" {
							detail += ", "
						}
						detail += fmt.Sprintf("%s=%s", k, v)
					}
					if detail != "" {
						fmt.Printf("%s  %-20s  %-20s  %s\n", ts, e.Type, e.Sandbox, detail)
					} else {
						fmt.Printf("%s  %-20s  %s\n", ts, e.Type, e.Sandbox)
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&sandboxName, "sandbox", "s", "", "Filter events by sandbox name")
	cmd.Flags().StringVarP(&eventType, "type", "t", "", "Filter by event type (create, start, stop, destroy, etc.)")
	cmd.Flags().StringVar(&since, "since", "", "Show events since duration ago (e.g. 1h, 30m, 24h)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum number of events to show")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Show compact output")

	return cmd
}
