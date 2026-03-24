package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"os"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/network"
)

func networkProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Configure HTTP/HTTPS proxy settings for sandboxes",
		Long: `Manage proxy settings that control how sandboxes route HTTP/HTTPS traffic.
Proxy settings are persisted per-sandbox and can inject environment variables
(http_proxy, https_proxy, no_proxy) into the guest.

Examples:
  tent network proxy set mybox --http http://proxy:8080
  tent network proxy set mybox --http http://proxy:8080 --https http://proxy:8443
  tent network proxy set mybox --http http://proxy:8080 --no-proxy localhost,10.0.0.0/8
  tent network proxy show mybox
  tent network proxy env mybox
  tent network proxy remove mybox`,
	}

	cmd.AddCommand(networkProxySetCmd())
	cmd.AddCommand(networkProxyShowCmd())
	cmd.AddCommand(networkProxyRemoveCmd())
	cmd.AddCommand(networkProxyEnvCmd())
	cmd.AddCommand(networkProxyListCmd())

	return cmd
}

func networkProxySetCmd() *cobra.Command {
	var (
		httpProxy  string
		httpsProxy string
		noProxy    string
	)

	cmd := &cobra.Command{
		Use:   "set <sandbox>",
		Short: "Set proxy configuration for a sandbox",
		Long: `Configure HTTP and HTTPS proxy settings for a sandbox. The proxy settings
are stored in the sandbox's network policy and can be used to inject proxy
environment variables into the guest.

If --https is not specified, it defaults to the same value as --http.

Examples:
  tent network proxy set mybox --http http://proxy.corp:8080
  tent network proxy set mybox --http http://proxy:8080 --https http://proxy:8443
  tent network proxy set mybox --http http://proxy:8080 --no-proxy localhost,10.0.0.0/8,.internal`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if httpProxy == "" && httpsProxy == "" {
				return fmt.Errorf("at least --http or --https must be specified")
			}

			// Default HTTPS proxy to HTTP proxy if not set
			if httpsProxy == "" && httpProxy != "" {
				httpsProxy = httpProxy
			}

			var noProxyList []string
			if noProxy != "" {
				for _, np := range strings.Split(noProxy, ",") {
					np = strings.TrimSpace(np)
					if np != "" {
						noProxyList = append(noProxyList, np)
					}
				}
			}

			baseDir := getBaseDir()
			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			proxy := &network.ProxySettings{
				HTTPProxy:  httpProxy,
				HTTPSProxy: httpsProxy,
				NoProxy:    noProxyList,
				Enabled:    true,
			}

			if err := pm.SetProxy(name, proxy); err != nil {
				return fmt.Errorf("failed to set proxy: %w", err)
			}

			policy, err := pm.GetPolicy(name)
			if err != nil {
				return fmt.Errorf("failed to get policy: %w", err)
			}

			if err := pm.SavePolicy(policy); err != nil {
				return fmt.Errorf("failed to save policy: %w", err)
			}

			fmt.Printf("Proxy configured for sandbox '%s'\n", name)
			fmt.Printf("  HTTP:  %s\n", httpProxy)
			fmt.Printf("  HTTPS: %s\n", httpsProxy)
			if len(noProxyList) > 0 {
				fmt.Printf("  No-Proxy: %s\n", strings.Join(noProxyList, ", "))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&httpProxy, "http", "", "HTTP proxy URL (e.g., http://proxy:8080)")
	cmd.Flags().StringVar(&httpsProxy, "https", "", "HTTPS proxy URL (defaults to --http if not set)")
	cmd.Flags().StringVar(&noProxy, "no-proxy", "", "Comma-separated list of hosts/CIDRs to bypass proxy")

	return cmd
}

func networkProxyShowCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "show <sandbox>",
		Short: "Show proxy configuration for a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := getBaseDir()
			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			proxy, err := pm.GetProxy(name)
			if err != nil {
				return fmt.Errorf("failed to get proxy settings: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(proxy)
			}

			if !proxy.Enabled {
				fmt.Printf("No proxy configured for sandbox '%s'\n", name)
				return nil
			}

			fmt.Printf("Proxy settings for '%s':\n", name)
			fmt.Printf("  Status:  enabled\n")
			if proxy.HTTPProxy != "" {
				fmt.Printf("  HTTP:    %s\n", proxy.HTTPProxy)
			}
			if proxy.HTTPSProxy != "" {
				fmt.Printf("  HTTPS:   %s\n", proxy.HTTPSProxy)
			}
			if len(proxy.NoProxy) > 0 {
				fmt.Printf("  No-Proxy: %s\n", strings.Join(proxy.NoProxy, ", "))
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func networkProxyRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <sandbox>",
		Short: "Remove proxy configuration from a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := getBaseDir()
			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			if err := pm.RemoveProxy(name); err != nil {
				return fmt.Errorf("failed to remove proxy: %w", err)
			}

			policy, err := pm.GetPolicy(name)
			if err != nil {
				return fmt.Errorf("failed to get policy: %w", err)
			}

			if err := pm.SavePolicy(policy); err != nil {
				return fmt.Errorf("failed to save policy: %w", err)
			}

			fmt.Printf("Proxy configuration removed from sandbox '%s'\n", name)
			return nil
		},
	}
}

func networkProxyEnvCmd() *cobra.Command {
	var shell string

	cmd := &cobra.Command{
		Use:   "env <sandbox>",
		Short: "Print proxy environment variables for a sandbox",
		Long: `Output the proxy environment variables that would be injected into the sandbox.
Useful for debugging or manually exporting into a shell session.

Examples:
  tent network proxy env mybox
  tent network proxy env mybox --shell bash
  eval $(tent network proxy env mybox --shell bash)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := getBaseDir()
			pm, err := network.NewPolicyManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create policy manager: %w", err)
			}

			proxy, err := pm.GetProxy(name)
			if err != nil {
				return fmt.Errorf("failed to get proxy settings: %w", err)
			}

			if !proxy.Enabled {
				fmt.Printf("# No proxy configured for sandbox '%s'\n", name)
				return nil
			}

			envVars := proxy.ProxyEnvVars()

			switch shell {
			case "bash", "sh", "zsh":
				for k, v := range envVars {
					fmt.Printf("export %s=%q\n", k, v)
				}
			case "fish":
				for k, v := range envVars {
					fmt.Printf("set -gx %s %q\n", k, v)
				}
			case "powershell", "ps":
				for k, v := range envVars {
					fmt.Printf("$env:%s = \"%s\"\n", k, v)
				}
			default:
				for k, v := range envVars {
					fmt.Printf("%s=%s\n", k, v)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&shell, "shell", "", "Shell format for output (bash, fish, powershell)")

	return cmd
}

func networkProxyListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all sandboxes with proxy configurations",
		Aliases: []string{"ls"},
		Args:  cobra.NoArgs,
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

			type proxyEntry struct {
				Sandbox    string                 `json:"sandbox"`
				Proxy      *network.ProxySettings `json:"proxy"`
			}

			var entries []proxyEntry
			for _, p := range policies {
				if p.Proxy != nil && p.Proxy.Enabled {
					entries = append(entries, proxyEntry{
						Sandbox: p.Name,
						Proxy:   p.Proxy,
					})
				}
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}

			if len(entries) == 0 {
				fmt.Println("No sandboxes have proxy configurations.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SANDBOX\tHTTP PROXY\tHTTPS PROXY\tNO-PROXY")
			for _, e := range entries {
				noProxy := "-"
				if len(e.Proxy.NoProxy) > 0 {
					noProxy = strings.Join(e.Proxy.NoProxy, ",")
				}
				httpP := e.Proxy.HTTPProxy
				if httpP == "" {
					httpP = "-"
				}
				httpsP := e.Proxy.HTTPSProxy
				if httpsP == "" {
					httpsP = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Sandbox, httpP, httpsP, noProxy)
			}
			w.Flush()

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}
