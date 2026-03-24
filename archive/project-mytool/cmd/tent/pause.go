package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/sandbox"
)

func pauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <name>",
		Short: "Freeze a running sandbox's vCPUs",
		Long: `Pause a running sandbox by freezing its vCPU execution.

The sandbox retains its memory, network configuration, and disk state.
Use 'tent unpause' to resume execution.

This is useful for temporarily suspending AI workloads to free host CPU
resources without losing sandbox state.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if err := manager.Pause(name); err != nil {
				return fmt.Errorf("failed to pause sandbox: %w", err)
			}

			fmt.Printf("Sandbox '%s' paused\n", name)
			return nil
		},
	}
}

func unpauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unpause <name>",
		Short: "Resume a paused sandbox's vCPUs",
		Long: `Resume vCPU execution for a paused sandbox.

The sandbox continues from exactly where it was paused, with all memory
and state intact.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if err := manager.Unpause(name); err != nil {
				return fmt.Errorf("failed to unpause sandbox: %w", err)
			}

			fmt.Printf("Sandbox '%s' resumed\n", name)
			return nil
		},
	}
}
