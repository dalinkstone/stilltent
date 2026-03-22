package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func importCmd() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "import <archive>",
		Short: "Import a sandbox from an archive",
		Long: `Import a sandbox from a .tar.gz archive previously created with 'tent export'.
The sandbox is restored in a stopped state and can be started with 'tent start'.

Examples:
  tent import mybox.tar.gz
  tent import mybox.tar.gz --name newbox`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			archivePath := args[0]

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

			label := name
			if label == "" {
				label = "(original name from archive)"
			}
			fmt.Printf("Importing sandbox from %s as %s...\n", archivePath, label)

			if err := manager.Import(archivePath, name); err != nil {
				return fmt.Errorf("import failed: %w", err)
			}

			fmt.Println("Import complete. Sandbox is in stopped state — use 'tent start' to boot it.")
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Override sandbox name (default: use original name from archive)")
	return cmd
}
