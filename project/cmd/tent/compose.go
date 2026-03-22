package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func composeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Multi-sandbox orchestration",
		Long:  "Manage groups of sandboxes defined in a YAML compose file.",
	}

	cmd.AddCommand(composeUpCmd())
	cmd.AddCommand(composeDownCmd())
	cmd.AddCommand(composeStatusCmd())

	return cmd
}

func composeUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up <file>",
		Short: "Start all sandboxes in a compose file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Starting compose environment from %s...\n", args[0])
			fmt.Println("Not yet implemented — compose engine is in internal/compose/")
			return nil
		},
	}
}

func composeDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down <file>",
		Short: "Stop and destroy all sandboxes in a compose group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Stopping compose environment from %s...\n", args[0])
			fmt.Println("Not yet implemented — compose engine is in internal/compose/")
			return nil
		},
	}
}

func composeStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <file>",
		Short: "Show status of all sandboxes in a compose group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Status of compose environment from %s...\n", args[0])
			fmt.Println("Not yet implemented — compose engine is in internal/compose/")
			return nil
		},
	}
}
