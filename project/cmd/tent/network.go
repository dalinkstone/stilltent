package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/network"
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

// getBaseDir gets the base directory, respecting TENT_BASE_DIR env var
func getBaseDir() string {
	if baseDir := os.Getenv("TENT_BASE_DIR"); baseDir != "" {
		return baseDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tent")
}
