package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/image"
)

func imageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Image management commands",
	}

	cmd.AddCommand(imageListCmd())
	cmd.AddCommand(imagePullCmd())
	cmd.AddCommand(imageExtractCmd())

	return cmd
}

func imageListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available base rootfs images",
		Long:  `List available base rootfs images.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Create image manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
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
			url := ""
			if len(args) > 1 {
				url = args[1]
			}

			// Default URLs for common images
			if url == "" {
				url = fmt.Sprintf("https://github.com/dalinkstone/tent/releases/download/images/%s.img", name)
			}

			fmt.Printf("Pulling image '%s' from %s...\n", name, url)

			// Create image manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			// Pull the image
			imagePath, err := manager.Pull(name, url)
			if err != nil {
				return fmt.Errorf("failed to pull image: %w", err)
			}

			fmt.Printf("Image '%s' pulled successfully to %s\n", name, imagePath)
			return nil
		},
	}
	return cmd
}

func imageExtractCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extract <image>",
		Short: "Extract kernel and initrd from an image",
		Long:  `Extract kernel and initrd from an ISO or rootfs image.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			imagePath := args[0]

			// Create image manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			// Extract the image
			info, err := manager.Extract(imagePath)
			if err != nil {
				return fmt.Errorf("failed to extract image: %w", err)
			}

			fmt.Printf("Extracted image '%s' to %s (%d MB)\n", info.Name, info.Path, info.SizeMB)
			return nil
		},
	}
	return cmd
}
