package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func secProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "security-profile",
		Aliases: []string{"secprofile", "sp"},
		Short:   "Manage sandbox security profiles",
		Long: `Manage security profiles that control sandbox isolation.

Security profiles define network policy, mount restrictions, resource caps,
and other isolation settings. Built-in profiles: default, strict, privileged.

Examples:
  tent security-profile list
  tent security-profile show default
  tent security-profile create --name custom --egress block --max-vcpus 2 --max-memory 2048
  tent security-profile check mybox --profile strict
  tent security-profile delete custom`,
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Usage()
		},
	}

	cmd.AddCommand(secProfileListCmd())
	cmd.AddCommand(secProfileShowCmd())
	cmd.AddCommand(secProfileCreateCmd())
	cmd.AddCommand(secProfileDeleteCmd())
	cmd.AddCommand(secProfileCheckCmd())

	return cmd
}

func secProfileListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all security profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewSecurityProfileManager(baseDir)
			profiles := mgr.List()

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(profiles)
			}

			if len(profiles) == 0 {
				fmt.Println("No security profiles found.")
				return nil
			}

			// Table output
			maxName := 4
			maxDesc := 11
			for _, p := range profiles {
				if len(p.Name) > maxName {
					maxName = len(p.Name)
				}
				desc := p.Description
				if len(desc) > 60 {
					desc = desc[:57] + "..."
				}
				if len(desc) > maxDesc {
					maxDesc = len(desc)
				}
			}

			hdrFmt := fmt.Sprintf("%%-%ds  %%-%ds  %%s  %%s\n", maxName, maxDesc)
			fmt.Printf(hdrFmt, "NAME", "DESCRIPTION", "EGRESS", "TYPE")
			fmt.Println(strings.Repeat("-", maxName+maxDesc+20))
			for _, p := range profiles {
				desc := p.Description
				if len(desc) > 60 {
					desc = desc[:57] + "..."
				}
				profileType := "custom"
				if p.Builtin {
					profileType = "builtin"
				}
				egress := p.NetworkPolicy.EgressPolicy
				if egress == "" {
					egress = "block"
				}
				fmt.Printf(hdrFmt, p.Name, desc, egress, profileType)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func secProfileShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show details of a security profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewSecurityProfileManager(baseDir)

			profile, err := mgr.Get(args[0])
			if err != nil {
				return err
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(profile)
		},
	}
	return cmd
}

func secProfileCreateCmd() *cobra.Command {
	var (
		name              string
		description       string
		egressPolicy      string
		allowDNS          bool
		allowInterSandbox bool
		allowHostMounts   bool
		forceReadonly     bool
		readonlyRootFS    bool
		noNewPrivileges   bool
		maxVCPUs          int
		maxMemoryMB       int
		maxDiskGB         int
		maxPids           int
		allowedEndpoints  []string
		allowedPaths      []string
		deniedPaths       []string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a custom security profile",
		Long: `Create a custom security profile with specified isolation settings.

Examples:
  tent security-profile create --name minimal --egress block --no-host-mounts --max-vcpus 2
  tent security-profile create --name dev --egress allow --allow-dns --max-memory 8192
  tent security-profile create --name ci --egress block --allow "registry.npmjs.org:443" --readonly-rootfs`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}

			profile := &vm.SecurityProfile{
				Name:        name,
				Description: description,
				NetworkPolicy: vm.ProfileNetworkPolicy{
					EgressPolicy:      egressPolicy,
					AllowedEndpoints:  allowedEndpoints,
					AllowDNS:          allowDNS,
					AllowInterSandbox: allowInterSandbox,
				},
				MountPolicy: vm.ProfileMountPolicy{
					AllowHostMounts: allowHostMounts,
					ForceReadonly:   forceReadonly,
					AllowedPaths:    allowedPaths,
					DeniedPaths:     deniedPaths,
				},
				ResourceCaps: vm.ProfileResourceCaps{
					MaxVCPUs:    maxVCPUs,
					MaxMemoryMB: maxMemoryMB,
					MaxDiskGB:   maxDiskGB,
					MaxPids:     maxPids,
				},
				ReadonlyRootFS:  readonlyRootFS,
				NoNewPrivileges: noNewPrivileges,
			}

			baseDir := getBaseDir()
			mgr := vm.NewSecurityProfileManager(baseDir)

			if err := mgr.Save(profile); err != nil {
				return fmt.Errorf("failed to create profile: %w", err)
			}

			fmt.Printf("Created security profile %q\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Profile name (required)")
	cmd.Flags().StringVar(&description, "description", "", "Human-readable description")
	cmd.Flags().StringVar(&egressPolicy, "egress", "block", "Egress policy: block or allow")
	cmd.Flags().BoolVar(&allowDNS, "allow-dns", true, "Allow DNS resolution")
	cmd.Flags().BoolVar(&allowInterSandbox, "allow-inter-sandbox", true, "Allow inter-sandbox communication")
	cmd.Flags().BoolVar(&allowHostMounts, "host-mounts", true, "Allow host filesystem mounts")
	cmd.Flags().BoolVar(&forceReadonly, "force-readonly-mounts", false, "Force all mounts to be read-only")
	cmd.Flags().BoolVar(&readonlyRootFS, "readonly-rootfs", false, "Mount root filesystem read-only")
	cmd.Flags().BoolVar(&noNewPrivileges, "no-new-privileges", true, "Prevent gaining additional privileges")
	cmd.Flags().IntVar(&maxVCPUs, "max-vcpus", 0, "Maximum vCPUs (0 = no cap)")
	cmd.Flags().IntVar(&maxMemoryMB, "max-memory", 0, "Maximum memory in MB (0 = no cap)")
	cmd.Flags().IntVar(&maxDiskGB, "max-disk", 0, "Maximum disk in GB (0 = no cap)")
	cmd.Flags().IntVar(&maxPids, "max-pids", 0, "Maximum processes (0 = no cap)")
	cmd.Flags().StringSliceVar(&allowedEndpoints, "allow", nil, "Allowed outbound endpoints (host:port)")
	cmd.Flags().StringSliceVar(&allowedPaths, "allowed-paths", nil, "Allowed host mount paths (glob patterns)")
	cmd.Flags().StringSliceVar(&deniedPaths, "denied-paths", nil, "Denied host mount paths (glob patterns)")

	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func secProfileDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a custom security profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewSecurityProfileManager(baseDir)

			if err := mgr.Delete(args[0]); err != nil {
				return err
			}

			fmt.Printf("Deleted security profile %q\n", args[0])
			return nil
		},
	}
	return cmd
}

func secProfileCheckCmd() *cobra.Command {
	var profileName string

	cmd := &cobra.Command{
		Use:   "check <sandbox>",
		Short: "Check if a sandbox complies with a security profile",
		Long: `Validate a sandbox's configuration against a security profile,
reporting any violations.

Examples:
  tent security-profile check mybox --profile strict
  tent security-profile check mybox --profile default`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			baseDir := getBaseDir()

			// Load profile
			profMgr := vm.NewSecurityProfileManager(baseDir)
			profile, err := profMgr.Get(profileName)
			if err != nil {
				return err
			}

			// Load sandbox state
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

			state, err := manager.Status(sandboxName)
			if err != nil {
				return fmt.Errorf("sandbox %q not found: %w", sandboxName, err)
			}

			// Check resource caps
			vcpus := state.VCPUs
			mem := state.MemoryMB
			disk := state.DiskGB

			violations := profile.CheckVMConfig(vcpus, mem, disk)

			// Check network policy
			if profile.NetworkPolicy.EgressPolicy == "block" {
				// Note: actual egress rules are checked at runtime
				fmt.Printf("Network policy: egress blocked by default\n")
				if profile.NetworkPolicy.AllowDNS {
					fmt.Printf("  DNS resolution: allowed\n")
				} else {
					fmt.Printf("  DNS resolution: blocked\n")
				}
			}

			if len(violations) == 0 {
				fmt.Printf("Sandbox %q is compliant with profile %q\n", sandboxName, profileName)
				return nil
			}

			fmt.Printf("Sandbox %q has %s against profile %q:\n",
				sandboxName, pluralize(len(violations), "violation"), profileName)
			for _, v := range violations {
				fmt.Printf("  - %s\n", v)
			}
			return fmt.Errorf("%d violation(s) found", len(violations))
		},
	}

	cmd.Flags().StringVar(&profileName, "profile", "default", "Security profile to check against")
	return cmd
}

func pluralize(n int, word string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
