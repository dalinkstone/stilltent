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

func waitCmd() *cobra.Command {
	var (
		targetState string
		timeout     time.Duration
		interval    time.Duration
	)

	cmd := &cobra.Command{
		Use:   "wait <name>",
		Short: "Wait for a sandbox to reach a specific state",
		Long: `Wait for a sandbox to reach a specific state before returning.
Useful for scripting and automation workflows.

Valid states: running, stopped, created`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			target, err := parseTargetState(targetState)
			if err != nil {
				return err
			}

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			deadline := time.After(timeout)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			// Check immediately before first tick
			if done, err := checkState(manager, name, target); err != nil {
				return err
			} else if done {
				fmt.Printf("Sandbox %q is %s\n", name, target)
				return nil
			}

			for {
				select {
				case <-deadline:
					return fmt.Errorf("timeout waiting for sandbox %q to reach state %q", name, target)
				case <-ticker.C:
					done, err := checkState(manager, name, target)
					if err != nil {
						return err
					}
					if done {
						fmt.Printf("Sandbox %q is %s\n", name, target)
						return nil
					}
				}
			}
		},
	}

	cmd.Flags().StringVar(&targetState, "state", "running", "Target state to wait for (running, stopped, created)")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Maximum time to wait")
	cmd.Flags().DurationVar(&interval, "interval", 1*time.Second, "Polling interval")

	return cmd
}

func parseTargetState(s string) (models.VMStatus, error) {
	switch strings.ToLower(s) {
	case "running":
		return models.VMStatusRunning, nil
	case "stopped":
		return models.VMStatusStopped, nil
	case "created":
		return models.VMStatusCreated, nil
	default:
		return "", fmt.Errorf("invalid target state %q: must be running, stopped, or created", s)
	}
}

func checkState(manager *vm.VMManager, name string, target models.VMStatus) (bool, error) {
	state, err := manager.Status(name)
	if err != nil {
		return false, fmt.Errorf("failed to get sandbox status: %w", err)
	}

	if state.Status == models.VMStatusError {
		return false, fmt.Errorf("sandbox %q entered error state", name)
	}

	return state.Status == target, nil
}
