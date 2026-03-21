package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/network"
)

func networkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Network management commands",
	}

	cmd.AddCommand(networkListCmd())

	return cmd
}

func networkListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List bridges and TAP devices",
		Long:  `List bridges and TAP devices managed by tent.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Create network manager
			manager, err := network.NewManager()
			if err != nil {
				return fmt.Errorf("failed to create network manager: %w", err)
			}

			// List network resources
			resources, err := manager.ListNetworkResources()
			if err != nil {
				return fmt.Errorf("failed to list network resources: %w", err)
			}

			if len(resources) == 0 {
				fmt.Println("No network devices found.")
				return nil
			}

			fmt.Println("Listing network devices:")
			for _, res := range resources {
				fmt.Printf("%s (%s): IP=%s\n", res.Name, res.Type, res.IP)
			}

			return nil
		},
	}
}
