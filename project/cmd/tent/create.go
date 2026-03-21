package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func createCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name> [--config <path>]",
		Short: "Create a new microVM",
		Long:  `Create a new microVM from a base image or YAML config.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			fmt.Printf("Creating VM: %s\n", name)
			// TODO: Implement create logic
			return nil
		},
	}
}
