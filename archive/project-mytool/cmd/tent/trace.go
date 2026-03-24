package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/network"
	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func networkTraceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Trace and debug sandbox network connections",
		Long: `Trace, simulate, and inspect network connection attempts from sandboxes.

Network tracing helps debug egress firewall rules by showing which connections
would be allowed or blocked. Use 'simulate' to test endpoints against the
current policy without generating real traffic.

Examples:
  tent network trace start mybox
  tent network trace start mybox --protocol tcp --port 443
  tent network trace stop mybox
  tent network trace show mybox
  tent network trace simulate mybox api.openai.com github.com:443 1.2.3.4
  tent network trace list mybox
  tent network trace stats mybox`,
	}

	cmd.AddCommand(traceStartCmd())
	cmd.AddCommand(traceStopCmd())
	cmd.AddCommand(traceShowCmd())
	cmd.AddCommand(traceSimulateCmd())
	cmd.AddCommand(traceListCmd())
	cmd.AddCommand(traceStatsCmd())
	cmd.AddCommand(traceDeleteCmd())

	return cmd
}

func traceStartCmd() *cobra.Command {
	var (
		protocol string
		port     int
		dstIP    string
		action   string
	)

	cmd := &cobra.Command{
		Use:   "start <name>",
		Short: "Start tracing network connections for a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return err
			}

			// Verify sandbox exists
			if _, err := manager.Status(name); err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}

			firewall := network.NewEgressFirewall()
			tm, err := network.NewTraceManager(baseDir, firewall)
			if err != nil {
				return fmt.Errorf("failed to create trace manager: %w", err)
			}

			var filter *network.TraceFilter
			if protocol != "" || port != 0 || dstIP != "" || action != "" {
				filter = &network.TraceFilter{
					Protocol: protocol,
					Port:     port,
					DstIP:    dstIP,
					Action:   action,
				}
			}

			session, err := tm.StartTrace(name, filter)
			if err != nil {
				return err
			}

			fmt.Printf("Trace started for sandbox %q (session: %s)\n", name, session.ID)
			if filter != nil {
				fmt.Printf("Filter:")
				if protocol != "" {
					fmt.Printf(" protocol=%s", protocol)
				}
				if port != 0 {
					fmt.Printf(" port=%d", port)
				}
				if dstIP != "" {
					fmt.Printf(" dst=%s", dstIP)
				}
				if action != "" {
					fmt.Printf(" action=%s", action)
				}
				fmt.Println()
			}
			fmt.Println("Use 'tent network trace stop' to end the trace and save results.")
			return nil
		},
	}

	cmd.Flags().StringVar(&protocol, "protocol", "", "Filter by protocol (tcp, udp, icmp)")
	cmd.Flags().IntVar(&port, "port", 0, "Filter by destination port")
	cmd.Flags().StringVar(&dstIP, "dst", "", "Filter by destination IP")
	cmd.Flags().StringVar(&action, "action", "", "Filter by action (allowed, blocked)")

	return cmd
}

func traceStopCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop tracing and save results",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			firewall := network.NewEgressFirewall()
			tm, err := network.NewTraceManager(baseDir, firewall)
			if err != nil {
				return fmt.Errorf("failed to create trace manager: %w", err)
			}

			session, err := tm.StopTrace(name)
			if err != nil {
				return err
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(session)
			}

			fmt.Printf("Trace stopped for sandbox %q\n", name)
			fmt.Printf("Session:  %s\n", session.ID)
			fmt.Printf("Duration: %s\n", session.StoppedAt.Sub(session.StartedAt).Round(time.Millisecond))
			fmt.Printf("Events:   %d\n", len(session.Events))
			fmt.Printf("Saved to: ~/.tent/network-traces/%s.yaml\n", session.ID)
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func traceShowCmd() *cobra.Command {
	var (
		outputJSON bool
		last       int
	)

	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show trace events for a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			firewall := network.NewEgressFirewall()
			tm, err := network.NewTraceManager(baseDir, firewall)
			if err != nil {
				return fmt.Errorf("failed to create trace manager: %w", err)
			}

			events, err := tm.GetTraceEvents(name)
			if err != nil {
				return err
			}

			if last > 0 && len(events) > last {
				events = events[len(events)-last:]
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(events)
			}

			if len(events) == 0 {
				fmt.Println("No trace events captured.")
				return nil
			}

			printTraceEvents(events)
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	cmd.Flags().IntVar(&last, "last", 0, "Show only the last N events")
	return cmd
}

func traceSimulateCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "simulate <name> <endpoint> [endpoint...]",
		Short: "Simulate connections to test firewall rules",
		Long: `Test whether endpoints would be allowed or blocked by the sandbox's egress policy.

This evaluates endpoints against the current firewall rules without generating
real traffic. Useful for verifying policy before deploying.

Endpoint formats:
  host                  e.g., api.openai.com
  host:port             e.g., github.com:443
  ip                    e.g., 1.2.3.4
  ip:port               e.g., 1.2.3.4:80
  cidr                  e.g., 10.0.0.0/8
  proto:host:port       e.g., tcp:api.anthropic.com:443`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			endpoints := args[1:]
			baseDir := getBaseDir()

			// Load the sandbox's policy into the firewall
			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			firewall := network.NewEgressFirewall()
			if err := firewall.Initialize(); err != nil {
				// Non-fatal — firewall may not be enabled
				_ = err
			}

			// Load and apply the sandbox's policy
			policy, err := pm.GetPolicy(name)
			if err != nil {
				// No policy = everything blocked
				policy = &network.Policy{
					Name:    name,
					Allowed: []string{},
					Denied:  []string{},
				}
			}
			_ = firewall.ApplyPolicy(name, policy)

			tm, err := network.NewTraceManager(baseDir, firewall)
			if err != nil {
				return fmt.Errorf("failed to create trace manager: %w", err)
			}

			events, err := tm.SimulateTrace(name, endpoints)
			if err != nil {
				return err
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(events)
			}

			printTraceEvents(events)

			// Summary
			allowed := 0
			blocked := 0
			for _, ev := range events {
				if ev.Action == "allowed" {
					allowed++
				} else {
					blocked++
				}
			}
			fmt.Printf("\nSummary: %d allowed, %d blocked out of %d endpoints\n", allowed, blocked, len(events))
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func traceListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list <name>",
		Short: "List saved trace sessions for a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			firewall := network.NewEgressFirewall()
			tm, err := network.NewTraceManager(baseDir, firewall)
			if err != nil {
				return fmt.Errorf("failed to create trace manager: %w", err)
			}

			traces, err := tm.ListTraces(name)
			if err != nil {
				return err
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(traces)
			}

			if len(traces) == 0 {
				fmt.Printf("No saved traces for sandbox %q.\n", name)
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTARTED\tDURATION\tEVENTS")
			for _, t := range traces {
				duration := "active"
				if t.StoppedAt != nil {
					duration = t.StoppedAt.Sub(t.StartedAt).Round(time.Millisecond).String()
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\n",
					t.ID,
					t.StartedAt.Format("2006-01-02 15:04:05"),
					duration,
					t.EventCount,
				)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func traceStatsCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "stats <name>",
		Short: "Show statistics for an active trace session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			firewall := network.NewEgressFirewall()
			tm, err := network.NewTraceManager(baseDir, firewall)
			if err != nil {
				return fmt.Errorf("failed to create trace manager: %w", err)
			}

			session := tm.GetActiveTrace(name)
			if session == nil {
				return fmt.Errorf("no active trace for sandbox %q", name)
			}

			stats := session.TraceStats()

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(stats)
			}

			fmt.Printf("Trace Statistics for %q\n", name)
			fmt.Printf("  Session:      %s\n", session.ID)
			fmt.Printf("  Running for:  %s\n", time.Since(session.StartedAt).Round(time.Second))
			fmt.Println()
			fmt.Printf("  Total events:        %d\n", stats.TotalEvents)
			fmt.Printf("  Allowed:             %d\n", stats.AllowedCount)
			fmt.Printf("  Blocked:             %d\n", stats.BlockedCount)
			fmt.Printf("  Unique destinations: %d\n", stats.UniqueDestCount)
			fmt.Println()
			fmt.Printf("  TCP: %d  UDP: %d  ICMP: %d\n", stats.TCPCount, stats.UDPCount, stats.ICMPCount)
			if stats.TotalBytesSent > 0 || stats.TotalBytesRecv > 0 {
				fmt.Printf("  Bytes sent: %d  recv: %d\n", stats.TotalBytesSent, stats.TotalBytesRecv)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

func traceDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <trace-id>",
		Short: "Delete a saved trace session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			traceID := args[0]
			baseDir := getBaseDir()

			firewall := network.NewEgressFirewall()
			tm, err := network.NewTraceManager(baseDir, firewall)
			if err != nil {
				return fmt.Errorf("failed to create trace manager: %w", err)
			}

			if err := tm.DeleteTrace(traceID); err != nil {
				return err
			}

			fmt.Printf("Trace %q deleted.\n", traceID)
			return nil
		},
	}
}

func printTraceEvents(events []*network.TraceEvent) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tPROTO\tDESTINATION\tPORT\tACTION\tMATCHED RULE")
	for _, ev := range events {
		dst := ev.DstIP
		if ev.DstHostname != "" {
			dst = ev.DstHostname
		}
		actionStr := ev.Action
		if ev.Action == "allowed" {
			actionStr = "ALLOW"
		} else if ev.Action == "blocked" {
			actionStr = "BLOCK"
		}
		matchedRule := ev.MatchedRule
		if matchedRule == "" {
			matchedRule = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
			ev.Timestamp.Format("15:04:05.000"),
			ev.Protocol,
			dst,
			ev.DstPort,
			actionStr,
			matchedRule,
		)
	}
	w.Flush()
}
