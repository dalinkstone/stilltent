package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/image"
)

func registryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Manage registry credentials",
		Long:  `Store, remove, and list credentials for OCI/Docker registries used for pulling and pushing images.`,
	}

	cmd.AddCommand(registryLoginCmd())
	cmd.AddCommand(registryLogoutCmd())
	cmd.AddCommand(registryListCmd())

	return cmd
}

func registryLoginCmd() *cobra.Command {
	var (
		username string
		password string
	)

	cmd := &cobra.Command{
		Use:   "login <registry>",
		Short: "Store credentials for a registry",
		Long: `Store authentication credentials for an OCI/Docker registry.
Credentials are saved locally and used automatically for pull and push operations.

Examples:
  tent registry login docker.io --username myuser --password mytoken
  tent registry login ghcr.io -u myuser -p ghp_xxxx
  tent registry login myregistry.example.com -u admin -p secret`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry := image.NormalizeRegistry(args[0])

			if username == "" {
				return fmt.Errorf("--username is required")
			}
			if password == "" {
				return fmt.Errorf("--password is required")
			}

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			store, err := image.NewCredentialStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open credential store: %w", err)
			}

			if err := store.Store(registry, username, password); err != nil {
				return fmt.Errorf("failed to store credentials: %w", err)
			}

			fmt.Printf("Login succeeded for %s\n", registry)
			return nil
		},
	}

	cmd.Flags().StringVarP(&username, "username", "u", "", "Registry username")
	cmd.Flags().StringVarP(&password, "password", "p", "", "Registry password or access token")

	return cmd
}

func registryLogoutCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "logout [registry]",
		Short: "Remove stored credentials for a registry",
		Long: `Remove stored credentials for an OCI/Docker registry.

Examples:
  tent registry logout docker.io
  tent registry logout ghcr.io
  tent registry logout --all`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			store, err := image.NewCredentialStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open credential store: %w", err)
			}

			if all {
				creds := store.List()
				if len(creds) == 0 {
					fmt.Println("No stored credentials.")
					return nil
				}
				for _, c := range creds {
					if err := store.Remove(c.Registry); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to remove %s: %v\n", c.Registry, err)
					} else {
						fmt.Printf("Removed credentials for %s\n", c.Registry)
					}
				}
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("registry name required (or use --all)")
			}

			registry := image.NormalizeRegistry(args[0])
			if err := store.Remove(registry); err != nil {
				return fmt.Errorf("failed to remove credentials: %w", err)
			}

			fmt.Printf("Logged out from %s\n", registry)
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Remove all stored credentials")

	return cmd
}

func registryListCmd() *cobra.Command {
	var quiet bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List stored registry credentials",
		Long:  `List all registries with stored credentials. Passwords are never displayed.`,
		Aliases: []string{"ls"},
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			store, err := image.NewCredentialStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open credential store: %w", err)
			}

			creds := store.List()
			if len(creds) == 0 {
				fmt.Println("No stored credentials.")
				return nil
			}

			if quiet {
				for _, c := range creds {
					fmt.Println(c.Registry)
				}
				return nil
			}

			// Header
			fmt.Printf("%-40s %-20s %s\n", "REGISTRY", "USERNAME", "CREATED")
			fmt.Println(strings.Repeat("-", 80))
			for _, c := range creds {
				created := c.CreatedAt.Local().Format("2006-01-02 15:04")
				fmt.Printf("%-40s %-20s %s\n", c.Registry, c.Username, created)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only show registry names")

	return cmd
}
