package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/internal/image"
	"github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

var (
	configPath string
)

// ConfigureCreateCmd creates a new create command with optional dependencies
func ConfigureCreateCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	var (
		fromImage string
		vcpus     int
		memoryMB  int
		diskGB    int
		allowList []string
		envVars   []string
	)

	cmd := &cobra.Command{
		Use:   "create <name> [--from <image-ref>] [--config <path>]",
		Short: "Create a new microVM",
		Long: `Create a new microVM from a Docker/OCI image, registry image, ISO, or raw disk image.

Examples:
  tent create mybox --from ubuntu:22.04
  tent create agent --from python:3.12-slim --vcpus 4 --memory 2048
  tent create dev --from ubuntu:22.04 --allow api.anthropic.com --allow openrouter.ai
  tent create mybox --config sandbox.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Determine config source
			var cfg *models.VMConfig
			var err error

			if configPath != "" {
				// Load config from file
				cfg, err = loadConfigFromFile(configPath)
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}
				// CLI name overrides config name
				cfg.Name = name
			} else {
				// Build config from flags
				cfg = &models.VMConfig{
					Name:     name,
					From:     fromImage,
					VCPUs:    vcpus,
					MemoryMB: memoryMB,
					DiskGB:   diskGB,
					Kernel:   "default",
					Network: models.NetworkConfig{
						Mode:   "bridge",
						Bridge: "tent0",
						Allow:  allowList,
					},
				}
			}

			// Parse --env KEY=VALUE pairs
			if len(envVars) > 0 {
				if cfg.Env == nil {
					cfg.Env = make(map[string]string)
				}
				for _, e := range envVars {
					parts := strings.SplitN(e, "=", 2)
					if len(parts) != 2 {
						return fmt.Errorf("invalid env format %q, expected KEY=VALUE", e)
					}
					key := parts[0]
					val := parts[1]
					// Expand environment variables in values (e.g., ${ANTHROPIC_API_KEY})
					if strings.HasPrefix(val, "${") && strings.HasSuffix(val, "}") {
						envName := val[2 : len(val)-1]
						val = os.Getenv(envName)
					}
					cfg.Env[key] = val
				}
			}

			// Validate config
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid configuration: %w", err)
			}

			// Create VM manager
			baseDir := getBaseDir()

			// If --from is specified, resolve the image
			if cfg.From != "" {
				imgMgr, err := image.NewManager(baseDir)
				if err != nil {
					return fmt.Errorf("failed to create image manager: %w", err)
				}

				rootfsPath, err := imgMgr.ResolveImage(cfg.From)
				if err != nil {
					return fmt.Errorf("failed to resolve image %q: %w", cfg.From, err)
				}
				cfg.RootFS = rootfsPath
				fmt.Printf("Resolved image '%s' -> %s\n", cfg.From, rootfsPath)
			}

			// Get platform-specific hypervisor backend if not provided
			hvBackend := opts.Hypervisor
			if hvBackend == nil {
				hvBackend, err = vm.NewPlatformBackend(baseDir)
				if err != nil {
					return fmt.Errorf("failed to create hypervisor backend: %w", err)
				}
			}

			// Create manager with dependencies
			manager, err := vm.NewManager(baseDir, opts.StateManager, hvBackend, opts.NetworkMgr, opts.StorageMgr)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Create the VM
			if err := manager.Create(name, cfg); err != nil {
				return fmt.Errorf("failed to create VM: %w", err)
			}

			fmt.Printf("Successfully created VM: %s\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&fromImage, "from", "", "Image reference (e.g., ubuntu:22.04, python:3.12-slim, /path/to/image.iso)")
	cmd.Flags().StringVar(&configPath, "config", "", "Path to YAML configuration file")
	cmd.Flags().IntVar(&vcpus, "vcpus", 2, "Number of virtual CPUs")
	cmd.Flags().IntVar(&memoryMB, "memory", 1024, "Memory in MB")
	cmd.Flags().IntVar(&diskGB, "disk", 10, "Disk size in GB")
	cmd.Flags().StringSliceVar(&allowList, "allow", nil, "Allowed external endpoints (can be repeated)")
	cmd.Flags().StringSliceVar(&envVars, "env", nil, "Environment variables in KEY=VALUE format (can be repeated)")

	return cmd
}

// createCmd is a convenience function that uses default dependencies
func createCmd() *cobra.Command {
	return ConfigureCreateCmd()
}

// loadConfigFromFile loads VM config from a YAML file
func loadConfigFromFile(path string) (*models.VMConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg models.VMConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &cfg, nil
}
