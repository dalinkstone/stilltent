package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func lockCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lock",
		Short: "Manage sandbox locks to prevent accidental modifications",
		Long: `Lock and unlock sandboxes to prevent accidental stop, destroy, or configuration changes.

Examples:
  tent lock set mybox --reason "production workload"
  tent lock rm mybox
  tent lock list`,
	}

	cmd.AddCommand(lockSetCmd())
	cmd.AddCommand(lockRmCmd())
	cmd.AddCommand(lockListCmd())

	return cmd
}

func lockSetCmd() *cobra.Command {
	var reason string

	cmd := &cobra.Command{
		Use:   "set <sandbox>",
		Short: "Lock a sandbox to prevent modifications",
		Long:  `Lock a sandbox so it cannot be stopped, destroyed, or reconfigured until unlocked.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if err := manager.Lock(name, reason); err != nil {
				return err
			}

			fmt.Printf("Sandbox %q is now locked", name)
			if reason != "" {
				fmt.Printf(" (reason: %s)", reason)
			}
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().StringVar(&reason, "reason", "", "Reason for locking the sandbox")
	return cmd
}

func lockRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <sandbox>",
		Short: "Unlock a sandbox to allow modifications",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if err := manager.Unlock(name); err != nil {
				return err
			}

			fmt.Printf("Sandbox %q is now unlocked\n", name)
			return nil
		},
	}
}

func lockListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all locked sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			vms, err := manager.List()
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			found := false
			fmt.Printf("%-20s %-10s %-24s %s\n", "NAME", "STATUS", "LOCKED SINCE", "REASON")
			for _, v := range vms {
				if !v.Locked {
					continue
				}
				found = true
				since := "-"
				if v.LockedAt > 0 {
					since = time.Unix(v.LockedAt, 0).Local().Format("2006-01-02 15:04:05")
				}
				reason := v.LockedReason
				if reason == "" {
					reason = "-"
				}
				fmt.Printf("%-20s %-10s %-24s %s\n", v.Name, v.Status, since, reason)
			}

			if !found {
				fmt.Println("No locked sandboxes.")
			}

			return nil
		},
	}
}
