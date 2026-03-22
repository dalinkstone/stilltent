package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/storage"
)

func diskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disk",
		Short: "Manage sandbox disk images",
		Long:  `Inspect, resize, convert, and compact sandbox disk images.`,
	}

	cmd.AddCommand(diskListCmd())
	cmd.AddCommand(diskInspectCmd())
	cmd.AddCommand(diskResizeCmd())
	cmd.AddCommand(diskConvertCmd())
	cmd.AddCommand(diskCompactCmd())

	return cmd
}

func newStorageManager() (*storage.Manager, error) {
	baseDir := os.Getenv("TENT_BASE_DIR")
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		baseDir = home + "/.tent"
	}
	return storage.NewManager(baseDir)
}

func diskListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all sandbox disk images",
		Long: `List all sandbox disk images with format, size, and efficiency information.

Examples:
  tent disk list
  tent disk list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := newStorageManager()
			if err != nil {
				return err
			}

			disks, err := mgr.ListDisks()
			if err != nil {
				return fmt.Errorf("failed to list disks: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(disks)
			}

			if len(disks) == 0 {
				fmt.Println("No disk images found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "SANDBOX\tFORMAT\tVIRTUAL\tACTUAL\tEFFICIENCY\n")
			for _, d := range disks {
				// Extract sandbox name from path
				name := extractSandboxName(d.Path)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.1f%%\n",
					name, d.Format,
					formatSize(d.VirtualSize),
					formatSize(d.ActualSize),
					d.Efficiency)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func diskInspectCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "inspect <sandbox>",
		Short: "Show detailed disk image information",
		Long: `Display detailed information about a sandbox's disk image including format,
virtual and actual sizes, cluster size, backing file, and space efficiency.

Examples:
  tent disk inspect mybox
  tent disk inspect mybox --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := newStorageManager()
			if err != nil {
				return err
			}

			info, err := mgr.InspectDisk(args[0])
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}

			fmt.Printf("Sandbox:      %s\n", args[0])
			fmt.Printf("Path:         %s\n", info.Path)
			fmt.Printf("Format:       %s\n", info.Format)
			fmt.Printf("Virtual Size: %s\n", formatSize(info.VirtualSize))
			fmt.Printf("Actual Size:  %s\n", formatSize(info.ActualSize))
			fmt.Printf("Efficiency:   %.1f%%\n", info.Efficiency)
			if info.ClusterSize > 0 {
				fmt.Printf("Cluster Size: %s\n", formatSize(uint64(info.ClusterSize)))
			}
			if info.BackingFile != "" {
				fmt.Printf("Backing File: %s\n", info.BackingFile)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func diskResizeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resize <sandbox> <size>",
		Short: "Resize a sandbox disk image",
		Long: `Resize a sandbox's disk image to the specified size. Size can be specified
with units: B, KB, MB, GB, TB. Only growing is supported for qcow2 images.

The sandbox must be stopped before resizing.

Examples:
  tent disk resize mybox 20GB
  tent disk resize mybox 512MB`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := newStorageManager()
			if err != nil {
				return err
			}

			sizeBytes, err := parseSize(args[1])
			if err != nil {
				return fmt.Errorf("invalid size %q: %w", args[1], err)
			}

			if err := mgr.ResizeDisk(args[0], sizeBytes); err != nil {
				return err
			}

			fmt.Printf("Disk for sandbox %q resized to %s\n", args[0], formatSize(sizeBytes))
			return nil
		},
	}

	return cmd
}

func diskConvertCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "convert <sandbox> <format>",
		Short: "Convert disk image format",
		Long: `Convert a sandbox's disk image between raw and qcow2 formats.
The sandbox must be stopped before converting.

qcow2 format supports copy-on-write, snapshots, and sparse allocation.
raw format provides maximum I/O performance.

Examples:
  tent disk convert mybox qcow2
  tent disk convert mybox raw`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := newStorageManager()
			if err != nil {
				return err
			}

			path, err := mgr.ConvertDisk(args[0], args[1])
			if err != nil {
				return err
			}

			fmt.Printf("Disk for sandbox %q converted to %s: %s\n", args[0], args[1], path)
			return nil
		},
	}

	return cmd
}

func diskCompactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compact <sandbox>",
		Short: "Reclaim unused space from a qcow2 disk image",
		Long: `Compact a qcow2 disk image by creating a new copy that excludes
zero-filled clusters. This reclaims space that was allocated but is
no longer in use.

The sandbox must be stopped before compacting.

Examples:
  tent disk compact mybox`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := newStorageManager()
			if err != nil {
				return err
			}

			saved, err := mgr.CompactDisk(args[0])
			if err != nil {
				return err
			}

			if saved == 0 {
				fmt.Printf("Disk for sandbox %q is already compact\n", args[0])
			} else {
				fmt.Printf("Disk for sandbox %q compacted, reclaimed %s\n", args[0], formatSize(saved))
			}
			return nil
		},
	}

	return cmd
}

// parseSize parses a human-readable size string into bytes
func parseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	s = strings.ToUpper(s)

	multipliers := []struct {
		suffix string
		mult   uint64
	}{
		{"TB", 1024 * 1024 * 1024 * 1024},
		{"GB", 1024 * 1024 * 1024},
		{"MB", 1024 * 1024},
		{"KB", 1024},
		{"B", 1},
	}

	for _, m := range multipliers {
		if strings.HasSuffix(s, m.suffix) {
			numStr := strings.TrimSuffix(s, m.suffix)
			numStr = strings.TrimSpace(numStr)
			num, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid number: %s", numStr)
			}
			return uint64(num * float64(m.mult)), nil
		}
	}

	// Try plain number as bytes
	num, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size format: %s (use B, KB, MB, GB, or TB suffix)", s)
	}
	return num, nil
}

// formatSize formats bytes into human-readable form
func formatSize(bytes uint64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
		tb = gb * 1024
	)

	switch {
	case bytes >= tb:
		return fmt.Sprintf("%.1f TB", float64(bytes)/float64(tb))
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// extractSandboxName extracts the sandbox name from a rootfs path
func extractSandboxName(path string) string {
	// Path format: .../rootfs/<name>/rootfs.img
	parts := strings.Split(path, string(os.PathSeparator))
	for i, p := range parts {
		if p == "rootfs" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return path
}
