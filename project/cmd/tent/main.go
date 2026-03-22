package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "tent",
		Short: "tent - MicroVM management tool",
		Long:  `tent is a command-line tool for creating, managing, and destroying microVMs as lightweight, isolated development environments.`,
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Usage()
		},
	}

	rootCmd.AddCommand(createCmd())
	rootCmd.AddCommand(startCmd())
	rootCmd.AddCommand(stopCmd())
	rootCmd.AddCommand(destroyCmd())
	rootCmd.AddCommand(listCmd())
	rootCmd.AddCommand(sshCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(logsCmd())
	rootCmd.AddCommand(snapshotCmd())
	rootCmd.AddCommand(networkCmd())
	rootCmd.AddCommand(imageCmd())
	rootCmd.AddCommand(composeCmd())
	rootCmd.AddCommand(execCmd())
	rootCmd.AddCommand(restartCmd())
	rootCmd.AddCommand(healthCmd())
	rootCmd.AddCommand(inspectCmd())
	rootCmd.AddCommand(cpCmd())
	rootCmd.AddCommand(configCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
