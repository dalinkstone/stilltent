package main

import (
	"fmt"

	"github.com/spf13/cobra"
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
			fmt.Println("Listing images:")
			// TODO: Implement image list logic
			fmt.Println("No images found.")
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
			// TODO: Implement image pull logic
			return nil
		},
	}
}
