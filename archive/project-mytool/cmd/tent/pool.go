package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/image"
	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func poolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pool",
		Short: "Manage pre-warmed sandbox pools for instant availability",
		Long: `Manage pools of pre-warmed sandboxes that eliminate boot latency.

Pools keep a configurable number of sandboxes pre-created and running,
ready to be instantly claimed by AI workloads or automation scripts.

Examples:
  tent pool create mypool --from ubuntu:22.04 --min-ready 3 --max-size 10
  tent pool list
  tent pool status mypool
  tent pool claim mypool --by "agent-1"
  tent pool release mypool pool-mypool-1234-0
  tent pool fill mypool
  tent pool drain mypool
  tent pool delete mypool`,
	}

	cmd.AddCommand(poolCreateCmd())
	cmd.AddCommand(poolDeleteCmd())
	cmd.AddCommand(poolListCmd())
	cmd.AddCommand(poolStatusCmd())
	cmd.AddCommand(poolClaimCmd())
	cmd.AddCommand(poolReleaseCmd())
	cmd.AddCommand(poolFillCmd())
	cmd.AddCommand(poolDrainCmd())

	return cmd
}

func poolCreateCmd() *cobra.Command {
	var (
		fromImage string
		minReady  int
		maxSize   int
		vcpus     int
		memoryMB  int
		diskGB    int
		allowList []string
		envVars   []string
		ttl       string
		jsonOut   bool
	)

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new sandbox pool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			env := make(map[string]string)
			for _, e := range envVars {
				k, v := parsePoolEnvVar(e)
				if k != "" {
					env[k] = v
				}
			}

			cfg := &vm.PoolConfig{
				Name:     name,
				From:     fromImage,
				MinReady: minReady,
				MaxSize:  maxSize,
				VCPUs:    vcpus,
				MemoryMB: memoryMB,
				DiskGB:   diskGB,
				Allow:    allowList,
				Env:      env,
				TTL:      ttl,
			}

			pm := vm.NewPoolManager(baseDir)
			if err := pm.CreatePool(cfg); err != nil {
				return err
			}

			if jsonOut {
				data, _ := json.MarshalIndent(cfg, "", "  ")
				fmt.Println(string(data))
			} else {
				fmt.Printf("Pool %q created (min_ready=%d, max_size=%d, from=%s)\n", name, minReady, maxSize, fromImage)
				fmt.Printf("Run 'tent pool fill %s' to provision initial sandboxes\n", name)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&fromImage, "from", "", "Image reference (required)")
	cmd.Flags().IntVar(&minReady, "min-ready", 2, "Minimum pre-warmed sandboxes")
	cmd.Flags().IntVar(&maxSize, "max-size", 10, "Maximum total sandboxes")
	cmd.Flags().IntVar(&vcpus, "vcpus", 2, "vCPUs per sandbox")
	cmd.Flags().IntVar(&memoryMB, "memory", 1024, "Memory in MB per sandbox")
	cmd.Flags().IntVar(&diskGB, "disk", 10, "Disk in GB per sandbox")
	cmd.Flags().StringSliceVar(&allowList, "allow", nil, "Allowed endpoints for pool sandboxes")
	cmd.Flags().StringSliceVar(&envVars, "env", nil, "Environment variables (KEY=VALUE)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "Auto-reclaim TTL for claimed sandboxes (e.g. 1h)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("from")

	return cmd
}

func poolDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a pool and optionally destroy its sandboxes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			pm := vm.NewPoolManager(baseDir)
			sandboxNames, err := pm.DeletePool(name)
			if err != nil {
				return err
			}

			if force && len(sandboxNames) > 0 {
				hvBackend, err := vm.NewPlatformBackend(baseDir)
				if err != nil {
					return fmt.Errorf("failed to create hypervisor backend: %w", err)
				}
				manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
				if err != nil {
					return fmt.Errorf("failed to create VM manager: %w", err)
				}
				if err := manager.Setup(); err != nil {
					return fmt.Errorf("failed to setup VM manager: %w", err)
				}

				for _, sb := range sandboxNames {
					fmt.Fprintf(os.Stderr, "Destroying pool sandbox %q...\n", sb)
					_ = manager.Stop(sb)
					if err := manager.Destroy(sb); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to destroy %q: %v\n", sb, err)
					}
				}
			} else if len(sandboxNames) > 0 {
				fmt.Fprintf(os.Stderr, "Pool deleted. %d sandbox(es) remain and should be cleaned up:\n", len(sandboxNames))
				for _, sb := range sandboxNames {
					fmt.Fprintf(os.Stderr, "  %s\n", sb)
				}
				fmt.Fprintf(os.Stderr, "Use --force to auto-destroy pool sandboxes\n")
			}

			fmt.Printf("Pool %q deleted\n", name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Destroy all pool sandboxes")
	return cmd
}

func poolListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all sandbox pools",
		Aliases: []string{"ls"},
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			pm := vm.NewPoolManager(baseDir)

			pools, err := pm.ListPools()
			if err != nil {
				return err
			}

			if len(pools) == 0 {
				fmt.Println("No pools configured")
				return nil
			}

			if jsonOut {
				data, _ := json.MarshalIndent(pools, "", "  ")
				fmt.Println(string(data))
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "NAME\tIMAGE\tREADY\tCLAIMED\tTOTAL\tMAX\n")
			for _, p := range pools {
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\n",
					p.Name, p.From, p.Ready, p.Claimed, p.Total, p.MaxSize)
			}
			w.Flush()

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func poolStatusCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Show detailed status of a pool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()
			pm := vm.NewPoolManager(baseDir)

			status, err := pm.GetPoolStatus(name)
			if err != nil {
				return err
			}

			if jsonOut {
				data, _ := json.MarshalIndent(status, "", "  ")
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("Pool: %s\n", status.Name)
			fmt.Printf("Image: %s\n", status.From)
			fmt.Printf("Min Ready: %d\n", status.MinReady)
			fmt.Printf("Max Size: %d\n", status.MaxSize)
			fmt.Println()
			fmt.Printf("Ready:        %d\n", status.Ready)
			fmt.Printf("Claimed:      %d\n", status.Claimed)
			fmt.Printf("Provisioning: %d\n", status.Provisioning)
			fmt.Printf("Errored:      %d\n", status.Errored)
			fmt.Printf("Total:        %d\n", status.Total)

			deficit := status.MinReady - status.Ready - status.Provisioning
			if deficit > 0 {
				fmt.Printf("\nDeficit: %d (run 'tent pool fill %s' to provision)\n", deficit, name)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func poolClaimCmd() *cobra.Command {
	var (
		claimedBy string
		jsonOut   bool
	)

	cmd := &cobra.Command{
		Use:   "claim <pool-name>",
		Short: "Claim a ready sandbox from the pool",
		Long: `Claim a pre-warmed sandbox from the pool for immediate use.
Returns the sandbox name which can be used with all tent commands.

The claimed sandbox is removed from the ready pool. Use 'tent pool fill'
afterward to replenish the pool.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			poolName := args[0]
			baseDir := getBaseDir()
			pm := vm.NewPoolManager(baseDir)

			if claimedBy == "" {
				claimedBy = fmt.Sprintf("cli-%d", os.Getpid())
			}

			member, err := pm.Claim(poolName, claimedBy)
			if err != nil {
				return err
			}

			if jsonOut {
				data, _ := json.MarshalIndent(member, "", "  ")
				fmt.Println(string(data))
			} else {
				fmt.Println(member.SandboxName)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&claimedBy, "by", "", "Identifier of the consumer claiming the sandbox")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func poolReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release <pool-name> <sandbox-name>",
		Short: "Release a claimed sandbox back (marks for recycling)",
		Long: `Release a previously claimed sandbox. The sandbox is removed from the pool
and should be destroyed. Run 'tent pool fill' to provision a replacement.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			poolName := args[0]
			sandboxName := args[1]
			baseDir := getBaseDir()
			pm := vm.NewPoolManager(baseDir)

			if err := pm.Release(poolName, sandboxName); err != nil {
				return err
			}

			fmt.Printf("Released %q from pool %q\n", sandboxName, poolName)
			fmt.Printf("Destroy with: tent destroy %s\n", sandboxName)
			return nil
		},
	}

	return cmd
}

func poolFillCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "fill <name>",
		Short: "Provision sandboxes to reach the min-ready target",
		Long: `Create and start sandboxes until the pool's min_ready threshold is met.
Each sandbox is created from the pool's configured image and settings.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			poolName := args[0]
			baseDir := getBaseDir()
			pm := vm.NewPoolManager(baseDir)

			deficit, cfg, err := pm.GetDeficit(poolName)
			if err != nil {
				return err
			}

			if deficit == 0 {
				fmt.Printf("Pool %q is already at min_ready target\n", poolName)
				return nil
			}

			if dryRun {
				fmt.Printf("Would provision %d sandbox(es) for pool %q\n", deficit, poolName)
				return nil
			}

			// Resolve image
			imgMgr, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			rootfsPath, err := imgMgr.ResolveImage(cfg.From)
			if err != nil {
				return fmt.Errorf("failed to resolve image %q: %w", cfg.From, err)
			}

			// Create hypervisor backend
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Provisioning %d sandbox(es) for pool %q...\n", deficit, poolName)

			for i := 0; i < deficit; i++ {
				sbName := pm.GenerateMemberName(poolName, i)
				vmCfg := pm.GenerateMemberConfig(cfg, sbName)
				vmCfg.RootFS = rootfsPath

				// Register as provisioning
				if err := pm.AddMember(poolName, sbName, vm.PoolMemberProvisioning); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to register %q: %v\n", sbName, err)
					continue
				}

				// Create
				fmt.Fprintf(os.Stderr, "  [%d/%d] Creating %s...\n", i+1, deficit, sbName)
				if err := manager.Create(sbName, vmCfg); err != nil {
					fmt.Fprintf(os.Stderr, "  Error creating %s: %v\n", sbName, err)
					_ = pm.SetMemberState(poolName, sbName, vm.PoolMemberError)
					continue
				}

				// Start
				fmt.Fprintf(os.Stderr, "  [%d/%d] Starting %s...\n", i+1, deficit, sbName)
				if err := manager.Start(sbName); err != nil {
					fmt.Fprintf(os.Stderr, "  Error starting %s: %v\n", sbName, err)
					_ = pm.SetMemberState(poolName, sbName, vm.PoolMemberError)
					continue
				}

				// Mark ready
				if err := pm.SetMemberState(poolName, sbName, vm.PoolMemberReady); err != nil {
					fmt.Fprintf(os.Stderr, "  Warning: failed to mark %s ready: %v\n", sbName, err)
				}
			}

			// Print final status
			status, err := pm.GetPoolStatus(poolName)
			if err == nil {
				fmt.Fprintf(os.Stderr, "Done. Pool %q: %d ready, %d total\n", poolName, status.Ready, status.Total)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be provisioned without doing it")
	return cmd
}

func poolDrainCmd() *cobra.Command {
	var destroy bool

	cmd := &cobra.Command{
		Use:   "drain <name>",
		Short: "Remove all unclaimed (ready) sandboxes from the pool",
		Long: `Drain removes all ready sandboxes from the pool, leaving only claimed ones.
Use --destroy to also stop and destroy the sandbox VMs.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			poolName := args[0]
			baseDir := getBaseDir()
			pm := vm.NewPoolManager(baseDir)

			status, err := pm.GetPoolStatus(poolName)
			if err != nil {
				return err
			}

			if status.Ready == 0 {
				fmt.Printf("Pool %q has no ready sandboxes to drain\n", poolName)
				return nil
			}

			// Load full state to get member names
			// We need to iterate and remove ready members
			var manager *vm.VMManager
			if destroy {
				hvBackend, err := vm.NewPlatformBackend(baseDir)
				if err != nil {
					return fmt.Errorf("failed to create hypervisor backend: %w", err)
				}
				manager, err = vm.NewManager(baseDir, nil, hvBackend, nil, nil)
				if err != nil {
					return fmt.Errorf("failed to create VM manager: %w", err)
				}
				if err := manager.Setup(); err != nil {
					return fmt.Errorf("failed to setup VM manager: %w", err)
				}
			}

			// Get sandbox names to drain by reading pool state members
			// We'll repeatedly get status and remove ready members
			drained := 0
			for {
				ps, err := pm.GetPoolStatus(poolName)
				if err != nil {
					break
				}
				if ps.Ready == 0 {
					break
				}

				// Claim and immediately release to remove
				member, err := pm.Claim(poolName, "drain-op")
				if err != nil {
					break
				}

				_ = pm.Release(poolName, member.SandboxName)

				if destroy && manager != nil {
					fmt.Fprintf(os.Stderr, "Destroying %s...\n", member.SandboxName)
					_ = manager.Stop(member.SandboxName)
					_ = manager.Destroy(member.SandboxName)
				}
				drained++
			}

			fmt.Printf("Drained %d sandbox(es) from pool %q\n", drained, poolName)
			if !destroy && drained > 0 {
				fmt.Println("Sandboxes are still running. Use --destroy to stop and remove them.")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&destroy, "destroy", false, "Stop and destroy drained sandboxes")
	return cmd
}

// parsePoolEnvVar splits KEY=VALUE, returning key and value.
func parsePoolEnvVar(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// formatTimeSince returns a human-readable duration since a unix timestamp.
func formatTimeSince(unixTime int64) string {
	if unixTime == 0 {
		return "never"
	}
	d := time.Since(time.Unix(unixTime, 0))
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}
