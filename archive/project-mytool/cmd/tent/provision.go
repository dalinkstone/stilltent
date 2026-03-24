package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

func provisionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Manage cloud-init provisioning for sandboxes",
		Long: `Generate and manage cloud-init NoCloud data sources for sandbox VMs.

Cloud-init provisioning automates first-boot setup including user creation,
package installation, file injection, and script execution.`,
	}

	cmd.AddCommand(provisionGenerateCmd())
	cmd.AddCommand(provisionApplyCmd())
	cmd.AddCommand(provisionShowCmd())
	cmd.AddCommand(provisionCleanCmd())
	cmd.AddCommand(provisionValidateCmd())

	return cmd
}

func provisionGenerateCmd() *cobra.Command {
	var (
		hostname      string
		packages      []string
		runcmds       []string
		writeFiles    []string
		sshKeys       []string
		timezone      string
		locale        string
		userName      string
		userGroups    string
		growPart      bool
		configFile    string
		buildISO      bool
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "generate <sandbox-name>",
		Short: "Generate cloud-init data for a sandbox",
		Long: `Generate cloud-init NoCloud data source files (meta-data, user-data, network-config)
for the specified sandbox. Optionally build a mountable ISO image.

Examples:
  tent provision generate mybox --packages curl,git,vim
  tent provision generate mybox --runcmd "apt-get update" --runcmd "apt-get install -y python3"
  tent provision generate mybox --write-file /etc/motd:Welcome --ssh-key "ssh-ed25519 AAAA..."
  tent provision generate mybox --config provision.yaml --iso
  tent provision generate mybox --user agent --user-groups sudo,docker --timezone UTC`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]

			baseDir := getBaseDir()

			var ciConfig *vm.CloudInitConfig

			// Load from config file if specified
			if configFile != "" {
				data, err := os.ReadFile(configFile)
				if err != nil {
					return fmt.Errorf("failed to read config file: %w", err)
				}
				ciConfig = &vm.CloudInitConfig{}
				if err := yaml.Unmarshal(data, ciConfig); err != nil {
					return fmt.Errorf("failed to parse config file: %w", err)
				}
			} else {
				ciConfig = &vm.CloudInitConfig{}
			}

			// Override with flags
			if hostname != "" {
				ciConfig.Hostname = hostname
			}
			if len(packages) > 0 {
				// Flatten comma-separated values
				for _, p := range packages {
					for _, pkg := range strings.Split(p, ",") {
						pkg = strings.TrimSpace(pkg)
						if pkg != "" {
							ciConfig.Packages = append(ciConfig.Packages, pkg)
						}
					}
				}
			}
			if len(runcmds) > 0 {
				ciConfig.RunCmds = append(ciConfig.RunCmds, runcmds...)
			}
			if len(writeFiles) > 0 {
				for _, wf := range writeFiles {
					parts := strings.SplitN(wf, ":", 2)
					if len(parts) != 2 {
						return fmt.Errorf("invalid --write-file format %q (expected path:content)", wf)
					}
					ciConfig.WriteFiles = append(ciConfig.WriteFiles, vm.CloudInitFile{
						Path:        parts[0],
						Content:     parts[1],
						Permissions: "0644",
					})
				}
			}
			if len(sshKeys) > 0 {
				ciConfig.SSHAuthorizedKeys = append(ciConfig.SSHAuthorizedKeys, sshKeys...)
			}
			if timezone != "" {
				ciConfig.Timezone = timezone
			}
			if locale != "" {
				ciConfig.Locale = locale
			}
			if growPart {
				ciConfig.GrowPart = true
			}
			if userName != "" {
				user := vm.CloudInitUser{
					Name:       userName,
					Shell:      "/bin/bash",
					Sudo:       "ALL=(ALL) NOPASSWD:ALL",
					LockPasswd: true,
				}
				if userGroups != "" {
					user.Groups = userGroups
				}
				if len(sshKeys) > 0 {
					user.SSHAuthorizedKeys = sshKeys
				}
				ciConfig.Users = append(ciConfig.Users, user)
			}

			// Build a minimal VMConfig for env var injection
			vmConfig := &models.VMConfig{
				Name: sandboxName,
			}

			generator := vm.NewCloudInitGenerator(baseDir)
			outputDir, err := generator.GenerateForVM(sandboxName, vmConfig, ciConfig)
			if err != nil {
				return fmt.Errorf("failed to generate cloud-init data: %w", err)
			}

			if buildISO {
				isoPath, err := generator.BuildCloudInitISO(sandboxName)
				if err != nil {
					return fmt.Errorf("failed to build cloud-init ISO: %w", err)
				}
				if outputJSON {
					out := map[string]string{
						"data_dir": outputDir,
						"iso_path": isoPath,
					}
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(out)
				}
				fmt.Printf("Cloud-init data generated: %s\n", outputDir)
				fmt.Printf("Cloud-init ISO created:    %s\n", isoPath)
			} else {
				if outputJSON {
					out := map[string]string{
						"data_dir": outputDir,
					}
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(out)
				}
				fmt.Printf("Cloud-init data generated: %s\n", outputDir)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&hostname, "hostname", "", "Set guest hostname")
	cmd.Flags().StringSliceVar(&packages, "packages", nil, "Packages to install (comma-separated)")
	cmd.Flags().StringArrayVar(&runcmds, "runcmd", nil, "Shell commands to run on first boot")
	cmd.Flags().StringArrayVar(&writeFiles, "write-file", nil, "Files to write (path:content)")
	cmd.Flags().StringArrayVar(&sshKeys, "ssh-key", nil, "SSH public keys to authorize")
	cmd.Flags().StringVar(&timezone, "timezone", "", "Guest timezone (e.g. UTC)")
	cmd.Flags().StringVar(&locale, "locale", "", "Guest locale (e.g. en_US.UTF-8)")
	cmd.Flags().StringVar(&userName, "user", "", "Create a user with this name")
	cmd.Flags().StringVar(&userGroups, "user-groups", "", "Groups for the created user")
	cmd.Flags().BoolVar(&growPart, "growpart", false, "Grow root partition to fill disk")
	cmd.Flags().StringVarP(&configFile, "config", "c", "", "Provision config YAML file")
	cmd.Flags().BoolVar(&buildISO, "iso", false, "Build a cloud-init ISO image")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")

	return cmd
}

func provisionApplyCmd() *cobra.Command {
	var configFile string

	cmd := &cobra.Command{
		Use:   "apply <sandbox-name>",
		Short: "Generate and apply cloud-init provisioning to a sandbox",
		Long: `Generate cloud-init data and build an ISO image that can be attached
to the sandbox as a secondary drive for automated first-boot setup.

Examples:
  tent provision apply mybox --config provision.yaml
  tent provision apply mybox`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]

			baseDir := getBaseDir()

			var ciConfig *vm.CloudInitConfig
			if configFile != "" {
				data, err := os.ReadFile(configFile)
				if err != nil {
					return fmt.Errorf("failed to read config file: %w", err)
				}
				ciConfig = &vm.CloudInitConfig{}
				if err := yaml.Unmarshal(data, ciConfig); err != nil {
					return fmt.Errorf("failed to parse config file: %w", err)
				}
			}

			vmConfig := &models.VMConfig{
				Name: sandboxName,
			}

			generator := vm.NewCloudInitGenerator(baseDir)

			// Generate data source files
			_, err := generator.GenerateForVM(sandboxName, vmConfig, ciConfig)
			if err != nil {
				return fmt.Errorf("failed to generate cloud-init data: %w", err)
			}

			// Build ISO
			isoPath, err := generator.BuildCloudInitISO(sandboxName)
			if err != nil {
				return fmt.Errorf("failed to build cloud-init ISO: %w", err)
			}

			fmt.Printf("Cloud-init ISO ready: %s\n", isoPath)
			fmt.Printf("Attach to sandbox '%s' as a secondary drive for provisioning on next boot.\n", sandboxName)

			return nil
		},
	}

	cmd.Flags().StringVarP(&configFile, "config", "c", "", "Provision config YAML file")

	return cmd
}

func provisionShowCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "show <sandbox-name>",
		Short: "Show cloud-init data for a sandbox",
		Long:  "Display the generated cloud-init data source files for a sandbox.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]

			baseDir := getBaseDir()

			ciDir := filepath.Join(baseDir, "vms", sandboxName, "cloud-init")
			if _, err := os.Stat(ciDir); os.IsNotExist(err) {
				return fmt.Errorf("no cloud-init data found for sandbox '%s'", sandboxName)
			}

			files := []string{"meta-data", "user-data", "network-config"}

			if outputJSON {
				result := make(map[string]string)
				for _, name := range files {
					data, err := os.ReadFile(filepath.Join(ciDir, name))
					if err != nil {
						continue
					}
					result[name] = string(data)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			for _, name := range files {
				data, err := os.ReadFile(filepath.Join(ciDir, name))
				if err != nil {
					continue
				}
				fmt.Printf("=== %s ===\n", name)
				fmt.Println(string(data))
			}

			// Check for ISO
			isoPath := filepath.Join(baseDir, "vms", sandboxName, "cloud-init.iso")
			if info, err := os.Stat(isoPath); err == nil {
				fmt.Printf("=== ISO ===\n")
				fmt.Printf("Path: %s\n", isoPath)
				fmt.Printf("Size: %d bytes\n", info.Size())
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")

	return cmd
}

func provisionCleanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clean <sandbox-name>",
		Short: "Remove cloud-init data for a sandbox",
		Long:  "Remove all generated cloud-init data source files and ISO for a sandbox.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]

			baseDir := getBaseDir()

			generator := vm.NewCloudInitGenerator(baseDir)
			if err := generator.CleanupForVM(sandboxName); err != nil {
				return fmt.Errorf("failed to clean cloud-init data: %w", err)
			}

			// Also remove ISO if present
			isoPath := filepath.Join(baseDir, "vms", sandboxName, "cloud-init.iso")
			os.Remove(isoPath) // best effort

			fmt.Printf("Cloud-init data cleaned for sandbox '%s'\n", sandboxName)
			return nil
		},
	}

	return cmd
}

func provisionValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <config-file>",
		Short: "Validate a cloud-init provision config file",
		Long: `Validate a cloud-init provision YAML config file for correctness.

Example:
  tent provision validate provision.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := args[0]

			data, err := os.ReadFile(configPath)
			if err != nil {
				return fmt.Errorf("failed to read config file: %w", err)
			}

			var ciConfig vm.CloudInitConfig
			if err := yaml.Unmarshal(data, &ciConfig); err != nil {
				return fmt.Errorf("invalid YAML: %w", err)
			}

			// Validate fields
			var warnings []string

			if ciConfig.Hostname != "" {
				if strings.ContainsAny(ciConfig.Hostname, " \t\n/\\") {
					return fmt.Errorf("invalid hostname: %q", ciConfig.Hostname)
				}
			}

			for i, u := range ciConfig.Users {
				if u.Name == "" {
					return fmt.Errorf("user[%d]: name is required", i)
				}
				if strings.ContainsAny(u.Name, " \t\n/\\") {
					return fmt.Errorf("user[%d]: invalid username: %q", i, u.Name)
				}
			}

			for i, f := range ciConfig.WriteFiles {
				if f.Path == "" {
					return fmt.Errorf("write_files[%d]: path is required", i)
				}
				if !strings.HasPrefix(f.Path, "/") {
					return fmt.Errorf("write_files[%d]: path must be absolute: %q", i, f.Path)
				}
			}

			if ciConfig.Timezone != "" && !strings.Contains(ciConfig.Timezone, "/") && ciConfig.Timezone != "UTC" {
				warnings = append(warnings, fmt.Sprintf("timezone %q may not be valid (expected format: Region/City or UTC)", ciConfig.Timezone))
			}

			if ciConfig.PowerState != nil {
				switch ciConfig.PowerState.Mode {
				case "poweroff", "reboot", "halt":
					// valid
				default:
					return fmt.Errorf("power_state.mode must be poweroff, reboot, or halt; got %q", ciConfig.PowerState.Mode)
				}
			}

			fmt.Printf("Config file '%s' is valid\n", configPath)

			if len(warnings) > 0 {
				fmt.Println("\nWarnings:")
				for _, w := range warnings {
					fmt.Printf("  - %s\n", w)
				}
			}

			// Summary
			fmt.Printf("\nProvisioning summary:\n")
			if ciConfig.Hostname != "" {
				fmt.Printf("  Hostname:  %s\n", ciConfig.Hostname)
			}
			fmt.Printf("  Users:     %d\n", len(ciConfig.Users))
			fmt.Printf("  Packages:  %d\n", len(ciConfig.Packages))
			fmt.Printf("  Run cmds:  %d\n", len(ciConfig.RunCmds))
			fmt.Printf("  Files:     %d\n", len(ciConfig.WriteFiles))
			fmt.Printf("  SSH keys:  %d\n", len(ciConfig.SSHAuthorizedKeys))

			return nil
		},
	}

	return cmd
}
