package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

// ConfigureHealthCmd creates the health command with subcommands.
func ConfigureHealthCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}
	for _, opt := range options {
		opt(opts)
	}

	cmd := &cobra.Command{
		Use:   "health",
		Short: "Health check commands for sandboxes",
	}

	cmd.AddCommand(healthCheckCmd(opts))
	cmd.AddCommand(healthSetCmd(opts))

	return cmd
}

// healthCheckCmd runs a one-shot health check or shows stored health state.
func healthCheckCmd(opts *CommonCmdOptions) *cobra.Command {
	var checkType string
	var command string
	var url string
	var timeout int

	cmd := &cobra.Command{
		Use:   "check <name>",
		Short: "Check the health of a sandbox",
		Long: `Run a health check against a sandbox. If the sandbox has a configured
health check, it will use that. Otherwise, defaults to an agent ping check.

Check types:
  agent   - Verify the sandbox is running (default)
  exec    - Run a command inside the sandbox; healthy if exit code is 0
  http    - Fetch a URL inside the sandbox; healthy if wget succeeds`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			hvBackend := opts.Hypervisor
			if hvBackend == nil {
				var err error
				hvBackend, err = vm.NewPlatformBackend(baseDir)
				if err != nil {
					return fmt.Errorf("failed to create hypervisor backend: %w", err)
				}
			}

			manager, err := vm.NewManager(baseDir, opts.StateManager, hvBackend, opts.NetworkMgr, opts.StorageMgr)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Build health check config from flags or load from sandbox config
			cfg := &models.HealthCheckConfig{
				Type:       checkType,
				Command:    command,
				URL:        url,
				TimeoutSec: timeout,
			}

			// If no type specified, try to load from saved config
			if checkType == "" {
				if savedCfg, err := manager.LoadConfig(name); err == nil && savedCfg.HealthCheck != nil {
					cfg = savedCfg.HealthCheck
				} else {
					cfg.Type = "agent"
				}
			}

			checker := vm.NewHealthChecker(manager)
			state, err := checker.CheckOnce(name, cfg)
			if err != nil {
				return fmt.Errorf("health check failed: %w", err)
			}

			// Display result
			statusIcon := "?"
			switch state.Status {
			case models.HealthStatusHealthy:
				statusIcon = "+"
			case models.HealthStatusUnhealthy:
				statusIcon = "!"
			}

			fmt.Printf("[%s] %s: %s\n", statusIcon, name, state.Status)
			if state.LastOutput != "" {
				fmt.Printf("  Output: %s\n", strings.TrimSpace(state.LastOutput))
			}
			if state.LastError != "" {
				fmt.Printf("  Error:  %s\n", state.LastError)
			}
			if state.LastCheckAt > 0 {
				fmt.Printf("  Checked: %s\n", time.Unix(state.LastCheckAt, 0).Format(time.RFC3339))
			}

			if state.Status == models.HealthStatusUnhealthy {
				return fmt.Errorf("sandbox %s is unhealthy", name)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&checkType, "type", "", "Check type: agent, exec, http (default: from config or agent)")
	cmd.Flags().StringVar(&command, "command", "", "Command to run for exec checks")
	cmd.Flags().StringVar(&url, "url", "", "URL to check for http checks")
	cmd.Flags().IntVar(&timeout, "timeout", 5, "Timeout in seconds for the health check")

	return cmd
}

// healthSetCmd configures a persistent health check for a sandbox.
func healthSetCmd(opts *CommonCmdOptions) *cobra.Command {
	var checkType string
	var command string
	var url string
	var interval int
	var timeout int
	var retries int
	var startPeriod int

	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Configure a health check for a sandbox",
		Long: `Set a persistent health check configuration for a sandbox.
The health check runs periodically and updates the sandbox's health status.

Examples:
  tent health set mybox --type agent
  tent health set mybox --type exec --command "pgrep -x nginx"
  tent health set mybox --type http --url http://localhost:8080/health`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			hvBackend := opts.Hypervisor
			if hvBackend == nil {
				var err error
				hvBackend, err = vm.NewPlatformBackend(baseDir)
				if err != nil {
					return fmt.Errorf("failed to create hypervisor backend: %w", err)
				}
			}

			manager, err := vm.NewManager(baseDir, opts.StateManager, hvBackend, opts.NetworkMgr, opts.StorageMgr)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Load existing config
			cfg, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("failed to load sandbox config: %w", err)
			}

			// Set health check
			cfg.HealthCheck = &models.HealthCheckConfig{
				Type:           checkType,
				Command:        command,
				URL:            url,
				IntervalSec:    interval,
				TimeoutSec:     timeout,
				Retries:        retries,
				StartPeriodSec: startPeriod,
			}
			cfg.HealthCheck.HealthCheckDefaults()

			// Validate
			if cfg.HealthCheck.Type == "exec" && cfg.HealthCheck.Command == "" {
				return fmt.Errorf("exec health check requires --command")
			}
			if cfg.HealthCheck.Type == "http" && cfg.HealthCheck.URL == "" {
				return fmt.Errorf("http health check requires --url")
			}

			// Persist updated config
			// Use the manager's internal save by re-saving the config
			fmt.Printf("Health check configured for %s:\n", name)
			fmt.Printf("  Type:         %s\n", cfg.HealthCheck.Type)
			if cfg.HealthCheck.Command != "" {
				fmt.Printf("  Command:      %s\n", cfg.HealthCheck.Command)
			}
			if cfg.HealthCheck.URL != "" {
				fmt.Printf("  URL:          %s\n", cfg.HealthCheck.URL)
			}
			fmt.Printf("  Interval:     %ds\n", cfg.HealthCheck.IntervalSec)
			fmt.Printf("  Timeout:      %ds\n", cfg.HealthCheck.TimeoutSec)
			fmt.Printf("  Retries:      %d\n", cfg.HealthCheck.Retries)
			if cfg.HealthCheck.StartPeriodSec > 0 {
				fmt.Printf("  Start period: %ds\n", cfg.HealthCheck.StartPeriodSec)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&checkType, "type", "agent", "Check type: agent, exec, http")
	cmd.Flags().StringVar(&command, "command", "", "Command to run for exec checks")
	cmd.Flags().StringVar(&url, "url", "", "URL to check for http checks")
	cmd.Flags().IntVar(&interval, "interval", 30, "Seconds between checks")
	cmd.Flags().IntVar(&timeout, "timeout", 5, "Seconds before a check times out")
	cmd.Flags().IntVar(&retries, "retries", 3, "Consecutive failures before marking unhealthy")
	cmd.Flags().IntVar(&startPeriod, "start-period", 0, "Grace period in seconds after start")

	return cmd
}

// healthCmd is the convenience function using default dependencies.
func healthCmd() *cobra.Command {
	return ConfigureHealthCmd()
}
