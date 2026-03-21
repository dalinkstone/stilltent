package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/vm"
)

func destroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "destroy <name>",
		Short: "Remove a microVM and all its resources",
		Long:  `Remove a microVM and all its associated resources (rootfs, network, state).`,
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

			// Destroy the VM
			if err := manager.Destroy(name); err != nil {
				return fmt.Errorf("failed to destroy VM: %w", err)
			}

			fmt.Printf("Successfully destroyed VM: %s\n", name)
			return nil
		},
	}
}
