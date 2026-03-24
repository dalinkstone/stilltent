package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

func pruneCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove all stopped sandboxes",
		Long:  `Remove all sandboxes that are in a stopped or created state. Running sandboxes are not affected.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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

			vms, err := manager.List()
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			// Find stopped/created/error sandboxes
			var candidates []*models.VMState
			for _, v := range vms {
				if v.Status == models.VMStatusStopped || v.Status == models.VMStatusCreated || v.Status == models.VMStatusError {
					candidates = append(candidates, v)
				}
			}

			if len(candidates) == 0 {
				fmt.Println("No stopped sandboxes to remove.")
				return nil
			}

			if !force {
				fmt.Printf("The following %d sandbox(es) will be removed:\n", len(candidates))
				for _, v := range candidates {
					fmt.Printf("  - %s (%s)\n", v.Name, v.Status)
				}
				fmt.Print("\nProceed? [y/N] ")

				var response string
				fmt.Scanln(&response)
				if response != "y" && response != "Y" && response != "yes" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			destroyed := 0
			var errors []string
			for _, v := range candidates {
				if err := manager.Destroy(v.Name); err != nil {
					errors = append(errors, fmt.Sprintf("  %s: %v", v.Name, err))
				} else {
					fmt.Printf("Removed: %s\n", v.Name)
					destroyed++
				}
			}

			if len(errors) > 0 {
				fmt.Printf("\n%d sandbox(es) removed, %d failed:\n", destroyed, len(errors))
				for _, e := range errors {
					fmt.Println(e)
				}
				return fmt.Errorf("some sandboxes could not be removed")
			}

			fmt.Printf("\nSuccessfully removed %d sandbox(es).\n", destroyed)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")
	return cmd
}
