package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/sandbox"
)

func cloneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clone <source> <new-name>",
		Short: "Clone a sandbox by copying its rootfs and configuration",
		Long: `Clone an existing sandbox to create a new one with an identical rootfs and
configuration. The source sandbox must be stopped. The clone gets its own
network setup, SSH keys, and independent rootfs copy.

This is useful for creating test variants from a known-good base, or for
forking an agent sandbox to try different approaches in parallel.

See also: tent create, tent snapshot, tent checkpoint`,
		Example: `  # Clone a sandbox
  tent clone mybox mybox-copy

  # Clone and rename for a new version
  tent clone agent agent-v2`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			srcName, dstName := args[0], args[1]

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

			if err := manager.Clone(srcName, dstName); err != nil {
				return fmt.Errorf("failed to clone sandbox: %w", err)
			}

			fmt.Printf("Successfully cloned '%s' -> '%s'\n", srcName, dstName)
			return nil
		},
	}

	return cmd
}
