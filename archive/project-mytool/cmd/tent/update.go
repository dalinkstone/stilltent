package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

func updateCmd() *cobra.Command {
	var (
		vcpus      int
		memoryMB   int
		diskGB     int
		addAllow   []string
		removeAllow []string
		addEnv     []string
		removeEnv  []string
		addMount   []string
		removeMount []string
		addPort    []string
		removePort []string
		addLabel   []string
		removeLabel []string
	)

	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update configuration of a stopped sandbox",
		Long: `Update the configuration of an existing sandbox. The sandbox must be stopped.

Examples:
  tent update mybox --vcpus 4 --memory 4096
  tent update mybox --add-allow api.openai.com --remove-allow openrouter.ai
  tent update mybox --add-env DEBUG=1 --remove-env OLD_VAR
  tent update mybox --add-mount ./data:/data:ro --remove-mount /workspace
  tent update mybox --add-port 3000:3000 --remove-port 8080:80
  tent update mybox --disk 20`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Load existing config
			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}

			// Check sandbox is stopped
			state, err := manager.Status(name)
			if err != nil {
				return fmt.Errorf("failed to get sandbox state: %w", err)
			}
			if state.Status == models.VMStatusRunning {
				return fmt.Errorf("cannot update running sandbox %q — stop it first", name)
			}

			changed := false

			// Update resource limits
			if cmd.Flags().Changed("vcpus") {
				if vcpus < 1 {
					return fmt.Errorf("vcpus must be at least 1")
				}
				config.VCPUs = vcpus
				changed = true
			}
			if cmd.Flags().Changed("memory") {
				if memoryMB < 128 {
					return fmt.Errorf("memory must be at least 128 MB")
				}
				config.MemoryMB = memoryMB
				changed = true
			}
			if cmd.Flags().Changed("disk") {
				if diskGB < 1 {
					return fmt.Errorf("disk must be at least 1 GB")
				}
				if diskGB < config.DiskGB {
					return fmt.Errorf("disk size can only be increased (current: %d GB)", config.DiskGB)
				}
				config.DiskGB = diskGB
				changed = true
			}

			// Update network allow list
			if len(addAllow) > 0 {
				for _, ep := range addAllow {
					if !containsString(config.Network.Allow, ep) {
						config.Network.Allow = append(config.Network.Allow, ep)
					}
				}
				changed = true
			}
			if len(removeAllow) > 0 {
				config.Network.Allow = removeStrings(config.Network.Allow, removeAllow)
				changed = true
			}

			// Update environment variables
			if len(addEnv) > 0 {
				if config.Env == nil {
					config.Env = make(map[string]string)
				}
				for _, e := range addEnv {
					parts := strings.SplitN(e, "=", 2)
					if len(parts) != 2 {
						return fmt.Errorf("invalid env format %q, expected KEY=VALUE", e)
					}
					key := parts[0]
					val := parts[1]
					if strings.HasPrefix(val, "${") && strings.HasSuffix(val, "}") {
						envName := val[2 : len(val)-1]
						val = os.Getenv(envName)
					}
					config.Env[key] = val
				}
				changed = true
			}
			if len(removeEnv) > 0 {
				for _, key := range removeEnv {
					delete(config.Env, key)
				}
				changed = true
			}

			// Update mounts
			if len(addMount) > 0 {
				for _, spec := range addMount {
					mount, err := parseMountSpec(spec)
					if err != nil {
						return fmt.Errorf("invalid mount spec %q: %w", spec, err)
					}
					// Replace existing mount with same guest path, or append
					replaced := false
					for i, existing := range config.Mounts {
						if existing.Guest == mount.Guest {
							config.Mounts[i] = mount
							replaced = true
							break
						}
					}
					if !replaced {
						config.Mounts = append(config.Mounts, mount)
					}
				}
				changed = true
			}
			if len(removeMount) > 0 {
				var kept []models.MountConfig
				for _, m := range config.Mounts {
					remove := false
					for _, rm := range removeMount {
						if m.Guest == rm || m.Host == rm {
							remove = true
							break
						}
					}
					if !remove {
						kept = append(kept, m)
					}
				}
				config.Mounts = kept
				changed = true
			}

			// Update port forwards
			if len(addPort) > 0 {
				for _, spec := range addPort {
					pf, err := parsePortSpec(spec)
					if err != nil {
						return fmt.Errorf("invalid port spec %q: %w", spec, err)
					}
					// Replace existing with same host port, or append
					replaced := false
					for i, existing := range config.Network.Ports {
						if existing.Host == pf.Host {
							config.Network.Ports[i] = pf
							replaced = true
							break
						}
					}
					if !replaced {
						config.Network.Ports = append(config.Network.Ports, pf)
					}
				}
				changed = true
			}
			if len(removePort) > 0 {
				for _, spec := range removePort {
					pf, err := parsePortSpec(spec)
					if err != nil {
						return fmt.Errorf("invalid port spec %q: %w", spec, err)
					}
					var kept []models.PortForward
					for _, existing := range config.Network.Ports {
						if existing.Host != pf.Host || existing.Guest != pf.Guest {
							kept = append(kept, existing)
						}
					}
					config.Network.Ports = kept
				}
				changed = true
			}

			// Update labels
			if len(addLabel) > 0 {
				if config.Labels == nil {
					config.Labels = make(map[string]string)
				}
				for _, l := range addLabel {
					parts := strings.SplitN(l, "=", 2)
					if len(parts) != 2 {
						return fmt.Errorf("invalid label format %q, expected key=value", l)
					}
					config.Labels[parts[0]] = parts[1]
				}
				changed = true
			}
			if len(removeLabel) > 0 {
				for _, key := range removeLabel {
					delete(config.Labels, key)
				}
				changed = true
			}

			if !changed {
				fmt.Println("No changes specified.")
				return nil
			}

			// Validate updated config
			if err := config.Validate(); err != nil {
				return fmt.Errorf("updated configuration is invalid: %w", err)
			}

			// Save updated config and state
			if err := manager.UpdateConfig(name, config); err != nil {
				return fmt.Errorf("failed to save updated config: %w", err)
			}

			fmt.Printf("Updated sandbox %q:\n", name)
			fmt.Printf("  vCPUs:    %d\n", config.VCPUs)
			fmt.Printf("  Memory:   %d MB\n", config.MemoryMB)
			fmt.Printf("  Disk:     %d GB\n", config.DiskGB)
			if len(config.Network.Allow) > 0 {
				fmt.Printf("  Allow:    %s\n", strings.Join(config.Network.Allow, ", "))
			}
			if len(config.Network.Ports) > 0 {
				var ports []string
				for _, p := range config.Network.Ports {
					ports = append(ports, fmt.Sprintf("%d:%d", p.Host, p.Guest))
				}
				fmt.Printf("  Ports:    %s\n", strings.Join(ports, ", "))
			}
			if len(config.Mounts) > 0 {
				var mounts []string
				for _, m := range config.Mounts {
					s := fmt.Sprintf("%s:%s", m.Host, m.Guest)
					if m.Readonly {
						s += " (ro)"
					}
					mounts = append(mounts, s)
				}
				fmt.Printf("  Mounts:   %s\n", strings.Join(mounts, ", "))
			}
			if len(config.Env) > 0 {
				var keys []string
				for k := range config.Env {
					keys = append(keys, k)
				}
				fmt.Printf("  Env vars: %s\n", strings.Join(keys, ", "))
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&vcpus, "vcpus", 0, "Number of virtual CPUs")
	cmd.Flags().IntVar(&memoryMB, "memory", 0, "Memory in MB")
	cmd.Flags().IntVar(&diskGB, "disk", 0, "Disk size in GB (can only increase)")
	cmd.Flags().StringSliceVar(&addAllow, "add-allow", nil, "Add allowed external endpoints")
	cmd.Flags().StringSliceVar(&removeAllow, "remove-allow", nil, "Remove allowed external endpoints")
	cmd.Flags().StringSliceVar(&addEnv, "add-env", nil, "Add/update environment variables (KEY=VALUE)")
	cmd.Flags().StringSliceVar(&removeEnv, "remove-env", nil, "Remove environment variables by key")
	cmd.Flags().StringSliceVar(&addMount, "add-mount", nil, "Add host-to-guest mounts (host:guest[:ro])")
	cmd.Flags().StringSliceVar(&removeMount, "remove-mount", nil, "Remove mounts by guest or host path")
	cmd.Flags().StringSliceVar(&addPort, "add-port", nil, "Add port forwards (hostPort:guestPort)")
	cmd.Flags().StringSliceVar(&removePort, "remove-port", nil, "Remove port forwards (hostPort:guestPort)")
	cmd.Flags().StringSliceVar(&addLabel, "add-label", nil, "Add/update labels (key=value)")
	cmd.Flags().StringSliceVar(&removeLabel, "remove-label", nil, "Remove labels by key")

	return cmd
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func removeStrings(slice []string, toRemove []string) []string {
	removeSet := make(map[string]bool)
	for _, s := range toRemove {
		removeSet[s] = true
	}
	var result []string
	for _, s := range slice {
		if !removeSet[s] {
			result = append(result, s)
		}
	}
	return result
}
