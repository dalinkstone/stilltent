package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func webhookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhook",
		Short: "Manage webhook subscriptions for sandbox lifecycle events",
		Long: `Configure HTTP webhook endpoints that receive sandbox lifecycle events
(create, start, stop, destroy, etc.) as JSON POST requests.

Webhooks support HMAC-SHA256 signature verification, event/sandbox filtering,
and delivery logging.

Examples:
  tent webhook add ci-notify --url https://ci.example.com/hook --events start,stop
  tent webhook add all-events --url https://monitor.local:9090/events --events '*'
  tent webhook add scoped --url https://example.com/hook --events start --sandboxes mybox,dev
  tent webhook list
  tent webhook show ci-notify
  tent webhook test ci-notify
  tent webhook deliveries ci-notify
  tent webhook enable ci-notify
  tent webhook disable ci-notify
  tent webhook remove ci-notify`,
	}

	cmd.AddCommand(webhookAddCmd())
	cmd.AddCommand(webhookRemoveCmd())
	cmd.AddCommand(webhookListCmd())
	cmd.AddCommand(webhookShowCmd())
	cmd.AddCommand(webhookTestCmd())
	cmd.AddCommand(webhookDeliveriesCmd())
	cmd.AddCommand(webhookEnableCmd())
	cmd.AddCommand(webhookDisableCmd())

	return cmd
}

func webhookAddCmd() *cobra.Command {
	var (
		url       string
		secret    string
		events    string
		sandboxes string
		inactive  bool
	)

	cmd := &cobra.Command{
		Use:   "add <id>",
		Short: "Add a new webhook subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			if url == "" {
				return fmt.Errorf("--url is required")
			}

			eventList := parseCSV(events)
			if len(eventList) == 0 {
				eventList = []string{"*"}
			}

			sandboxList := parseCSV(sandboxes)

			baseDir := getBaseDir()
			mgr := vm.NewWebhookManager(baseDir)

			cfg := vm.WebhookConfig{
				ID:        id,
				URL:       url,
				Secret:    secret,
				Events:    eventList,
				Sandboxes: sandboxList,
				Active:    !inactive,
			}

			if err := mgr.Add(cfg); err != nil {
				return err
			}

			fmt.Printf("Webhook %q added (url=%s, events=%s)\n", id, url, strings.Join(eventList, ","))
			return nil
		},
	}

	cmd.Flags().StringVar(&url, "url", "", "Webhook endpoint URL (required)")
	cmd.Flags().StringVar(&secret, "secret", "", "HMAC-SHA256 secret for signature verification")
	cmd.Flags().StringVar(&events, "events", "*", "Comma-separated event types to subscribe to")
	cmd.Flags().StringVar(&sandboxes, "sandboxes", "", "Comma-separated sandbox names to filter (empty = all)")
	cmd.Flags().BoolVar(&inactive, "inactive", false, "Create the webhook in disabled state")

	return cmd
}

func webhookRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove a webhook subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewWebhookManager(baseDir)
			if err := mgr.Remove(args[0]); err != nil {
				return err
			}
			fmt.Printf("Webhook %q removed\n", args[0])
			return nil
		},
	}
}

func webhookListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all webhook subscriptions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewWebhookManager(baseDir)

			hooks, err := mgr.List()
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(hooks)
			}

			if len(hooks) == 0 {
				fmt.Println("No webhooks configured")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tURL\tEVENTS\tACTIVE\tCREATED")
			for _, h := range hooks {
				evts := strings.Join(h.Events, ",")
				if len(evts) > 30 {
					evts = evts[:27] + "..."
				}
				status := "yes"
				if !h.Active {
					status = "no"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					h.ID, h.URL, evts, status,
					h.CreatedAt.Format(time.RFC3339))
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func webhookShowCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show details of a webhook subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewWebhookManager(baseDir)

			hook, err := mgr.Get(args[0])
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(hook)
			}

			fmt.Printf("ID:        %s\n", hook.ID)
			fmt.Printf("URL:       %s\n", hook.URL)
			fmt.Printf("Active:    %v\n", hook.Active)
			fmt.Printf("Events:    %s\n", strings.Join(hook.Events, ", "))
			if len(hook.Sandboxes) > 0 {
				fmt.Printf("Sandboxes: %s\n", strings.Join(hook.Sandboxes, ", "))
			} else {
				fmt.Printf("Sandboxes: (all)\n")
			}
			if hook.Secret != "" {
				fmt.Printf("Secret:    (configured)\n")
			}
			fmt.Printf("Created:   %s\n", hook.CreatedAt.Format(time.RFC3339))

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func webhookTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <id>",
		Short: "Send a test event to a webhook endpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewWebhookManager(baseDir)

			delivery, err := mgr.Test(args[0])
			if err != nil {
				return err
			}

			if delivery.Error != "" {
				fmt.Printf("Test delivery FAILED: %s (took %s)\n", delivery.Error, delivery.Duration)
				return fmt.Errorf("webhook test failed")
			}

			fmt.Printf("Test delivery OK: HTTP %d (took %s)\n", delivery.StatusCode, delivery.Duration)
			return nil
		},
	}
}

func webhookDeliveriesCmd() *cobra.Command {
	var (
		limit    int
		jsonOut  bool
	)

	cmd := &cobra.Command{
		Use:   "deliveries [id]",
		Short: "Show recent webhook delivery attempts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewWebhookManager(baseDir)

			webhookID := ""
			if len(args) == 1 {
				webhookID = args[0]
			}

			deliveries, err := mgr.DeliveryLog(webhookID, limit)
			if err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(deliveries)
			}

			if len(deliveries) == 0 {
				fmt.Println("No deliveries found")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "WEBHOOK\tEVENT\tSANDBOX\tSTATUS\tDURATION\tTIME")
			for _, d := range deliveries {
				status := fmt.Sprintf("%d", d.StatusCode)
				if d.Error != "" {
					status = "ERR"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					d.WebhookID, d.Event, d.Sandbox, status,
					d.Duration, d.Timestamp.Format(time.RFC3339))
			}
			return w.Flush()
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of deliveries to show")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func webhookEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <id>",
		Short: "Enable a webhook subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewWebhookManager(baseDir)
			if err := mgr.SetActive(args[0], true); err != nil {
				return err
			}
			fmt.Printf("Webhook %q enabled\n", args[0])
			return nil
		},
	}
}

func webhookDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a webhook subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewWebhookManager(baseDir)
			if err := mgr.SetActive(args[0], false); err != nil {
				return err
			}
			fmt.Printf("Webhook %q disabled\n", args[0])
			return nil
		},
	}
}

func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
