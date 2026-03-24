package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/internal/network"
	"github.com/dalinkstone/tent/internal/state"
	"github.com/dalinkstone/tent/pkg/models"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "View, validate, and generate sandbox configuration",
		Long: `View, validate, and generate sandbox configuration files.

Configuration files are YAML documents that describe sandbox resources,
networking, mounts, environment variables, and lifecycle hooks. Use
"tent config init" to generate a starter template.

Subcommands:
  init       Generate a configuration template with sensible defaults
  validate   Check a configuration file for errors
  show       Display the effective configuration for a sandbox
  get        Get a specific configuration value
  set        Update a specific configuration value

See also: tent create --config, tent inspect`,
	}

	cmd.AddCommand(configInitCmd())
	cmd.AddCommand(configValidateCmd())
	cmd.AddCommand(configShowCmd())
	cmd.AddCommand(configGetCmd())
	cmd.AddCommand(configSetCmd())

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

func configGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <sandbox> <key>",
		Short: "Get a configuration value for a sandbox",
		Long: `Get a specific configuration property for an existing sandbox.

Available keys:
  vcpus, memory, disk, image, restart-policy, ip, status, labels, locked, ttl

Examples:
  tent config get mybox vcpus
  tent config get mybox memory
  tent config get mybox restart-policy`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			key := strings.ToLower(args[1])
			baseDir := getBaseDir()

			sm, err := state.NewStateManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open state: %w", err)
			}

			vmState, err := sm.GetVM(sandboxName)
			if err != nil {
				return fmt.Errorf("sandbox '%s' not found", sandboxName)
			}

			value, err := getConfigValue(vmState, key, baseDir)
			if err != nil {
				return err
			}

			fmt.Println(value)
			return nil
		},
	}
}

func getConfigValue(vm *models.VMState, key, baseDir string) (string, error) {
	switch key {
	case "vcpus":
		return strconv.Itoa(vm.VCPUs), nil
	case "memory", "memory_mb", "memory-mb":
		return strconv.Itoa(vm.MemoryMB), nil
	case "disk", "disk_gb", "disk-gb":
		return strconv.Itoa(vm.DiskGB), nil
	case "image", "from":
		return vm.ImageRef, nil
	case "restart-policy", "restart_policy":
		return string(vm.RestartPolicy), nil
	case "ip":
		return vm.IP, nil
	case "status":
		return string(vm.Status), nil
	case "rootfs":
		return vm.RootFSPath, nil
	case "locked":
		return strconv.FormatBool(vm.Locked), nil
	case "ttl":
		return vm.TTL, nil
	case "labels":
		if len(vm.Labels) == 0 {
			return "{}", nil
		}
		data, err := json.Marshal(vm.Labels)
		if err != nil {
			return "", err
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("unknown config key '%s'. Available keys: vcpus, memory, disk, image, restart-policy, ip, status, rootfs, locked, ttl, labels", key)
	}
}

func configSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <sandbox> <key> <value>",
		Short: "Set a configuration value for a sandbox",
		Long: `Set a specific configuration property for an existing sandbox.
The sandbox must be stopped to change most properties.

Settable keys:
  vcpus, memory, disk, restart-policy, ttl

Examples:
  tent config set mybox vcpus 4
  tent config set mybox memory 2048
  tent config set mybox restart-policy on-failure
  tent config set mybox ttl 2h`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			key := strings.ToLower(args[1])
			value := args[2]
			baseDir := getBaseDir()

			sm, err := state.NewStateManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open state: %w", err)
			}

			vmState, err := sm.GetVM(sandboxName)
			if err != nil {
				return fmt.Errorf("sandbox '%s' not found", sandboxName)
			}

			if vmState.Locked {
				return fmt.Errorf("sandbox '%s' is locked: %s", sandboxName, vmState.LockedReason)
			}

			// Most config changes require sandbox to be stopped
			requiresStopped := key != "ttl" && key != "restart-policy" && key != "restart_policy"
			if requiresStopped && vmState.Status == models.VMStatusRunning {
				return fmt.Errorf("sandbox '%s' must be stopped to change '%s' (current status: %s)", sandboxName, key, vmState.Status)
			}

			err = sm.UpdateVM(sandboxName, func(vm *models.VMState) error {
				return setConfigValue(vm, key, value)
			})
			if err != nil {
				return err
			}

			fmt.Printf("Set %s=%s for sandbox '%s'\n", key, value, sandboxName)
			return nil
		},
	}
}

func setConfigValue(vm *models.VMState, key, value string) error {
	switch key {
	case "vcpus":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid vcpus value: %w", err)
		}
		if v < 1 || v > 128 {
			return fmt.Errorf("vcpus must be between 1 and 128")
		}
		vm.VCPUs = v
	case "memory", "memory_mb", "memory-mb":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid memory value: %w", err)
		}
		if v < 128 {
			return fmt.Errorf("memory must be at least 128 MB")
		}
		vm.MemoryMB = v
	case "disk", "disk_gb", "disk-gb":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid disk value: %w", err)
		}
		if v < 1 {
			return fmt.Errorf("disk must be at least 1 GB")
		}
		vm.DiskGB = v
	case "restart-policy", "restart_policy":
		switch models.RestartPolicy(value) {
		case models.RestartPolicyNever, models.RestartPolicyAlways, models.RestartPolicyOnFailure:
			vm.RestartPolicy = models.RestartPolicy(value)
		default:
			return fmt.Errorf("invalid restart policy '%s'. Options: never, always, on-failure", value)
		}
	case "ttl":
		vm.TTL = value
	default:
		return fmt.Errorf("cannot set key '%s'. Settable keys: vcpus, memory, disk, restart-policy, ttl", key)
	}
	return nil
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
