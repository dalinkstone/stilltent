package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func logsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <name>",
		Short: "View microVM console/boot logs",
		Long:  `View microVM console/boot logs.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			fmt.Printf("Logs for VM: %s\n", name)
			// TODO: Implement logs logic
			return nil
		},
	}
}
