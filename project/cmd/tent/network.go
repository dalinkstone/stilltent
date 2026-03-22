package main

import (
	"encoding/json"
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
	cmd.AddCommand(networkCreateCmd())
	cmd.AddCommand(networkDeleteCmd())
	cmd.AddCommand(networkInspectCmd())
	cmd.AddCommand(networkConnectCmd())
	cmd.AddCommand(networkDisconnectCmd())
	cmd.AddCommand(networkLsCmd())
	cmd.AddCommand(networkBandwidthCmd())

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

func networkCreateCmd() *cobra.Command {
	var (
		subnet   string
		gateway  string
		internal bool
		labels   []string
	)

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a custom bridge network",
		Long: `Create a named bridge network with its own subnet. Sandboxes connected
to the same network can communicate with each other. Use --internal to
prevent external connectivity.

Examples:
  tent network create mynet
  tent network create mynet --subnet 10.0.1.0/24 --gateway 10.0.1.1
  tent network create isolated --internal
  tent network create dev --label env=development`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			store, err := network.NewNetworkStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open network store: %w", err)
			}

			labelMap := make(map[string]string)
			for _, l := range labels {
				parts := strings.SplitN(l, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid label %q, expected key=value", l)
				}
				labelMap[parts[0]] = parts[1]
			}

			n, err := store.CreateNetwork(name, subnet, gateway, internal, labelMap)
			if err != nil {
				return fmt.Errorf("failed to create network: %w", err)
			}

			fmt.Printf("Created network %q\n", n.Name)
			fmt.Printf("  Subnet:   %s\n", n.Subnet)
			fmt.Printf("  Gateway:  %s\n", n.Gateway)
			fmt.Printf("  Driver:   %s\n", n.Driver)
			if n.Internal {
				fmt.Printf("  Internal: yes (no external connectivity)\n")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&subnet, "subnet", "", "Subnet in CIDR notation (auto-allocated if empty)")
	cmd.Flags().StringVar(&gateway, "gateway", "", "Gateway IP (defaults to first IP in subnet)")
	cmd.Flags().BoolVar(&internal, "internal", false, "Restrict to inter-sandbox traffic only")
	cmd.Flags().StringSliceVar(&labels, "label", nil, "Labels in key=value format")

	return cmd
}

func networkDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a custom network",
		Long: `Delete a custom bridge network. All sandboxes must be disconnected first.

Examples:
  tent network delete mynet`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			store, err := network.NewNetworkStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open network store: %w", err)
			}

			if err := store.DeleteNetwork(name); err != nil {
				return fmt.Errorf("failed to delete network: %w", err)
			}

			fmt.Printf("Deleted network %q\n", name)
			return nil
		},
	}
}

func networkInspectCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "inspect <name>",
		Short: "Show details of a custom network",
		Long: `Display detailed information about a custom network including subnet,
gateway, connected sandboxes, and labels.

Examples:
  tent network inspect mynet
  tent network inspect mynet --format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			store, err := network.NewNetworkStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open network store: %w", err)
			}

			n, err := store.GetNetwork(name)
			if err != nil {
				return fmt.Errorf("network not found: %w", err)
			}

			if outputFormat == "json" {
				data, err := json.MarshalIndent(n, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("Network: %s\n", n.Name)
			fmt.Printf("  Driver:   %s\n", n.Driver)
			fmt.Printf("  Subnet:   %s\n", n.Subnet)
			fmt.Printf("  Gateway:  %s\n", n.Gateway)
			fmt.Printf("  Internal: %v\n", n.Internal)
			if len(n.Labels) > 0 {
				fmt.Printf("  Labels:\n")
				for k, v := range n.Labels {
					fmt.Printf("    %s=%s\n", k, v)
				}
			}
			if len(n.Sandboxes) > 0 {
				fmt.Printf("  Sandboxes:\n")
				for _, s := range n.Sandboxes {
					fmt.Printf("    - %s\n", s)
				}
			} else {
				fmt.Printf("  Sandboxes: (none)\n")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&outputFormat, "format", "text", "Output format: text, json")
	return cmd
}

func networkConnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connect <network> <sandbox>",
		Short: "Connect a sandbox to a custom network",
		Long: `Attach a sandbox to a named network so it can communicate with other
sandboxes on the same network.

Examples:
  tent network connect mynet agent-box
  tent network connect shared-net db-box`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			networkName := args[0]
			sandboxName := args[1]
			baseDir := getBaseDir()

			store, err := network.NewNetworkStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open network store: %w", err)
			}

			if err := store.ConnectSandbox(networkName, sandboxName); err != nil {
				return err
			}

			fmt.Printf("Connected sandbox %q to network %q\n", sandboxName, networkName)
			return nil
		},
	}
}

func networkDisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect <network> <sandbox>",
		Short: "Disconnect a sandbox from a custom network",
		Long: `Detach a sandbox from a named network.

Examples:
  tent network disconnect mynet agent-box`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			networkName := args[0]
			sandboxName := args[1]
			baseDir := getBaseDir()

			store, err := network.NewNetworkStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open network store: %w", err)
			}

			if err := store.DisconnectSandbox(networkName, sandboxName); err != nil {
				return err
			}

			fmt.Printf("Disconnected sandbox %q from network %q\n", sandboxName, networkName)
			return nil
		},
	}
}

func networkLsCmd() *cobra.Command {
	var quiet bool

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List custom networks",
		Long: `List all user-created networks with their subnet, gateway, and
connected sandbox count.

Examples:
  tent network ls
  tent network ls -q`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			store, err := network.NewNetworkStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to open network store: %w", err)
			}

			networks := store.ListNetworks()
			if len(networks) == 0 {
				fmt.Println("No custom networks found.")
				return nil
			}

			if quiet {
				for _, n := range networks {
					fmt.Println(n.Name)
				}
				return nil
			}

			fmt.Printf("%-20s %-8s %-20s %-16s %-10s %s\n",
				"NAME", "DRIVER", "SUBNET", "GATEWAY", "INTERNAL", "SANDBOXES")
			for _, n := range networks {
				internalStr := "no"
				if n.Internal {
					internalStr = "yes"
				}
				fmt.Printf("%-20s %-8s %-20s %-16s %-10s %d\n",
					n.Name, n.Driver, n.Subnet, n.Gateway, internalStr, len(n.Sandboxes))
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only show network names")
	return cmd
}

func networkBandwidthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bandwidth",
		Short: "Manage per-sandbox network bandwidth limits",
		Long: `Set, view, or remove network bandwidth rate limits for sandboxes.

Bandwidth limits control the maximum ingress (download) and egress (upload)
rates for a sandbox's network interface. Rates are specified in human-readable
format: bps, kbps, mbps, or gbps.

Examples:
  tent network bandwidth set mybox --ingress 100mbps --egress 50mbps
  tent network bandwidth get mybox
  tent network bandwidth remove mybox`,
	}

	cmd.AddCommand(networkBandwidthSetCmd())
	cmd.AddCommand(networkBandwidthGetCmd())
	cmd.AddCommand(networkBandwidthRemoveCmd())
	cmd.AddCommand(networkBandwidthListCmd())

	return cmd
}

func networkBandwidthSetCmd() *cobra.Command {
	var (
		ingress      string
		egress       string
		ingressBurst string
		egressBurst  string
	)

	cmd := &cobra.Command{
		Use:   "set <sandbox>",
		Short: "Set bandwidth limits for a sandbox",
		Long: `Configure ingress and/or egress rate limits for a sandbox's network traffic.

Rates can be specified with suffixes: bps, kbps, mbps, gbps.
Use "unlimited" or "0" to remove a specific limit.

Examples:
  tent network bandwidth set mybox --ingress 100mbps --egress 50mbps
  tent network bandwidth set mybox --ingress 1gbps
  tent network bandwidth set mybox --egress 10mbps --egress-burst 65536`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]

			if ingress == "" && egress == "" {
				return fmt.Errorf("at least one of --ingress or --egress must be specified")
			}

			baseDir := getBaseDir()
			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			// Get existing limit if any
			existing, _ := pm.GetBandwidthLimit(sandboxName)
			limit := &network.BandwidthLimit{}
			if existing != nil && existing.HasLimits() {
				*limit = *existing
			}

			if ingress != "" {
				rate, err := network.ParseRate(ingress)
				if err != nil {
					return fmt.Errorf("invalid ingress rate: %w", err)
				}
				limit.IngressRate = rate
			}

			if egress != "" {
				rate, err := network.ParseRate(egress)
				if err != nil {
					return fmt.Errorf("invalid egress rate: %w", err)
				}
				limit.EgressRate = rate
			}

			if ingressBurst != "" {
				burst, err := parseBytes(ingressBurst)
				if err != nil {
					return fmt.Errorf("invalid ingress burst: %w", err)
				}
				limit.IngressBurst = burst
			}

			if egressBurst != "" {
				burst, err := parseBytes(egressBurst)
				if err != nil {
					return fmt.Errorf("invalid egress burst: %w", err)
				}
				limit.EgressBurst = burst
			}

			if err := pm.SetBandwidthLimit(sandboxName, limit); err != nil {
				return fmt.Errorf("failed to set bandwidth limit: %w", err)
			}

			policy, err := pm.GetPolicy(sandboxName)
			if err != nil {
				// Create a minimal policy with just bandwidth
				policy = &network.Policy{Name: sandboxName}
			}
			policy.Bandwidth = limit
			if err := pm.SavePolicy(policy); err != nil {
				return fmt.Errorf("failed to save policy: %w", err)
			}

			fmt.Printf("Bandwidth limits for '%s':\n", sandboxName)
			fmt.Printf("  Ingress: %s\n", network.FormatRate(limit.IngressRate))
			fmt.Printf("  Egress:  %s\n", network.FormatRate(limit.EgressRate))
			if limit.IngressBurst > 0 {
				fmt.Printf("  Ingress burst: %d bytes\n", limit.IngressBurst)
			}
			if limit.EgressBurst > 0 {
				fmt.Printf("  Egress burst:  %d bytes\n", limit.EgressBurst)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&ingress, "ingress", "", "Max inbound rate (e.g. 100mbps, 1gbps)")
	cmd.Flags().StringVar(&egress, "egress", "", "Max outbound rate (e.g. 50mbps, 500kbps)")
	cmd.Flags().StringVar(&ingressBurst, "ingress-burst", "", "Ingress burst size in bytes")
	cmd.Flags().StringVar(&egressBurst, "egress-burst", "", "Egress burst size in bytes")

	return cmd
}

func networkBandwidthGetCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "get <sandbox>",
		Short: "Show bandwidth limits for a sandbox",
		Long: `Display the current bandwidth rate limits for a sandbox.

Examples:
  tent network bandwidth get mybox
  tent network bandwidth get mybox --format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			baseDir := getBaseDir()

			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			limit, err := pm.GetBandwidthLimit(sandboxName)
			if err != nil {
				return fmt.Errorf("failed to get bandwidth limit: %w", err)
			}

			if !limit.HasLimits() {
				fmt.Printf("No bandwidth limits set for '%s'\n", sandboxName)
				return nil
			}

			if outputFormat == "json" {
				data, err := json.MarshalIndent(limit, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("Bandwidth limits for '%s':\n", sandboxName)
			fmt.Printf("  Ingress: %s\n", network.FormatRate(limit.IngressRate))
			fmt.Printf("  Egress:  %s\n", network.FormatRate(limit.EgressRate))
			if limit.IngressBurst > 0 {
				fmt.Printf("  Ingress burst: %d bytes\n", limit.IngressBurst)
			}
			if limit.EgressBurst > 0 {
				fmt.Printf("  Egress burst:  %d bytes\n", limit.EgressBurst)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&outputFormat, "format", "text", "Output format: text, json")
	return cmd
}

func networkBandwidthRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <sandbox>",
		Short: "Remove bandwidth limits from a sandbox",
		Long: `Clear all bandwidth rate limits for a sandbox, restoring unlimited throughput.

Examples:
  tent network bandwidth remove mybox`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			baseDir := getBaseDir()

			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			if err := pm.RemoveBandwidthLimit(sandboxName); err != nil {
				return fmt.Errorf("failed to remove bandwidth limit: %w", err)
			}

			policy, err := pm.GetPolicy(sandboxName)
			if err != nil {
				return fmt.Errorf("failed to get policy: %w", err)
			}

			if err := pm.SavePolicy(policy); err != nil {
				return fmt.Errorf("failed to save policy: %w", err)
			}

			fmt.Printf("Removed bandwidth limits for '%s'\n", sandboxName)
			return nil
		},
	}
}

func networkBandwidthListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all sandboxes with bandwidth limits",
		Long: `Show bandwidth limits for all sandboxes that have rate limiting configured.

Examples:
  tent network bandwidth list`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			policies, err := pm.ListPolicies()
			if err != nil {
				return fmt.Errorf("failed to list policies: %w", err)
			}

			found := false
			fmt.Printf("%-20s %-15s %-15s %-12s %-12s\n",
				"SANDBOX", "INGRESS", "EGRESS", "IN-BURST", "OUT-BURST")
			for _, p := range policies {
				if p.Bandwidth == nil || !p.Bandwidth.HasLimits() {
					continue
				}
				found = true
				inBurst := "-"
				outBurst := "-"
				if p.Bandwidth.IngressBurst > 0 {
					inBurst = fmt.Sprintf("%d", p.Bandwidth.IngressBurst)
				}
				if p.Bandwidth.EgressBurst > 0 {
					outBurst = fmt.Sprintf("%d", p.Bandwidth.EgressBurst)
				}
				fmt.Printf("%-20s %-15s %-15s %-12s %-12s\n",
					p.Name,
					network.FormatRate(p.Bandwidth.IngressRate),
					network.FormatRate(p.Bandwidth.EgressRate),
					inBurst,
					outBurst,
				)
			}

			if !found {
				fmt.Println("No sandboxes with bandwidth limits configured.")
			}
			return nil
		},
	}
}

// parseBytes parses a byte count string (plain number)
func parseBytes(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	val, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte count %q: %w", s, err)
	}
	return val, nil
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
