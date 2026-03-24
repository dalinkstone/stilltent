package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/internal/compose"
	"github.com/dalinkstone/tent/internal/image"
	"github.com/dalinkstone/tent/internal/sandbox"
)

func composeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Multi-sandbox orchestration from a compose file",
		Long: `Manage groups of sandboxes defined in a YAML compose file.

Compose files describe multiple sandboxes, their images, resource limits,
network connections, mounts, and dependency ordering. A single "tent compose up"
command creates and starts all sandboxes in the correct order.

Profiles allow selective startup of subsets of sandboxes. Sandboxes with no
profiles are always started; sandboxes with profiles are started only when
at least one of their profiles is active.

The compose file format is similar to Docker Compose but tailored for
microVM sandboxes.

See also: tent create, tent pool, tent template`,
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
	cmd.AddCommand(composePullCmd())
	cmd.AddCommand(composePauseCmd())
	cmd.AddCommand(composeUnpauseCmd())
	cmd.AddCommand(composeConfigCmd())
	cmd.AddCommand(composeGraphCmd())
	cmd.AddCommand(composeVolumeCmd())
	cmd.AddCommand(composeHealthCmd())
	cmd.AddCommand(composeHooksCmd())
	cmd.AddCommand(composeProfilesCmd())
	cmd.AddCommand(composeWatchCmd())
	cmd.AddCommand(composeEventsCmd())
	cmd.AddCommand(composeTopCmd())
	cmd.AddCommand(composePortCmd())

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
	var profiles []string

	cmd := &cobra.Command{
		Use:   "up <file>",
		Short: "Start all sandboxes in a compose file",
		Long: `Start sandboxes defined in a compose file. Use --profile to selectively
start only sandboxes assigned to specific profiles. Sandboxes with no profiles
are always started. Sandboxes with profiles are only started when at least one
of their profiles is active.

Examples:
  tent compose up compose.yaml
  tent compose up compose.yaml --profile dev
  tent compose up compose.yaml --profile dev --profile debug`,
		Args: cobra.ExactArgs(1),
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

			// Apply profile filtering
			if len(profiles) > 0 {
				config = config.FilterByProfiles(profiles)
				if len(config.Sandboxes) == 0 {
					return fmt.Errorf("no sandboxes match the active profiles: %s", strings.Join(profiles, ", "))
				}
			}

			manager, err := newComposeManager()
			if err != nil {
				return err
			}

			groupName := composeGroupName(filePath)
			if len(profiles) > 0 {
				fmt.Printf("Starting compose group '%s' (profiles: %s) from %s...\n",
					groupName, strings.Join(profiles, ", "), filePath)
			} else {
				fmt.Printf("Starting compose group '%s' from %s...\n", groupName, filePath)
			}

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

	cmd.Flags().StringSliceVar(&profiles, "profile", nil, "Activate profiles to select which sandboxes to start")
	return cmd
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

			// Try to parse compose file for lifecycle hooks
			var config *compose.ComposeConfig
			if data, readErr := os.ReadFile(filePath); readErr == nil {
				config, _ = compose.ParseConfig(data)
			}

			groupName := composeGroupName(filePath)
			fmt.Printf("Stopping compose group '%s'...\n", groupName)

			if err := manager.DownWithConfig(groupName, config); err != nil {
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

func composePullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <file>",
		Short: "Pull all images referenced in a compose file",
		Long: `Pre-pull all images referenced by sandboxes in a compose file.
This is useful for staging images before running 'tent compose up',
especially in environments with slow or unreliable network access.

Duplicate image references are only pulled once.

Examples:
  tent compose pull tent-compose.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read compose file: %w", err)
			}

			config, err := compose.ParseConfig(data)
			if err != nil {
				return fmt.Errorf("failed to parse compose file: %w", err)
			}

			// Collect unique image references
			seen := make(map[string]bool)
			var imageRefs []string
			for _, sb := range config.Sandboxes {
				if sb.From != "" && !seen[sb.From] {
					seen[sb.From] = true
					imageRefs = append(imageRefs, sb.From)
				}
			}

			if len(imageRefs) == 0 {
				fmt.Println("No images to pull.")
				return nil
			}

			baseDir := getBaseDir()
			imgMgr, err := image.NewManager(baseDir, image.WithProgressCallback(func(bytes, total int64) {
				if total > 0 {
					percent := float64(bytes) / float64(total) * 100
					fmt.Printf("\r  Downloading: %.1f%% (%.1f MB / %.1f MB)",
						percent, float64(bytes)/(1024*1024), float64(total)/(1024*1024))
				} else {
					fmt.Printf("\r  Downloading: %.1f MB", float64(bytes)/(1024*1024))
				}
				if bytes >= total && total > 0 {
					fmt.Println()
				}
			}))
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			fmt.Printf("Pulling %d image(s) for compose file '%s'...\n", len(imageRefs), filePath)

			var pullErrors []string
			for _, ref := range imageRefs {
				fmt.Printf("Pulling '%s'...\n", ref)
				var pullErr error
				if isDockerReference(ref) {
					_, pullErr = imgMgr.PullOCI(sanitizeImageName(ref), ref)
				} else {
					name := sanitizeImageName(ref)
					_, pullErr = imgMgr.Pull(name, ref)
				}
				if pullErr != nil {
					pullErrors = append(pullErrors, fmt.Sprintf("%s: %v", ref, pullErr))
					fmt.Printf("  Failed: %v\n", pullErr)
				} else {
					fmt.Printf("  Done.\n")
				}
			}

			if len(pullErrors) > 0 {
				fmt.Printf("\n%d of %d image(s) failed to pull:\n", len(pullErrors), len(imageRefs))
				for _, e := range pullErrors {
					fmt.Printf("  - %s\n", e)
				}
				return fmt.Errorf("%d image pull(s) failed", len(pullErrors))
			}

			fmt.Printf("\nAll %d image(s) pulled successfully.\n", len(imageRefs))
			return nil
		},
	}
}

// sanitizeImageName derives a short name from an image reference for storage.
func sanitizeImageName(ref string) string {
	// Strip tag
	name := ref
	if idx := strings.LastIndex(name, ":"); idx >= 0 {
		name = name[:idx]
	}
	// Use last path component
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		name = "image"
	}
	return name
}

func composePauseCmd() *cobra.Command {
	var services []string

	cmd := &cobra.Command{
		Use:   "pause <file>",
		Short: "Pause sandboxes in a compose group",
		Long: `Pause (freeze) all or selected sandboxes in a compose group.
Paused sandboxes retain their memory state but stop executing.

Examples:
  tent compose pause tent-compose.yaml
  tent compose pause tent-compose.yaml --service agent`,
		Args: cobra.ExactArgs(1),
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

			targets := resolveComposeTargets(status, services)
			if len(targets) == 0 {
				fmt.Println("No sandboxes to pause.")
				return nil
			}

			baseDir := getBaseDir()
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}
			vmManager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := vmManager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			var pauseErrors []string
			for _, name := range targets {
				if err := vmManager.Pause(name); err != nil {
					pauseErrors = append(pauseErrors, fmt.Sprintf("%s: %v", name, err))
				} else {
					fmt.Printf("Paused %s\n", name)
				}
			}

			if len(pauseErrors) > 0 {
				return fmt.Errorf("errors pausing sandboxes: %s", strings.Join(pauseErrors, "; "))
			}
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&services, "service", nil, "Pause only specific services")
	return cmd
}

func composeUnpauseCmd() *cobra.Command {
	var services []string

	cmd := &cobra.Command{
		Use:   "unpause <file>",
		Short: "Unpause sandboxes in a compose group",
		Long: `Resume execution of paused sandboxes in a compose group.

Examples:
  tent compose unpause tent-compose.yaml
  tent compose unpause tent-compose.yaml --service agent`,
		Args: cobra.ExactArgs(1),
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

			targets := resolveComposeTargets(status, services)
			if len(targets) == 0 {
				fmt.Println("No sandboxes to unpause.")
				return nil
			}

			baseDir := getBaseDir()
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}
			vmManager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := vmManager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			var unpauseErrors []string
			for _, name := range targets {
				if err := vmManager.Unpause(name); err != nil {
					unpauseErrors = append(unpauseErrors, fmt.Sprintf("%s: %v", name, err))
				} else {
					fmt.Printf("Unpaused %s\n", name)
				}
			}

			if len(unpauseErrors) > 0 {
				return fmt.Errorf("errors unpausing sandboxes: %s", strings.Join(unpauseErrors, "; "))
			}
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&services, "service", nil, "Unpause only specific services")
	return cmd
}

func composeConfigCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "config <file>",
		Short: "Show resolved compose configuration",
		Long: `Parse a compose file, expand environment variables, apply defaults,
and display the fully resolved configuration. Useful for debugging
variable expansion and verifying the final configuration.

Examples:
  tent compose config tent-compose.yaml
  tent compose config tent-compose.yaml --format json
  tent compose config tent-compose.yaml --format yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read compose file: %w", err)
			}

			config, err := compose.ParseConfig(data)
			if err != nil {
				return fmt.Errorf("failed to parse compose file: %w", err)
			}

			// Apply defaults to make the resolved config complete
			for name, sb := range config.Sandboxes {
				if sb.VCPUs <= 0 {
					sb.VCPUs = 2
				}
				if sb.MemoryMB <= 0 {
					sb.MemoryMB = 1024
				}
				if sb.DiskGB <= 0 {
					sb.DiskGB = 10
				}
				if sb.Name == "" {
					sb.Name = name
				}
			}

			switch outputFormat {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(config)
			case "yaml":
				out, err := yaml.Marshal(config)
				if err != nil {
					return fmt.Errorf("failed to marshal config: %w", err)
				}
				fmt.Print(string(out))
			default:
				order := config.TopologicalOrder()
				fmt.Printf("# Resolved compose configuration from %s\n\n", filePath)
				for _, name := range order {
					sb := config.Sandboxes[name]
					fmt.Printf("Sandbox: %s\n", name)
					fmt.Printf("  from:      %s\n", sb.From)
					fmt.Printf("  vcpus:     %d\n", sb.VCPUs)
					fmt.Printf("  memory_mb: %d\n", sb.MemoryMB)
					fmt.Printf("  disk_gb:   %d\n", sb.DiskGB)
					if sb.Network != nil {
						if len(sb.Network.Allow) > 0 {
							fmt.Printf("  network.allow:\n")
							for _, ep := range sb.Network.Allow {
								fmt.Printf("    - %s\n", ep)
							}
						}
						if len(sb.Network.Deny) > 0 {
							fmt.Printf("  network.deny:\n")
							for _, ep := range sb.Network.Deny {
								fmt.Printf("    - %s\n", ep)
							}
						}
						if sb.Network.Allow == nil && sb.Network.Deny == nil {
							fmt.Printf("  network:   block-all (no allow/deny rules)\n")
						}
					} else {
						fmt.Printf("  network:   block-all (default)\n")
					}
					if len(sb.Env) > 0 {
						fmt.Printf("  env:\n")
						envKeys := make([]string, 0, len(sb.Env))
						for k := range sb.Env {
							envKeys = append(envKeys, k)
						}
						sort.Strings(envKeys)
						for _, k := range envKeys {
							v := sb.Env[k]
							// Mask potentially sensitive values
							if isSensitiveKey(k) && len(v) > 4 {
								fmt.Printf("    %s: %s...%s\n", k, v[:2], v[len(v)-2:])
							} else {
								fmt.Printf("    %s: %s\n", k, v)
							}
						}
					}
					if len(sb.Mounts) > 0 {
						fmt.Printf("  mounts:\n")
						for _, m := range sb.Mounts {
							ro := ""
							if m.Readonly {
								ro = " (readonly)"
							}
							fmt.Printf("    - %s -> %s%s\n", m.Host, m.Guest, ro)
						}
					}
					if len(sb.DependsOn) > 0 {
						fmt.Printf("  depends_on: %s\n", strings.Join(sb.DependsOn, ", "))
					}
					fmt.Println()
				}
				fmt.Printf("Start order: %s\n", strings.Join(order, " -> "))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&outputFormat, "format", "text", "Output format: text, json, yaml")
	return cmd
}

// isSensitiveKey returns true if the env var key likely contains a secret
func isSensitiveKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, s := range []string{"KEY", "SECRET", "TOKEN", "PASSWORD", "CREDENTIAL"} {
		if strings.Contains(upper, s) {
			return true
		}
	}
	return false
}

func composeGraphCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "graph <file>",
		Short: "Show dependency graph of compose services",
		Long: `Display the dependency graph for sandboxes defined in a compose file.
Shows which services depend on which, and the boot order.

Examples:
  tent compose graph tent-compose.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read compose file: %w", err)
			}

			config, err := compose.ParseConfig(data)
			if err != nil {
				return fmt.Errorf("failed to parse compose file: %w", err)
			}

			order := config.TopologicalOrder()

			// Build reverse dependency map (what depends on each service)
			dependedBy := make(map[string][]string)
			for name, sb := range config.Sandboxes {
				for _, dep := range sb.DependsOn {
					dependedBy[dep] = append(dependedBy[dep], name)
				}
			}

			// Find root nodes (no dependencies)
			var roots []string
			for _, name := range order {
				sb := config.Sandboxes[name]
				if len(sb.DependsOn) == 0 {
					roots = append(roots, name)
				}
			}

			fmt.Printf("Dependency graph for %s:\n\n", filePath)

			// Print tree from each root
			printed := make(map[string]bool)
			for i, root := range roots {
				isLast := i == len(roots)-1
				printTree(root, "", isLast, dependedBy, config.Sandboxes, printed)
			}

			// Print any orphans (shouldn't happen after validation, but be safe)
			for _, name := range order {
				if !printed[name] {
					fmt.Printf("[%s] %s\n", config.Sandboxes[name].From, name)
				}
			}

			fmt.Printf("\nBoot order: %s\n", strings.Join(order, " -> "))
			fmt.Printf("Total services: %d\n", len(config.Sandboxes))

			return nil
		},
	}
}

// printTree prints a tree representation of the dependency graph
func printTree(name string, prefix string, isLast bool, dependedBy map[string][]string, sandboxes map[string]*compose.SandboxConfig, printed map[string]bool) {
	if printed[name] {
		connector := "|-- "
		if isLast {
			connector = "`-- "
		}
		fmt.Printf("%s%s%s (already shown)\n", prefix, connector, name)
		return
	}
	printed[name] = true

	connector := "|-- "
	childPrefix := "|   "
	if isLast {
		connector = "`-- "
		childPrefix = "    "
	}

	sb := sandboxes[name]
	if prefix == "" {
		fmt.Printf("[%s] %s\n", sb.From, name)
	} else {
		fmt.Printf("%s%s[%s] %s\n", prefix, connector, sb.From, name)
	}

	children := dependedBy[name]
	sort.Strings(children)
	for i, child := range children {
		childIsLast := i == len(children)-1
		printTree(child, prefix+childPrefix, childIsLast, dependedBy, sandboxes, printed)
	}
}

// resolveComposeTargets returns the list of sandbox names to operate on,
// filtered by the given service names. If services is empty, all sandboxes
// in the group are returned.
func resolveComposeTargets(status *compose.ComposeStatus, services []string) []string {
	if len(services) > 0 {
		var targets []string
		for _, svc := range services {
			if _, ok := status.Sandboxes[svc]; ok {
				targets = append(targets, svc)
			}
		}
		return targets
	}
	targets := make([]string, 0, len(status.Sandboxes))
	for name := range status.Sandboxes {
		targets = append(targets, name)
	}
	return targets
}

func composeVolumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Manage compose volumes",
		Long: `View and manage named volumes used by compose groups.

Named volumes allow sandboxes in a compose group to share persistent
storage. Volumes are defined in the compose YAML and automatically
created when the group starts.

Example compose file with volumes:
  volumes:
    shared-data:
      size_mb: 1024
    model-cache:
      labels:
        type: cache
  sandboxes:
    agent:
      from: ubuntu:22.04
      volumes:
        - name: shared-data
          guest: /data
        - name: model-cache
          guest: /models
          readonly: true
    worker:
      from: python:3.12
      volumes:
        - name: shared-data
          guest: /data`,
	}

	cmd.AddCommand(composeVolumeListCmd())
	cmd.AddCommand(composeVolumeInspectCmd())
	cmd.AddCommand(composeVolumeRemoveCmd())

	return cmd
}

func composeVolumeListCmd() *cobra.Command {
	var group string
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List compose volumes",
		Long: `List named volumes. Optionally filter by compose group.

Examples:
  tent compose volume list
  tent compose volume list --group myapp
  tent compose volume list --format json`,
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			volMgr := compose.NewVolumeManager(baseDir)

			var volumes []*compose.VolumeState
			var err error

			if group != "" {
				volumes, err = volMgr.ListVolumes(group)
			} else {
				volumes, err = volMgr.ListAllVolumes()
			}
			if err != nil {
				return fmt.Errorf("failed to list volumes: %w", err)
			}

			if len(volumes) == 0 {
				fmt.Println("No volumes found.")
				return nil
			}

			if outputFormat == "json" {
				data, err := json.MarshalIndent(volumes, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("%-20s %-15s %-8s %-8s %s\n", "NAME", "GROUP", "DRIVER", "SIZE", "CREATED")
			for _, v := range volumes {
				size := "-"
				if v.SizeMB > 0 {
					size = fmt.Sprintf("%dMB", v.SizeMB)
				}
				fmt.Printf("%-20s %-15s %-8s %-8s %s\n",
					v.Name, v.Group, v.Driver, size,
					v.CreatedAt.Format("2006-01-02 15:04"))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&group, "group", "", "Filter by compose group name")
	cmd.Flags().StringVar(&outputFormat, "format", "text", "Output format: text, json")
	return cmd
}

func composeVolumeInspectCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "inspect <group> <volume>",
		Short: "Show volume details",
		Long: `Display detailed information about a named volume.

Examples:
  tent compose volume inspect myapp shared-data
  tent compose volume inspect myapp shared-data --format json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			group := args[0]
			name := args[1]
			baseDir := getBaseDir()
			volMgr := compose.NewVolumeManager(baseDir)

			vol, err := volMgr.GetVolume(group, name)
			if err != nil {
				return err
			}

			if outputFormat == "json" {
				data, err := json.MarshalIndent(vol, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("Volume: %s\n", vol.Name)
			fmt.Printf("  Group:   %s\n", vol.Group)
			fmt.Printf("  Driver:  %s\n", vol.Driver)
			if vol.SizeMB > 0 {
				fmt.Printf("  Size:    %d MB\n", vol.SizeMB)
			}
			fmt.Printf("  Path:    %s\n", vol.Path)
			fmt.Printf("  Created: %s\n", vol.CreatedAt.Format("2006-01-02 15:04:05"))
			if len(vol.Labels) > 0 {
				fmt.Printf("  Labels:\n")
				for k, v := range vol.Labels {
					fmt.Printf("    %s=%s\n", k, v)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&outputFormat, "format", "text", "Output format: text, json")
	return cmd
}

func composeVolumeRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "remove <group> [volume]",
		Short: "Remove compose volumes",
		Long: `Remove named volumes for a compose group. If a volume name is given,
only that volume is removed. Otherwise all volumes for the group are removed.

Examples:
  tent compose volume remove myapp shared-data
  tent compose volume remove myapp --force`,
		Aliases: []string{"rm"},
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			group := args[0]
			baseDir := getBaseDir()
			volMgr := compose.NewVolumeManager(baseDir)

			if len(args) == 2 {
				name := args[1]
				if err := volMgr.RemoveVolume(group, name); err != nil {
					return err
				}
				fmt.Printf("Removed volume %q from group %q\n", name, group)
				return nil
			}

			if !force {
				fmt.Printf("This will remove ALL volumes for group %q. Use --force to confirm.\n", group)
				return nil
			}

			if err := volMgr.RemoveVolumes(group); err != nil {
				return err
			}
			fmt.Printf("Removed all volumes for group %q\n", group)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force removal of all volumes")
	return cmd
}

func composeHealthCmd() *cobra.Command {
	var (
		outputJSON bool
		service    string
		watch      bool
	)

	cmd := &cobra.Command{
		Use:   "health <file>",
		Short: "Show health status of services in a compose group",
		Long: `Display the health check status of all services in a compose group.
Services without health checks configured are shown as "none".

Examples:
  tent compose health tent-compose.yaml
  tent compose health tent-compose.yaml --service agent
  tent compose health tent-compose.yaml --json
  tent compose health tent-compose.yaml --watch`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			manager, err := newComposeManager()
			if err != nil {
				return err
			}

			groupName := composeGroupName(filePath)

			// Parse config to get health check definitions
			config, err := manager.ParseConfig(filePath)
			if err != nil {
				return fmt.Errorf("failed to parse compose config: %w", err)
			}

			// Get current compose status
			status, err := manager.Status(groupName)
			if err != nil {
				return fmt.Errorf("failed to get compose status: %w", err)
			}

			if len(status.Sandboxes) == 0 {
				fmt.Printf("No sandboxes found for compose group '%s'\n", groupName)
				return nil
			}

			// Build health info from config + state
			type healthInfo struct {
				Name          string `json:"name"`
				Status        string `json:"status"`
				Health        string `json:"health"`
				HealthDetail  string `json:"health_detail,omitempty"`
				Restarts      int    `json:"restarts"`
				RestartPolicy string `json:"restart_policy"`
				CheckCmd      string `json:"check_command,omitempty"`
				IntervalSec   int    `json:"interval_sec,omitempty"`
			}

			var infos []healthInfo
			for name, ss := range status.Sandboxes {
				hi := healthInfo{
					Name:     name,
					Status:   ss.Status,
					Health:   ss.Health,
					Restarts: ss.Restarts,
				}

				if hi.Health == "" {
					hi.Health = "none"
				}
				if hi.HealthDetail == "" {
					hi.HealthDetail = ss.HealthDetail
				}

				if svcCfg, ok := config.Sandboxes[name]; ok {
					hi.RestartPolicy = svcCfg.RestartPolicy
					if hi.RestartPolicy == "" {
						hi.RestartPolicy = "no"
					}
					if svcCfg.HealthCheck != nil {
						hi.CheckCmd = strings.Join(svcCfg.HealthCheck.Command, " ")
						hi.IntervalSec = svcCfg.HealthCheck.IntervalSec
						if hi.IntervalSec <= 0 {
							hi.IntervalSec = 30
						}
					}
				}

				if service != "" && name != service {
					continue
				}
				infos = append(infos, hi)
			}

			sort.Slice(infos, func(i, j int) bool {
				return infos[i].Name < infos[j].Name
			})

			if len(infos) == 0 && service != "" {
				return fmt.Errorf("service %q not found in compose group %q", service, groupName)
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(infos)
			}

			fmt.Printf("Compose group '%s' health:\n", groupName)
			fmt.Printf("  %-20s %-12s %-12s %-10s %-12s %s\n",
				"SERVICE", "STATUS", "HEALTH", "RESTARTS", "POLICY", "CHECK")
			for _, hi := range infos {
				checkStr := hi.CheckCmd
				if len(checkStr) > 30 {
					checkStr = checkStr[:27] + "..."
				}
				if checkStr == "" {
					checkStr = "-"
				}
				fmt.Printf("  %-20s %-12s %-12s %-10d %-12s %s\n",
					hi.Name, hi.Status, hi.Health, hi.Restarts, hi.RestartPolicy, checkStr)
			}

			if watch {
				// Start health monitor and display updates
				baseDir := getBaseDir()
				hvBackend, err := vm.NewPlatformBackend(baseDir)
				if err != nil {
					return err
				}
				vmManager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
				if err != nil {
					return err
				}
				if err := vmManager.Setup(); err != nil {
					return err
				}

				stateMgr := compose.NewFileStateManager(baseDir)
				monitor := compose.NewHealthMonitor(groupName, config, vmManager, stateMgr)
				monitor.Start()
				defer monitor.Stop()

				fmt.Println("\nWatching health checks (Ctrl+C to stop)...")

				sigCh := make(chan os.Signal, 1)
				signal.Notify(sigCh, os.Interrupt)

				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()

				for {
					select {
					case <-sigCh:
						fmt.Println("\nStopping health monitor...")
						return nil
					case <-ticker.C:
						statuses := monitor.Statuses()
						sort.Slice(statuses, func(i, j int) bool {
							return statuses[i].Service < statuses[j].Service
						})
						fmt.Printf("\n  %-20s %-12s %-6s %-10s %s\n",
							"SERVICE", "HEALTH", "FAILS", "RESTARTS", "LAST OUTPUT")
						for _, hs := range statuses {
							if service != "" && hs.Service != service {
								continue
							}
							output := hs.LastOutput
							if len(output) > 40 {
								output = output[:37] + "..."
							}
							if output == "" {
								output = "-"
							}
							fmt.Printf("  %-20s %-12s %-6d %-10d %s\n",
								hs.Service, hs.Status, hs.FailCount, hs.Restarts, output)
						}
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	cmd.Flags().StringVar(&service, "service", "", "Show health for a specific service only")
	cmd.Flags().BoolVar(&watch, "watch", false, "Watch health checks in real-time")
	return cmd
}

func composeHooksCmd() *cobra.Command {
	var (
		outputJSON bool
		service    string
	)

	cmd := &cobra.Command{
		Use:   "hooks <file>",
		Short: "Show lifecycle hooks configured for services in a compose file",
		Long: `Display lifecycle hooks (post_create, post_start, pre_stop, pre_destroy)
configured for each service in a compose file.

Hooks run at specific points in the sandbox lifecycle:
  post_create  - After creation, before first start (one-time setup)
  post_start   - After the sandbox is started and reachable (initialization)
  pre_stop     - Before the sandbox is stopped (graceful shutdown)
  pre_destroy  - Before the sandbox is destroyed (cleanup)

Examples:
  tent compose hooks tent-compose.yaml
  tent compose hooks tent-compose.yaml --service agent
  tent compose hooks tent-compose.yaml --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read compose file: %w", err)
			}

			config, err := compose.ParseConfig(data)
			if err != nil {
				return fmt.Errorf("failed to parse compose file: %w", err)
			}

			// Collect hooks info
			type hookInfo struct {
				Service    string   `json:"service"`
				PostCreate []string `json:"post_create,omitempty"`
				PostStart  []string `json:"post_start,omitempty"`
				PreStop    []string `json:"pre_stop,omitempty"`
				PreDestroy []string `json:"pre_destroy,omitempty"`
			}

			var hooks []hookInfo
			names := make([]string, 0, len(config.Sandboxes))
			for name := range config.Sandboxes {
				names = append(names, name)
			}
			sort.Strings(names)

			for _, name := range names {
				if service != "" && name != service {
					continue
				}
				cfg := config.Sandboxes[name]
				if cfg.Hooks == nil {
					continue
				}
				h := cfg.Hooks
				if len(h.PostCreate) == 0 && len(h.PostStart) == 0 &&
					len(h.PreStop) == 0 && len(h.PreDestroy) == 0 {
					continue
				}
				hooks = append(hooks, hookInfo{
					Service:    name,
					PostCreate: h.PostCreate,
					PostStart:  h.PostStart,
					PreStop:    h.PreStop,
					PreDestroy: h.PreDestroy,
				})
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(hooks)
			}

			if len(hooks) == 0 {
				if service != "" {
					fmt.Printf("No lifecycle hooks configured for service %q\n", service)
				} else {
					fmt.Println("No lifecycle hooks configured in this compose file")
				}
				return nil
			}

			for _, h := range hooks {
				fmt.Printf("Service: %s\n", h.Service)
				printHookList("  post_create", h.PostCreate)
				printHookList("  post_start", h.PostStart)
				printHookList("  pre_stop", h.PreStop)
				printHookList("  pre_destroy", h.PreDestroy)
				fmt.Println()
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	cmd.Flags().StringVar(&service, "service", "", "Show hooks for a specific service only")
	return cmd
}

func printHookList(label string, commands []string) {
	if len(commands) == 0 {
		return
	}
	fmt.Printf("%s:\n", label)
	for _, cmd := range commands {
		fmt.Printf("    - %s\n", cmd)
	}
}

func composeProfilesCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "profiles <file>",
		Short: "List available profiles in a compose file",
		Long: `List all profiles defined across sandboxes in a compose file, along with
which sandboxes belong to each profile.

Examples:
  tent compose profiles compose.yaml
  tent compose profiles compose.yaml --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read compose file: %w", err)
			}

			config, err := compose.ParseConfig(data)
			if err != nil {
				return fmt.Errorf("failed to parse compose file: %w", err)
			}

			profiles := config.ListProfiles()

			if outputJSON {
				// Build profile -> services mapping
				profileMap := make(map[string][]string)
				for _, p := range profiles {
					profileMap[p] = []string{}
				}
				for name, sb := range config.Sandboxes {
					for _, p := range sb.Profiles {
						profileMap[p] = append(profileMap[p], name)
					}
				}
				// Sort service lists
				for _, svcs := range profileMap {
					sort.Strings(svcs)
				}

				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(profileMap)
			}

			if len(profiles) == 0 {
				fmt.Println("No profiles defined in compose file.")
				return nil
			}

			// Build profile -> services mapping for display
			profileServices := make(map[string][]string)
			for name, sb := range config.Sandboxes {
				for _, p := range sb.Profiles {
					profileServices[p] = append(profileServices[p], name)
				}
			}
			for _, svcs := range profileServices {
				sort.Strings(svcs)
			}

			// Count sandboxes with no profiles (always started)
			alwaysCount := 0
			for _, sb := range config.Sandboxes {
				if len(sb.Profiles) == 0 {
					alwaysCount++
				}
			}

			fmt.Printf("Profiles in compose file (%d total):\n\n", len(profiles))
			for _, p := range profiles {
				svcs := profileServices[p]
				fmt.Printf("  %s (%d sandboxes)\n", p, len(svcs))
				for _, svc := range svcs {
					fmt.Printf("    - %s\n", svc)
				}
			}

			if alwaysCount > 0 {
				fmt.Printf("\n  (always) %d sandbox(es) with no profile (always started)\n", alwaysCount)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

func composeWatchCmd() *cobra.Command {
	var (
		intervalSec int
		services    []string
		dryRun      bool
	)

	cmd := &cobra.Command{
		Use:   "watch <file>",
		Short: "Watch for file changes and restart affected sandboxes",
		Long: `Monitor files and directories specified in sandbox watch configurations.
When changes are detected, the affected sandbox is automatically restarted.

Add a watch block to sandbox definitions in your compose file:

  sandboxes:
    agent:
      from: ubuntu:22.04
      watch:
        paths:
          - ./src
          - ./config.yaml
        ignore:
          - "*.log"
          - "*.tmp"
        action: restart    # "restart" (default) or "rebuild"

Examples:
  tent compose watch tent-compose.yaml
  tent compose watch tent-compose.yaml --interval 5
  tent compose watch tent-compose.yaml --service agent
  tent compose watch tent-compose.yaml --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read compose file: %w", err)
			}

			config, err := compose.ParseConfig(data)
			if err != nil {
				return fmt.Errorf("failed to parse compose file: %w", err)
			}

			// Extract watch configurations
			watches := compose.ExtractWatchConfigs(config)
			if len(watches) == 0 {
				fmt.Println("No watch configurations found in compose file.")
				fmt.Println("Add a 'watch' block to sandbox definitions to enable file watching.")
				return nil
			}

			// Filter by --service flag
			if len(services) > 0 {
				filtered := make(map[string]*compose.WatchConfig)
				for _, svc := range services {
					if w, ok := watches[svc]; ok {
						filtered[svc] = w
					} else {
						return fmt.Errorf("service %q has no watch configuration", svc)
					}
				}
				watches = filtered
			}

			composeDir := filepath.Dir(filePath)
			if abs, err := filepath.Abs(composeDir); err == nil {
				composeDir = abs
			}

			groupName := composeGroupName(filePath)

			fmt.Printf("Watching for changes in compose group '%s':\n", groupName)
			fmt.Print(compose.FormatWatchSummary(watches))

			if dryRun {
				fmt.Println("\n(dry-run mode: changes will be detected but sandboxes will not be restarted)")
			}

			// Set up compose manager for restarts
			var manager *compose.ComposeManager
			if !dryRun {
				var err error
				manager, err = newComposeManager()
				if err != nil {
					return fmt.Errorf("failed to create compose manager: %w", err)
				}
			}

			// Track debounce per service
			var mu sync.Mutex
			lastRestart := make(map[string]time.Time)
			debounceDuration := time.Duration(intervalSec) * time.Second

			interval := time.Duration(intervalSec) * time.Second
			if interval < time.Second {
				interval = 2 * time.Second
			}

			watcher := compose.NewFileWatcher(compose.FileWatcherOpts{
				Interval:   interval,
				ComposeDir: composeDir,
				OnChange: func(event compose.WatchEvent) {
					mu.Lock()
					last, exists := lastRestart[event.Service]
					if exists && time.Since(last) < debounceDuration {
						mu.Unlock()
						return
					}
					lastRestart[event.Service] = time.Now()
					mu.Unlock()

					rel, _ := filepath.Rel(composeDir, event.Path)
					if rel == "" {
						rel = event.Path
					}

					ts := event.Time.Format("15:04:05")
					fmt.Printf("[%s] Change detected: %s (service: %s, action: %s)\n",
						ts, rel, event.Service, event.Action)

					if dryRun {
						fmt.Printf("[%s] Would %s service %s\n", ts, event.Action, event.Service)
						return
					}

					fmt.Printf("[%s] Restarting service %s...\n", ts, event.Service)
					if err := manager.Restart(groupName, []string{event.Service}, 30); err != nil {
						fmt.Printf("[%s] Error restarting %s: %v\n", ts, event.Service, err)
					} else {
						fmt.Printf("[%s] Service %s restarted successfully\n", ts, event.Service)
					}
				},
			})

			for name, cfg := range watches {
				if err := watcher.AddService(name, cfg); err != nil {
					return fmt.Errorf("failed to set up watch for %s: %w", name, err)
				}
			}

			watcher.Start()
			defer watcher.Stop()

			fmt.Printf("\nWatching for changes (polling every %s, Ctrl+C to stop)...\n", interval)

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt)
			<-sigCh

			fmt.Println("\nStopping file watcher...")
			return nil
		},
	}

	cmd.Flags().IntVar(&intervalSec, "interval", 2, "Polling interval in seconds")
	cmd.Flags().StringSliceVar(&services, "service", nil, "Watch only specific services")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Detect changes without restarting sandboxes")
	return cmd
}

func composeEventsCmd() *cobra.Command {
	var (
		follow    bool
		jsonOut   bool
		eventType string
	)

	cmd := &cobra.Command{
		Use:   "events <file>",
		Short: "Stream lifecycle events for a compose group",
		Long: `Display or stream lifecycle events scoped to the sandboxes in a compose group.
Shows events like create, start, stop, destroy, snapshot, health checks, and hooks.

Examples:
  tent compose events compose.yaml
  tent compose events compose.yaml --follow
  tent compose events compose.yaml --json
  tent compose events compose.yaml --type start`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read compose file: %w", err)
			}

			config, err := compose.ParseConfig(data)
			if err != nil {
				return fmt.Errorf("failed to parse compose file: %w", err)
			}

			groupName := composeGroupName(filePath)

			// Build set of sandbox names in this compose group
			sandboxNames := make(map[string]bool)
			for name := range config.Sandboxes {
				sandboxNames[groupName+"-"+name] = true
				sandboxNames[name] = true
			}

			baseDir := getBaseDir()
			logger := vm.NewEventLogger(baseDir)

			if follow {
				// Stream events in real-time
				done := make(chan struct{})
				filter := vm.EventFilter{}
				if eventType != "" {
					filter.Type = vm.EventType(eventType)
				}

				ch := logger.Watch(filter, 500*time.Millisecond, done)

				sigCh := make(chan os.Signal, 1)
				signal.Notify(sigCh, os.Interrupt)

				fmt.Printf("Streaming events for compose group '%s' (Ctrl+C to stop)...\n", groupName)

				for {
					select {
					case <-sigCh:
						close(done)
						return nil
					case we, ok := <-ch:
						if !ok {
							return nil
						}
						if we.Err != nil {
							fmt.Fprintf(os.Stderr, "event error: %v\n", we.Err)
							continue
						}
						if !sandboxNames[we.Event.Sandbox] {
							continue
						}
						printComposeEvent(we.Event, jsonOut)
					}
				}
			}

			// Non-follow mode: show historical events
			filter := vm.EventFilter{
				Limit: 100,
			}
			if eventType != "" {
				filter.Type = vm.EventType(eventType)
			}

			events, err := logger.Query(filter)
			if err != nil {
				return fmt.Errorf("failed to query events: %w", err)
			}

			// Filter to only sandboxes in this compose group
			count := 0
			for _, ev := range events {
				if !sandboxNames[ev.Sandbox] {
					continue
				}
				printComposeEvent(ev, jsonOut)
				count++
			}

			if count == 0 {
				fmt.Printf("No events found for compose group '%s'\n", groupName)
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Stream events in real-time")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output events as JSON")
	cmd.Flags().StringVar(&eventType, "type", "", "Filter by event type (e.g. start, stop, create)")
	return cmd
}

func printComposeEvent(ev vm.Event, jsonOut bool) {
	if jsonOut {
		data, _ := json.Marshal(ev)
		fmt.Println(string(data))
		return
	}

	ts := ev.Timestamp.Local().Format("2006-01-02 15:04:05")
	details := ""
	if len(ev.Details) > 0 {
		parts := make([]string, 0, len(ev.Details))
		for k, v := range ev.Details {
			parts = append(parts, k+"="+v)
		}
		sort.Strings(parts)
		details = " " + strings.Join(parts, " ")
	}
	fmt.Printf("%s  %-20s  %-22s%s\n", ts, ev.Sandbox, ev.Type, details)
}

func composeTopCmd() *cobra.Command {
	var sortBy string

	cmd := &cobra.Command{
		Use:   "top <file>",
		Short: "Display running processes across all sandboxes in a compose group",
		Long: `Show running processes inside each sandbox of a compose group.
Aggregates ps output from all running sandboxes in the group.

Examples:
  tent compose top compose.yaml
  tent compose top compose.yaml --sort cpu`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

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
			status, err := manager.Status(groupName)
			if err != nil {
				return fmt.Errorf("failed to get compose status: %w", err)
			}

			baseDir := getBaseDir()
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}
			vmManager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := vmManager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Iterate through sandboxes and collect process info
			for name := range config.Sandboxes {
				sbStatus, ok := status.Sandboxes[name]
				if !ok || sbStatus.Status != "running" {
					continue
				}

				vmName := groupName + "-" + name
				output, _, err := vmManager.Exec(vmName, []string{"ps", "aux"})
				if err != nil {
					fmt.Printf("\n=== %s (failed to get processes: %v) ===\n", name, err)
					continue
				}

				processes, err := parsePSOutput(output)
				if err != nil {
					fmt.Printf("\n=== %s (failed to parse processes) ===\n", name)
					continue
				}

				sortProcesses(processes, sortBy)

				fmt.Printf("\n=== %s ===\n", name)
				printProcessTable(processes, false)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&sortBy, "sort", "pid", "Sort by field: pid, cpu, mem, user, command")
	return cmd
}

func composePortCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port <file> [service]",
		Short: "Show port mappings for sandboxes in a compose group",
		Long: `Display host-to-guest port mappings configured for sandboxes in a compose group.
Optionally filter to a specific service name.

Examples:
  tent compose port compose.yaml
  tent compose port compose.yaml web`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]
			filterService := ""
			if len(args) > 1 {
				filterService = args[1]
			}

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read compose file: %w", err)
			}

			config, err := compose.ParseConfig(data)
			if err != nil {
				return fmt.Errorf("failed to parse compose file: %w", err)
			}

			found := false
			for name, sb := range config.Sandboxes {
				if filterService != "" && name != filterService {
					continue
				}
				if sb.Network == nil {
					continue
				}

				// Check for port mappings via the sandbox config's network ports
				// The port spec is typically in the sandbox's full config (from models.VMConfig)
				// For compose, we show the allow/deny network policy and any port info
				if !found {
					fmt.Printf("%-15s %-8s %-10s %-10s\n", "SERVICE", "PROTO", "HOST", "GUEST")
					found = true
				}

				// Compose configs may not have explicit port mappings in the network block,
				// but we can check for them via the running VM state
				baseDir := getBaseDir()
				hvBackend, bErr := vm.NewPlatformBackend(baseDir)
				if bErr != nil {
					continue
				}
				vmManager, mErr := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
				if mErr != nil {
					continue
				}
				if sErr := vmManager.Setup(); sErr != nil {
					continue
				}

				groupName := composeGroupName(filePath)
				vmName := groupName + "-" + name
				forwards, _ := vmManager.ListPortForwards(vmName)
				for _, pf := range forwards {
					fmt.Printf("%-15s %-8s %-10d %-10d\n", name, "tcp", pf.HostPort, pf.GuestPort)
				}

				if len(forwards) == 0 {
					// Show network policy instead
					if len(sb.Network.Allow) > 0 {
						for _, ep := range sb.Network.Allow {
							fmt.Printf("%-15s %-8s %-10s %-10s\n", name, "egress", "->", ep)
						}
					}
				}
			}

			if !found {
				fmt.Println("No port mappings configured in this compose group.")
			}

			return nil
		},
	}

	return cmd
}
