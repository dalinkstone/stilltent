package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func commitCmd() *cobra.Command {
	var (
		message string
	)

	cmd := &cobra.Command{
		Use:   "commit <sandbox> <image-name>",
		Short: "Create a new image from a sandbox's current state",
		Long: `Save a sandbox's current root filesystem as a reusable image.

The committed image can be used to create new sandboxes with
'tent create --from <image-name>'. This is useful for capturing
a sandbox after installing packages, configuring services, or
reaching a known-good state.

The sandbox can be running or stopped. For consistency, stopping
the sandbox first is recommended.

Examples:
  tent commit mybox my-configured-image
  tent commit mybox dev-snapshot -m "installed dev tools"
  tent commit mybox base-image --message "base image with Python 3.11"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			imageName := args[1]

			// Validate image name
			if strings.ContainsAny(imageName, " /\\:*?\"<>|") {
				return fmt.Errorf("invalid image name %q — must not contain spaces or special characters", imageName)
			}

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

			fmt.Printf("Committing sandbox %q as image %q...\n", sandboxName, imageName)

			destPath, err := manager.Commit(sandboxName, imageName, message)
			if err != nil {
				return fmt.Errorf("commit failed: %w", err)
			}

			// Report result
			if info, err := os.Stat(destPath); err == nil {
				sizeMB := float64(info.Size()) / (1024 * 1024)
				if sizeMB >= 1 {
					fmt.Printf("Image %q created (%.1f MB)\n", imageName, sizeMB)
				} else {
					fmt.Printf("Image %q created (%d bytes)\n", imageName, info.Size())
				}
			} else {
				fmt.Printf("Image %q created at %s\n", imageName, destPath)
			}

			fmt.Printf("Use 'tent create <name> --from %s' to create a sandbox from this image.\n", imageName)

			return nil
		},
	}

	cmd.Flags().StringVarP(&message, "message", "m", "", "Commit message describing the image")

	return cmd
}
