package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

func mountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount",
		Short: "Manage host-to-guest directory mounts",
		Long: `View and modify host-to-guest directory mounts for sandboxes.

Mounts use virtio-9p to share host directories with the guest. Changes to mounts
on a running sandbox take effect on next restart.`,
	}

	cmd.AddCommand(mountListCmd())
	cmd.AddCommand(mountAddCmd())
	cmd.AddCommand(mountRemoveCmd())

	return cmd
}

func mountListCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "list <sandbox>",
		Short: "List mounts for a sandbox",
		Long: `Display all host-to-guest directory mounts configured for a sandbox.

Examples:
  tent mount list mybox
  tent mount list mybox --format json`,
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

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("failed to load config for sandbox '%s': %w", name, err)
			}

			mounts := config.Mounts
			if mounts == nil {
				mounts = []models.MountConfig{}
			}

			if outputFormat == "json" {
				data, err := json.MarshalIndent(mounts, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			if len(mounts) == 0 {
				fmt.Printf("No mounts configured for sandbox '%s'\n", name)
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "HOST PATH\tGUEST PATH\tMODE\n")
			for _, m := range mounts {
				mode := "rw"
				if m.Readonly {
					mode = "ro"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", m.Host, m.Guest, mode)
			}
			w.Flush()

			return nil
		},
	}

	cmd.Flags().StringVar(&outputFormat, "format", "text", "Output format: text, json")
	return cmd
}

func mountAddCmd() *cobra.Command {
	var readonly bool

	cmd := &cobra.Command{
		Use:   "add <sandbox> <host-path> <guest-path>",
		Short: "Add a host-to-guest mount to a sandbox",
		Long: `Add a directory mount that shares a host directory with the guest via virtio-9p.
The host path must exist and be a directory. The guest path must be absolute.
Changes take effect on next sandbox start/restart.

Examples:
  tent mount add mybox ./workspace /workspace
  tent mount add mybox /data /mnt/data --readonly
  tent mount add mybox ~/projects /home/user/projects`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			hostPath := args[1]
			guestPath := args[2]

			// Resolve host path to absolute
			if !filepath.IsAbs(hostPath) {
				abs, err := filepath.Abs(hostPath)
				if err != nil {
					return fmt.Errorf("failed to resolve host path %q: %w", hostPath, err)
				}
				hostPath = abs
			}

			// Validate host path exists and is a directory
			info, err := os.Stat(hostPath)
			if err != nil {
				return fmt.Errorf("host path %q does not exist: %w", hostPath, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("host path %q is not a directory", hostPath)
			}

			// Validate guest path is absolute
			if !filepath.IsAbs(guestPath) {
				return fmt.Errorf("guest path %q must be absolute", guestPath)
			}

			baseDir := getBaseDir()
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("failed to load config for sandbox '%s': %w", name, err)
			}

			// Check for duplicate guest mount point
			guestNorm := filepath.Clean(guestPath)
			for _, existing := range config.Mounts {
				if filepath.Clean(existing.Guest) == guestNorm {
					return fmt.Errorf("guest mount point %q already exists", guestPath)
				}
			}

			// Check for duplicate host path
			for _, existing := range config.Mounts {
				existingAbs := existing.Host
				if !filepath.IsAbs(existingAbs) {
					if abs, err := filepath.Abs(existingAbs); err == nil {
						existingAbs = abs
					}
				}
				if existingAbs == hostPath {
					return fmt.Errorf("host path %q is already mounted at %q", hostPath, existing.Guest)
				}
			}

			config.Mounts = append(config.Mounts, models.MountConfig{
				Host:     hostPath,
				Guest:    guestNorm,
				Readonly: readonly,
			})

			if err := manager.UpdateConfig(name, config); err != nil {
				return fmt.Errorf("failed to update config: %w", err)
			}

			mode := "read-write"
			if readonly {
				mode = "read-only"
			}
			fmt.Printf("Added mount %s -> %s (%s) for sandbox '%s'\n", hostPath, guestNorm, mode, name)
			fmt.Println("Changes take effect on next start/restart.")

			return nil
		},
	}

	cmd.Flags().BoolVar(&readonly, "readonly", false, "Mount as read-only")
	return cmd
}

func mountRemoveCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "remove <sandbox> <guest-path>",
		Short: "Remove a mount from a sandbox",
		Long: `Remove a host-to-guest directory mount by its guest path.
Changes take effect on next sandbox start/restart.

Examples:
  tent mount remove mybox /workspace
  tent mount remove mybox --all`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if !all && len(args) < 2 {
				return fmt.Errorf("guest path required (or use --all to remove all mounts)")
			}

			baseDir := getBaseDir()
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("failed to load config for sandbox '%s': %w", name, err)
			}

			if all {
				count := len(config.Mounts)
				if count == 0 {
					fmt.Printf("No mounts to remove for sandbox '%s'\n", name)
					return nil
				}
				config.Mounts = nil

				if err := manager.UpdateConfig(name, config); err != nil {
					return fmt.Errorf("failed to update config: %w", err)
				}

				fmt.Printf("Removed all %d mount(s) from sandbox '%s'\n", count, name)
				fmt.Println("Changes take effect on next start/restart.")
				return nil
			}

			guestPath := args[1]
			guestNorm := filepath.Clean(guestPath)

			var remaining []models.MountConfig
			found := false
			var removedHost string
			for _, m := range config.Mounts {
				if filepath.Clean(m.Guest) == guestNorm {
					found = true
					removedHost = m.Host
				} else {
					remaining = append(remaining, m)
				}
			}

			if !found {
				return fmt.Errorf("no mount found with guest path %q for sandbox '%s'", guestPath, name)
			}

			config.Mounts = remaining

			if err := manager.UpdateConfig(name, config); err != nil {
				return fmt.Errorf("failed to update config: %w", err)
			}

			fmt.Printf("Removed mount %s -> %s from sandbox '%s'\n", removedHost, guestNorm, name)
			fmt.Println("Changes take effect on next start/restart.")

			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Remove all mounts")
	return cmd
}
