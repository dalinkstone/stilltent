package main

import (
	"fmt"

	"github.com/spf13/cobra"
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
			fmt.Println("Listing network devices:")
			// TODO: Implement network list logic
			fmt.Println("No network devices found.")
			return nil
		},
	}
}
