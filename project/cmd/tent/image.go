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

			// List images
			images, err := manager.ListImages()
			if err != nil {
				return fmt.Errorf("failed to list images: %w", err)
			}

			if len(images) == 0 {
				fmt.Println("No base images found.")
				fmt.Println("Run 'tent image pull <name> [url]' to download a base image.")
				return nil
			}

			fmt.Println("Available base images:")
			fmt.Println("----------------------")
			for _, img := range images {
				fmt.Printf("  %s (%d MB)\n", img.Name, img.SizeMB)
			}

			return nil
		},
	}
}

func imagePullCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull <name> [url]",
		Short: "Download a base rootfs image",
		Long:  `Download a base rootfs image from a URL.`,
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			url := args[1]

			// Default URLs for common images
			if url == "" {
				url = fmt.Sprintf("https://github.com/dalinkstone/tent/releases/download/images/%s.img", name)
			}

			fmt.Printf("Pulling image '%s' from %s...\n", name, url)

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

			// Pull the image
			imagePath, err := manager.PullImage(name, url)
			if err != nil {
				return fmt.Errorf("failed to pull image: %w", err)
			}

			fmt.Printf("Image '%s' pulled successfully to %s\n", name, imagePath)
			return nil
		},
	}
	return cmd
}
