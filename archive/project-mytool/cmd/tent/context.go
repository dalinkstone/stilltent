package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// TentContext represents a named connection target for tent operations
type TentContext struct {
	Name        string `json:"name"`
	Host        string `json:"host"`              // "local" or SSH host (user@host:port)
	Description string `json:"description"`       // user-provided description
	DataDir     string `json:"data_dir,omitempty"` // override data directory
	SSHKey      string `json:"ssh_key,omitempty"`  // path to SSH key for remote hosts
	SSHPort     int    `json:"ssh_port,omitempty"` // SSH port (default 22)
	CreatedAt   string `json:"created_at"`
}

// ContextConfig holds all contexts and the active one
type ContextConfig struct {
	Active   string         `json:"active"`
	Contexts []*TentContext `json:"contexts"`
}

func contextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Manage tent execution contexts (local, remote hosts)",
		Long: `Manage named contexts that determine where tent commands execute.

A context defines a target host — either the local machine or a remote host
reachable via SSH. This allows managing sandboxes across multiple machines
from a single CLI.

Examples:
  tent context create local --host local --description "Local development"
  tent context create staging --host admin@staging.example.com --ssh-key ~/.ssh/id_ed25519
  tent context use staging
  tent context list
  tent context current`,
	}

	cmd.AddCommand(contextCreateCmd())
	cmd.AddCommand(contextListCmd())
	cmd.AddCommand(contextUseCmd())
	cmd.AddCommand(contextCurrentCmd())
	cmd.AddCommand(contextDeleteCmd())
	cmd.AddCommand(contextInspectCmd())
	cmd.AddCommand(contextUpdateCmd())
	cmd.AddCommand(contextExportCmd())
	cmd.AddCommand(contextImportCmd())

	return cmd
}

func getContextConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tent", "contexts.json"), nil
}

func loadContextConfig() (*ContextConfig, error) {
	path, err := getContextConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return default config with local context
			return &ContextConfig{
				Active: "default",
				Contexts: []*TentContext{
					{
						Name:        "default",
						Host:        "local",
						Description: "Local machine (default)",
						CreatedAt:   time.Now().UTC().Format(time.RFC3339),
					},
				},
			}, nil
		}
		return nil, err
	}

	var cfg ContextConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse contexts config: %w", err)
	}
	return &cfg, nil
}

func saveContextConfig(cfg *ContextConfig) error {
	path, err := getContextConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

func findContext(cfg *ContextConfig, name string) *TentContext {
	for _, ctx := range cfg.Contexts {
		if ctx.Name == name {
			return ctx
		}
	}
	return nil
}

func contextCreateCmd() *cobra.Command {
	var host, description, dataDir, sshKey string
	var sshPort int

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := loadContextConfig()
			if err != nil {
				return err
			}

			if findContext(cfg, name) != nil {
				return fmt.Errorf("context %q already exists", name)
			}

			if host == "" {
				host = "local"
			}

			ctx := &TentContext{
				Name:        name,
				Host:        host,
				Description: description,
				DataDir:     dataDir,
				SSHKey:      sshKey,
				SSHPort:     sshPort,
				CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			}

			cfg.Contexts = append(cfg.Contexts, ctx)

			if err := saveContextConfig(cfg); err != nil {
				return err
			}

			fmt.Printf("Context %q created\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "local", "Target host (\"local\" or user@host)")
	cmd.Flags().StringVar(&description, "description", "", "Human-readable description")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "Override data directory on target host")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "Path to SSH private key for remote hosts")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port for remote hosts")

	return cmd
}

func contextListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all contexts",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadContextConfig()
			if err != nil {
				return err
			}

			if outputJSON {
				data, err := json.MarshalIndent(cfg, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			sort.Slice(cfg.Contexts, func(i, j int) bool {
				return cfg.Contexts[i].Name < cfg.Contexts[j].Name
			})

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CURRENT\tNAME\tHOST\tDESCRIPTION")
			for _, ctx := range cfg.Contexts {
				current := ""
				if ctx.Name == cfg.Active {
					current = "*"
				}
				desc := ctx.Description
				if len(desc) > 50 {
					desc = desc[:47] + "..."
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", current, ctx.Name, ctx.Host, desc)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

func contextUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Switch the active context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := loadContextConfig()
			if err != nil {
				return err
			}

			if findContext(cfg, name) == nil {
				return fmt.Errorf("context %q not found", name)
			}

			cfg.Active = name

			if err := saveContextConfig(cfg); err != nil {
				return err
			}

			fmt.Printf("Switched to context %q\n", name)
			return nil
		},
	}
}

func contextCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the current active context",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadContextConfig()
			if err != nil {
				return err
			}

			ctx := findContext(cfg, cfg.Active)
			if ctx == nil {
				fmt.Printf("Active context: %s (not found in config)\n", cfg.Active)
				return nil
			}

			fmt.Printf("Active context: %s\n", ctx.Name)
			fmt.Printf("Host:           %s\n", ctx.Host)
			if ctx.Description != "" {
				fmt.Printf("Description:    %s\n", ctx.Description)
			}
			if ctx.DataDir != "" {
				fmt.Printf("Data directory: %s\n", ctx.DataDir)
			}
			if ctx.SSHKey != "" {
				fmt.Printf("SSH key:        %s\n", ctx.SSHKey)
			}
			if ctx.SSHPort != 0 && ctx.SSHPort != 22 {
				fmt.Printf("SSH port:       %d\n", ctx.SSHPort)
			}
			return nil
		},
	}
}

func contextDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a context",
		Aliases: []string{"rm"},
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := loadContextConfig()
			if err != nil {
				return err
			}

			if name == "default" && !force {
				return fmt.Errorf("cannot delete the default context (use --force to override)")
			}

			found := false
			filtered := make([]*TentContext, 0, len(cfg.Contexts))
			for _, ctx := range cfg.Contexts {
				if ctx.Name == name {
					found = true
					continue
				}
				filtered = append(filtered, ctx)
			}

			if !found {
				return fmt.Errorf("context %q not found", name)
			}

			cfg.Contexts = filtered

			// If we deleted the active context, switch to default
			if cfg.Active == name {
				cfg.Active = "default"
				if findContext(cfg, "default") == nil && len(cfg.Contexts) > 0 {
					cfg.Active = cfg.Contexts[0].Name
				}
			}

			if err := saveContextConfig(cfg); err != nil {
				return err
			}

			fmt.Printf("Context %q deleted\n", name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force delete even for default context")
	return cmd
}

func contextInspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <name>",
		Short: "Show detailed information about a context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := loadContextConfig()
			if err != nil {
				return err
			}

			ctx := findContext(cfg, name)
			if ctx == nil {
				return fmt.Errorf("context %q not found", name)
			}

			data, err := json.MarshalIndent(ctx, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		},
	}
}

func contextUpdateCmd() *cobra.Command {
	var host, description, dataDir, sshKey string
	var sshPort int

	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update an existing context's properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := loadContextConfig()
			if err != nil {
				return err
			}

			ctx := findContext(cfg, name)
			if ctx == nil {
				return fmt.Errorf("context %q not found", name)
			}

			if cmd.Flags().Changed("host") {
				ctx.Host = host
			}
			if cmd.Flags().Changed("description") {
				ctx.Description = description
			}
			if cmd.Flags().Changed("data-dir") {
				ctx.DataDir = dataDir
			}
			if cmd.Flags().Changed("ssh-key") {
				ctx.SSHKey = sshKey
			}
			if cmd.Flags().Changed("ssh-port") {
				ctx.SSHPort = sshPort
			}

			if err := saveContextConfig(cfg); err != nil {
				return err
			}

			fmt.Printf("Context %q updated\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "Target host")
	cmd.Flags().StringVar(&description, "description", "", "Human-readable description")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "Override data directory")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "Path to SSH private key")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port for remote hosts")

	return cmd
}

func contextExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export",
		Short: "Export all contexts as JSON to stdout",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadContextConfig()
			if err != nil {
				return err
			}

			data, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		},
	}
}

func contextImportCmd() *cobra.Command {
	var merge bool

	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Import contexts from a JSON file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("failed to read file: %w", err)
			}

			var imported ContextConfig
			if err := json.Unmarshal(data, &imported); err != nil {
				return fmt.Errorf("invalid contexts file: %w", err)
			}

			if !merge {
				if err := saveContextConfig(&imported); err != nil {
					return err
				}
				fmt.Printf("Imported %d contexts\n", len(imported.Contexts))
				return nil
			}

			// Merge mode: add imported contexts that don't already exist
			cfg, err := loadContextConfig()
			if err != nil {
				return err
			}

			added := 0
			for _, ictx := range imported.Contexts {
				if findContext(cfg, ictx.Name) == nil {
					cfg.Contexts = append(cfg.Contexts, ictx)
					added++
				}
			}

			if err := saveContextConfig(cfg); err != nil {
				return err
			}

			fmt.Printf("Merged %d new contexts (%d skipped as duplicates)\n", added, len(imported.Contexts)-added)
			return nil
		},
	}

	cmd.Flags().BoolVar(&merge, "merge", false, "Merge with existing contexts instead of replacing")
	return cmd
}
