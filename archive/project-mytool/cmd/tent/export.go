package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func exportCmd() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "export <name>",
		Short: "Export a sandbox to a portable archive",
		Long: `Export a stopped sandbox to a .tar.gz archive containing its configuration,
state metadata, and root filesystem. The archive can be imported on the same
or a different machine using 'tent import'.

Examples:
  tent export mybox
  tent export mybox --output /tmp/mybox-backup.tar.gz`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

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

			// Determine output path
			if output == "" {
				output = fmt.Sprintf("%s.tar.gz", name)
			}
			if !strings.HasSuffix(output, ".tar.gz") && !strings.HasSuffix(output, ".tgz") {
				output += ".tar.gz"
			}

			// Resolve to absolute path
			if !filepath.IsAbs(output) {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get working directory: %w", err)
				}
				output = filepath.Join(wd, output)
			}

			fmt.Printf("Exporting sandbox %q to %s...\n", name, output)

			if err := manager.Export(name, output); err != nil {
				return fmt.Errorf("export failed: %w", err)
			}

			// Report file size
			if info, err := os.Stat(output); err == nil {
				sizeMB := float64(info.Size()) / (1024 * 1024)
				if sizeMB >= 1 {
					fmt.Printf("Exported: %s (%.1f MB)\n", output, sizeMB)
				} else {
					fmt.Printf("Exported: %s (%d bytes)\n", output, info.Size())
				}
			} else {
				fmt.Printf("Exported: %s\n", output)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path (default: <name>.tar.gz)")
	return cmd
}
