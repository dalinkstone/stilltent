package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/storage"
)

func imageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Image management commands",
	}

	cmd.AddCommand(imageListCmd())
	cmd.AddCommand(imagePullCmd())

	return cmd
}

func imageListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available base rootfs images",
		Long:  `List available base rootfs images.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Create storage manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := storage.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create storage manager: %w", err)
			}

			// List images (rootfs directories)
			rootfsDir := manager.GetBaseDir()
			if rootfsDir == "" {
				rootfsDir = "/var/lib/tent/rootfs"
			}

			// List rootfs directories
			fmt.Println("Listing images:")
			fmt.Println("(Base images are stored in", rootfsDir, ")")
			fmt.Println("Run 'tent create <name>' to create a new VM from a base image.")

			return nil
		},
	}
}

func imagePullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <name>",
		Short: "Download a base rootfs image",
		Long:  `Download a base rootfs image.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			fmt.Printf("Pulling image: %s\n", name)
			fmt.Println("Note: Base image downloading not yet implemented.")
			fmt.Println("Run 'tent create <name>' to create a new VM with a base image.")

			return nil
		},
	}
}
