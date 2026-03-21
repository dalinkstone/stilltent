package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/internal/vm"
	"github.com/dalinkstone/tent/pkg/models"
)

var (
	configPath string
)

func createCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name> [--config <path>]",
		Short: "Create a new microVM",
		Long:  `Create a new microVM from a base image or YAML config.`,
		Args:  cobra.ExactArgs(1),
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
			} else {
				// Create default config
				cfg = &models.VMConfig{
					Name:     name,
					VCPUs:    2,
					MemoryMB: 1024,
					DiskGB:   10,
					Kernel:   "default",
					RootFS:   "",
					Network: models.NetworkConfig{
						Mode:   "bridge",
						Bridge: "tent0",
					},
				}
			}

			// Validate config
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid configuration: %w", err)
			}

			// Create VM manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
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

	cmd.Flags().StringVar(&configPath, "config", "", "Path to YAML configuration file")

	return cmd
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
