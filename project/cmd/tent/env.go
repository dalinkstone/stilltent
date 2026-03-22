package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func envCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage sandbox environment variables",
		Long: `View and modify environment variables for sandboxes.

Environment variables are passed to the sandbox on start. Changes to env vars
on a running sandbox take effect on next restart.`,
	}

	cmd.AddCommand(envListCmd())
	cmd.AddCommand(envSetCmd())
	cmd.AddCommand(envUnsetCmd())
	cmd.AddCommand(envExportCmd())
	cmd.AddCommand(envImportCmd())

	return cmd
}

func envListCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "list <sandbox>",
		Short: "List environment variables for a sandbox",
		Long: `Display all environment variables configured for a sandbox.

Examples:
  tent env list mybox
  tent env list mybox --format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("failed to load config for sandbox '%s': %w", name, err)
			}

			env := config.Env
			if env == nil {
				env = make(map[string]string)
			}

			if outputFormat == "json" {
				data, err := json.MarshalIndent(env, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			if len(env) == 0 {
				fmt.Printf("No environment variables set for sandbox '%s'\n", name)
				return nil
			}

			// Sort keys for consistent output
			keys := make([]string, 0, len(env))
			for k := range env {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			fmt.Printf("Environment variables for sandbox '%s':\n", name)
			for _, k := range keys {
				fmt.Printf("  %s=%s\n", k, env[k])
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&outputFormat, "format", "text", "Output format: text, json")
	return cmd
}

func envSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <sandbox> <KEY=VALUE> [KEY=VALUE...]",
		Short: "Set environment variables for a sandbox",
		Long: `Set one or more environment variables for a sandbox. If a variable already
exists, its value is updated. Changes take effect on next sandbox start/restart.

Examples:
  tent env set mybox ANTHROPIC_API_KEY=sk-ant-123
  tent env set mybox DB_HOST=localhost DB_PORT=5432
  tent env set mybox PATH=/usr/local/bin:/usr/bin`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			pairs := args[1:]

			// Parse key=value pairs
			envVars := make(map[string]string)
			for _, pair := range pairs {
				idx := strings.IndexByte(pair, '=')
				if idx <= 0 {
					return fmt.Errorf("invalid format %q: expected KEY=VALUE", pair)
				}
				key := pair[:idx]
				value := pair[idx+1:]
				envVars[key] = value
			}

			baseDir := getBaseDir()
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("failed to load config for sandbox '%s': %w", name, err)
			}

			if config.Env == nil {
				config.Env = make(map[string]string)
			}

			for k, v := range envVars {
				config.Env[k] = v
			}

			if err := manager.UpdateConfig(name, config); err != nil {
				return fmt.Errorf("failed to update config: %w", err)
			}

			for k, v := range envVars {
				fmt.Printf("Set %s=%s for sandbox '%s'\n", k, v, name)
			}

			return nil
		},
	}
}

func envUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <sandbox> <KEY> [KEY...]",
		Short: "Remove environment variables from a sandbox",
		Long: `Remove one or more environment variables from a sandbox's configuration.
Changes take effect on next sandbox start/restart.

Examples:
  tent env unset mybox ANTHROPIC_API_KEY
  tent env unset mybox DB_HOST DB_PORT`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			keys := args[1:]

			baseDir := getBaseDir()
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("failed to load config for sandbox '%s': %w", name, err)
			}

			if config.Env == nil {
				fmt.Printf("No environment variables set for sandbox '%s'\n", name)
				return nil
			}

			removed := 0
			for _, key := range keys {
				if _, exists := config.Env[key]; exists {
					delete(config.Env, key)
					fmt.Printf("Removed %s from sandbox '%s'\n", key, name)
					removed++
				} else {
					fmt.Printf("Variable %s not found in sandbox '%s'\n", key, name)
				}
			}

			if removed > 0 {
				if err := manager.UpdateConfig(name, config); err != nil {
					return fmt.Errorf("failed to update config: %w", err)
				}
			}

			return nil
		},
	}
}

func envExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export <sandbox>",
		Short: "Export environment variables in shell format",
		Long: `Export all environment variables for a sandbox in shell-compatible format,
suitable for sourcing in a shell script.

Examples:
  tent env export mybox
  tent env export mybox > mybox.env
  eval $(tent env export mybox)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("failed to load config for sandbox '%s': %w", name, err)
			}

			if config.Env == nil || len(config.Env) == 0 {
				return nil
			}

			keys := make([]string, 0, len(config.Env))
			for k := range config.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, k := range keys {
				// Shell-escape the value
				v := strings.ReplaceAll(config.Env[k], "'", "'\"'\"'")
				fmt.Printf("export %s='%s'\n", k, v)
			}
			return nil
		},
	}
}

func envImportCmd() *cobra.Command {
	var overwrite bool

	cmd := &cobra.Command{
		Use:   "import <sandbox> <KEY=VALUE> [KEY=VALUE...]",
		Short: "Import environment variables from key=value pairs",
		Long: `Bulk import environment variables into a sandbox. By default, existing
variables are preserved. Use --overwrite to replace all existing variables.

Examples:
  tent env import mybox FOO=bar BAZ=qux
  tent env import mybox --overwrite FOO=bar`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			pairs := args[1:]

			// Parse all key=value pairs
			envVars := make(map[string]string)
			for _, pair := range pairs {
				idx := strings.IndexByte(pair, '=')
				if idx <= 0 {
					return fmt.Errorf("invalid format %q: expected KEY=VALUE", pair)
				}
				envVars[pair[:idx]] = pair[idx+1:]
			}

			baseDir := getBaseDir()
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("failed to load config for sandbox '%s': %w", name, err)
			}

			if overwrite || config.Env == nil {
				config.Env = make(map[string]string)
			}

			for k, v := range envVars {
				config.Env[k] = v
			}

			if err := manager.UpdateConfig(name, config); err != nil {
				return fmt.Errorf("failed to update config: %w", err)
			}

			if overwrite {
				fmt.Printf("Replaced all env vars for sandbox '%s' with %d variable(s)\n", name, len(envVars))
			} else {
				fmt.Printf("Imported %d variable(s) into sandbox '%s'\n", len(envVars), name)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Replace all existing variables instead of merging")
	return cmd
}
