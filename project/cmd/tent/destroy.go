package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func destroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "destroy <name>",
		Short: "Remove a microVM and all its resources",
		Long:  `Remove a microVM and all its associated resources (rootfs, network, state).`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			fmt.Printf("Destroying VM: %s\n", name)
			// TODO: Implement destroy logic
			return nil
		},
	}
}
