package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/network"
	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func networkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Network management commands",
	}

	cmd.AddCommand(networkListCmd())
	cmd.AddCommand(networkAllowCmd())
	cmd.AddCommand(networkDenyCmd())
	cmd.AddCommand(networkStatusCmd())
	cmd.AddCommand(networkPortsCmd())
	cmd.AddCommand(networkPortAddCmd())
	cmd.AddCommand(networkPortRemoveCmd())

	return cmd
}

func networkListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List bridges and TAP devices",
		Long:  `List bridges and TAP devices managed by tent.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Create network manager
			manager, err := network.NewManager()
			if err != nil {
				return fmt.Errorf("failed to create network manager: %w", err)
			}

			// List network resources
			resources, err := manager.ListNetworkResources()
			if err != nil {
				return fmt.Errorf("failed to list network resources: %w", err)
			}

			if len(resources) == 0 {
				fmt.Println("No network devices found.")
				return nil
			}

			fmt.Println("Listing network devices:")
			for _, res := range resources {
				fmt.Printf("%s (%s): IP=%s\n", res.Name, res.Type, res.IP)
			}

			return nil
		},
	}
}

func networkAllowCmd() *cobra.Command {
	var sandboxName, endpoint string

	cmd := &cobra.Command{
		Use:   "allow <sandbox> <endpoint>",
		Short: "Allow a sandbox to reach an external endpoint",
		Long:  `Allow a sandbox to reach an external endpoint (e.g., api.anthropic.com).`,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName = args[0]
			endpoint = args[1]

			// Create policy manager
			baseDir := getBaseDir()
			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			// Add endpoint to allowed list
			if err := pm.AddAllowedEndpoint(sandboxName, endpoint); err != nil {
				return fmt.Errorf("failed to add endpoint to allowed list: %w", err)
			}

			// Save policy
			policy, err := pm.GetPolicy(sandboxName)
			if err != nil {
				return fmt.Errorf("failed to get policy: %w", err)
			}

			if err := pm.SavePolicy(policy); err != nil {
				return fmt.Errorf("failed to save policy: %w", err)
			}

			fmt.Printf("Allowed sandbox '%s' to reach '%s'\n", sandboxName, endpoint)
			return nil
		},
	}

	return cmd
}

func networkDenyCmd() *cobra.Command {
	var sandboxName, endpoint string

	cmd := &cobra.Command{
		Use:   "deny <sandbox> <endpoint>",
		Short: "Revoke a sandbox's access to an external endpoint",
		Long:  `Revoke a sandbox's access to an external endpoint.`,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName = args[0]
			endpoint = args[1]

			// Create policy manager
			baseDir := getBaseDir()
			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			// Remove endpoint from allowed list and add to denied list
			if err := pm.RemoveAllowedEndpoint(sandboxName, endpoint); err != nil {
				// If not in allowed list, try to add to denied list
				if err := pm.AddDeniedEndpoint(sandboxName, endpoint); err != nil {
					return fmt.Errorf("failed to deny endpoint: %w", err)
				}
			} else {
				// Also add to denied list to explicitly deny
				if err := pm.AddDeniedEndpoint(sandboxName, endpoint); err != nil {
					return fmt.Errorf("failed to add to denied list: %w", err)
				}
			}

			// Save policy
			policy, err := pm.GetPolicy(sandboxName)
			if err != nil {
				return fmt.Errorf("failed to get policy: %w", err)
			}

			if err := pm.SavePolicy(policy); err != nil {
				return fmt.Errorf("failed to save policy: %w", err)
			}

			fmt.Printf("Denied sandbox '%s' from reaching '%s'\n", sandboxName, endpoint)
			return nil
		},
	}

	return cmd
}

func networkStatusCmd() *cobra.Command {
	var sandboxName string

	cmd := &cobra.Command{
		Use:   "status <sandbox>",
		Short: "Show a sandbox's network policy",
		Long:  `Show allowed and denied endpoints for a sandbox.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName = args[0]

			// Create policy manager
			baseDir := getBaseDir()
			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			// Get policy
			policy, err := pm.GetPolicy(sandboxName)
			if err != nil {
				return fmt.Errorf("failed to get policy: %w", err)
			}

			fmt.Printf("Network policy for sandbox '%s':\n", sandboxName)
			fmt.Printf("  Allowed endpoints:\n")
			if len(policy.Allowed) == 0 {
				fmt.Printf("    (none)\n")
			} else {
				for _, ep := range policy.Allowed {
					fmt.Printf("    - %s\n", ep)
				}
			}

			fmt.Printf("  Denied endpoints:\n")
			if len(policy.Denied) == 0 {
				fmt.Printf("    (none)\n")
			} else {
				for _, ep := range policy.Denied {
					fmt.Printf("    - %s\n", ep)
				}
			}

			return nil
		},
	}

	return cmd
}

func networkPortsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ports [sandbox]",
		Short: "Show port forwarding rules",
		Long:  `Show active port forwarding rules. Optionally filter by sandbox name.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if len(args) == 1 {
				forwards, err := manager.ListPortForwards(args[0])
				if err != nil {
					return err
				}
				if len(forwards) == 0 {
					fmt.Printf("No port forwarding rules for sandbox '%s'\n", args[0])
					return nil
				}
				fmt.Printf("Port forwarding for '%s':\n", args[0])
				for _, f := range forwards {
					status := "inactive"
					if f.Active {
						status = "active"
					}
					fmt.Printf("  :%d -> %s:%d (%s)\n", f.HostPort, f.GuestIP, f.GuestPort, status)
				}
			} else {
				forwards := manager.ListAllPortForwards()
				if len(forwards) == 0 {
					fmt.Println("No port forwarding rules configured.")
					return nil
				}
				fmt.Println("Port forwarding rules:")
				for _, f := range forwards {
					status := "inactive"
					if f.Active {
						status = "active"
					}
					fmt.Printf("  %s: :%d -> %s:%d (%s)\n", f.VMName, f.HostPort, f.GuestIP, f.GuestPort, status)
				}
			}
			return nil
		},
	}
}

func networkPortAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "port-add <sandbox> <host-port>:<guest-port>",
		Short: "Add a port forwarding rule to a running sandbox",
		Long: `Dynamically add a TCP port forwarding rule to a running sandbox.

Examples:
  tent network port-add mybox 8080:80
  tent network port-add mybox 3000:3000`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			hostPort, guestPort, err := parsePortMapping(args[1])
			if err != nil {
				return err
			}

			baseDir := getBaseDir()
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.AddPortForward(sandboxName, hostPort, guestPort); err != nil {
				return fmt.Errorf("failed to add port forward: %w", err)
			}

			fmt.Printf("Added port forward: :%d -> %s:%d\n", hostPort, sandboxName, guestPort)
			return nil
		},
	}
}

func networkPortRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "port-remove <sandbox> <host-port>",
		Short: "Remove a port forwarding rule from a sandbox",
		Long: `Remove a TCP port forwarding rule from a sandbox.

Examples:
  tent network port-remove mybox 8080`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			hostPort, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid host port: %w", err)
			}

			baseDir := getBaseDir()
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.RemovePortForward(sandboxName, hostPort); err != nil {
				return fmt.Errorf("failed to remove port forward: %w", err)
			}

			fmt.Printf("Removed port forward on host port :%d from '%s'\n", hostPort, sandboxName)
			return nil
		},
	}
}

// parsePortMapping parses "host:guest" port mapping string
func parsePortMapping(s string) (int, int, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid port mapping %q, expected host:guest (e.g. 8080:80)", s)
	}
	hostPort, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid host port %q: %w", parts[0], err)
	}
	guestPort, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid guest port %q: %w", parts[1], err)
	}
	return hostPort, guestPort, nil
}

// getBaseDir gets the base directory, respecting TENT_BASE_DIR env var
func getBaseDir() string {
	if baseDir := os.Getenv("TENT_BASE_DIR"); baseDir != "" {
		return baseDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tent")
}
