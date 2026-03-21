package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/vm"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <name>",
		Short: "Detailed status of a specific microVM",
		Long:  `Detailed status of a specific microVM.`,
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

			// Get VM status
			vmState, err := manager.Status(name)
			if err != nil {
				return fmt.Errorf("failed to get VM status: %w", err)
			}

			fmt.Printf("VM: %s\n", name)
			fmt.Printf("  Status:    %s\n", vmState.Status)
			fmt.Printf("  PID:       %d\n", vmState.PID)
			fmt.Printf("  IP:        %s\n", vmState.IP)
			fmt.Printf("  RootFS:    %s\n", vmState.RootFSPath)
			fmt.Printf("  TAP:       %s\n", vmState.TAPDevice)
			fmt.Printf("  Socket:    %s\n", vmState.SocketPath)
			fmt.Printf("  Created:   %d\n", vmState.CreatedAt)
			fmt.Printf("  Updated:   %d\n", vmState.UpdatedAt)

			return nil
		},
	}
}
