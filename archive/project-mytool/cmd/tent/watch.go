package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func watchCmd() *cobra.Command {
	var (
		sandboxName string
		eventType   string
		interval    string
		jsonOutput  bool
		noColor     bool
	)

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch sandbox events in real time",
		Long: `Stream sandbox lifecycle events as they happen. Events are tailed from the
event log and displayed in real time with optional filtering.

This is similar to 'tent events' but continuously watches for new events
instead of showing historical ones.

Examples:
  tent watch
  tent watch --sandbox mybox
  tent watch --type start
  tent watch --type stop --sandbox mybox
  tent watch --interval 500ms
  tent watch --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			logger := vm.NewEventLogger(baseDir)

			pollInterval := 1 * time.Second
			if interval != "" {
				d, err := time.ParseDuration(interval)
				if err != nil {
					return fmt.Errorf("invalid interval %q: %w", interval, err)
				}
				if d < 100*time.Millisecond {
					return fmt.Errorf("interval must be at least 100ms")
				}
				pollInterval = d
			}

			filter := vm.EventFilter{
				Sandbox: sandboxName,
				Type:    vm.EventType(eventType),
			}

			// Print header
			if !jsonOutput {
				parts := []string{"Watching events"}
				if sandboxName != "" {
					parts = append(parts, fmt.Sprintf("sandbox=%s", sandboxName))
				}
				if eventType != "" {
					parts = append(parts, fmt.Sprintf("type=%s", eventType))
				}
				fmt.Printf("%s (press Ctrl+C to stop)\n", strings.Join(parts, " "))
				fmt.Println(strings.Repeat("─", 72))
			}

			done := make(chan struct{})
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			eventCh := logger.Watch(filter, pollInterval, done)

			for {
				select {
				case <-sigCh:
					close(done)
					fmt.Println("\nStopped watching.")
					return nil
				case we, ok := <-eventCh:
					if !ok {
						return nil
					}
					if we.Err != nil {
						fmt.Fprintf(os.Stderr, "Error: %v\n", we.Err)
						continue
					}
					printWatchEvent(we.Event, jsonOutput, noColor)
				}
			}
		},
	}

	cmd.Flags().StringVarP(&sandboxName, "sandbox", "s", "", "Filter events by sandbox name")
	cmd.Flags().StringVarP(&eventType, "type", "t", "", "Filter by event type")
	cmd.Flags().StringVarP(&interval, "interval", "i", "1s", "Poll interval (e.g. 500ms, 1s, 2s)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output events as JSON lines")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "Disable colored output")

	return cmd
}

func printWatchEvent(e vm.Event, jsonOut bool, noColor bool) {
	if jsonOut {
		fmt.Printf(`{"timestamp":"%s","type":"%s","sandbox":"%s"`, e.Timestamp.Format(time.RFC3339), e.Type, e.Sandbox)
		if len(e.Details) > 0 {
			fmt.Print(`,"details":{`)
			i := 0
			for k, v := range e.Details {
				if i > 0 {
					fmt.Print(",")
				}
				fmt.Printf(`"%s":"%s"`, k, v)
				i++
			}
			fmt.Print("}")
		}
		fmt.Println("}")
		return
	}

	ts := e.Timestamp.Local().Format("15:04:05")
	typeStr := string(e.Type)
	icon := eventIcon(e.Type)

	detail := ""
	for k, v := range e.Details {
		if detail != "" {
			detail += ", "
		}
		detail += fmt.Sprintf("%s=%s", k, v)
	}

	if noColor {
		if detail != "" {
			fmt.Printf("%s  %s %-22s %-20s  %s\n", ts, icon, typeStr, e.Sandbox, detail)
		} else {
			fmt.Printf("%s  %s %-22s %s\n", ts, icon, typeStr, e.Sandbox)
		}
		return
	}

	// ANSI colors
	color := eventColor(e.Type)
	reset := "\033[0m"
	dim := "\033[2m"
	bold := "\033[1m"

	if detail != "" {
		fmt.Printf("%s%s%s  %s %s%-22s%s %s%-20s%s  %s%s%s\n",
			dim, ts, reset, icon, color, typeStr, reset, bold, e.Sandbox, reset, dim, detail, reset)
	} else {
		fmt.Printf("%s%s%s  %s %s%-22s%s %s%s%s\n",
			dim, ts, reset, icon, color, typeStr, reset, bold, e.Sandbox, reset)
	}
}

func eventIcon(t vm.EventType) string {
	switch {
	case t == vm.EventCreate:
		return "+"
	case t == vm.EventStart:
		return ">"
	case t == vm.EventStop:
		return "#"
	case t == vm.EventDestroy:
		return "x"
	case t == vm.EventRestart:
		return "~"
	case t == vm.EventPause:
		return "|"
	case t == vm.EventUnpause:
		return ">"
	case strings.HasPrefix(string(t), "snapshot."):
		return "S"
	case strings.HasPrefix(string(t), "checkpoint."):
		return "C"
	case strings.HasPrefix(string(t), "network."):
		return "N"
	case strings.HasPrefix(string(t), "hook."):
		return "H"
	default:
		return "*"
	}
}

func eventColor(t vm.EventType) string {
	green := "\033[32m"
	red := "\033[31m"
	yellow := "\033[33m"
	blue := "\033[34m"
	cyan := "\033[36m"
	magenta := "\033[35m"

	switch {
	case t == vm.EventCreate || t == vm.EventClone:
		return green
	case t == vm.EventStart || t == vm.EventUnpause:
		return green
	case t == vm.EventStop || t == vm.EventPause:
		return yellow
	case t == vm.EventDestroy || t == vm.EventPrune:
		return red
	case strings.HasPrefix(string(t), "snapshot.") || strings.HasPrefix(string(t), "checkpoint."):
		return cyan
	case strings.HasPrefix(string(t), "network."):
		return blue
	case strings.HasPrefix(string(t), "hook."):
		return magenta
	default:
		return ""
	}
}
