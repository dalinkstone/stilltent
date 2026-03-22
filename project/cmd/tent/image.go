package main

import (
	"fmt"
	"os"
	"strings"

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
		Use:   "pull <image-ref>",
		Short: "Download an image from a registry or URL",
		Long:  `Download an image from a Docker/OCI registry (Docker Hub, GCR, ECR, etc.) or URL.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			imageRef := args[0]

			fmt.Printf("Pulling image '%s'...\n", imageRef)

			// Create image manager with progress callback
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := image.NewManager(baseDir, image.WithProgressCallback(func(bytes, total int64) {
				if total > 0 {
					percent := float64(bytes) / float64(total) * 100
					fmt.Printf("\rDownloading: %.1f%% (%.1f MB / %.1f MB)", 
						percent, float64(bytes)/(1024*1024), float64(total)/(1024*1024))
				} else {
					fmt.Printf("\rDownloading: %.1f MB", float64(bytes)/(1024*1024))
				}
				if bytes >= total && total > 0 {
					fmt.Println() // Add newline when complete
				}
			}))
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			// Determine if this is a Docker-style reference or a URL
			// Docker references contain '/' (namespace/image), ':' (tag), or both
			// URLs contain '://' or start with 'http'
			var imagePath string
			if isDockerReference(imageRef) {
				imagePath, err = manager.PullOCI("image", imageRef)
			} else {
				// Treat as URL - pull with default name
				name := "image"
				if strings.Contains(imageRef, "/") {
					// Extract name from URL path
					parts := strings.Split(imageRef, "/")
					name = strings.TrimSuffix(parts[len(parts)-1], ".img")
				}
				imagePath, err = manager.Pull(name, imageRef)
			}

			if err != nil {
				return fmt.Errorf("failed to pull image: %w", err)
			}

			fmt.Printf("\nImage '%s' pulled successfully to %s\n", imageRef, imagePath)
			return nil
		},
	}
	return cmd
}

// isDockerReference checks if the string looks like a Docker/OCI image reference
// Examples: "ubuntu:22.04", "gcr.io/project/image:tag", "registry.com:5000/repo/image"
func isDockerReference(ref string) bool {
	// URLs with http/https protocol
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return false
	}
	// Contains ':' (tag specification) - common in Docker refs
	if strings.Contains(ref, ":") {
		return true
	}
	// Contains '/' (namespace/repo) - indicates registry or namespace
	if strings.Contains(ref, "/") {
		// But check if it's a local file path (contains '..' or starts with './' or '/')
		if strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "/") || strings.Contains(ref, "..") {
			return false
		}
		return true
	}
	// Simple name without separators - likely just a repo name, treat as Docker ref
	// Default registry will be used (Docker Hub)
	return true
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
