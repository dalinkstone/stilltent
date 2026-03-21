package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Gracefully shut down a running microVM",
		Long:  `Gracefully shut down a running microVM.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			fmt.Printf("Stopping VM: %s\n", name)
			// TODO: Implement stop logic
			return nil
		},
	}
}
