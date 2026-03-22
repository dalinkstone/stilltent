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
	"github.com/dalinkstone/tent/internal/storage"
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
	cmd.AddCommand(imagePushCmd())
	cmd.AddCommand(imageSaveCmd())
	cmd.AddCommand(imageLoadCmd())
	cmd.AddCommand(imageCacheCmd())
	cmd.AddCommand(imageVerifyCmd())
	cmd.AddCommand(imageConvertCmd())
	cmd.AddCommand(imageSearchCmd())
	cmd.AddCommand(imageTagsCmd())

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

func imagePushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push <name> <registry-ref>",
		Short: "Push a local image to an OCI registry",
		Long: `Push a locally stored image to a remote OCI-compliant registry.

The registry reference should include the full repository path and optional tag.
If no tag is specified, "latest" is used.

Examples:
  tent image push myimage registry.example.com/myrepo/myimage:v1
  tent image push myimage ghcr.io/user/myimage:latest
  tent image push myimage docker.io/library/myimage`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ref := args[1]

			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home directory: %w", err)
			}
			baseDir := filepath.Join(homeDir, ".tent")

			fmt.Printf("Pushing image %s to %s...\n", name, ref)

			manager, err := image.NewManager(baseDir, image.WithProgressCallback(func(bytes, total int64) {
				if total > 0 {
					pct := float64(bytes) / float64(total) * 100
					fmt.Printf("\r  Uploading: %.1f%% (%d / %d bytes)", pct, bytes, total)
				}
			}))
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			if err := manager.PushOCI(name, ref); err != nil {
				return fmt.Errorf("push failed: %w", err)
			}

			fmt.Printf("\nPushed %s to %s\n", name, ref)
			return nil
		},
	}
}

func imageSaveCmd() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "save <name>",
		Short: "Save a local image to a tarball",
		Long: `Export a locally stored image to a compressed tarball file.
The tarball includes the image data and a JSON manifest with metadata.
Use "tent image load" to import the tarball on another machine.

Examples:
  tent image save ubuntu-22.04 -o ubuntu.tar.gz
  tent image save myimage -o /tmp/myimage.tar.gz`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if output == "" {
				output = name + ".tar.gz"
			}

			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home directory: %w", err)
			}
			baseDir := filepath.Join(homeDir, ".tent")

			manager, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			fmt.Printf("Saving image %q to %s...\n", name, output)
			if err := manager.SaveImage(name, output); err != nil {
				return fmt.Errorf("save failed: %w", err)
			}

			info, _ := os.Stat(output)
			if info != nil {
				sizeMB := float64(info.Size()) / (1024 * 1024)
				fmt.Printf("Saved %s (%.1f MB)\n", output, sizeMB)
			} else {
				fmt.Printf("Saved %s\n", output)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path (default: <name>.tar.gz)")
	return cmd
}

func imageLoadCmd() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "load <file>",
		Short: "Load an image from a tarball",
		Long: `Import a previously saved image from a compressed tarball.
If --name is not specified, the original image name from the tarball is used.

Examples:
  tent image load ubuntu.tar.gz
  tent image load myimage.tar.gz --name custom-name`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			archivePath := args[0]

			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home directory: %w", err)
			}
			baseDir := filepath.Join(homeDir, ".tent")

			manager, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			fmt.Printf("Loading image from %s...\n", archivePath)
			finalName, err := manager.LoadImage(archivePath, name)
			if err != nil {
				return fmt.Errorf("load failed: %w", err)
			}

			fmt.Printf("Loaded image %q\n", finalName)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Override the image name (default: use name from tarball)")
	return cmd
}

func imageCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage the layer cache for OCI image pulls",
		Long: `View and manage the content-addressable layer cache. Downloaded OCI layers
are cached by digest so subsequent pulls of images sharing layers skip
re-downloading them.

Examples:
  tent image cache list
  tent image cache stats
  tent image cache prune --older-than 7d
  tent image cache prune --max-size 1GB
  tent image cache clear`,
	}

	cmd.AddCommand(imageCacheListCmd())
	cmd.AddCommand(imageCacheStatsCmd())
	cmd.AddCommand(imageCachePruneCmd())
	cmd.AddCommand(imageCacheClearCmd())
	return cmd
}

func imageCacheListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List cached image layers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr, err := image.NewManager(baseDir)
			if err != nil {
				return err
			}
			cache := mgr.GetLayerCache()
			if cache == nil {
				return fmt.Errorf("layer cache not available")
			}

			layers := cache.List()
			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(layers)
			}

			if len(layers) == 0 {
				fmt.Println("No cached layers.")
				return nil
			}

			fmt.Printf("%-72s  %10s  %s\n", "DIGEST", "SIZE", "LAST USED")
			for _, l := range layers {
				digest := l.Digest
				if len(digest) > 72 {
					digest = digest[:72]
				}
				sizeMB := float64(l.Size) / (1024 * 1024)
				fmt.Printf("%-72s  %8.1f MB  %s\n", digest, sizeMB, l.LastUsed)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func imageCacheStatsCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show layer cache statistics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr, err := image.NewManager(baseDir)
			if err != nil {
				return err
			}
			cache := mgr.GetLayerCache()
			if cache == nil {
				return fmt.Errorf("layer cache not available")
			}

			stats := cache.Stats()
			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(stats)
			}

			sizeMB := float64(stats.TotalSizeBytes) / (1024 * 1024)
			fmt.Printf("Cached layers:  %d\n", stats.TotalLayers)
			fmt.Printf("Total size:     %.1f MB\n", sizeMB)
			fmt.Printf("Cache hits:     %d\n", stats.TotalHits)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func imageCachePruneCmd() *cobra.Command {
	var (
		olderThan string
		maxSize   string
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove old or excess cached layers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr, err := image.NewManager(baseDir)
			if err != nil {
				return err
			}
			cache := mgr.GetLayerCache()
			if cache == nil {
				return fmt.Errorf("layer cache not available")
			}

			if maxSize != "" {
				maxBytes, err := parseByteSize(maxSize)
				if err != nil {
					return fmt.Errorf("invalid --max-size: %w", err)
				}
				removed, freed, err := cache.PruneToSize(maxBytes)
				if err != nil {
					return err
				}
				fmt.Printf("Removed %d layers, freed %.1f MB\n", removed, float64(freed)/(1024*1024))
				return nil
			}

			if olderThan != "" {
				dur, err := parseCacheDuration(olderThan)
				if err != nil {
					return fmt.Errorf("invalid --older-than: %w", err)
				}
				cutoff := time.Now().Add(-dur)
				removed, freed, err := cache.Prune(cutoff)
				if err != nil {
					return err
				}
				fmt.Printf("Removed %d layers, freed %.1f MB\n", removed, float64(freed)/(1024*1024))
				return nil
			}

			// Default: prune layers older than 30 days
			cutoff := time.Now().AddDate(0, 0, -30)
			removed, freed, err := cache.Prune(cutoff)
			if err != nil {
				return err
			}
			fmt.Printf("Removed %d layers older than 30 days, freed %.1f MB\n", removed, float64(freed)/(1024*1024))
			return nil
		},
	}

	cmd.Flags().StringVar(&olderThan, "older-than", "", "Remove layers not used since duration (e.g., 7d, 24h)")
	cmd.Flags().StringVar(&maxSize, "max-size", "", "Shrink cache to this maximum size (e.g., 1GB, 500MB)")
	return cmd
}

func imageCacheClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove all cached layers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr, err := image.NewManager(baseDir)
			if err != nil {
				return err
			}
			cache := mgr.GetLayerCache()
			if cache == nil {
				return fmt.Errorf("layer cache not available")
			}

			layers := cache.List()
			var totalFreed int64
			for _, l := range layers {
				totalFreed += l.Size
				_ = cache.Remove(l.Digest)
			}
			fmt.Printf("Cleared %d cached layers, freed %.1f MB\n", len(layers), float64(totalFreed)/(1024*1024))
			return nil
		},
	}
}

func imageVerifyCmd() *cobra.Command {
	var (
		expectedDigest string
		storeDigest    bool
		jsonOutput     bool
	)

	cmd := &cobra.Command{
		Use:   "verify <name>",
		Short: "Verify image integrity via SHA-256 digest",
		Long: `Compute the SHA-256 digest of an image and verify it against an expected value.

If --digest is provided, the image digest is compared against that value.
Otherwise, a sidecar .sha256 file is checked automatically. If neither exists,
the computed digest is printed (useful for initial recording).

Use --store to write the computed digest to a sidecar file so future verify
calls can check integrity without passing --digest explicitly.

Examples:
  tent image verify ubuntu-22.04
  tent image verify ubuntu-22.04 --digest sha256:abc123...
  tent image verify ubuntu-22.04 --store
  tent image verify ubuntu-22.04 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()
			mgr, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			if storeDigest {
				digest, err := mgr.StoreDigest(name)
				if err != nil {
					return err
				}
				if jsonOutput {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(map[string]string{
						"name":   name,
						"digest": digest,
						"stored": "true",
					})
				}
				fmt.Printf("Stored digest for %s: %s\n", name, digest)
				return nil
			}

			result, err := mgr.VerifyImage(name, expectedDigest)
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			fmt.Printf("Image:     %s\n", result.Name)
			fmt.Printf("Digest:    %s\n", result.Digest)
			fmt.Printf("Size:      %.1f MB\n", float64(result.SizeBytes)/(1024*1024))

			if result.Expected != "" {
				fmt.Printf("Expected:  %s\n", result.Expected)
				if result.DigestFile != "" {
					fmt.Printf("Source:    %s\n", result.DigestFile)
				}
				if result.Match {
					fmt.Println("Status:    OK — digest matches")
				} else {
					fmt.Println("Status:    FAILED — digest mismatch")
					return fmt.Errorf("image verification failed: digest mismatch")
				}
			} else {
				fmt.Println("Status:    computed (no expected digest to compare)")
				fmt.Println("Hint:      use --store to save this digest for future verification")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&expectedDigest, "digest", "", "Expected digest to verify against (sha256:...)")
	cmd.Flags().BoolVar(&storeDigest, "store", false, "Compute and store digest in a sidecar .sha256 file")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func imageConvertCmd() *cobra.Command {
	var (
		outputPath string
		format     string
		flatten    bool
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "convert <source>",
		Short: "Convert a disk image between raw and qcow2 formats",
		Long: `Convert a disk image between supported formats (raw, qcow2).

The source format is auto-detected. The target format is specified with --format.
Use --flatten to resolve a qcow2 backing chain into a standalone image.

Examples:
  tent image convert disk.qcow2 --format raw -o disk.raw
  tent image convert disk.raw --format qcow2 -o disk.qcow2
  tent image convert overlay.qcow2 --format qcow2 --flatten -o standalone.qcow2
  tent image convert disk.qcow2 --format raw --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			srcPath := args[0]

			if format == "" {
				return fmt.Errorf("--format is required (raw or qcow2)")
			}

			targetFormat := storage.ImageFormat(strings.ToLower(format))
			switch targetFormat {
			case storage.FormatRaw, storage.FormatQCOW2:
			default:
				return fmt.Errorf("unsupported format %q: use 'raw' or 'qcow2'", format)
			}

			// Auto-generate output path if not specified
			if outputPath == "" {
				ext := ".raw"
				if targetFormat == storage.FormatQCOW2 {
					ext = ".qcow2"
				}
				base := strings.TrimSuffix(srcPath, filepath.Ext(srcPath))
				outputPath = base + ext
			}

			// Don't overwrite source
			srcAbs, _ := filepath.Abs(srcPath)
			dstAbs, _ := filepath.Abs(outputPath)
			if srcAbs == dstAbs {
				return fmt.Errorf("output path cannot be the same as source path")
			}

			if !jsonOutput {
				srcFmt, _ := storage.DetectFormat(srcPath)
				fmt.Printf("Converting %s (%s) -> %s (%s)\n", srcPath, srcFmt, outputPath, targetFormat)
				if flatten {
					fmt.Println("Flattening backing chain...")
				}
			}

			result, err := storage.ConvertImage(srcPath, outputPath, targetFormat, flatten)
			if err != nil {
				return fmt.Errorf("conversion failed: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			fmt.Printf("Done.\n")
			fmt.Printf("  Source:       %s (%s, %.1f MB)\n", result.SourcePath, result.SourceFormat,
				float64(result.SourceBytes)/(1024*1024))
			fmt.Printf("  Output:       %s (%s, %.1f MB)\n", result.OutputPath, result.OutputFormat,
				float64(result.OutputBytes)/(1024*1024))
			fmt.Printf("  Virtual size: %.1f MB\n", float64(result.VirtualSize)/(1024*1024))

			if result.SourceFormat == storage.FormatRaw && result.OutputFormat == storage.FormatQCOW2 && result.OutputBytes < result.SourceBytes {
				saved := float64(result.SourceBytes-result.OutputBytes) / float64(result.SourceBytes) * 100
				fmt.Printf("  Space saved:  %.1f%%\n", saved)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output file path (auto-generated if omitted)")
	cmd.Flags().StringVar(&format, "format", "", "Target format: raw or qcow2 (required)")
	cmd.Flags().BoolVar(&flatten, "flatten", false, "Flatten backing chain into standalone image")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result in JSON format")
	_ = cmd.MarkFlagRequired("format")
	return cmd
}

// parseByteSize parses human-readable byte sizes like "1GB", "500MB", "100KB".
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	multiplier := int64(1)
	switch {
	case strings.HasSuffix(s, "GB"):
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		multiplier = 1024
		s = strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}

	var val float64
	if _, err := fmt.Sscanf(s, "%f", &val); err != nil {
		return 0, fmt.Errorf("cannot parse %q as byte size", s)
	}
	return int64(val * float64(multiplier)), nil
}

// parseCacheDuration parses durations with day support (e.g., "7d", "24h", "30m").
func parseCacheDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		var days float64
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%f", &days); err != nil {
			return 0, fmt.Errorf("cannot parse %q", s)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}

func imageSearchCmd() *cobra.Command {
	var (
		registry   string
		limit      int
		jsonOutput bool
		official   bool
	)

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search a registry for images",
		Long: `Search Docker Hub or another OCI registry for images matching a query.

By default searches Docker Hub. Use --registry to search a different registry.

Examples:
  tent image search ubuntu
  tent image search python --limit 10
  tent image search nginx --official
  tent image search myimage --registry ghcr.io
  tent image search alpine --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]

			client := image.NewRegistryClient()
			results, err := client.SearchImages(registry, query, limit)
			if err != nil {
				return fmt.Errorf("search failed: %w", err)
			}

			if official {
				var filtered []image.SearchResult
				for _, r := range results {
					if r.Official {
						filtered = append(filtered, r)
					}
				}
				results = filtered
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(results)
			}

			if len(results) == 0 {
				fmt.Println("No images found.")
				return nil
			}

			// Print table header
			fmt.Printf("%-40s %-8s %-10s %s\n", "NAME", "STARS", "OFFICIAL", "DESCRIPTION")
			for _, r := range results {
				officialStr := ""
				if r.Official {
					officialStr = "[OK]"
				}
				desc := r.Description
				if len(desc) > 50 {
					desc = desc[:47] + "..."
				}
				fmt.Printf("%-40s %-8d %-10s %s\n", r.Name, r.Stars, officialStr, desc)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&registry, "registry", "", "Registry to search (default: Docker Hub)")
	cmd.Flags().IntVar(&limit, "limit", 25, "Maximum number of results")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&official, "official", false, "Show only official images")

	return cmd
}

func imageTagsCmd() *cobra.Command {
	var (
		registry   string
		limit      int
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "tags <repository>",
		Short: "List tags for a repository",
		Long: `List available tags for an image repository in a registry.

By default queries Docker Hub. Use --registry to query a different registry.

Examples:
  tent image tags ubuntu
  tent image tags python
  tent image tags library/nginx --limit 10
  tent image tags myorg/myimage --registry ghcr.io
  tent image tags alpine --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo := args[0]

			// For Docker Hub, prepend "library/" for official images
			reg := registry
			if reg == "" && !strings.Contains(repo, "/") {
				repo = "library/" + repo
			}

			client := image.NewRegistryClient()
			tags, err := client.ListTags(reg, repo)
			if err != nil {
				return fmt.Errorf("failed to list tags: %w", err)
			}

			if limit > 0 && len(tags) > limit {
				tags = tags[len(tags)-limit:]
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(tags)
			}

			if len(tags) == 0 {
				fmt.Println("No tags found.")
				return nil
			}

			fmt.Printf("Tags for %s (%d total):\n", args[0], len(tags))
			for _, tag := range tags {
				fmt.Printf("  %s\n", tag)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&registry, "registry", "", "Registry to query (default: Docker Hub)")
	cmd.Flags().IntVar(&limit, "limit", 0, "Show only the last N tags")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}
