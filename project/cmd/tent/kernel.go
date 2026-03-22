package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/boot"
)

func kernelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kernel",
		Short: "Manage locally stored kernels for microVM boot",
	}

	cmd.AddCommand(kernelListCmd())
	cmd.AddCommand(kernelAddCmd())
	cmd.AddCommand(kernelRemoveCmd())
	cmd.AddCommand(kernelInspectCmd())
	cmd.AddCommand(kernelSetDefaultCmd())
	cmd.AddCommand(kernelScanCmd())
	cmd.AddCommand(kernelGetCmd())

	return cmd
}

func getKernelStore() (*boot.KernelStore, error) {
	baseDir := os.Getenv("TENT_BASE_DIR")
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("cannot determine home directory: %w", err)
		}
		baseDir = home + "/.tent"
	}
	return boot.NewKernelStore(baseDir)
}

func kernelListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List locally stored kernels",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := getKernelStore()
			if err != nil {
				return err
			}

			kernels, err := store.List()
			if err != nil {
				return fmt.Errorf("failed to list kernels: %w", err)
			}

			if len(kernels) == 0 {
				fmt.Println("No kernels stored. Use 'tent kernel add <path>' to import a kernel.")
				return nil
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(kernels)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "VERSION\tFORMAT\tARCH\tSIZE\tDEFAULT\tSHA256")
			for _, k := range kernels {
				def := ""
				if k.Default {
					def = "*"
				}
				sizeStr := formatBytes(k.Size)
				shortHash := k.SHA256
				if len(shortHash) > 12 {
					shortHash = shortHash[:12]
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					k.Version, k.Format, k.Arch, sizeStr, def, shortHash)
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func kernelAddCmd() *cobra.Command {
	var (
		version string
		labels  []string
		setDef  bool
	)

	cmd := &cobra.Command{
		Use:   "add <kernel-path>",
		Short: "Import a kernel image into the local store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := getKernelStore()
			if err != nil {
				return err
			}

			labelMap := make(map[string]string)
			for _, l := range labels {
				k, v := parseLabel(l)
				if k != "" {
					labelMap[k] = v
				}
			}

			entry, err := store.Add(args[0], version, labelMap)
			if err != nil {
				return fmt.Errorf("failed to add kernel: %w", err)
			}

			if setDef {
				if err := store.SetDefault(entry.Version); err != nil {
					return fmt.Errorf("failed to set default: %w", err)
				}
				entry.Default = true
			}

			fmt.Printf("Added kernel %s (%s, %s)\n", entry.Version, entry.Format, entry.Arch)
			fmt.Printf("  Path:   %s\n", entry.Path)
			fmt.Printf("  SHA256: %s\n", entry.SHA256)
			if entry.InitrdPath != "" {
				fmt.Printf("  Initrd: %s\n", entry.InitrdPath)
			}
			if entry.Default {
				fmt.Println("  Default: yes")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Override version string (auto-detected from filename)")
	cmd.Flags().StringSliceVar(&labels, "label", nil, "Labels in key=value format")
	cmd.Flags().BoolVar(&setDef, "set-default", false, "Set this kernel as the default")
	return cmd
}

func kernelRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <version>",
		Aliases: []string{"rm"},
		Short:   "Remove a kernel from the local store",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := getKernelStore()
			if err != nil {
				return err
			}

			if err := store.Remove(args[0]); err != nil {
				return fmt.Errorf("failed to remove kernel: %w", err)
			}

			fmt.Printf("Removed kernel %s\n", args[0])
			return nil
		},
	}
}

func kernelInspectCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "inspect <kernel-path>",
		Short: "Inspect a kernel image file and show detailed information",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inspection, err := boot.Inspect(args[0])
			if err != nil {
				return fmt.Errorf("failed to inspect kernel: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(inspection)
			}

			fmt.Printf("Path:          %s\n", inspection.Path)
			fmt.Printf("Size:          %s (%d bytes)\n", formatBytes(inspection.Size), inspection.Size)
			fmt.Printf("SHA256:        %s\n", inspection.SHA256)
			fmt.Printf("Format:        %s\n", inspection.Format)
			fmt.Printf("Bootable:      %v\n", inspection.Bootable)

			if inspection.IsBzImage {
				fmt.Printf("Protocol:      %s\n", inspection.ProtoVersion)
				fmt.Printf("Setup sectors: %d\n", inspection.SetupSects)
				fmt.Printf("Setup size:    %s\n", formatBytes(int64(inspection.SetupDataSize)))
				fmt.Printf("Kernel size:   %s\n", formatBytes(int64(inspection.ProtModeSize)))
				fmt.Printf("Alignment:     0x%x\n", inspection.KernelAlign)
				fmt.Printf("Load flags:    0x%02x\n", inspection.LoadFlags)
			}

			if len(inspection.Details) > 0 {
				fmt.Println("Details:")
				for k, v := range inspection.Details {
					fmt.Printf("  %s: %s\n", k, v)
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func kernelSetDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-default <version>",
		Short: "Set the default kernel version for new sandboxes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := getKernelStore()
			if err != nil {
				return err
			}

			if err := store.SetDefault(args[0]); err != nil {
				return fmt.Errorf("failed to set default kernel: %w", err)
			}

			fmt.Printf("Default kernel set to %s\n", args[0])
			return nil
		},
	}
}

func kernelScanCmd() *cobra.Command {
	var addFound bool

	cmd := &cobra.Command{
		Use:   "scan <directory>",
		Short: "Scan a directory for kernel images",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := getKernelStore()
			if err != nil {
				return err
			}

			found, err := store.ScanDirectory(args[0])
			if err != nil {
				return fmt.Errorf("failed to scan directory: %w", err)
			}

			if len(found) == 0 {
				fmt.Println("No kernel images found.")
				return nil
			}

			fmt.Printf("Found %d kernel image(s):\n\n", len(found))

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "VERSION\tFORMAT\tARCH\tSIZE\tPATH")
			for _, k := range found {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					k.Version, k.Format, k.Arch, formatBytes(k.Size), k.Path)
			}
			if err := w.Flush(); err != nil {
				return err
			}

			if addFound {
				fmt.Println()
				added := 0
				for _, k := range found {
					entry, err := store.Add(k.Path, k.Version, nil)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to add %s: %v\n", k.Path, err)
						continue
					}
					fmt.Printf("Added %s (%s)\n", entry.Version, entry.Format)
					added++
				}
				fmt.Printf("\nImported %d kernel(s)\n", added)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&addFound, "add", false, "Automatically import found kernels into the store")
	return cmd
}

func kernelGetCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "get <version>",
		Short: "Show details of a stored kernel by version",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := getKernelStore()
			if err != nil {
				return err
			}

			entry, err := store.Get(args[0])
			if err != nil {
				return fmt.Errorf("kernel not found: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(entry)
			}

			fmt.Printf("Version:   %s\n", entry.Version)
			fmt.Printf("Format:    %s\n", entry.Format)
			fmt.Printf("Arch:      %s\n", entry.Arch)
			fmt.Printf("Size:      %s\n", formatBytes(entry.Size))
			fmt.Printf("SHA256:    %s\n", entry.SHA256)
			fmt.Printf("Path:      %s\n", entry.Path)
			if entry.InitrdPath != "" {
				fmt.Printf("Initrd:    %s\n", entry.InitrdPath)
			}
			fmt.Printf("Added:     %s\n", entry.AddedAt.Format("2006-01-02 15:04:05"))
			fmt.Printf("Default:   %v\n", entry.Default)
			if len(entry.Labels) > 0 {
				fmt.Println("Labels:")
				for k, v := range entry.Labels {
					fmt.Printf("  %s=%s\n", k, v)
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func parseLabel(s string) (string, string) {
	for i, c := range s {
		if c == '=' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}
