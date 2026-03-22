package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func ttlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ttl",
		Short: "Manage sandbox time-to-live expiry policies",
		Long: `Set, view, and manage automatic expiry policies for sandboxes.

TTL (time-to-live) lets you set a duration after which a sandbox is automatically
stopped or destroyed. Useful for temporary AI workloads, development environments,
or CI sandboxes that should be cleaned up after a set period.

Examples:
  tent ttl set mybox 2h              # destroy after 2 hours
  tent ttl set mybox 30m --action stop  # stop after 30 minutes
  tent ttl set mybox 7d              # destroy after 7 days
  tent ttl get mybox                 # show TTL for a sandbox
  tent ttl rm mybox                  # remove TTL
  tent ttl list                      # list all TTL policies
  tent ttl enforce                   # enforce expired TTLs now`,
	}

	cmd.AddCommand(ttlSetCmd())
	cmd.AddCommand(ttlGetCmd())
	cmd.AddCommand(ttlRmCmd())
	cmd.AddCommand(ttlListCmd())
	cmd.AddCommand(ttlEnforceCmd())

	return cmd
}

func ttlSetCmd() *cobra.Command {
	var action string

	cmd := &cobra.Command{
		Use:   "set <sandbox> <duration>",
		Short: "Set a TTL on a sandbox",
		Long: `Set a time-to-live on a sandbox. After the duration expires, the configured
action (stop or destroy) is performed.

Duration format: Go duration (30m, 2h, 24h) or days (1d, 7d, 30d).

Examples:
  tent ttl set mybox 2h
  tent ttl set mybox 30m --action stop
  tent ttl set mybox 7d --action destroy`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ttlStr := args[1]
			baseDir := getBaseDir()

			// Verify sandbox exists
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if _, err := manager.LoadConfig(name); err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}

			ttlMgr := vm.NewTTLManager(baseDir)
			entry, err := ttlMgr.Set(name, ttlStr, action)
			if err != nil {
				return err
			}

			fmt.Printf("TTL set on %q: expires at %s (action: %s)\n",
				name,
				entry.ExpiresAt.Local().Format("2006-01-02 15:04:05"),
				entry.Action,
			)
			remaining := time.Until(entry.ExpiresAt)
			fmt.Printf("Time remaining: %s\n", formatTTLDuration(remaining))

			return nil
		},
	}

	cmd.Flags().StringVar(&action, "action", "destroy", "Action on expiry: stop or destroy")

	return cmd
}

func ttlGetCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "get <sandbox>",
		Short: "Show TTL for a sandbox",
		Long: `Display the TTL policy and remaining time for a sandbox.

Examples:
  tent ttl get mybox
  tent ttl get mybox --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			ttlMgr := vm.NewTTLManager(baseDir)
			entry, err := ttlMgr.Get(name)
			if err != nil {
				return err
			}
			if entry == nil {
				fmt.Printf("No TTL set for sandbox %q\n", name)
				return nil
			}

			if jsonOut {
				remaining := time.Until(entry.ExpiresAt)
				if remaining < 0 {
					remaining = 0
				}
				data := map[string]interface{}{
					"sandbox":          entry.Sandbox,
					"ttl":              entry.TTL,
					"action":           entry.Action,
					"set_at":           entry.SetAt,
					"expires_at":       entry.ExpiresAt,
					"expired":          time.Now().UTC().After(entry.ExpiresAt),
					"remaining_seconds": int(remaining.Seconds()),
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(data)
			}

			remaining := time.Until(entry.ExpiresAt)
			expired := remaining <= 0
			if expired {
				remaining = 0
			}

			fmt.Printf("Sandbox:    %s\n", entry.Sandbox)
			fmt.Printf("TTL:        %s\n", entry.TTL)
			fmt.Printf("Action:     %s\n", entry.Action)
			fmt.Printf("Set at:     %s\n", entry.SetAt.Local().Format("2006-01-02 15:04:05"))
			fmt.Printf("Expires at: %s\n", entry.ExpiresAt.Local().Format("2006-01-02 15:04:05"))
			if expired {
				fmt.Printf("Status:     EXPIRED\n")
			} else {
				fmt.Printf("Remaining:  %s\n", formatTTLDuration(remaining))
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")

	return cmd
}

func ttlRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <sandbox>",
		Short: "Remove TTL from a sandbox",
		Long: `Remove the TTL policy from a sandbox, preventing automatic expiry.

Examples:
  tent ttl rm mybox`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			ttlMgr := vm.NewTTLManager(baseDir)
			if err := ttlMgr.Remove(name); err != nil {
				return err
			}

			fmt.Printf("TTL removed from sandbox %q\n", name)
			return nil
		},
	}
}

func ttlListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all sandbox TTL policies",
		Long: `Display all active TTL policies across sandboxes.

Examples:
  tent ttl list
  tent ttl list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			ttlMgr := vm.NewTTLManager(baseDir)
			entries, err := ttlMgr.List()
			if err != nil {
				return err
			}

			if len(entries) == 0 {
				fmt.Println("No TTL policies set.")
				return nil
			}

			// Sort by expiry time
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].ExpiresAt.Before(entries[j].ExpiresAt)
			})

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "SANDBOX\tTTL\tACTION\tEXPIRES AT\tREMAINING\tSTATUS\n")
			now := time.Now().UTC()
			for _, e := range entries {
				remaining := e.ExpiresAt.Sub(now)
				status := "active"
				remainStr := formatTTLDuration(remaining)
				if remaining <= 0 {
					status = "EXPIRED"
					remainStr = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					e.Sandbox,
					e.TTL,
					e.Action,
					e.ExpiresAt.Local().Format("2006-01-02 15:04:05"),
					remainStr,
					status,
				)
			}
			w.Flush()

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")

	return cmd
}

func ttlEnforceCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "enforce",
		Short: "Enforce expired TTL policies",
		Long: `Check all TTL policies and perform the configured action on expired sandboxes.

This can be run manually or scheduled via cron to enforce TTL policies.

Examples:
  tent ttl enforce
  tent ttl enforce --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			ttlMgr := vm.NewTTLManager(baseDir)

			if dryRun {
				expired, err := ttlMgr.Expired()
				if err != nil {
					return err
				}
				if len(expired) == 0 {
					fmt.Println("No expired TTL policies.")
					return nil
				}
				fmt.Printf("Would act on %d expired sandbox(es):\n", len(expired))
				for _, e := range expired {
					fmt.Printf("  %s: %s (expired %s ago)\n",
						e.Sandbox,
						e.Action,
						formatTTLDuration(time.Since(e.ExpiresAt)),
					)
				}
				return nil
			}

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			acted, err := ttlMgr.Enforce(manager)
			if err != nil {
				return err
			}

			if len(acted) == 0 {
				fmt.Println("No expired TTL policies to enforce.")
			} else {
				fmt.Printf("Enforced TTL on %d sandbox(es):\n", len(acted))
				for _, name := range acted {
					fmt.Printf("  %s\n", name)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without acting")

	return cmd
}

// formatTTLDuration returns a human-readable duration string.
func formatTTLDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		if hours > 0 {
			return fmt.Sprintf("%dd%dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	}
	if hours > 0 {
		if minutes > 0 {
			return fmt.Sprintf("%dh%dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
