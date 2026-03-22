package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"strconv"

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
	cmd.AddCommand(composeRestartCmd())
	cmd.AddCommand(composeExecCmd())
	cmd.AddCommand(composeListCmd())
	cmd.AddCommand(composeValidateCmd())
	cmd.AddCommand(composeScaleCmd())

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

func composeRestartCmd() *cobra.Command {
	var (
		services []string
		timeout  int
	)

	cmd := &cobra.Command{
		Use:   "restart <file>",
		Short: "Restart sandboxes in a compose group",
		Long: `Restart all or selected sandboxes in a compose group.
Sandboxes are stopped in reverse dependency order and restarted in forward order.

Examples:
  tent compose restart tent-compose.yaml
  tent compose restart tent-compose.yaml --service agent --service tool-runner
  tent compose restart tent-compose.yaml --timeout 10`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			manager, err := newComposeManager()
			if err != nil {
				return err
			}

			groupName := composeGroupName(filePath)

			if len(services) > 0 {
				fmt.Printf("Restarting services %v in compose group '%s'...\n", services, groupName)
			} else {
				fmt.Printf("Restarting all sandboxes in compose group '%s'...\n", groupName)
			}

			if err := manager.Restart(groupName, services, timeout); err != nil {
				return fmt.Errorf("failed to restart compose group: %w", err)
			}

			fmt.Printf("Compose group '%s' restarted successfully\n", groupName)
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&services, "service", nil, "Restart only specific services (can be repeated)")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "Seconds to wait for graceful shutdown before restart")

	return cmd
}

func composeListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all compose groups",
		Long: `List all known compose groups and their sandbox counts.

Examples:
  tent compose list
  tent compose list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := newComposeManager()
			if err != nil {
				return err
			}

			groups, err := manager.List()
			if err != nil {
				return fmt.Errorf("failed to list compose groups: %w", err)
			}

			if len(groups) == 0 {
				fmt.Println("No compose groups found.")
				return nil
			}

			if jsonOutput {
				type groupInfo struct {
					Name      string `json:"name"`
					Sandboxes int    `json:"sandboxes"`
					Status    string `json:"status"`
				}
				var infos []groupInfo
				for _, name := range groups {
					info := groupInfo{Name: name}
					status, err := manager.Status(name)
					if err == nil && status != nil {
						info.Sandboxes = len(status.Sandboxes)
						running := 0
						for _, s := range status.Sandboxes {
							if s.Status == "running" {
								running++
							}
						}
						if running == len(status.Sandboxes) {
							info.Status = "running"
						} else if running > 0 {
							info.Status = "partial"
						} else {
							info.Status = "stopped"
						}
					}
					infos = append(infos, info)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(infos)
			}

			fmt.Printf("%-25s %-10s %-10s\n", "GROUP", "SANDBOXES", "STATUS")
			for _, name := range groups {
				sandboxCount := 0
				groupStatus := "unknown"
				status, err := manager.Status(name)
				if err == nil && status != nil {
					sandboxCount = len(status.Sandboxes)
					running := 0
					for _, s := range status.Sandboxes {
						if s.Status == "running" {
							running++
						}
					}
					if sandboxCount == 0 {
						groupStatus = "empty"
					} else if running == sandboxCount {
						groupStatus = "running"
					} else if running > 0 {
						groupStatus = "partial"
					} else {
						groupStatus = "stopped"
					}
				}
				fmt.Printf("%-25s %-10d %-10s\n", name, sandboxCount, groupStatus)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func composeValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <file>",
		Short: "Validate a compose file without deploying",
		Long: `Parse and validate a compose file, checking for errors such as missing fields,
invalid configurations, dependency cycles, and unknown sandbox references.

Examples:
  tent compose validate tent-compose.yaml
  tent compose validate ./my-stack.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read compose file: %w", err)
			}

			config, err := compose.ParseConfig(data)
			if err != nil {
				return fmt.Errorf("validation failed: %w", err)
			}

			if err := config.Validate(); err != nil {
				return fmt.Errorf("validation failed: %w", err)
			}

			order := config.TopologicalOrder()

			fmt.Printf("Compose file '%s' is valid.\n", filePath)
			fmt.Printf("  Sandboxes: %d\n", len(config.Sandboxes))
			for _, name := range order {
				sb := config.Sandboxes[name]
				vcpus := sb.VCPUs
				if vcpus == 0 {
					vcpus = 1
				}
				mem := sb.MemoryMB
				if mem == 0 {
					mem = 512
				}
				deps := ""
				if len(sb.DependsOn) > 0 {
					deps = fmt.Sprintf(" (depends: %s)", strings.Join(sb.DependsOn, ", "))
				}
				fmt.Printf("    %s: from=%s vcpus=%d mem=%dMB%s\n", name, sb.From, vcpus, mem, deps)
			}
			fmt.Printf("  Start order: %s\n", strings.Join(order, " -> "))

			return nil
		},
	}
}

func composeExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <file> <service> -- <command> [args...]",
		Short: "Execute a command in a compose service sandbox",
		Long: `Execute a command inside a running sandbox belonging to a compose group.

Examples:
  tent compose exec tent-compose.yaml agent -- ls /
  tent compose exec tent-compose.yaml tool-runner -- cat /etc/hostname
  tent compose exec tent-compose.yaml shared-db -- psql -c "SELECT 1"`,
		Args: cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]
			service := args[1]
			execArgs := args[2:]

			// Strip leading "--" if present (cobra may leave it)
			if len(execArgs) > 0 && execArgs[0] == "--" {
				execArgs = execArgs[1:]
			}

			if len(execArgs) == 0 {
				return fmt.Errorf("no command specified")
			}

			manager, err := newComposeManager()
			if err != nil {
				return err
			}

			groupName := composeGroupName(filePath)
			output, exitCode, err := manager.Exec(groupName, service, execArgs)
			if err != nil {
				return fmt.Errorf("exec failed: %w", err)
			}

			if output != "" {
				fmt.Print(output)
			}

			if exitCode != 0 {
				os.Exit(exitCode)
			}

			return nil
		},
	}
}

func composeScaleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scale <file> <service>=<replicas>",
		Short: "Scale a service to the specified number of replicas",
		Long: `Scale a service within a compose group up or down.

When scaling up, new sandbox replicas are created using the service's
configuration from the compose file. Replicas are named <service>-1,
<service>-2, etc. The original sandbox counts as the first replica.

When scaling down, excess replicas are stopped and destroyed in reverse order.

Examples:
  tent compose scale tent-compose.yaml agent=3
  tent compose scale tent-compose.yaml tool-runner=5
  tent compose scale tent-compose.yaml agent=1`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]
			scaleSpec := args[1]

			// Parse service=replicas
			parts := strings.SplitN(scaleSpec, "=", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid scale spec %q, expected service=replicas", scaleSpec)
			}
			service := parts[0]
			replicas, err := strconv.Atoi(parts[1])
			if err != nil {
				return fmt.Errorf("invalid replica count %q: %w", parts[1], err)
			}
			if replicas < 1 {
				return fmt.Errorf("replica count must be at least 1")
			}

			manager, err := newComposeManager()
			if err != nil {
				return err
			}

			config, err := manager.ParseConfig(filePath)
			if err != nil {
				return fmt.Errorf("failed to parse compose file: %w", err)
			}

			groupName := composeGroupName(filePath)

			// Get current replica count for display
			currentCount, _ := manager.ReplicaCount(groupName, service)

			if err := manager.Scale(groupName, service, replicas, config); err != nil {
				return fmt.Errorf("failed to scale service: %w", err)
			}

			if replicas > currentCount {
				fmt.Printf("Scaled service %q up from %d to %d replicas\n", service, currentCount, replicas)
			} else if replicas < currentCount {
				fmt.Printf("Scaled service %q down from %d to %d replicas\n", service, currentCount, replicas)
			} else {
				fmt.Printf("Service %q already at %d replicas\n", service, replicas)
			}

			return nil
		},
	}
}
