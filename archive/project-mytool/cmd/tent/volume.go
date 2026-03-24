package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/storage"
)

func volumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Manage persistent named volumes",
		Long: `Create, list, inspect, and remove persistent named volumes.

Volumes provide durable storage that persists independently of sandbox lifecycle.
A volume can be attached to one or more sandboxes and retains its data even after
sandboxes are destroyed.

Examples:
  tent volume create mydata
  tent volume create bigvol --size 10240 --label env=prod
  tent volume list
  tent volume inspect mydata
  tent volume attach mydata my-sandbox
  tent volume detach mydata my-sandbox
  tent volume remove mydata
  tent volume prune`,
	}

	cmd.AddCommand(volumeCreateCmd())
	cmd.AddCommand(volumeListCmd())
	cmd.AddCommand(volumeInspectCmd())
	cmd.AddCommand(volumeRemoveCmd())
	cmd.AddCommand(volumePruneCmd())
	cmd.AddCommand(volumeAttachCmd())
	cmd.AddCommand(volumeDetachCmd())

	return cmd
}

func openVolumeStore() (*storage.VolumeStore, error) {
	baseDir := os.Getenv("TENT_BASE_DIR")
	if baseDir == "" {
		home, _ := os.UserHomeDir()
		baseDir = home + "/.tent"
	}
	return storage.NewVolumeStore(baseDir)
}

func volumeCreateCmd() *cobra.Command {
	var (
		sizeMB  int
		driver  string
		labels  []string
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new named volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			store, err := openVolumeStore()
			if err != nil {
				return fmt.Errorf("failed to open volume store: %w", err)
			}

			labelMap := make(map[string]string)
			for _, l := range labels {
				parts := strings.SplitN(l, "=", 2)
				if len(parts) == 2 {
					labelMap[parts[0]] = parts[1]
				}
			}

			vol, err := store.Create(name, driver, sizeMB, labelMap)
			if err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(vol)
			}

			fmt.Printf("Volume %q created (%d MB, driver: %s)\n", vol.Name, vol.SizeMB, vol.Driver)
			return nil
		},
	}

	cmd.Flags().IntVar(&sizeMB, "size", 1024, "Volume size in MB")
	cmd.Flags().StringVar(&driver, "driver", "local", "Volume driver")
	cmd.Flags().StringSliceVar(&labels, "label", nil, "Labels (key=value)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")

	return cmd
}

func volumeListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all volumes",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openVolumeStore()
			if err != nil {
				return fmt.Errorf("failed to open volume store: %w", err)
			}

			vols := store.List()
			sort.Slice(vols, func(i, j int) bool {
				return vols[i].Name < vols[j].Name
			})

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(vols)
			}

			if len(vols) == 0 {
				fmt.Println("No volumes found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tDRIVER\tSIZE (MB)\tSANDBOXES\tCREATED")
			for _, v := range vols {
				sandboxes := "-"
				if len(v.Sandboxes) > 0 {
					sandboxes = strings.Join(v.Sandboxes, ", ")
				}
				created := time.Unix(v.CreatedAt, 0).Format("2006-01-02 15:04")
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", v.Name, v.Driver, v.SizeMB, sandboxes, created)
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func volumeInspectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect <name>",
		Short: "Show detailed information about a volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openVolumeStore()
			if err != nil {
				return fmt.Errorf("failed to open volume store: %w", err)
			}

			vol, err := store.Get(args[0])
			if err != nil {
				return err
			}

			dataPath, _ := store.DataPath(args[0])

			type inspectOutput struct {
				Name      string            `json:"name"`
				Driver    string            `json:"driver"`
				SizeMB    int               `json:"size_mb"`
				UsedBytes int64             `json:"used_bytes"`
				DataPath  string            `json:"data_path"`
				Labels    map[string]string `json:"labels,omitempty"`
				Sandboxes []string          `json:"sandboxes"`
				CreatedAt string            `json:"created_at"`
			}

			out := inspectOutput{
				Name:      vol.Name,
				Driver:    vol.Driver,
				SizeMB:    vol.SizeMB,
				UsedBytes: vol.UsedBytes,
				DataPath:  dataPath,
				Labels:    vol.Labels,
				Sandboxes: vol.Sandboxes,
				CreatedAt: time.Unix(vol.CreatedAt, 0).Format(time.RFC3339),
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}

	return cmd
}

func volumeRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "remove <name>...",
		Aliases: []string{"rm"},
		Short:   "Remove one or more volumes",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openVolumeStore()
			if err != nil {
				return fmt.Errorf("failed to open volume store: %w", err)
			}

			var errs []string
			for _, name := range args {
				if err := store.Remove(name, force); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", name, err))
				} else {
					fmt.Printf("Removed volume %q\n", name)
				}
			}

			if len(errs) > 0 {
				return fmt.Errorf("errors removing volumes:\n  %s", strings.Join(errs, "\n  "))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force removal even if volume is in use")
	return cmd
}

func volumePruneCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove all unused volumes",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openVolumeStore()
			if err != nil {
				return fmt.Errorf("failed to open volume store: %w", err)
			}

			pruned, err := store.Prune()
			if err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]interface{}{
					"pruned": pruned,
					"count":  len(pruned),
				})
			}

			if len(pruned) == 0 {
				fmt.Println("No unused volumes to prune.")
				return nil
			}

			for _, name := range pruned {
				fmt.Printf("Pruned volume %q\n", name)
			}
			fmt.Printf("Total: %d volume(s) removed\n", len(pruned))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func volumeAttachCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attach <volume> <sandbox>",
		Short: "Attach a volume to a sandbox",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openVolumeStore()
			if err != nil {
				return fmt.Errorf("failed to open volume store: %w", err)
			}

			if err := store.Attach(args[0], args[1]); err != nil {
				return err
			}

			fmt.Printf("Volume %q attached to sandbox %q\n", args[0], args[1])
			return nil
		},
	}

	return cmd
}

func volumeDetachCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "detach <volume> <sandbox>",
		Short: "Detach a volume from a sandbox",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openVolumeStore()
			if err != nil {
				return fmt.Errorf("failed to open volume store: %w", err)
			}

			if err := store.Detach(args[0], args[1]); err != nil {
				return err
			}

			fmt.Printf("Volume %q detached from sandbox %q\n", args[0], args[1])
			return nil
		},
	}

	return cmd
}
