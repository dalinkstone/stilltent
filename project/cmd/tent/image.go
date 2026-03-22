package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/image"
	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func imageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Image management commands",
	}

	cmd.AddCommand(imageListCmd())
	cmd.AddCommand(imagePullCmd())
	cmd.AddCommand(imageExtractCmd())
	cmd.AddCommand(imageRmCmd())
	cmd.AddCommand(imageInspectCmd())
	cmd.AddCommand(imageTagCmd())
	cmd.AddCommand(imagePruneCmd())
	cmd.AddCommand(imageBuildCmd())

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

func imageRmCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "rm <image> [image...]",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove one or more locally cached images",
		Long:    `Remove one or more locally cached images from the local store.`,
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			var errs []error
			for _, name := range args {
				if err := manager.RemoveImage(name); err != nil {
					if force {
						fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
					} else {
						errs = append(errs, err)
					}
				} else {
					fmt.Printf("Removed image '%s'\n", name)
				}
			}

			if len(errs) > 0 {
				return fmt.Errorf("failed to remove %d image(s): %v", len(errs), errs[0])
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Ignore errors for missing images")
	return cmd
}

func imageTagCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tag <source> <target>",
		Short: "Create an alias for an existing image",
		Long: `Tag an existing image with a new name, creating an alias.
The original image is preserved. If the target name already exists, it is replaced.

Examples:
  tent image tag ubuntu_22.04 my-base
  tent image tag my-base production-image`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := args[0]
			target := args[1]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			if err := manager.TagImage(source, target); err != nil {
				return fmt.Errorf("failed to tag image: %w", err)
			}

			fmt.Printf("Tagged image '%s' as '%s'\n", source, target)
			return nil
		},
	}
	return cmd
}

func imageInspectCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "inspect <image>",
		Short: "Show detailed information about an image",
		Long:  `Display detailed metadata and format information about a locally cached image.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			detail, err := manager.InspectImage(name)
			if err != nil {
				return err
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(detail)
			}

			fmt.Printf("Image: %s\n", detail.Name)
			fmt.Printf("  Path:      %s\n", detail.Path)
			fmt.Printf("  Format:    %s\n", detail.Format)
			fmt.Printf("  Size:      %d MB (%d bytes)\n", detail.SizeMB, detail.SizeBytes)
			fmt.Printf("  Created:   %s\n", detail.CreatedAt)
			fmt.Printf("  Modified:  %s\n", detail.ModTime)
			if detail.HasRootfs {
				fmt.Printf("  Rootfs:    %s\n", detail.RootfsPath)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

func imagePruneCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove images not used by any sandbox",
		Long: `Remove locally cached images that are not referenced by any existing sandbox.
Running sandboxes, stopped sandboxes, and created sandboxes all count as "in use".

Examples:
  tent image prune
  tent image prune --force`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			// Get sandbox manager to find in-use images
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			sandboxMgr, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create sandbox manager: %w", err)
			}

			if err := sandboxMgr.Setup(); err != nil {
				return fmt.Errorf("failed to setup sandbox manager: %w", err)
			}

			vms, err := sandboxMgr.List()
			if err != nil {
				return fmt.Errorf("failed to list sandboxes: %w", err)
			}

			// Build set of image names referenced by sandboxes
			inUse := make(map[string]bool)
			for _, v := range vms {
				if v.ImageRef != "" {
					// Normalize: strip tag separators for matching
					// Image refs may be like "ubuntu:22.04" -> stored as "ubuntu_22.04"
					ref := strings.ReplaceAll(v.ImageRef, ":", "_")
					ref = strings.ReplaceAll(ref, "/", "_")
					inUse[ref] = true
					// Also keep the raw ref in case it matches directly
					inUse[v.ImageRef] = true
				}
			}

			// Create image manager
			imgMgr, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			// Preview what would be removed
			images, err := imgMgr.ListImages()
			if err != nil {
				return fmt.Errorf("failed to list images: %w", err)
			}

			var candidates []string
			for _, img := range images {
				if !inUse[img.Name] {
					candidates = append(candidates, img.Name)
				}
			}

			if len(candidates) == 0 {
				fmt.Println("No unused images to remove.")
				return nil
			}

			if !force {
				fmt.Printf("The following %d image(s) will be removed:\n", len(candidates))
				for _, name := range candidates {
					fmt.Printf("  - %s\n", name)
				}
				fmt.Print("\nProceed? [y/N] ")
				var response string
				fmt.Scanln(&response)
				if response != "y" && response != "Y" && response != "yes" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			removed, freedBytes, err := imgMgr.PruneImages(inUse)
			if err != nil {
				return fmt.Errorf("failed to prune images: %w", err)
			}

			for _, name := range removed {
				fmt.Printf("Removed: %s\n", name)
			}

			freedMB := float64(freedBytes) / (1024 * 1024)
			fmt.Printf("\nRemoved %d image(s), freed %.1f MB\n", len(removed), freedMB)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")
	return cmd
}

func imageBuildCmd() *cobra.Command {
	var file string
	var tag string

	cmd := &cobra.Command{
		Use:   "build <name>",
		Short: "Build a custom image from a Tentfile",
		Long: `Build a custom sandbox image from a Tentfile (similar to a Dockerfile).

The Tentfile supports the following instructions:
  FROM <base-image>       Base image (required, must be first)
  RUN <command>           Shell command to run during build
  COPY <src> <dst>        Copy files from host to image
  ENV <key>=<value>       Set environment variable
  WORKDIR <path>          Set working directory
  EXPOSE <port>           Document exposed ports
  LABEL <key>=<value>     Add metadata label

RUN commands are recorded as a build script at /etc/tent/build.sh
and executed inside the sandbox on first boot.

Examples:
  tent image build myimage
  tent image build myimage -f custom.tentfile
  tent image build myimage -t v1.0`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Find Tentfile
			tentfilePath := file
			if tentfilePath == "" {
				// Look for Tentfile in current directory
				for _, candidate := range []string{"Tentfile", "tentfile", "Tentfile.tent"} {
					if _, err := os.Stat(candidate); err == nil {
						tentfilePath = candidate
						break
					}
				}
				if tentfilePath == "" {
					return fmt.Errorf("no Tentfile found in current directory (use -f to specify)")
				}
			}

			// Resolve to absolute path
			if !filepath.IsAbs(tentfilePath) {
				abs, err := filepath.Abs(tentfilePath)
				if err != nil {
					return fmt.Errorf("failed to resolve Tentfile path: %w", err)
				}
				tentfilePath = abs
			}

			// Apply tag to name
			imageName := name
			if tag != "" {
				imageName = name + "_" + tag
			}

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			imgMgr, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			fmt.Printf("Building image '%s' from %s...\n", imageName, tentfilePath)

			result, err := imgMgr.BuildImage(imageName, tentfilePath)
			if err != nil {
				return fmt.Errorf("build failed: %w", err)
			}

			fmt.Printf("\nBuild complete:\n")
			fmt.Printf("  Image:  %s\n", result.ImageName)
			fmt.Printf("  Base:   %s\n", result.BaseImage)
			fmt.Printf("  Steps:  %d\n", result.Steps)
			fmt.Printf("  Time:   %s\n", result.Duration.Round(time.Millisecond))
			if len(result.Labels) > 0 {
				fmt.Printf("  Labels:\n")
				for k, v := range result.Labels {
					fmt.Printf("    %s=%s\n", k, v)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to Tentfile (default: ./Tentfile)")
	cmd.Flags().StringVarP(&tag, "tag", "t", "", "Tag for the built image")
	return cmd
}
