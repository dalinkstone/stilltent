package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/vm"
)

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all microVMs",
		Long:  `List all microVMs with status, IP, resource usage.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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

			// List VMs
			vms, err := manager.List()
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			if len(vms) == 0 {
				fmt.Println("No VMs found.")
				return nil
			}

			fmt.Println("Listing VMs:")
			fmt.Println("NAME\tSTATUS\tIP\tPID\tROOTFS")
			for _, vm := range vms {
				fmt.Printf("%s\t%s\t%s\t%d\t%s\n",
					vm.Name, vm.Status, vm.IP, vm.PID, vm.RootFSPath)
			}

			return nil
		},
	}
}
