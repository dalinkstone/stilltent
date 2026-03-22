package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/network"
)

func linkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Manage point-to-point network links between sandboxes",
		Long: `Create and manage direct network links between pairs of sandboxes.

Unlike compose networks (shared L2 for a group), links are explicit
bidirectional connections between exactly two sandboxes. Each link gets
its own /30 subnet with dedicated IPs for each endpoint.

Use cases:
  - Service mesh topologies (A <-> B, B <-> C, but not A <-> C)
  - Micro-segmented architectures with fine-grained connectivity
  - Database-to-app links without exposing the DB to the full network

Examples:
  tent link create frontend backend
  tent link create worker db --mtu 9000 --encrypted
  tent link list
  tent link list --sandbox frontend
  tent link inspect link-frontend-backend
  tent link remove link-frontend-backend`,
	}

	cmd.AddCommand(linkCreateCmd())
	cmd.AddCommand(linkRemoveCmd())
	cmd.AddCommand(linkListCmd())
	cmd.AddCommand(linkInspectCmd())
	cmd.AddCommand(linkPeersCmd())

	return cmd
}

func linkCreateCmd() *cobra.Command {
	var (
		mtu       int
		encrypted bool
		labels    []string
		jsonOut   bool
	)

	cmd := &cobra.Command{
		Use:   "create <sandbox-a> <sandbox-b>",
		Short: "Create a point-to-point network link between two sandboxes",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxA := args[0]
			sandboxB := args[1]
			baseDir := getBaseDir()

			lm, err := network.NewLinkManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to initialize link manager: %w", err)
			}

			opts := network.LinkOptions{
				MTU:       mtu,
				Encrypted: encrypted,
			}

			if len(labels) > 0 {
				opts.Labels = make(map[string]string)
				for _, l := range labels {
					parts := strings.SplitN(l, "=", 2)
					if len(parts) != 2 {
						return fmt.Errorf("invalid label format %q: expected key=value", l)
					}
					opts.Labels[parts[0]] = parts[1]
				}
			}

			link, err := lm.CreateLink(sandboxA, sandboxB, opts)
			if err != nil {
				return err
			}

			if jsonOut {
				data, _ := json.MarshalIndent(link, "", "  ")
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("Created link %q\n", link.ID)
			fmt.Printf("  %s (%s) <---> %s (%s)\n", link.SandboxA, link.AddressA, link.SandboxB, link.AddressB)
			fmt.Printf("  Network: %s  MTU: %d", link.Network, link.MTU)
			if link.Encrypted {
				fmt.Print("  Encrypted: yes")
			}
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().IntVar(&mtu, "mtu", 0, "Link MTU (default: 1500)")
	cmd.Flags().BoolVar(&encrypted, "encrypted", false, "Enable WireGuard encryption on the link")
	cmd.Flags().StringSliceVarP(&labels, "label", "l", nil, "Labels in key=value format (can specify multiple)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")

	return cmd
}

func linkRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "remove <link-id>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a network link",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			linkID := args[0]
			baseDir := getBaseDir()

			lm, err := network.NewLinkManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to initialize link manager: %w", err)
			}

			// Show what we're removing unless force
			if !force {
				link, err := lm.GetLink(linkID)
				if err != nil {
					return err
				}
				fmt.Printf("Removing link %s (%s <-> %s)\n", link.ID, link.SandboxA, link.SandboxB)
			}

			if err := lm.RemoveLink(linkID); err != nil {
				return err
			}

			fmt.Printf("Link %q removed.\n", linkID)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}

func linkListCmd() *cobra.Command {
	var (
		sandbox string
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all network links",
		Aliases: []string{"ls"},
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			lm, err := network.NewLinkManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to initialize link manager: %w", err)
			}

			links := lm.ListLinks(sandbox)

			if jsonOut {
				data, _ := json.MarshalIndent(links, "", "  ")
				fmt.Println(string(data))
				return nil
			}

			if len(links) == 0 {
				if sandbox != "" {
					fmt.Printf("No links found for sandbox %q.\n", sandbox)
				} else {
					fmt.Println("No links configured.")
				}
				fmt.Println("Use 'tent link create <a> <b>' to create a link.")
				return nil
			}

			// Sort by ID for consistent output
			sort.Slice(links, func(i, j int) bool {
				return links[i].ID < links[j].ID
			})

			fmt.Printf("%-30s %-15s %-15s %-18s %-5s %s\n",
				"ID", "SANDBOX-A", "SANDBOX-B", "NETWORK", "MTU", "FLAGS")

			for _, link := range links {
				flags := ""
				if link.Encrypted {
					flags = "encrypted"
				}
				fmt.Printf("%-30s %-15s %-15s %-18s %-5d %s\n",
					link.ID,
					link.SandboxA,
					link.SandboxB,
					link.Network,
					link.MTU,
					flags,
				)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&sandbox, "sandbox", "", "Filter links by sandbox name")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")

	return cmd
}

func linkInspectCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "inspect <link-id>",
		Short: "Show detailed information about a network link",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			linkID := args[0]
			baseDir := getBaseDir()

			lm, err := network.NewLinkManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to initialize link manager: %w", err)
			}

			link, err := lm.GetLink(linkID)
			if err != nil {
				return err
			}

			if jsonOut {
				data, _ := json.MarshalIndent(link, "", "  ")
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("Link: %s\n", link.ID)
			fmt.Println()
			fmt.Printf("  Endpoint A:   %s (%s)\n", link.SandboxA, link.AddressA)
			fmt.Printf("  Endpoint B:   %s (%s)\n", link.SandboxB, link.AddressB)
			fmt.Printf("  Network:      %s\n", link.Network)
			fmt.Printf("  MTU:          %d\n", link.MTU)
			fmt.Printf("  Encrypted:    %v\n", link.Encrypted)
			fmt.Printf("  Created:      %s\n", link.CreatedAt.Format("2006-01-02 15:04:05 UTC"))

			if len(link.Labels) > 0 {
				fmt.Printf("  Labels:\n")
				keys := make([]string, 0, len(link.Labels))
				for k := range link.Labels {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Printf("    %s=%s\n", k, link.Labels[k])
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")

	return cmd
}

func linkPeersCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "peers <sandbox>",
		Short: "List all sandboxes linked to a specific sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			baseDir := getBaseDir()

			lm, err := network.NewLinkManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to initialize link manager: %w", err)
			}

			links := lm.LinksForSandbox(sandboxName)

			type peerInfo struct {
				Peer    string `json:"peer"`
				LinkID  string `json:"link_id"`
				LocalIP string `json:"local_ip"`
				PeerIP  string `json:"peer_ip"`
				Network string `json:"network"`
			}

			var peers []peerInfo
			for _, link := range links {
				peer := link.Peer(sandboxName)
				if peer == "" {
					continue
				}
				peers = append(peers, peerInfo{
					Peer:    peer,
					LinkID:  link.ID,
					LocalIP: link.AddressFor(sandboxName),
					PeerIP:  link.AddressFor(peer),
					Network: link.Network,
				})
			}

			if jsonOut {
				data, _ := json.MarshalIndent(peers, "", "  ")
				fmt.Println(string(data))
				return nil
			}

			if len(peers) == 0 {
				fmt.Printf("Sandbox %q has no linked peers.\n", sandboxName)
				return nil
			}

			fmt.Printf("Peers of sandbox %q:\n\n", sandboxName)
			fmt.Printf("  %-15s %-16s %-16s %s\n", "PEER", "LOCAL-IP", "PEER-IP", "LINK")
			for _, p := range peers {
				fmt.Printf("  %-15s %-16s %-16s %s\n",
					p.Peer, p.LocalIP, p.PeerIP, p.LinkID)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")

	return cmd
}

func init() {
	// Suppress unused import warnings
	_ = os.Stderr
}
