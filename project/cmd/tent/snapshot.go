package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/sandbox"
)

func snapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Snapshot management commands",
	}

	cmd.AddCommand(snapshotCreateCmd())
	cmd.AddCommand(snapshotRestoreCmd())
	cmd.AddCommand(snapshotListCmd())
	cmd.AddCommand(snapshotDeleteCmd())

	return cmd
}

func snapshotCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name> <tag>",
		Short: "Snapshot a microVM's state",
		Long:  `Snapshot a microVM's state.`,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, tag := args[0], args[1]

			// Create VM manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Create snapshot
			snapshotPath, err := manager.CreateSnapshot(name, tag)
			if err != nil {
				return fmt.Errorf("failed to create snapshot: %w", err)
			}

			fmt.Printf("Successfully created snapshot of VM %s with tag %s\n", name, tag)
			fmt.Printf("Snapshot path: %s\n", snapshotPath)

			return nil
		},
	}
}

func snapshotRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <name> <tag>",
		Short: "Restore from a snapshot",
		Long:  `Restore from a snapshot.`,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, tag := args[0], args[1]

			// Create VM manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Restore snapshot
			if err := manager.RestoreSnapshot(name, tag); err != nil {
				return fmt.Errorf("failed to restore snapshot: %w", err)
			}

			fmt.Printf("Successfully restored VM %s from snapshot %s\n", name, tag)

			return nil
		},
	}
}

func snapshotListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <name>",
		Short: "List available snapshots",
		Long:  `List available snapshots for a VM.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Create VM manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// List snapshots
			snapshots, err := manager.ListSnapshots(name)
			if err != nil {
				return fmt.Errorf("failed to list snapshots: %w", err)
			}

			if len(snapshots) == 0 {
				fmt.Printf("No snapshots found for VM %s\n", name)
				return nil
			}

			fmt.Printf("Snapshots for VM %s:\n", name)
			fmt.Println("TAG\tSIZE_MB\tCREATED")
			for _, snap := range snapshots {
				fmt.Printf("%s\t%d\t%s\n", snap.Tag, snap.SizeMB, snap.Timestamp)
			}

			return nil
		},
	}
}

func snapshotDeleteCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "delete <name> [tag]",
		Short: "Delete a snapshot or all snapshots for a VM",
		Long:  `Delete a specific snapshot by tag, or use --all to delete all snapshots for a VM.`,
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Create VM manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if all {
				count, err := manager.DeleteAllSnapshots(name)
				if err != nil {
					return fmt.Errorf("failed to delete snapshots: %w", err)
				}
				if count == 0 {
					fmt.Printf("No snapshots found for VM %s\n", name)
				} else {
					fmt.Printf("Deleted %d snapshot(s) for VM %s\n", count, name)
				}
				return nil
			}

			if len(args) < 2 {
				return fmt.Errorf("tag argument required (or use --all to delete all snapshots)")
			}

			tag := args[1]
			if err := manager.DeleteSnapshot(name, tag); err != nil {
				return fmt.Errorf("failed to delete snapshot: %w", err)
			}

			fmt.Printf("Deleted snapshot '%s' for VM %s\n", tag, name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Delete all snapshots for the VM")

	return cmd
}
