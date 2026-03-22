package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func secretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage encrypted secrets for sandboxes",
		Long: `Store, retrieve, and bind encrypted secrets to sandboxes.

Secrets are encrypted at rest using AES-256-GCM and can be bound to sandboxes
as environment variables. This provides secure management of API keys, tokens,
and other sensitive values without storing them in plaintext config files.`,
	}

	cmd.AddCommand(secretSetCmd())
	cmd.AddCommand(secretGetCmd())
	cmd.AddCommand(secretDeleteCmd())
	cmd.AddCommand(secretListCmd())
	cmd.AddCommand(secretBindCmd())
	cmd.AddCommand(secretUnbindCmd())
	cmd.AddCommand(secretInjectCmd())

	return cmd
}

func secretSetCmd() *cobra.Command {
	var fromEnv string
	var fromFile string

	cmd := &cobra.Command{
		Use:   "set <name> [value]",
		Short: "Store an encrypted secret",
		Long: `Store a secret encrypted at rest. The value can be provided as an argument,
read from an environment variable with --from-env, or from a file with --from-file.

Examples:
  tent secret set anthropic-key sk-ant-api03-xxxxx
  tent secret set anthropic-key --from-env ANTHROPIC_API_KEY
  tent secret set tls-cert --from-file ./cert.pem`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			store, err := vm.NewSecretStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open secret store: %w", err)
			}

			var value []byte

			switch {
			case fromFile != "":
				value, err = os.ReadFile(fromFile)
				if err != nil {
					return fmt.Errorf("failed to read file %q: %w", fromFile, err)
				}
			case fromEnv != "":
				envVal := os.Getenv(fromEnv)
				if envVal == "" {
					return fmt.Errorf("environment variable %q is not set or empty", fromEnv)
				}
				value = []byte(envVal)
			case len(args) == 2:
				value = []byte(args[1])
			default:
				return fmt.Errorf("provide a value as argument, --from-env, or --from-file")
			}

			if err := store.Set(name, value); err != nil {
				return fmt.Errorf("failed to store secret: %w", err)
			}

			fmt.Printf("Secret %q stored (%d bytes)\n", name, len(value))
			return nil
		},
	}

	cmd.Flags().StringVar(&fromEnv, "from-env", "", "Read value from environment variable")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "Read value from file")
	return cmd
}

func secretGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Retrieve and decrypt a secret",
		Long: `Retrieve a secret value. The decrypted value is printed to stdout.

Examples:
  tent secret get anthropic-key
  tent secret get anthropic-key | pbcopy`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			store, err := vm.NewSecretStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open secret store: %w", err)
			}

			value, err := store.Get(name)
			if err != nil {
				return fmt.Errorf("failed to retrieve secret: %w", err)
			}

			fmt.Print(string(value))
			return nil
		},
	}
}

func secretDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a secret",
		Long: `Remove a secret from the store. This does not remove bindings that reference
this secret — use 'tent secret unbind' to remove bindings first.

Examples:
  tent secret delete anthropic-key`,
		Args:    cobra.ExactArgs(1),
		Aliases: []string{"rm", "remove"},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			store, err := vm.NewSecretStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open secret store: %w", err)
			}

			if err := store.Delete(name); err != nil {
				return err
			}

			fmt.Printf("Secret %q deleted\n", name)
			return nil
		},
	}
}

func secretListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all secrets",
		Long: `List all stored secrets with metadata. Secret values are not shown.

Examples:
  tent secret list
  tent secret list --json`,
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			store, err := vm.NewSecretStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open secret store: %w", err)
			}

			secrets, err := store.List()
			if err != nil {
				return fmt.Errorf("failed to list secrets: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(secrets)
			}

			if len(secrets) == 0 {
				fmt.Println("No secrets stored.")
				return nil
			}

			fmt.Printf("%-30s %-20s %-20s\n", "NAME", "CREATED", "UPDATED")
			for _, s := range secrets {
				fmt.Printf("%-30s %-20s %-20s\n",
					s.Name,
					s.CreatedAt.Format("2006-01-02 15:04"),
					s.UpdatedAt.Format("2006-01-02 15:04"),
				)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func secretBindCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bind <sandbox> <secret-name> <ENV_VAR>",
		Short: "Bind a secret to a sandbox as an environment variable",
		Long: `Bind a secret to a sandbox so it is injected as an environment variable
when the sandbox starts. The secret value is decrypted at injection time.

Examples:
  tent secret bind mybox anthropic-key ANTHROPIC_API_KEY
  tent secret bind mybox openrouter-key OPENROUTER_API_KEY`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			secretName := args[1]
			envVar := args[2]

			baseDir := getBaseDir()

			store, err := vm.NewSecretStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open secret store: %w", err)
			}

			if err := store.BindToSandbox(sandboxName, secretName, envVar); err != nil {
				return fmt.Errorf("failed to bind secret: %w", err)
			}

			fmt.Printf("Bound secret %q to sandbox %q as $%s\n", secretName, sandboxName, envVar)
			return nil
		},
	}
}

func secretUnbindCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unbind <sandbox> <secret-name>",
		Short: "Remove a secret binding from a sandbox",
		Long: `Remove a secret-to-environment-variable binding from a sandbox.
The secret itself is not deleted.

Examples:
  tent secret unbind mybox anthropic-key`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			secretName := args[1]

			baseDir := getBaseDir()

			store, err := vm.NewSecretStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open secret store: %w", err)
			}

			if err := store.UnbindFromSandbox(sandboxName, secretName); err != nil {
				return err
			}

			fmt.Printf("Unbound secret %q from sandbox %q\n", secretName, sandboxName)
			return nil
		},
	}
}

func secretInjectCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "inject <sandbox>",
		Short: "Show resolved secrets for a sandbox",
		Long: `Resolve and display all bound secrets for a sandbox as environment variables.
This is useful for debugging and verifying which secrets will be injected.

With --json, outputs a JSON object of env var names to values.
Without --json, outputs shell-compatible export statements.

Examples:
  tent secret inject mybox
  tent secret inject mybox --json
  eval $(tent secret inject mybox)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			baseDir := getBaseDir()

			store, err := vm.NewSecretStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open secret store: %w", err)
			}

			secrets, err := store.GetSandboxSecrets(sandboxName)
			if err != nil {
				return fmt.Errorf("failed to resolve secrets: %w", err)
			}

			if secrets == nil || len(secrets) == 0 {
				fmt.Printf("No secrets bound to sandbox %q\n", sandboxName)
				return nil
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(secrets)
			}

			// Sort keys for consistent output
			keys := make([]string, 0, len(secrets))
			for k := range secrets {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, k := range keys {
				v := strings.ReplaceAll(secrets[k], "'", "'\"'\"'")
				fmt.Printf("export %s='%s'\n", k, v)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}
