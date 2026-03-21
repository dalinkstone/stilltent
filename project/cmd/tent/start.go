package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Boot a stopped microVM",
		Long:  `Boot a stopped microVM.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			fmt.Printf("Starting VM: %s\n", name)
			// TODO: Implement start logic
			return nil
		},
	}
}
