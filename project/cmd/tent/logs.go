package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/vm"
)

func logsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <name>",
		Short: "View microVM console/boot logs",
		Long:  `View microVM console/boot logs.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Create VM manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := vm.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Get VM logs
			logs, err := manager.Logs(name)
			if err != nil {
				return fmt.Errorf("failed to get VM logs: %w", err)
			}

			fmt.Printf("Logs for VM: %s\n", name)
			fmt.Println(logs)

			return nil
		},
	}
}
