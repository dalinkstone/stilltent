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
	rootCmd.AddCommand(statsCmd())
	rootCmd.AddCommand(updateCmd())
	rootCmd.AddCommand(renameCmd())
	rootCmd.AddCommand(pruneCmd())
	rootCmd.AddCommand(exportCmd())
	rootCmd.AddCommand(importCmd())
	rootCmd.AddCommand(waitCmd())
	rootCmd.AddCommand(eventsCmd())
	rootCmd.AddCommand(attachCmd())
	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(cloneCmd())
	rootCmd.AddCommand(labelCmd())
	rootCmd.AddCommand(pauseCmd())
	rootCmd.AddCommand(unpauseCmd())
	rootCmd.AddCommand(topCmd())
	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(completionCmd())
	rootCmd.AddCommand(commitCmd())
	rootCmd.AddCommand(systemCmd())
	rootCmd.AddCommand(templateCmd())
	rootCmd.AddCommand(registryCmd())
	rootCmd.AddCommand(resourcesCmd())
	rootCmd.AddCommand(envCmd())
	rootCmd.AddCommand(portCmd())
	rootCmd.AddCommand(mountCmd())
	rootCmd.AddCommand(diffCmd())
	rootCmd.AddCommand(checkpointCmd())
	rootCmd.AddCommand(secretCmd())
	rootCmd.AddCommand(scanCmd())
	rootCmd.AddCommand(watchCmd())
	rootCmd.AddCommand(rollbackCmd())
	rootCmd.AddCommand(debugCmd())
	rootCmd.AddCommand(tunnelCmd())
	rootCmd.AddCommand(apiCmd())
	rootCmd.AddCommand(provisionCmd())
	rootCmd.AddCommand(kernelCmd())
	rootCmd.AddCommand(migrateCmd())
	rootCmd.AddCommand(quotaCmd())
	rootCmd.AddCommand(auditCmd())
	rootCmd.AddCommand(pluginCmd())
	rootCmd.AddCommand(benchCmd())
	rootCmd.AddCommand(secProfileCmd())
	rootCmd.AddCommand(deviceCmd())
	rootCmd.AddCommand(webhookCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
