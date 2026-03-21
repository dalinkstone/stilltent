package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/vm"
)

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Gracefully shut down a running microVM",
		Long:  `Gracefully shut down a running microVM.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Create VM manager
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

			// Stop the VM
			if err := manager.Stop(name); err != nil {
				return fmt.Errorf("failed to stop VM: %w", err)
			}

			fmt.Printf("Successfully stopped VM: %s\n", name)
			return nil
		},
	}
}
