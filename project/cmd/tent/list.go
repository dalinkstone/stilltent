package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all microVMs",
		Long:  `List all microVMs with status, IP, resource usage.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Listing VMs:")
			// TODO: Implement list logic
			fmt.Println("No VMs found.")
			return nil
		},
	}
}
