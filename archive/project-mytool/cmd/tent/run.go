package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/internal/config"
	"github.com/dalinkstone/tent/internal/image"
	"github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

func runCmd() *cobra.Command {
	var (
		fromImage  string
		pullPolicy string
		vcpus      int
		memoryMB   int
		diskGB     int
		allowList  []string
		envVars    []string
		mountSpecs []string
		portSpecs  []string
		cfgPath    string
		rmAfter    bool
		detach     bool
		name       string
		enableSSH  bool
	)

	cmd := &cobra.Command{
		Use:   "run [flags] -- <command> [args...]",
		Short: "Create, start, and execute a command in a single step",
		Long: `Create a new sandbox, start it, and execute a command in one step.

This is the fastest way to run an isolated command. It combines the
"create", "start", and "exec" lifecycle steps into a single invocation.

Use --rm to automatically destroy the sandbox after the command exits.
Without --rm the sandbox remains and can be reused with "tent start".

Use -d (--detach) to start the sandbox in the background and return
immediately without executing a command. The sandbox name is printed
to stdout for scripting.

A sandbox name is auto-generated if --name is not provided.

See also: tent create, tent start, tent exec, tent destroy`,
		Example: `  # Run a command and remove the sandbox afterward
  tent run --from ubuntu:22.04 --rm -- echo hello

  # Run Python in a slim container
  tent run --from python:3.12-slim --rm -- python -c "print('hello')"

  # Run with network allow-list
  tent run --name mybox --from ubuntu:22.04 --allow api.anthropic.com -- curl https://api.anthropic.com

  # Run with environment variables and mounts
  tent run --from ubuntu:22.04 --env KEY=value --mount ./data:/data -- ls /data

  # Start a detached sandbox for later use
  tent run --from ubuntu:22.04 -d --name worker -- /usr/bin/long-running-task

  # Run from a config file
  tent run --config sandbox.yaml -- my-script.sh`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			command := args

			// Generate a name if not provided
			if name == "" {
				name = fmt.Sprintf("tent-run-%d", os.Getpid())
			}

			// Build or load config
			var cfg *models.VMConfig
			var err error

			if cfgPath != "" {
				cfg, err = loadRunConfigFromFile(cfgPath)
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}
				cfg.Name = name
			} else {
				if fromImage == "" {
					return fmt.Errorf("--from is required (or use --config)")
				}
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
					if strings.HasPrefix(val, "${") && strings.HasSuffix(val, "}") {
						envName := val[2 : len(val)-1]
						val = os.Getenv(envName)
					}
					cfg.Env[key] = val
				}
			}

			// Parse --mount host:guest[:ro] specs
			for _, spec := range mountSpecs {
				mount, err := parseMountSpec(spec)
				if err != nil {
					return fmt.Errorf("invalid mount spec %q: %w", spec, err)
				}
				cfg.Mounts = append(cfg.Mounts, mount)
			}

			// Parse --port hostPort:guestPort specs
			for _, spec := range portSpecs {
				pf, err := parsePortSpec(spec)
				if err != nil {
					return fmt.Errorf("invalid port spec %q: %w", spec, err)
				}
				cfg.Network.Ports = append(cfg.Network.Ports, pf)
			}

			// Validate
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid configuration: %w", err)
			}

			baseDir := getBaseDir()

			// Resolve image
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
			}

			// Create hypervisor backend
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			// Create manager
			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Ensure cleanup on --rm
			if rmAfter {
				defer func() {
					fmt.Fprintf(os.Stderr, "Removing sandbox '%s'...\n", name)
					if stopErr := manager.Stop(name); stopErr != nil {
						// Ignore stop errors — sandbox may already be stopped
						_ = stopErr
					}
					if destroyErr := manager.Destroy(name); destroyErr != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to destroy sandbox '%s': %v\n", name, destroyErr)
					}
				}()
			}

			// Step 1: Create
			fmt.Fprintf(os.Stderr, "Creating sandbox '%s' from '%s'...\n", name, cfg.From)
			if err := manager.Create(name, cfg); err != nil {
				return fmt.Errorf("failed to create sandbox: %w", err)
			}

			// Step 2: Start
			fmt.Fprintf(os.Stderr, "Starting sandbox '%s'...\n", name)
			if err := manager.Start(name); err != nil {
				return fmt.Errorf("failed to start sandbox: %w", err)
			}

			// If detached, just print the sandbox name and return
			if detach {
				fmt.Println(name)
				return nil
			}

			// Step 3: Exec
			fmt.Fprintf(os.Stderr, "Executing: %s\n", strings.Join(command, " "))
			output, exitCode, err := manager.Exec(name, command)
			if err != nil {
				return fmt.Errorf("failed to execute command: %w", err)
			}

			fmt.Print(output)
			if exitCode != 0 {
				os.Exit(exitCode)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&fromImage, "from", "", "Image reference (e.g., ubuntu:22.04)")
	cmd.Flags().StringVar(&pullPolicy, "pull", "", "Image pull policy: missing, always, never (default \"missing\")")
	cmd.Flags().StringVar(&name, "name", "", "Sandbox name (default: auto-generated)")
	cmd.Flags().StringVar(&cfgPath, "config", "", "Path to YAML configuration file")
	cmd.Flags().IntVar(&vcpus, "vcpus", 2, "Number of virtual CPUs")
	cmd.Flags().IntVar(&memoryMB, "memory", 1024, "Memory in MB")
	cmd.Flags().IntVar(&diskGB, "disk", 10, "Disk size in GB")
	cmd.Flags().StringSliceVar(&allowList, "allow", nil, "Allowed external endpoints")
	cmd.Flags().StringSliceVar(&envVars, "env", nil, "Environment variables (KEY=VALUE)")
	cmd.Flags().StringSliceVar(&mountSpecs, "mount", nil, "Host-to-guest mounts (host:guest[:ro])")
	cmd.Flags().StringSliceVar(&portSpecs, "port", nil, "Port forwarding (hostPort:guestPort)")
	cmd.Flags().BoolVar(&rmAfter, "rm", false, "Remove sandbox after command exits")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "Start sandbox and return without executing command")
	cmd.Flags().BoolVar(&enableSSH, "ssh", false, "Enable SSH server in guest (default: use vsock agent)")

	return cmd
}

// loadRunConfigFromFile loads VM config from a YAML file with env expansion
func loadRunConfigFromFile(path string) (*models.VMConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	data = config.ExpandEnvBytes(data)

	var cfg models.VMConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &cfg, nil
}
