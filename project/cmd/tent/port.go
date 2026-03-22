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

func portCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port",
		Short: "Manage port forwarding for sandboxes",
		Long: `Manage host-to-sandbox TCP port forwarding.

Examples:
  tent port add mybox 8080:80        Forward host:8080 to sandbox:80
  tent port add mybox 3000            Forward host:3000 to sandbox:3000
  tent port rm mybox 8080             Remove forward on host port 8080
  tent port ls mybox                  List forwards for a sandbox
  tent port ls                        List all port forwards`,
	}

	cmd.AddCommand(portAddCmd())
	cmd.AddCommand(portRmCmd())
	cmd.AddCommand(portLsCmd())

	return cmd
}

func portAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <sandbox> <host-port>:<guest-port>",
		Short: "Add a port forward to a running sandbox",
		Long: `Add a TCP port forward from the host to a running sandbox.

Port can be specified as:
  8080:80    - forward host port 8080 to guest port 80
  3000       - forward host port 3000 to guest port 3000 (same port)`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			hostPort, guestPort, err := portForwardParseSpec(args[1])
			if err != nil {
				return err
			}

			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if err := manager.AddPortForward(name, hostPort, guestPort); err != nil {
				return fmt.Errorf("failed to add port forward: %w", err)
			}

			fmt.Printf("Forwarding host:%d -> %s:%d\n", hostPort, name, guestPort)
			return nil
		},
	}
}

func portRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <sandbox> <host-port>",
		Short: "Remove a port forward",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			hostPort, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid host port %q: %w", args[1], err)
			}

			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if err := manager.RemovePortForward(name, hostPort); err != nil {
				return fmt.Errorf("failed to remove port forward: %w", err)
			}

			fmt.Printf("Removed port forward on host:%d for sandbox %q\n", hostPort, name)
			return nil
		},
	}
}

func portLsCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "ls [sandbox]",
		Short: "List port forwards",
		Long:  `List port forwards for a specific sandbox, or all sandboxes if no name is given.`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if len(args) == 1 {
				return listPortForwardsForVM(manager, args[0], outputJSON)
			}
			return listAllPortForwards(manager, outputJSON)
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

func listPortForwardsForVM(manager *vm.VMManager, name string, jsonOut bool) error {
	forwards, err := manager.ListPortForwards(name)
	if err != nil {
		return err
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(forwards)
	}

	if len(forwards) == 0 {
		fmt.Printf("No port forwards for sandbox %q\n", name)
		return nil
	}

	fmt.Printf("%-20s %-12s %-12s %-16s %-8s\n", "SANDBOX", "HOST PORT", "GUEST PORT", "GUEST IP", "ACTIVE")
	for _, f := range forwards {
		active := "no"
		if f.Active {
			active = "yes"
		}
		fmt.Printf("%-20s %-12d %-12d %-16s %-8s\n", f.VMName, f.HostPort, f.GuestPort, f.GuestIP, active)
	}
	return nil
}

func listAllPortForwards(manager *vm.VMManager, jsonOut bool) error {
	forwards := manager.ListAllPortForwards()

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(forwards)
	}

	if len(forwards) == 0 {
		fmt.Println("No port forwards configured.")
		return nil
	}

	fmt.Printf("%-20s %-12s %-12s %-16s %-8s\n", "SANDBOX", "HOST PORT", "GUEST PORT", "GUEST IP", "ACTIVE")
	for _, f := range forwards {
		active := "no"
		if f.Active {
			active = "yes"
		}
		fmt.Printf("%-20s %-12d %-12d %-16s %-8s\n", f.VMName, f.HostPort, f.GuestPort, f.GuestIP, active)
	}
	return nil
}

// portForwardParseSpec parses a port spec like "8080:80" or "3000" (same port shorthand)
func portForwardParseSpec(spec string) (hostPort, guestPort int, err error) {
	if strings.Contains(spec, ":") {
		return parsePortMapping(spec)
	}
	// Same port shorthand: "3000" means 3000:3000
	port, err := strconv.Atoi(spec)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid port %q: %w", spec, err)
	}
	if port <= 0 || port > 65535 {
		return 0, 0, fmt.Errorf("port %d out of range (1-65535)", port)
	}
	return port, port, nil
}
