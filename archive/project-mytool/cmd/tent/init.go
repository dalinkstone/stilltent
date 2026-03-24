package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// initProjectConfig is the scaffold written to tent.yaml
type initProjectConfig struct {
	Sandboxes map[string]*initSandboxDef `yaml:"sandboxes"`
}

type initSandboxDef struct {
	Image   string            `yaml:"image"`
	VCPUs   int               `yaml:"vcpus"`
	Memory  int               `yaml:"memory"`
	Disk    string            `yaml:"disk,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Network *initNetworkDef   `yaml:"network,omitempty"`
	Mounts  []initMountDef    `yaml:"mounts,omitempty"`
}

type initNetworkDef struct {
	Allow []string `yaml:"allow,omitempty"`
}

type initMountDef struct {
	Source string `yaml:"source"`
	Target string `yaml:"target"`
}

func initCmd() *cobra.Command {
	var (
		image      string
		name       string
		vcpus      int
		memory     int
		force      bool
		composeOnly bool
	)

	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "Initialize a tent project in a directory",
		Long: `Initialize a new tent project by creating a tent.yaml compose file and
a .tent/ directory for local configuration.

If no directory is specified, the current directory is used.

The generated tent.yaml includes a single sandbox definition that can be
customized. Use --image to set the base image, --name for the sandbox name,
and --vcpus/--memory for resource allocation.

Examples:
  tent init
  tent init ./my-project
  tent init --image ubuntu:22.04 --name dev --vcpus 4 --memory 4096
  tent init --force   # overwrite existing tent.yaml`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}

			absDir, err := filepath.Abs(dir)
			if err != nil {
				return fmt.Errorf("failed to resolve directory: %w", err)
			}

			// Ensure directory exists
			if err := os.MkdirAll(absDir, 0o755); err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}

			composePath := filepath.Join(absDir, "tent.yaml")
			tentDir := filepath.Join(absDir, ".tent")

			// Check if tent.yaml already exists
			if !force {
				if _, err := os.Stat(composePath); err == nil {
					return fmt.Errorf("tent.yaml already exists in %s (use --force to overwrite)", absDir)
				}
			}

			// Create .tent directory
			if err := os.MkdirAll(tentDir, 0o755); err != nil {
				return fmt.Errorf("failed to create .tent directory: %w", err)
			}

			// Write .tent/.gitignore
			gitignorePath := filepath.Join(tentDir, ".gitignore")
			if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
				gitignoreContent := "# tent local state\n*.log\nstate/\nsecrets/\n"
				if err := os.WriteFile(gitignorePath, []byte(gitignoreContent), 0o644); err != nil {
					return fmt.Errorf("failed to write .gitignore: %w", err)
				}
			}

			// Build scaffold config
			sandboxName := name
			if sandboxName == "" {
				sandboxName = filepath.Base(absDir)
				if sandboxName == "." || sandboxName == "/" {
					sandboxName = "default"
				}
			}

			sandboxImage := image
			if sandboxImage == "" {
				sandboxImage = "ubuntu:22.04"
			}

			config := &initProjectConfig{
				Sandboxes: map[string]*initSandboxDef{
					sandboxName: {
						Image:  sandboxImage,
						VCPUs:  vcpus,
						Memory: memory,
						Env:    map[string]string{},
						Network: &initNetworkDef{
							Allow: []string{},
						},
						Mounts: []initMountDef{
							{
								Source: ".",
								Target: "/workspace",
							},
						},
					},
				},
			}

			if composeOnly {
				// Skip .tent directory creation, just write tent.yaml
			}

			// Marshal to YAML
			data, err := yaml.Marshal(config)
			if err != nil {
				return fmt.Errorf("failed to generate tent.yaml: %w", err)
			}

			// Add header comment
			header := "# tent project configuration\n# See: tent compose up to start this environment\n\n"
			content := header + string(data)

			if err := os.WriteFile(composePath, []byte(content), 0o644); err != nil {
				return fmt.Errorf("failed to write tent.yaml: %w", err)
			}

			fmt.Printf("Initialized tent project in %s\n", absDir)
			fmt.Printf("  Created tent.yaml with sandbox %q (image: %s)\n", sandboxName, sandboxImage)
			fmt.Printf("  Created .tent/ directory for local state\n")
			fmt.Println()
			fmt.Println("Next steps:")
			fmt.Printf("  tent compose up tent.yaml    # Start the environment\n")
			fmt.Printf("  tent exec %s bash      # Open a shell\n", sandboxName)

			return nil
		},
	}

	cmd.Flags().StringVar(&image, "image", "", "Base image (default: ubuntu:22.04)")
	cmd.Flags().StringVar(&name, "name", "", "Sandbox name (default: directory name)")
	cmd.Flags().IntVar(&vcpus, "vcpus", 2, "Number of virtual CPUs")
	cmd.Flags().IntVar(&memory, "memory", 2048, "Memory in MB")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing tent.yaml")
	cmd.Flags().BoolVar(&composeOnly, "compose-only", false, "Only generate tent.yaml, skip .tent/ directory")

	return cmd
}
