package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/console"
)

func replayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Record and replay sandbox console sessions",
		Long: `Record sandbox console sessions with timing and replay them later.

Recordings capture all console output with precise timing data, allowing
faithful playback at variable speed. Recordings can also be exported to
asciicast v2 format for sharing via asciinema.

Subcommands:
  list     - List available recordings
  play     - Replay a recorded session
  delete   - Delete a recording
  export   - Export a recording to asciicast format
  inspect  - Show recording metadata and statistics`,
	}

	cmd.AddCommand(replayListCmd())
	cmd.AddCommand(replayPlayCmd())
	cmd.AddCommand(replayDeleteCmd())
	cmd.AddCommand(replayExportCmd())
	cmd.AddCommand(replayInspectCmd())

	return cmd
}

func replayListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list [sandbox]",
		Short: "List available recordings",
		Long:  `List all recorded console sessions, optionally filtered by sandbox name.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			var infos []console.RecordingInfo
			var err error

			if len(args) == 1 {
				infos, err = console.ListRecordings(baseDir, args[0])
			} else {
				infos, err = console.ListAllRecordings(baseDir)
			}
			if err != nil {
				return fmt.Errorf("failed to list recordings: %w", err)
			}

			if len(infos) == 0 {
				if len(args) == 1 {
					fmt.Printf("No recordings found for sandbox %q\n", args[0])
				} else {
					fmt.Println("No recordings found")
				}
				return nil
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(infos)
			}

			fmt.Printf("%-20s %-20s %-12s %-8s %-10s\n",
				"SANDBOX", "TAG", "DURATION", "EVENTS", "SIZE")
			for _, info := range infos {
				fmt.Printf("%-20s %-20s %-12s %-8d %-10s\n",
					replayTruncate(info.Sandbox, 20),
					replayTruncate(info.Tag, 20),
					replayFormatDuration(info.Duration),
					info.Events,
					replayFormatBytes(info.Size),
				)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func replayPlayCmd() *cobra.Command {
	var (
		speed    float64
		maxDelay string
	)

	cmd := &cobra.Command{
		Use:   "play <sandbox> <tag>",
		Short: "Replay a recorded session",
		Long: `Replay a previously recorded console session with timing preserved.

The --speed flag controls playback speed:
  1.0  = realtime (default)
  2.0  = 2x speed
  0.5  = half speed

The --max-delay flag caps the maximum pause between events, useful for
sessions with long idle periods.

Press Ctrl+C to stop playback.

Examples:
  tent replay play mybox 20260322-143021
  tent replay play mybox latest --speed 2.0
  tent replay play mybox session1 --max-delay 2s`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			tag := args[1]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			// Handle "latest" tag
			if tag == "latest" {
				infos, err := console.ListRecordings(baseDir, sandboxName)
				if err != nil {
					return err
				}
				if len(infos) == 0 {
					return fmt.Errorf("no recordings found for sandbox %q", sandboxName)
				}
				tag = infos[len(infos)-1].Tag
			}

			path := fmt.Sprintf("%s/recordings/%s/%s.json", baseDir, sandboxName, tag)
			rec, err := console.LoadRecording(path)
			if err != nil {
				return fmt.Errorf("failed to load recording: %w", err)
			}

			var maxDelayDur time.Duration
			if maxDelay != "" {
				maxDelayDur, err = time.ParseDuration(maxDelay)
				if err != nil {
					return fmt.Errorf("invalid --max-delay value: %w", err)
				}
			}

			fmt.Fprintf(os.Stderr, "Replaying session %q from sandbox %q (%.1fx speed)\n",
				tag, sandboxName, speed)
			fmt.Fprintf(os.Stderr, "Recorded at %s, duration %s\n",
				rec.StartedAt.Format(time.RFC3339), replayFormatDuration(rec.Duration))
			fmt.Fprintf(os.Stderr, "---\n")

			// Set up cancellation
			done := make(chan struct{})
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				close(done)
			}()

			if err := console.Replay(rec, os.Stdout, speed, maxDelayDur, done); err != nil {
				return fmt.Errorf("replay error: %w", err)
			}

			fmt.Fprintf(os.Stderr, "\n--- replay complete ---\n")
			return nil
		},
	}

	cmd.Flags().Float64Var(&speed, "speed", 1.0, "Playback speed multiplier (e.g., 2.0 for 2x)")
	cmd.Flags().StringVar(&maxDelay, "max-delay", "", "Maximum delay between events (e.g., 2s, 500ms)")
	return cmd
}

func replayDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <sandbox> <tag>",
		Short: "Delete a recording",
		Long:  `Delete a specific recorded console session.`,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			tag := args[1]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			if !force {
				fmt.Printf("Delete recording %q for sandbox %q? [y/N] ", tag, sandboxName)
				var confirm string
				fmt.Scanln(&confirm)
				if strings.ToLower(confirm) != "y" {
					fmt.Println("Cancelled")
					return nil
				}
			}

			if err := console.DeleteRecording(baseDir, sandboxName, tag); err != nil {
				return err
			}

			fmt.Printf("Deleted recording %q for sandbox %q\n", tag, sandboxName)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")
	return cmd
}

func replayExportCmd() *cobra.Command {
	var outputFile string

	cmd := &cobra.Command{
		Use:   "export <sandbox> <tag>",
		Short: "Export a recording to asciicast format",
		Long: `Export a recorded session in asciicast v2 format, compatible with asciinema.

The exported file can be uploaded to asciinema.org or played locally with
the asciinema CLI tool.

Examples:
  tent replay export mybox session1 -o session.cast
  tent replay export mybox latest | asciinema play -`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			tag := args[1]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			// Handle "latest" tag
			if tag == "latest" {
				infos, err := console.ListRecordings(baseDir, sandboxName)
				if err != nil {
					return err
				}
				if len(infos) == 0 {
					return fmt.Errorf("no recordings found for sandbox %q", sandboxName)
				}
				tag = infos[len(infos)-1].Tag
			}

			path := fmt.Sprintf("%s/recordings/%s/%s.json", baseDir, sandboxName, tag)
			rec, err := console.LoadRecording(path)
			if err != nil {
				return fmt.Errorf("failed to load recording: %w", err)
			}

			var out *os.File
			if outputFile != "" && outputFile != "-" {
				out, err = os.Create(outputFile)
				if err != nil {
					return fmt.Errorf("failed to create output file: %w", err)
				}
				defer out.Close()
			} else {
				out = os.Stdout
			}

			if err := console.ExportAsciicast(rec, out); err != nil {
				return fmt.Errorf("export error: %w", err)
			}

			if outputFile != "" && outputFile != "-" {
				fmt.Fprintf(os.Stderr, "Exported to %s\n", outputFile)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output file (default: stdout)")
	return cmd
}

func replayInspectCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "inspect <sandbox> <tag>",
		Short: "Show recording metadata and statistics",
		Long:  `Display detailed information about a recorded session including timing statistics.`,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			tag := args[1]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			path := fmt.Sprintf("%s/recordings/%s/%s.json", baseDir, sandboxName, tag)
			rec, err := console.LoadRecording(path)
			if err != nil {
				return fmt.Errorf("failed to load recording: %w", err)
			}

			// Calculate statistics
			var totalOutput, totalInput int64
			var outputEvents, inputEvents int
			var maxDelay time.Duration

			for _, evt := range rec.Events {
				switch evt.Direction {
				case "o":
					totalOutput += int64(len(evt.Data))
					outputEvents++
				case "i":
					totalInput += int64(len(evt.Data))
					inputEvents++
				}
				if evt.Delay > maxDelay {
					maxDelay = evt.Delay
				}
			}

			info := map[string]interface{}{
				"sandbox":       rec.SandboxName,
				"tag":           tag,
				"started_at":    rec.StartedAt.Format(time.RFC3339),
				"duration":      replayFormatDuration(rec.Duration),
				"duration_ns":   rec.Duration,
				"total_events":  len(rec.Events),
				"output_events": outputEvents,
				"input_events":  inputEvents,
				"output_bytes":  totalOutput,
				"input_bytes":   totalInput,
				"max_delay":     replayFormatDuration(maxDelay),
				"metadata":      rec.Metadata,
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}

			fmt.Printf("Recording: %s/%s\n", sandboxName, tag)
			fmt.Printf("  Started:       %s\n", rec.StartedAt.Format(time.RFC3339))
			fmt.Printf("  Duration:      %s\n", replayFormatDuration(rec.Duration))
			fmt.Printf("  Total events:  %d\n", len(rec.Events))
			fmt.Printf("  Output events: %d (%s)\n", outputEvents, replayFormatBytes(totalOutput))
			fmt.Printf("  Input events:  %d (%s)\n", inputEvents, replayFormatBytes(totalInput))
			fmt.Printf("  Max delay:     %s\n", replayFormatDuration(maxDelay))

			if len(rec.Metadata) > 0 {
				fmt.Println("  Metadata:")
				for k, v := range rec.Metadata {
					fmt.Printf("    %s: %s\n", k, v)
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func replayFormatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

func replayFormatBytes(b int64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	default:
		return fmt.Sprintf("%dB", b)
	}
}

func replayTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
