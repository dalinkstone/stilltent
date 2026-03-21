package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func sshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh <name>",
		Short: "SSH into a running microVM",
		Long:  `SSH into a running microVM.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			fmt.Printf("SSH into VM: %s\n", name)
			// TODO: Implement SSH logic
			return nil
		},
	}
}
