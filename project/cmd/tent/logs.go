package main

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

// ConfigureLogsCmd creates a new logs command with optional dependencies
func ConfigureLogsCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	var follow bool
	var tail int
	var clear bool

	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "View microVM console/boot logs",
		Long: `View microVM console/boot logs.

Use --follow (-f) to stream logs in real time.
Use --tail (-n) to show only the last N lines.
Use --clear to remove all logs for a sandbox.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

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

			// Handle --clear flag
			if clear {
				if err := manager.ClearLogs(name); err != nil {
					return fmt.Errorf("failed to clear logs: %w", err)
				}
				fmt.Printf("Cleared logs for VM: %s\n", name)
				return nil
			}

			// Handle --follow flag
			if follow {
				done := make(chan struct{})
				sig := make(chan os.Signal, 1)
				signal.Notify(sig, os.Interrupt)
				go func() {
					<-sig
					close(done)
				}()

				fmt.Printf("Following logs for VM: %s (Ctrl+C to stop)\n", name)
				return manager.FollowLogs(name, tail, os.Stdout, done)
			}

			// Handle --tail flag
			if tail > 0 {
				logs, err := manager.TailLogs(name, tail)
				if err != nil {
					return fmt.Errorf("failed to get VM logs: %w", err)
				}
				fmt.Print(logs)
				return nil
			}

			// Default: show all logs
			logs, err := manager.Logs(name)
			if err != nil {
				return fmt.Errorf("failed to get VM logs: %w", err)
			}

			fmt.Print(logs)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output in real time")
	cmd.Flags().IntVarP(&tail, "tail", "n", 0, "Show only the last N lines")
	cmd.Flags().BoolVar(&clear, "clear", false, "Clear all logs for the sandbox")

	return cmd
}

// logsCmd is a convenience function that uses default dependencies
func logsCmd() *cobra.Command {
	return ConfigureLogsCmd()
}
