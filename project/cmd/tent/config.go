package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/internal/network"
	"github.com/dalinkstone/tent/internal/state"
	"github.com/dalinkstone/tent/pkg/models"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management commands",
		Long:  "View, validate, and generate sandbox configuration files.",
	}

	cmd.AddCommand(configInitCmd())
	cmd.AddCommand(configValidateCmd())
	cmd.AddCommand(configShowCmd())

	return cmd
}

func configInitCmd() *cobra.Command {
	var (
		outputPath string
		name       string
		fromImage  string
		withAI     bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a sandbox configuration template",
		Long: `Generate a YAML configuration file with sensible defaults.

Examples:
  tent config init
  tent config init -o sandbox.yaml
  tent config init --name mybox --from ubuntu:22.04
  tent config init --ai-defaults`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := models.VMConfig{
				Name:     name,
				From:     fromImage,
				VCPUs:    2,
				MemoryMB: 1024,
				DiskGB:   10,
				Network: models.NetworkConfig{
					Mode:   "bridge",
					Bridge: "tent0",
				},
				Mounts: []models.MountConfig{
					{
						Host:     "./workspace",
						Guest:    "/workspace",
						Readonly: false,
					},
				},
				Env: map[string]string{
					"TERM": "xterm-256color",
				},
			}

			if withAI {
				cfg.Network.Allow = network.DefaultAIAllowlist()
				cfg.Env["ANTHROPIC_API_KEY"] = "${ANTHROPIC_API_KEY}"
				cfg.Env["OPENROUTER_API_KEY"] = "${OPENROUTER_API_KEY}"
			}

			data, err := yaml.Marshal(&cfg)
			if err != nil {
				return fmt.Errorf("failed to generate config: %w", err)
			}

			header := "# tent sandbox configuration\n# See: tent config validate <file> to check validity\n\n"
			output := []byte(header)
			output = append(output, data...)

			if outputPath != "" {
				if err := os.WriteFile(outputPath, output, 0644); err != nil {
					return fmt.Errorf("failed to write config file: %w", err)
				}
				fmt.Printf("Config written to %s\n", outputPath)
			} else {
				fmt.Print(string(output))
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output file path (default: stdout)")
	cmd.Flags().StringVar(&name, "name", "my-sandbox", "Sandbox name")
	cmd.Flags().StringVar(&fromImage, "from", "ubuntu:22.04", "Base image reference")
	cmd.Flags().BoolVar(&withAI, "ai-defaults", false, "Include AI API endpoint allowlist and env vars")

	return cmd
}

func configValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <file>",
		Short: "Validate a sandbox configuration file",
		Long: `Parse and validate a YAML configuration file, reporting any errors.

Examples:
  tent config validate sandbox.yaml
  tent config validate tent-compose.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read file: %w", err)
			}

			var cfg models.VMConfig
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return fmt.Errorf("YAML parse error: %w", err)
			}

			// Run validation
			if err := cfg.Validate(); err != nil {
				fmt.Printf("Validation FAILED for %s:\n", filePath)
				fmt.Printf("  %v\n", err)
				return fmt.Errorf("config validation failed")
			}

			// Additional warnings
			warnings := validateWarnings(&cfg)
			if len(warnings) > 0 {
				fmt.Printf("Config %s is valid with warnings:\n", filePath)
				for _, w := range warnings {
					fmt.Printf("  warning: %s\n", w)
				}
			} else {
				fmt.Printf("Config %s is valid.\n", filePath)
			}

			// Print summary
			fmt.Printf("\nSummary:\n")
			fmt.Printf("  Name:     %s\n", cfg.Name)
			fmt.Printf("  From:     %s\n", cfg.From)
			fmt.Printf("  vCPUs:    %d\n", cfg.VCPUs)
			fmt.Printf("  Memory:   %d MB\n", cfg.MemoryMB)
			fmt.Printf("  Disk:     %d GB\n", cfg.DiskGB)
			if len(cfg.Network.Allow) > 0 {
				fmt.Printf("  Allowed:  %d endpoints\n", len(cfg.Network.Allow))
			}
			if len(cfg.Network.Deny) > 0 {
				fmt.Printf("  Denied:   %d endpoints\n", len(cfg.Network.Deny))
			}
			if len(cfg.Mounts) > 0 {
				fmt.Printf("  Mounts:   %d\n", len(cfg.Mounts))
			}
			if len(cfg.Network.Ports) > 0 {
				fmt.Printf("  Ports:    %d forwarded\n", len(cfg.Network.Ports))
			}

			return nil
		},
	}
}

func configShowCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "show <sandbox>",
		Short: "Show configuration for an existing sandbox",
		Long: `Display the current configuration and state of a sandbox.

Examples:
  tent config show mybox
  tent config show mybox --format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			baseDir := getBaseDir()

			sm, err := state.NewStateManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open state: %w", err)
			}

			vmState, err := sm.GetVM(sandboxName)
			if err != nil {
				return fmt.Errorf("sandbox '%s' not found", sandboxName)
			}

			// Reconstruct a VMConfig from state for display
			cfg := models.VMConfig{
				Name:          vmState.Name,
				From:          vmState.ImageRef,
				VCPUs:         vmState.VCPUs,
				MemoryMB:      vmState.MemoryMB,
				DiskGB:        vmState.DiskGB,
				RootFS:        vmState.RootFSPath,
				RestartPolicy: vmState.RestartPolicy,
			}

			// Load network policy if available
			pm, err := network.NewPolicyManager(baseDir)
			if err == nil {
				if policy, err := pm.GetPolicy(sandboxName); err == nil {
					cfg.Network.Allow = policy.Allowed
					cfg.Network.Deny = policy.Denied
				}
			}

			switch outputFormat {
			case "json":
				data, err := json.MarshalIndent(cfg, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal config: %w", err)
				}
				fmt.Println(string(data))
			case "yaml":
				data, err := yaml.Marshal(cfg)
				if err != nil {
					return fmt.Errorf("failed to marshal config: %w", err)
				}
				fmt.Print(string(data))
			default:
				// Human-readable output
				fmt.Printf("Sandbox: %s\n", cfg.Name)
				fmt.Printf("  Status:   %s\n", vmState.Status)
				if cfg.From != "" {
					fmt.Printf("  Image:    %s\n", cfg.From)
				}
				fmt.Printf("  vCPUs:    %d\n", cfg.VCPUs)
				fmt.Printf("  Memory:   %d MB\n", cfg.MemoryMB)
				fmt.Printf("  Disk:     %d GB\n", cfg.DiskGB)
				if vmState.IP != "" {
					fmt.Printf("  IP:       %s\n", vmState.IP)
				}
				if cfg.RootFS != "" {
					fmt.Printf("  RootFS:   %s\n", cfg.RootFS)
				}
				if cfg.RestartPolicy != "" {
					fmt.Printf("  Restart:  %s\n", cfg.RestartPolicy)
				}
				if vmState.Health != nil {
					fmt.Printf("  Health:   %s\n", vmState.Health.Status)
				}
				if len(cfg.Network.Allow) > 0 {
					fmt.Printf("  Network Allow:\n")
					for _, ep := range cfg.Network.Allow {
						fmt.Printf("    - %s\n", ep)
					}
				}
				if len(cfg.Network.Deny) > 0 {
					fmt.Printf("  Network Deny:\n")
					for _, ep := range cfg.Network.Deny {
						fmt.Printf("    - %s\n", ep)
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&outputFormat, "format", "text", "Output format: text, json, yaml")

	return cmd
}

// validateWarnings returns non-fatal warnings about a config
func validateWarnings(cfg *models.VMConfig) []string {
	var warnings []string

	if cfg.From == "" {
		warnings = append(warnings, "no 'from' image specified — you'll need --from when creating")
	}

	if cfg.VCPUs > 8 {
		warnings = append(warnings, fmt.Sprintf("high vCPU count (%d) — ensure your host has enough cores", cfg.VCPUs))
	}

	if cfg.MemoryMB > 8192 {
		warnings = append(warnings, fmt.Sprintf("high memory (%d MB) — ensure your host has enough RAM", cfg.MemoryMB))
	}

	if cfg.DiskGB > 100 {
		warnings = append(warnings, fmt.Sprintf("large disk (%d GB) — ensure sufficient disk space", cfg.DiskGB))
	}

	if len(cfg.Network.Allow) == 0 && len(cfg.Network.Deny) == 0 {
		warnings = append(warnings, "no network policy — sandbox will use default (block all external)")
	}

	for _, m := range cfg.Mounts {
		if m.Host == "" || m.Guest == "" {
			warnings = append(warnings, "mount with empty host or guest path")
		}
	}

	for _, p := range cfg.Network.Ports {
		if p.Host < 1024 {
			warnings = append(warnings, fmt.Sprintf("port %d requires elevated privileges on most systems", p.Host))
		}
	}

	return warnings
}
