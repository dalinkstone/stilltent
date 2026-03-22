package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func labelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label",
		Short: "Manage sandbox labels",
		Long: `Manage key=value labels on sandboxes for organization and filtering.

Examples:
  tent label set mybox project=api env=staging
  tent label get mybox
  tent label rm mybox env
  tent label list --filter project=api`,
	}

	cmd.AddCommand(labelSetCmd())
	cmd.AddCommand(labelGetCmd())
	cmd.AddCommand(labelRmCmd())
	cmd.AddCommand(labelListCmd())

	return cmd
}

func labelSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <sandbox> <key=value> [key=value...]",
		Short: "Set labels on a sandbox",
		Long:  `Set one or more key=value labels on a sandbox. Existing labels with the same key are overwritten.`,
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			labels := make(map[string]string)
			for _, arg := range args[1:] {
				parts := strings.SplitN(arg, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid label format %q, expected key=value", arg)
				}
				key := strings.TrimSpace(parts[0])
				if key == "" {
					return fmt.Errorf("label key cannot be empty")
				}
				labels[key] = parts[1]
			}

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if err := manager.SetLabels(name, labels); err != nil {
				return fmt.Errorf("failed to set labels: %w", err)
			}

			for k, v := range labels {
				fmt.Printf("Set label %s=%s on sandbox %q\n", k, v, name)
			}
			return nil
		},
	}
}

func labelGetCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "get <sandbox>",
		Short: "Show labels for a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			labels, err := manager.GetLabels(name)
			if err != nil {
				return err
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(labels)
			}

			if len(labels) == 0 {
				fmt.Printf("No labels set on sandbox %q\n", name)
				return nil
			}

			keys := make([]string, 0, len(labels))
			for k := range labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, k := range keys {
				fmt.Printf("%s=%s\n", k, labels[k])
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

func labelRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <sandbox> <key> [key...]",
		Short: "Remove labels from a sandbox",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			keys := args[1:]
			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if err := manager.RemoveLabels(name, keys); err != nil {
				return fmt.Errorf("failed to remove labels: %w", err)
			}

			for _, k := range keys {
				fmt.Printf("Removed label %q from sandbox %q\n", k, name)
			}
			return nil
		},
	}
}

func labelListCmd() *cobra.Command {
	var filter []string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all sandboxes with their labels, optionally filtered",
		Long: `List all sandboxes showing their labels. Use --filter to match specific labels.

Examples:
  tent label list
  tent label list --filter project=api
  tent label list --filter env=staging --filter team=ml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			vms, err := manager.List()
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			// Parse filters
			filterMap := make(map[string]string)
			for _, f := range filter {
				parts := strings.SplitN(f, "=", 2)
				if len(parts) == 2 {
					filterMap[parts[0]] = parts[1]
				} else {
					// Filter by key existence only
					filterMap[parts[0]] = ""
				}
			}

			fmt.Printf("%-20s %-10s %s\n", "NAME", "STATUS", "LABELS")
			for _, v := range vms {
				// Apply filter
				if len(filterMap) > 0 {
					if !matchLabels(v.Labels, filterMap) {
						continue
					}
				}

				labelStr := formatLabels(v.Labels)
				fmt.Printf("%-20s %-10s %s\n", v.Name, v.Status, labelStr)
			}

			return nil
		},
	}

	cmd.Flags().StringSliceVar(&filter, "filter", nil, "Filter by label (key=value or key)")
	return cmd
}

// matchLabels checks if a sandbox's labels match all filter criteria.
func matchLabels(labels map[string]string, filters map[string]string) bool {
	for k, v := range filters {
		labelVal, ok := labels[k]
		if !ok {
			return false
		}
		if v != "" && labelVal != v {
			return false
		}
	}
	return true
}

// formatLabels formats labels as a comma-separated key=value string.
func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + labels[k]
	}
	return strings.Join(parts, ", ")
}
