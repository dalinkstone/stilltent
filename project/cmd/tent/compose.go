package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/compose"
	"github.com/dalinkstone/tent/internal/sandbox"
)

func composeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Multi-sandbox orchestration",
		Long:  "Manage groups of sandboxes defined in a YAML compose file.",
	}

	cmd.AddCommand(composeUpCmd())
	cmd.AddCommand(composeDownCmd())
	cmd.AddCommand(composeStatusCmd())
	cmd.AddCommand(composeLogsCmd())

	return cmd
}

// composeGroupName derives a compose group name from a file path
func composeGroupName(filePath string) string {
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

func newComposeManager() (*compose.ComposeManager, error) {
	baseDir := getBaseDir()

	hvBackend, err := vm.NewPlatformBackend(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create hypervisor backend: %w", err)
	}

	vmManager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM manager: %w", err)
	}

	if err := vmManager.Setup(); err != nil {
		return nil, fmt.Errorf("failed to setup VM manager: %w", err)
	}

	stateMgr := compose.NewFileStateManager(baseDir)
	return compose.NewComposeManager(baseDir, vmManager, stateMgr), nil
}

func composeUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up <file>",
		Short: "Start all sandboxes in a compose file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			// Parse the compose file
			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read compose file: %w", err)
			}

			config, err := compose.ParseConfig(data)
			if err != nil {
				return fmt.Errorf("failed to parse compose file: %w", err)
			}

			manager, err := newComposeManager()
			if err != nil {
				return err
			}

			groupName := composeGroupName(filePath)
			fmt.Printf("Starting compose group '%s' from %s...\n", groupName, filePath)

			status, err := manager.Up(groupName, config)
			if err != nil {
				return fmt.Errorf("failed to start compose group: %w", err)
			}

			for name, s := range status.Sandboxes {
				fmt.Printf("  %s: %s (IP: %s)\n", name, s.Status, s.IP)
			}

			fmt.Printf("Compose group '%s' is up (%d sandboxes)\n", groupName, len(status.Sandboxes))
			return nil
		},
	}
}

func composeDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down <file>",
		Short: "Stop and destroy all sandboxes in a compose group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			manager, err := newComposeManager()
			if err != nil {
				return err
			}

			groupName := composeGroupName(filePath)
			fmt.Printf("Stopping compose group '%s'...\n", groupName)

			if err := manager.Down(groupName); err != nil {
				return fmt.Errorf("failed to stop compose group: %w", err)
			}

			fmt.Printf("Compose group '%s' is down\n", groupName)
			return nil
		},
	}
}

func composeStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <file>",
		Short: "Show status of all sandboxes in a compose group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			manager, err := newComposeManager()
			if err != nil {
				return err
			}

			groupName := composeGroupName(filePath)
			status, err := manager.Status(groupName)
			if err != nil {
				return fmt.Errorf("failed to get compose status: %w", err)
			}

			if len(status.Sandboxes) == 0 {
				fmt.Printf("No sandboxes found for compose group '%s'\n", groupName)
				return nil
			}

			fmt.Printf("Compose group '%s':\n", groupName)
			fmt.Printf("  %-20s %-12s %-16s %s\n", "NAME", "STATUS", "IP", "PID")
			for name, s := range status.Sandboxes {
				pid := ""
				if s.PID > 0 {
					pid = fmt.Sprintf("%d", s.PID)
				}
				fmt.Printf("  %-20s %-12s %-16s %s\n", name, s.Status, s.IP, pid)
			}

			return nil
		},
	}
}

func composeLogsCmd() *cobra.Command {
	var (
		follow   bool
		tail     int
		services []string
	)

	cmd := &cobra.Command{
		Use:   "logs <file> [--service <name>]...",
		Short: "View logs from sandboxes in a compose group",
		Long: `View console logs from all sandboxes in a compose group.
Optionally filter by service name with --service. Use --follow to stream live output.

Examples:
  tent compose logs tent-compose.yaml
  tent compose logs tent-compose.yaml --service agent --service tool-runner
  tent compose logs tent-compose.yaml --follow --tail 50`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			manager, err := newComposeManager()
			if err != nil {
				return err
			}

			groupName := composeGroupName(filePath)

			if follow {
				// Stream logs until interrupted
				done := make(chan struct{})
				sigCh := make(chan os.Signal, 1)
				signal.Notify(sigCh, os.Interrupt)
				go func() {
					<-sigCh
					close(done)
				}()

				fmt.Printf("Following logs for compose group '%s' (Ctrl+C to stop)...\n", groupName)
				return manager.FollowComposeLogs(groupName, services, tail, os.Stdout, done)
			}

			// One-shot log dump
			logs, err := manager.Logs(groupName, services, tail)
			if err != nil {
				return fmt.Errorf("failed to get compose logs: %w", err)
			}

			for _, sl := range logs {
				fmt.Printf("=== %s ===\n", sl.Service)
				fmt.Println(sl.Logs)
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Stream logs in real-time")
	cmd.Flags().IntVarP(&tail, "tail", "n", 0, "Number of lines to show from the end (0 = all)")
	cmd.Flags().StringSliceVar(&services, "service", nil, "Filter by service name (can be repeated)")

	return cmd
}
