package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	tentconfig "github.com/dalinkstone/tent/internal/config"
	"github.com/dalinkstone/tent/internal/state"
	"github.com/dalinkstone/tent/pkg/models"
)

func templateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage reusable sandbox configuration templates",
		Long: `Save, list, apply, and delete reusable sandbox configuration templates.

Templates store a full sandbox configuration (image, resources, network policy,
mounts, environment) so you can quickly create new sandboxes with known-good settings.

Examples:
  tent template save ai-agent --from-sandbox mybox --description "Claude agent environment"
  tent template save web-server --from ubuntu:22.04 --vcpus 2 --memory 2048
  tent template list
  tent template show ai-agent
  tent template apply ai-agent new-agent
  tent template delete ai-agent`,
	}

	cmd.AddCommand(templateSaveCmd())
	cmd.AddCommand(templateListCmd())
	cmd.AddCommand(templateShowCmd())
	cmd.AddCommand(templateApplyCmd())
	cmd.AddCommand(templateDeleteCmd())

	return cmd
}

func templateSaveCmd() *cobra.Command {
	var (
		fromSandbox string
		description string
		fromImage   string
		vcpus       int
		memoryMB    int
		diskGB      int
		allowList   []string
		envVars     []string
		outputJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "save <template-name>",
		Short: "Save a sandbox configuration as a reusable template",
		Long: `Save a template from an existing sandbox's configuration or from explicit flags.

Examples:
  tent template save ai-agent --from-sandbox mybox
  tent template save ai-agent --from-sandbox mybox --description "Agent environment"
  tent template save web-app --from ubuntu:22.04 --vcpus 4 --memory 4096 --allow api.example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			templateName := args[0]

			tm, err := tentconfig.NewTemplateManager("")
			if err != nil {
				return fmt.Errorf("failed to initialize template manager: %w", err)
			}

			var cfg models.VMConfig

			if fromSandbox != "" {
				// Load config from existing sandbox
				sm, err := state.NewStateManager("")
				if err != nil {
					return fmt.Errorf("failed to initialize state manager: %w", err)
				}

				vmState, err := sm.GetVM(fromSandbox)
				if err != nil {
					return fmt.Errorf("sandbox '%s' not found: %w", fromSandbox, err)
				}

				cfg = models.VMConfig{
					From:          vmState.ImageRef,
					VCPUs:         vmState.VCPUs,
					MemoryMB:      vmState.MemoryMB,
					DiskGB:        vmState.DiskGB,
					Labels:        vmState.Labels,
					RestartPolicy: vmState.RestartPolicy,
				}
			} else if fromImage != "" {
				// Build config from flags
				cfg = models.VMConfig{
					From:     fromImage,
					VCPUs:    vcpus,
					MemoryMB: memoryMB,
					DiskGB:   diskGB,
				}
				if len(allowList) > 0 {
					cfg.Network.Allow = allowList
				}
				if len(envVars) > 0 {
					cfg.Env = make(map[string]string)
					for _, ev := range envVars {
						parts := splitEnvVar(ev)
						if len(parts) == 2 {
							cfg.Env[parts[0]] = parts[1]
						}
					}
				}
			} else {
				return fmt.Errorf("either --from-sandbox or --from is required")
			}

			// Apply defaults for unset fields
			if cfg.VCPUs == 0 {
				cfg.VCPUs = 1
			}
			if cfg.MemoryMB == 0 {
				cfg.MemoryMB = 512
			}
			if cfg.DiskGB == 0 {
				cfg.DiskGB = 10
			}

			if err := tm.Save(templateName, description, cfg); err != nil {
				return fmt.Errorf("failed to save template: %w", err)
			}

			if outputJSON {
				out := map[string]string{
					"status":   "saved",
					"template": templateName,
				}
				data, _ := json.Marshal(out)
				fmt.Println(string(data))
			} else {
				fmt.Printf("Template '%s' saved successfully.\n", templateName)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&fromSandbox, "from-sandbox", "", "Copy configuration from an existing sandbox")
	cmd.Flags().StringVar(&description, "description", "", "Human-readable description of the template")
	cmd.Flags().StringVar(&fromImage, "from", "", "Base image for the template")
	cmd.Flags().IntVar(&vcpus, "vcpus", 1, "Number of virtual CPUs")
	cmd.Flags().IntVar(&memoryMB, "memory", 512, "Memory in MB")
	cmd.Flags().IntVar(&diskGB, "disk", 10, "Disk size in GB")
	cmd.Flags().StringSliceVar(&allowList, "allow", nil, "Allowed egress endpoints")
	cmd.Flags().StringSliceVar(&envVars, "env", nil, "Environment variables (KEY=VALUE)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")

	return cmd
}

func templateListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all saved templates",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			tm, err := tentconfig.NewTemplateManager("")
			if err != nil {
				return fmt.Errorf("failed to initialize template manager: %w", err)
			}

			templates, err := tm.List()
			if err != nil {
				return fmt.Errorf("failed to list templates: %w", err)
			}

			if len(templates) == 0 {
				if outputJSON {
					fmt.Println("[]")
				} else {
					fmt.Println("No templates saved. Use 'tent template save' to create one.")
				}
				return nil
			}

			if outputJSON {
				data, err := json.MarshalIndent(templates, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "NAME\tIMAGE\tVCPUS\tMEMORY\tDISK\tDESCRIPTION\tCREATED\n")
			for _, t := range templates {
				image := t.Config.From
				if image == "" {
					image = "-"
				}
				desc := t.Description
				if len(desc) > 40 {
					desc = desc[:37] + "..."
				}
				created := t.CreatedAt.Format(time.RFC3339)
				fmt.Fprintf(w, "%s\t%s\t%d\t%dMB\t%dGB\t%s\t%s\n",
					t.Name, image, t.Config.VCPUs, t.Config.MemoryMB, t.Config.DiskGB, desc, created)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

func templateShowCmd() *cobra.Command {
	var (
		outputJSON bool
		outputYAML bool
	)

	cmd := &cobra.Command{
		Use:   "show <template-name>",
		Short: "Show detailed template configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tm, err := tentconfig.NewTemplateManager("")
			if err != nil {
				return fmt.Errorf("failed to initialize template manager: %w", err)
			}

			tmpl, err := tm.Get(args[0])
			if err != nil {
				return err
			}

			if outputJSON {
				data, err := json.MarshalIndent(tmpl, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			if outputYAML {
				data, err := yaml.Marshal(tmpl.Config)
				if err != nil {
					return err
				}
				fmt.Print(string(data))
				return nil
			}

			// Human-readable output
			fmt.Printf("Template: %s\n", tmpl.Name)
			if tmpl.Description != "" {
				fmt.Printf("Description: %s\n", tmpl.Description)
			}
			fmt.Printf("Created: %s\n", tmpl.CreatedAt.Format(time.RFC3339))
			fmt.Printf("Updated: %s\n", tmpl.UpdatedAt.Format(time.RFC3339))
			fmt.Println()
			fmt.Printf("Configuration:\n")
			fmt.Printf("  Image:    %s\n", tmpl.Config.From)
			fmt.Printf("  vCPUs:    %d\n", tmpl.Config.VCPUs)
			fmt.Printf("  Memory:   %d MB\n", tmpl.Config.MemoryMB)
			fmt.Printf("  Disk:     %d GB\n", tmpl.Config.DiskGB)

			if len(tmpl.Config.Network.Allow) > 0 {
				fmt.Printf("  Network Allow:\n")
				for _, a := range tmpl.Config.Network.Allow {
					fmt.Printf("    - %s\n", a)
				}
			}
			if len(tmpl.Config.Network.Deny) > 0 {
				fmt.Printf("  Network Deny:\n")
				for _, d := range tmpl.Config.Network.Deny {
					fmt.Printf("    - %s\n", d)
				}
			}
			if len(tmpl.Config.Network.Ports) > 0 {
				fmt.Printf("  Port Forwards:\n")
				for _, p := range tmpl.Config.Network.Ports {
					fmt.Printf("    - %d:%d\n", p.Host, p.Guest)
				}
			}
			if len(tmpl.Config.Mounts) > 0 {
				fmt.Printf("  Mounts:\n")
				for _, m := range tmpl.Config.Mounts {
					ro := ""
					if m.Readonly {
						ro = " (readonly)"
					}
					fmt.Printf("    - %s -> %s%s\n", m.Host, m.Guest, ro)
				}
			}
			if len(tmpl.Config.Env) > 0 {
				fmt.Printf("  Environment:\n")
				for k, v := range tmpl.Config.Env {
					// Mask values that look like secrets
					display := v
					if len(v) > 8 {
						display = v[:4] + "****"
					}
					fmt.Printf("    %s=%s\n", k, display)
				}
			}
			if tmpl.Config.RestartPolicy != "" {
				fmt.Printf("  Restart Policy: %s\n", tmpl.Config.RestartPolicy)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&outputYAML, "yaml", false, "Output config in YAML format")
	return cmd
}

func templateApplyCmd() *cobra.Command {
	var (
		overrideVCPUs    int
		overrideMemoryMB int
		overrideDiskGB   int
		overrideEnv      []string
		outputJSON       bool
	)

	cmd := &cobra.Command{
		Use:   "apply <template-name> <sandbox-name>",
		Short: "Create a new sandbox from a template",
		Long: `Create a new sandbox using a saved template's configuration.
You can override specific settings with flags.

Examples:
  tent template apply ai-agent my-agent
  tent template apply ai-agent my-agent --vcpus 4 --memory 4096`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			templateName := args[0]
			sandboxName := args[1]

			tm, err := tentconfig.NewTemplateManager("")
			if err != nil {
				return fmt.Errorf("failed to initialize template manager: %w", err)
			}

			cfg, err := tm.Apply(templateName, sandboxName)
			if err != nil {
				return err
			}

			// Apply overrides
			if cmd.Flags().Changed("vcpus") {
				cfg.VCPUs = overrideVCPUs
			}
			if cmd.Flags().Changed("memory") {
				cfg.MemoryMB = overrideMemoryMB
			}
			if cmd.Flags().Changed("disk") {
				cfg.DiskGB = overrideDiskGB
			}
			if len(overrideEnv) > 0 {
				if cfg.Env == nil {
					cfg.Env = make(map[string]string)
				}
				for _, ev := range overrideEnv {
					parts := splitEnvVar(ev)
					if len(parts) == 2 {
						cfg.Env[parts[0]] = parts[1]
					}
				}
			}

			// Register the sandbox in state
			sm, err := state.NewStateManager("")
			if err != nil {
				return fmt.Errorf("failed to initialize state manager: %w", err)
			}

			vmState := &models.VMState{
				Name:          sandboxName,
				Status:        models.VMStatusCreated,
				ImageRef:      cfg.From,
				VCPUs:         cfg.VCPUs,
				MemoryMB:      cfg.MemoryMB,
				DiskGB:        cfg.DiskGB,
				Labels:        cfg.Labels,
				RestartPolicy: cfg.RestartPolicy,
			}

			if err := sm.StoreVM(vmState); err != nil {
				return fmt.Errorf("failed to create sandbox: %w", err)
			}

			if outputJSON {
				out := map[string]string{
					"status":   "created",
					"sandbox":  sandboxName,
					"template": templateName,
				}
				data, _ := json.Marshal(out)
				fmt.Println(string(data))
			} else {
				fmt.Printf("Sandbox '%s' created from template '%s'.\n", sandboxName, templateName)
				fmt.Printf("  Image:  %s\n", cfg.From)
				fmt.Printf("  vCPUs:  %d\n", cfg.VCPUs)
				fmt.Printf("  Memory: %d MB\n", cfg.MemoryMB)
				fmt.Printf("  Disk:   %d GB\n", cfg.DiskGB)
				fmt.Printf("\nUse 'tent start %s' to boot the sandbox.\n", sandboxName)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&overrideVCPUs, "vcpus", 0, "Override vCPU count")
	cmd.Flags().IntVar(&overrideMemoryMB, "memory", 0, "Override memory (MB)")
	cmd.Flags().IntVar(&overrideDiskGB, "disk", 0, "Override disk size (GB)")
	cmd.Flags().StringSliceVar(&overrideEnv, "env", nil, "Override/add environment variables (KEY=VALUE)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")

	return cmd
}

func templateDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <template-name>",
		Short: "Delete a saved template",
		Aliases: []string{"rm"},
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			templateName := args[0]

			tm, err := tentconfig.NewTemplateManager("")
			if err != nil {
				return fmt.Errorf("failed to initialize template manager: %w", err)
			}

			// Verify it exists first (for a better error message)
			if _, err := tm.Get(templateName); err != nil {
				return err
			}

			if !force {
				fmt.Printf("Delete template '%s'? This cannot be undone. Use --force to skip confirmation.\n", templateName)
				return nil
			}

			if err := tm.Delete(templateName); err != nil {
				return err
			}

			fmt.Printf("Template '%s' deleted.\n", templateName)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	return cmd
}

// splitEnvVar splits a KEY=VALUE string into [KEY, VALUE].
func splitEnvVar(s string) []string {
	idx := -1
	for i, c := range s {
		if c == '=' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return []string{s}
	}
	return []string{s[:idx], s[idx+1:]}
}
