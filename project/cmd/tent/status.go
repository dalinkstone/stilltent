package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <name>",
		Short: "Detailed status of a specific microVM",
		Long:  `Detailed status of a specific microVM.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			fmt.Printf("Status for VM: %s\n", name)
			// TODO: Implement status logic
			return nil
		},
	}
}
