package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/internal/config"
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
		fromImage   string
		pullPolicy  string
		vcpus       int
		memoryMB    int
		diskGB      int
		allowList   []string
		envVars     []string
		mountSpecs  []string
		portSpecs   []string
		labelSpecs  []string
		hookSpecs   []string
		backendName string
		cpuWeight   int
		cpuMax      int
		memMax      int
		pidsMax     int
		enableSSH   bool
	)

	cmd := &cobra.Command{
		Use:   "create <name> [--from <image-ref>] [--config <path>]",
		Short: "Create a new microVM sandbox",
		Long: `Create a new microVM sandbox from a Docker/OCI image, registry image, ISO, or
raw disk image.

The sandbox is created but not started. Use "tent start" to boot it, or use
"tent run" to create, start, and execute a command in a single step.

Images are pulled automatically from Docker Hub or any OCI-compatible registry
when not available locally. Use "tent image list" to see cached images.

On macOS the sandbox runs on Virtualization.framework; on Linux it uses KVM.
The hypervisor backend can be overridden with the --backend flag.

Configuration can be provided via CLI flags or a YAML file (--config). When
both are given, CLI flags override values from the config file. Environment
variable references in YAML (e.g., ${MY_VAR}) are expanded automatically.

See also: tent start, tent run, tent destroy, tent config init`,
		Example: `  # Create a sandbox from an Ubuntu image
  tent create mybox --from ubuntu:22.04

  # Create with custom resources
  tent create devbox --from python:3.12-slim --vcpus 4 --memory 2048

  # Create with network allow-list for specific APIs
  tent create agent --from ubuntu:22.04 --allow api.anthropic.com --allow openrouter.ai

  # Create from a YAML config file
  tent create mybox --config sandbox.yaml

  # Create with host directory mounts (read-write and read-only)
  tent create dev --from ubuntu:22.04 --mount ./workspace:/workspace --mount ./data:/data:ro

  # Create with port forwarding
  tent create web --from ubuntu:22.04 --port 8080:80 --port 2222:22

  # Create with environment variables
  tent create agent --from ubuntu:22.04 --env TERM=xterm-256color --env API_KEY='${MY_KEY}'

  # Create with lifecycle hooks
  tent create mybox --from ubuntu:22.04 --hook pre_start:"echo starting"

  # Create with resource limits
  tent create mybox --from ubuntu:22.04 --cpu-weight 100 --memory-max 4096 --pids-max 500`,
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
					Name:      name,
					From:      fromImage,
					VCPUs:     vcpus,
					MemoryMB:  memoryMB,
					DiskGB:    diskGB,
					Kernel:    "default",
					EnableSSH: enableSSH,
					Network: models.NetworkConfig{
						Mode:   "bridge",
						Bridge: "tent0",
						Allow:  allowList,
					},
				}
			}

			// Apply --ssh flag if set (overrides config file)
			if cmd.Flags().Changed("ssh") {
				cfg.EnableSSH = enableSSH
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

			// Parse --mount host:guest[:ro] specs
			if len(mountSpecs) > 0 {
				for _, spec := range mountSpecs {
					mount, err := parseMountSpec(spec)
					if err != nil {
						return fmt.Errorf("invalid mount spec %q: %w", spec, err)
					}
					cfg.Mounts = append(cfg.Mounts, mount)
				}
			}

			// Parse --label key=value pairs
			if len(labelSpecs) > 0 {
				if cfg.Labels == nil {
					cfg.Labels = make(map[string]string)
				}
				for _, l := range labelSpecs {
					parts := strings.SplitN(l, "=", 2)
					if len(parts) != 2 {
						return fmt.Errorf("invalid label format %q, expected key=value", l)
					}
					cfg.Labels[parts[0]] = parts[1]
				}
			}

			// Parse --port hostPort:guestPort specs
			if len(portSpecs) > 0 {
				for _, spec := range portSpecs {
					pf, err := parsePortSpec(spec)
					if err != nil {
						return fmt.Errorf("invalid port spec %q: %w", spec, err)
					}
					cfg.Network.Ports = append(cfg.Network.Ports, pf)
				}
			}

			// Parse --hook phase:command specs
			if len(hookSpecs) > 0 {
				if cfg.Hooks == nil {
					cfg.Hooks = &models.LifecycleHooks{}
				}
				for _, spec := range hookSpecs {
					phase, action, err := parseHookSpec(spec)
					if err != nil {
						return fmt.Errorf("invalid hook spec %q: %w", spec, err)
					}
					switch phase {
					case "pre_start":
						cfg.Hooks.PreStart = append(cfg.Hooks.PreStart, action)
					case "post_start":
						cfg.Hooks.PostStart = append(cfg.Hooks.PostStart, action)
					case "pre_stop":
						cfg.Hooks.PreStop = append(cfg.Hooks.PreStop, action)
					case "post_stop":
						cfg.Hooks.PostStop = append(cfg.Hooks.PostStop, action)
					default:
						return fmt.Errorf("unknown hook phase %q (use pre_start, post_start, pre_stop, post_stop)", phase)
					}
				}
			}

			// Parse resource limit flags
			if cmd.Flags().Changed("cpu-weight") || cmd.Flags().Changed("cpu-max") ||
				cmd.Flags().Changed("memory-max") || cmd.Flags().Changed("pids-max") {
				if cfg.Resources == nil {
					cfg.Resources = &models.ResourceLimits{}
				}
				if cmd.Flags().Changed("cpu-weight") {
					cfg.Resources.CPUWeight = cpuWeight
				}
				if cmd.Flags().Changed("cpu-max") {
					cfg.Resources.CPUMaxPercent = cpuMax
				}
				if cmd.Flags().Changed("memory-max") {
					cfg.Resources.MemoryMaxMB = memMax
				}
				if cmd.Flags().Changed("pids-max") {
					cfg.Resources.PidsMax = pidsMax
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

				// Parse pull policy
				pp := image.PullMissing
				if pullPolicy != "" {
					pp, err = image.ValidatePullPolicy(pullPolicy)
					if err != nil {
						return err
					}
				}

				rootfsPath, err := imgMgr.ResolveImage(cfg.From, pp)
				if err != nil {
					return fmt.Errorf("failed to resolve image %q: %w", cfg.From, err)
				}

				cfg.RootFS = rootfsPath
				fmt.Printf("Resolved image '%s' -> %s\n", cfg.From, rootfsPath)
			}

			// Get hypervisor backend: explicit --backend flag, injected option, or platform default
			hvBackend := opts.Hypervisor
			if hvBackend == nil {
				if backendName != "" {
					hvBackend, err = vm.NewBackendByName(backendName, baseDir)
				} else {
					hvBackend, err = vm.NewPlatformBackend(baseDir)
				}
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
	cmd.Flags().StringVar(&pullPolicy, "pull", "", "Image pull policy: missing, always, never (default \"missing\")")
	cmd.Flags().StringVar(&configPath, "config", "", "Path to YAML configuration file")
	cmd.Flags().IntVar(&vcpus, "vcpus", 2, "Number of virtual CPUs")
	cmd.Flags().IntVar(&memoryMB, "memory", 1024, "Memory in MB")
	cmd.Flags().IntVar(&diskGB, "disk", 10, "Disk size in GB")
	cmd.Flags().StringSliceVar(&allowList, "allow", nil, "Allowed external endpoints (can be repeated)")
	cmd.Flags().StringSliceVar(&envVars, "env", nil, "Environment variables in KEY=VALUE format (can be repeated)")
	cmd.Flags().StringSliceVar(&mountSpecs, "mount", nil, "Host-to-guest directory mounts in host:guest[:ro] format (can be repeated)")
	cmd.Flags().StringSliceVar(&portSpecs, "port", nil, "Port forwarding in hostPort:guestPort format (can be repeated)")
	cmd.Flags().StringSliceVar(&labelSpecs, "label", nil, "Labels in key=value format (can be repeated)")
	cmd.Flags().StringSliceVar(&hookSpecs, "hook", nil, "Lifecycle hooks in phase:command format (e.g., pre_start:echo hello)")
	cmd.Flags().StringVar(&backendName, "backend", "", "Hypervisor backend (e.g., hvf, vz, kvm, firecracker)")
	cmd.Flags().IntVar(&cpuWeight, "cpu-weight", 0, "CPU scheduling weight (1-10000)")
	cmd.Flags().IntVar(&cpuMax, "cpu-max", 0, "Maximum CPU usage as percentage")
	cmd.Flags().IntVar(&memMax, "memory-max", 0, "Hard memory limit in MB")
	cmd.Flags().IntVar(&pidsMax, "pids-max", 0, "Maximum number of processes")
	cmd.Flags().BoolVar(&enableSSH, "ssh", false, "Enable SSH server in guest (default: use vsock agent instead)")

	return cmd
}

// createCmd is a convenience function that uses default dependencies
func createCmd() *cobra.Command {
	return ConfigureCreateCmd()
}

// parseMountSpec parses a mount spec in the format host:guest[:ro]
func parseMountSpec(spec string) (models.MountConfig, error) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return models.MountConfig{}, fmt.Errorf("expected host:guest[:ro], got %q", spec)
	}

	mount := models.MountConfig{
		Host:  parts[0],
		Guest: parts[1],
	}

	if len(parts) == 3 {
		if parts[2] == "ro" {
			mount.Readonly = true
		} else {
			return models.MountConfig{}, fmt.Errorf("expected 'ro' as third component, got %q", parts[2])
		}
	}

	if mount.Host == "" || mount.Guest == "" {
		return models.MountConfig{}, fmt.Errorf("host and guest paths cannot be empty")
	}

	return mount, nil
}

// parsePortSpec parses a port spec in the format hostPort:guestPort
func parsePortSpec(spec string) (models.PortForward, error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return models.PortForward{}, fmt.Errorf("expected hostPort:guestPort, got %q", spec)
	}

	hostPort, err := strconv.Atoi(parts[0])
	if err != nil {
		return models.PortForward{}, fmt.Errorf("invalid host port %q: %w", parts[0], err)
	}

	guestPort, err := strconv.Atoi(parts[1])
	if err != nil {
		return models.PortForward{}, fmt.Errorf("invalid guest port %q: %w", parts[1], err)
	}

	if hostPort < 1 || hostPort > 65535 || guestPort < 1 || guestPort > 65535 {
		return models.PortForward{}, fmt.Errorf("port numbers must be between 1 and 65535")
	}

	return models.PortForward{Host: hostPort, Guest: guestPort}, nil
}

// parseHookSpec parses a hook spec in the format phase:command
// e.g., "pre_start:echo hello" or "post_stop:cleanup.sh"
func parseHookSpec(spec string) (string, models.HookAction, error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", models.HookAction{}, fmt.Errorf("expected phase:command (e.g., pre_start:echo hello)")
	}

	phase := parts[0]
	validPhases := map[string]bool{
		"pre_start": true, "post_start": true,
		"pre_stop": true, "post_stop": true,
	}
	if !validPhases[phase] {
		return "", models.HookAction{}, fmt.Errorf("unknown phase %q", phase)
	}

	return phase, models.HookAction{
		Command: parts[1],
		Where:   "host",
	}, nil
}

// loadConfigFromFile loads VM config from a YAML file
func loadConfigFromFile(path string) (*models.VMConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Expand environment variable references (e.g., ${ANTHROPIC_API_KEY})
	data = config.ExpandEnvBytes(data)

	var cfg models.VMConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &cfg, nil
}
