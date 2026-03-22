package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func checkpointCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "checkpoint",
		Short: "Manage full VM checkpoints (memory + CPU + disk)",
		Long: `Save and restore complete VM execution state including memory, CPU registers,
and optionally the disk image. Unlike snapshots which only capture disk state,
checkpoints capture the entire VM context so it can resume exactly where it
left off.

This is particularly useful for AI agent workloads where you want to freeze
an agent mid-execution and resume it later, or fork execution from a known
good state.`,
	}

	cmd.AddCommand(checkpointCreateCmd())
	cmd.AddCommand(checkpointRestoreCmd())
	cmd.AddCommand(checkpointListCmd())
	cmd.AddCommand(checkpointDeleteCmd())
	cmd.AddCommand(checkpointInspectCmd())

	return cmd
}

func checkpointCreateCmd() *cobra.Command {
	var (
		description string
		includeDisk bool
	)

	cmd := &cobra.Command{
		Use:   "create <name> <tag>",
		Short: "Create a full checkpoint of a sandbox",
		Long: `Save a complete checkpoint of the sandbox's execution state including
memory contents, CPU registers, virtio device state, and optionally the
disk image.

Examples:
  tent checkpoint create mybox pre-training
  tent checkpoint create mybox v1 --description "Before fine-tuning"
  tent checkpoint create mybox full-backup --include-disk`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, tag := args[0], args[1]
			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			info, err := manager.CreateCheckpoint(name, tag, description, includeDisk)
			if err != nil {
				return fmt.Errorf("failed to create checkpoint: %w", err)
			}

			fmt.Printf("Checkpoint created for sandbox '%s'\n", name)
			fmt.Printf("  Tag:          %s\n", info.Tag)
			fmt.Printf("  Size:         %d MB\n", info.SizeMB)
			fmt.Printf("  Memory:       %d MB\n", info.MemoryMB)
			fmt.Printf("  vCPUs:        %d\n", info.VCPUs)
			fmt.Printf("  Disk:         %v\n", info.DiskIncluded)
			if info.Description != "" {
				fmt.Printf("  Description:  %s\n", info.Description)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&description, "description", "d", "", "Description for the checkpoint")
	cmd.Flags().BoolVar(&includeDisk, "include-disk", false, "Include disk image in the checkpoint")

	return cmd
}

func checkpointRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <name> <tag>",
		Short: "Restore a sandbox from a checkpoint",
		Long: `Restore a sandbox's state from a previously saved checkpoint. The sandbox
must be stopped before restoring.

If the checkpoint includes a disk image, the current rootfs will be replaced.
VM configuration (vCPUs, memory) will be restored to match the checkpoint.

Examples:
  tent checkpoint restore mybox pre-training
  tent checkpoint restore mybox v1`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, tag := args[0], args[1]
			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if err := manager.RestoreCheckpoint(name, tag); err != nil {
				return fmt.Errorf("failed to restore checkpoint: %w", err)
			}

			fmt.Printf("Restored sandbox '%s' from checkpoint '%s'\n", name, tag)
			fmt.Println("Start the sandbox with: tent start", name)

			return nil
		},
	}
}

func checkpointListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list <name>",
		Short: "List checkpoints for a sandbox",
		Long: `Display all saved checkpoints for a sandbox with their metadata.

Examples:
  tent checkpoint list mybox
  tent checkpoint list mybox --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			checkpoints, err := manager.ListCheckpoints(name)
			if err != nil {
				return fmt.Errorf("failed to list checkpoints: %w", err)
			}

			if len(checkpoints) == 0 {
				fmt.Printf("No checkpoints found for sandbox '%s'\n", name)
				return nil
			}

			if jsonOutput {
				data, err := json.MarshalIndent(checkpoints, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal JSON: %w", err)
				}
				fmt.Println(string(data))
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "TAG\tSIZE_MB\tMEMORY\tVCPUS\tDISK\tSTATUS\tCREATED\tDESCRIPTION\n")
			for _, cp := range checkpoints {
				disk := "no"
				if cp.DiskIncluded {
					disk = "yes"
				}
				desc := cp.Description
				if len(desc) > 30 {
					desc = desc[:27] + "..."
				}
				fmt.Fprintf(w, "%s\t%d\t%dMB\t%d\t%s\t%s\t%s\t%s\n",
					cp.Tag, cp.SizeMB, cp.MemoryMB, cp.VCPUs, disk, cp.VMStatus, cp.Timestamp, desc)
			}
			w.Flush()

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func checkpointDeleteCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "delete <name> [tag]",
		Short: "Delete a checkpoint or all checkpoints",
		Long: `Delete a specific checkpoint by tag, or use --all to remove all checkpoints
for a sandbox.

Examples:
  tent checkpoint delete mybox v1
  tent checkpoint delete mybox --all`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if all {
				count, err := manager.DeleteAllCheckpoints(name)
				if err != nil {
					return fmt.Errorf("failed to delete checkpoints: %w", err)
				}
				if count == 0 {
					fmt.Printf("No checkpoints found for sandbox '%s'\n", name)
				} else {
					fmt.Printf("Deleted %d checkpoint(s) for sandbox '%s'\n", count, name)
				}
				return nil
			}

			if len(args) < 2 {
				return fmt.Errorf("tag argument required (or use --all to delete all checkpoints)")
			}

			tag := args[1]
			if err := manager.DeleteCheckpoint(name, tag); err != nil {
				return fmt.Errorf("failed to delete checkpoint: %w", err)
			}

			fmt.Printf("Deleted checkpoint '%s' for sandbox '%s'\n", tag, name)

			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Delete all checkpoints for the sandbox")

	return cmd
}

func checkpointInspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <name> <tag>",
		Short: "Show detailed information about a checkpoint",
		Long: `Display detailed metadata about a specific checkpoint including memory layout,
CPU state, device state, and integrity verification.

Examples:
  tent checkpoint inspect mybox v1`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, tag := args[0], args[1]
			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// List to get info
			checkpoints, err := manager.ListCheckpoints(name)
			if err != nil {
				return fmt.Errorf("failed to list checkpoints: %w", err)
			}

			for _, cp := range checkpoints {
				if cp.Tag == tag {
					data, err := json.MarshalIndent(cp, "", "  ")
					if err != nil {
						return fmt.Errorf("failed to marshal JSON: %w", err)
					}
					fmt.Println(string(data))
					return nil
				}
			}

			return fmt.Errorf("checkpoint %q not found for sandbox %q", tag, name)
		},
	}
}
