package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

// SandboxGroup represents a named collection of sandboxes.
type SandboxGroup struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Sandboxes   []string  `json:"sandboxes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func groupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "group",
		Short: "Manage named groups of sandboxes",
		Long: `Create and manage ad-hoc groups of sandboxes for batch operations.

Groups let you organize sandboxes into named collections and perform
lifecycle operations (start, stop, destroy) on all members at once.

Unlike compose, groups are ad-hoc — you manually add/remove sandboxes
rather than defining infrastructure in YAML.

Examples:
  tent group create dev-cluster --description "Development sandbox pool"
  tent group add dev-cluster sandbox1 sandbox2 sandbox3
  tent group start dev-cluster
  tent group stop dev-cluster
  tent group list`,
	}

	cmd.AddCommand(groupCreateCmd())
	cmd.AddCommand(groupDeleteCmd())
	cmd.AddCommand(groupListCmd())
	cmd.AddCommand(groupShowCmd())
	cmd.AddCommand(groupAddCmd())
	cmd.AddCommand(groupRemoveCmd())
	cmd.AddCommand(groupStartCmd())
	cmd.AddCommand(groupStopCmd())
	cmd.AddCommand(groupDestroyCmd())
	cmd.AddCommand(groupStatusCmd())

	return cmd
}

func groupsDir() string {
	baseDir := getBaseDir()
	return filepath.Join(baseDir, "groups")
}

func loadGroup(name string) (*SandboxGroup, error) {
	path := filepath.Join(groupsDir(), name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("group %q not found", name)
		}
		return nil, fmt.Errorf("failed to read group: %w", err)
	}

	var g SandboxGroup
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("failed to parse group: %w", err)
	}
	return &g, nil
}

func saveGroup(g *SandboxGroup) error {
	dir := groupsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create groups directory: %w", err)
	}

	g.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal group: %w", err)
	}

	path := filepath.Join(dir, g.Name+".json")
	return os.WriteFile(path, data, 0644)
}

func listAllGroups() ([]*SandboxGroup, error) {
	dir := groupsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var groups []*SandboxGroup
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		g, err := loadGroup(name)
		if err != nil {
			continue
		}
		groups = append(groups, g)
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Name < groups[j].Name
	})
	return groups, nil
}

func groupCreateCmd() *cobra.Command {
	var description string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new sandbox group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Check if group already exists
			if _, err := loadGroup(name); err == nil {
				return fmt.Errorf("group %q already exists", name)
			}

			g := &SandboxGroup{
				Name:        name,
				Description: description,
				Sandboxes:   []string{},
				CreatedAt:   time.Now(),
			}

			if err := saveGroup(g); err != nil {
				return err
			}

			fmt.Printf("Created group %q\n", name)
			return nil
		},
	}

	cmd.Flags().StringVarP(&description, "description", "d", "", "Group description")
	return cmd
}

func groupDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <name>",
		Short:   "Delete a sandbox group (does not affect sandboxes)",
		Aliases: []string{"rm"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			path := filepath.Join(groupsDir(), name+".json")
			if err := os.Remove(path); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("group %q not found", name)
				}
				return fmt.Errorf("failed to delete group: %w", err)
			}

			fmt.Printf("Deleted group %q\n", name)
			return nil
		},
	}
}

func groupListCmd() *cobra.Command {
	var quiet bool

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List all sandbox groups",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			groups, err := listAllGroups()
			if err != nil {
				return err
			}

			if len(groups) == 0 {
				fmt.Println("No groups defined.")
				return nil
			}

			if quiet {
				for _, g := range groups {
					fmt.Println(g.Name)
				}
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSANDBOXES\tDESCRIPTION\tCREATED")
			for _, g := range groups {
				created := g.CreatedAt.Local().Format("2006-01-02 15:04")
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", g.Name, len(g.Sandboxes), g.Description, created)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only show group names")
	return cmd
}

func groupShowCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show details and members of a group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := loadGroup(args[0])
			if err != nil {
				return err
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(g)
			}

			fmt.Printf("Group: %s\n", g.Name)
			if g.Description != "" {
				fmt.Printf("Description: %s\n", g.Description)
			}
			fmt.Printf("Created: %s\n", g.CreatedAt.Local().Format("2006-01-02 15:04:05"))
			fmt.Printf("Updated: %s\n", g.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
			fmt.Printf("Members (%d):\n", len(g.Sandboxes))

			if len(g.Sandboxes) == 0 {
				fmt.Println("  (none)")
				return nil
			}

			for _, s := range g.Sandboxes {
				fmt.Printf("  - %s\n", s)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

func groupAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <group> <sandbox>...",
		Short: "Add sandboxes to a group",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			groupName := args[0]
			sandboxNames := args[1:]

			g, err := loadGroup(groupName)
			if err != nil {
				return err
			}

			// Build existing set
			existing := make(map[string]bool, len(g.Sandboxes))
			for _, s := range g.Sandboxes {
				existing[s] = true
			}

			added := 0
			for _, name := range sandboxNames {
				if existing[name] {
					fmt.Printf("  %s already in group\n", name)
					continue
				}
				g.Sandboxes = append(g.Sandboxes, name)
				existing[name] = true
				added++
				fmt.Printf("  + %s\n", name)
			}

			if added == 0 {
				fmt.Println("No new sandboxes added.")
				return nil
			}

			sort.Strings(g.Sandboxes)
			if err := saveGroup(g); err != nil {
				return err
			}

			fmt.Printf("Added %d sandbox(es) to group %q (%d total)\n", added, groupName, len(g.Sandboxes))
			return nil
		},
	}
}

func groupRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <group> <sandbox>...",
		Short: "Remove sandboxes from a group",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			groupName := args[0]
			sandboxNames := args[1:]

			g, err := loadGroup(groupName)
			if err != nil {
				return err
			}

			toRemove := make(map[string]bool, len(sandboxNames))
			for _, s := range sandboxNames {
				toRemove[s] = true
			}

			var remaining []string
			removed := 0
			for _, s := range g.Sandboxes {
				if toRemove[s] {
					removed++
					fmt.Printf("  - %s\n", s)
				} else {
					remaining = append(remaining, s)
				}
			}

			if removed == 0 {
				fmt.Println("No matching sandboxes found in group.")
				return nil
			}

			g.Sandboxes = remaining
			if err := saveGroup(g); err != nil {
				return err
			}

			fmt.Printf("Removed %d sandbox(es) from group %q (%d remaining)\n", removed, groupName, len(g.Sandboxes))
			return nil
		},
	}
}

func groupStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <group>",
		Short: "Start all sandboxes in a group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := loadGroup(args[0])
			if err != nil {
				return err
			}

			if len(g.Sandboxes) == 0 {
				fmt.Printf("Group %q has no sandboxes.\n", g.Name)
				return nil
			}

			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return err
			}

			var errs []string
			started := 0
			for _, name := range g.Sandboxes {
				st, err := manager.Status(name)
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s: not found", name))
					continue
				}
				if st.Status == models.VMStatusRunning {
					fmt.Printf("  %s: already running\n", name)
					continue
				}
				if err := manager.Start(name); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", name, err))
					continue
				}
				started++
				fmt.Printf("  %s: started\n", name)
			}

			fmt.Printf("Started %d/%d sandboxes in group %q\n", started, len(g.Sandboxes), g.Name)
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "  error: %s\n", e)
			}
			return nil
		},
	}
}

func groupStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <group>",
		Short: "Stop all sandboxes in a group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := loadGroup(args[0])
			if err != nil {
				return err
			}

			if len(g.Sandboxes) == 0 {
				fmt.Printf("Group %q has no sandboxes.\n", g.Name)
				return nil
			}

			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return err
			}

			var errs []string
			stopped := 0
			for _, name := range g.Sandboxes {
				st, err := manager.Status(name)
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s: not found", name))
					continue
				}
				if st.Status == models.VMStatusStopped {
					fmt.Printf("  %s: already stopped\n", name)
					continue
				}
				if err := manager.Stop(name); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", name, err))
					continue
				}
				stopped++
				fmt.Printf("  %s: stopped\n", name)
			}

			fmt.Printf("Stopped %d/%d sandboxes in group %q\n", stopped, len(g.Sandboxes), g.Name)
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "  error: %s\n", e)
			}
			return nil
		},
	}
}

func groupDestroyCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "destroy <group>",
		Short: "Destroy all sandboxes in a group and delete the group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := loadGroup(args[0])
			if err != nil {
				return err
			}

			if len(g.Sandboxes) == 0 {
				// Just remove the empty group
				path := filepath.Join(groupsDir(), g.Name+".json")
				os.Remove(path)
				fmt.Printf("Deleted empty group %q\n", g.Name)
				return nil
			}

			if !force {
				return fmt.Errorf("refusing to destroy %d sandboxes in group %q without --force", len(g.Sandboxes), g.Name)
			}

			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return err
			}

			var errs []string
			destroyed := 0
			for _, name := range g.Sandboxes {
				if err := manager.Destroy(name); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", name, err))
					continue
				}
				destroyed++
				fmt.Printf("  %s: destroyed\n", name)
			}

			// Delete the group file
			path := filepath.Join(groupsDir(), g.Name+".json")
			os.Remove(path)

			fmt.Printf("Destroyed %d/%d sandboxes and deleted group %q\n", destroyed, len(g.Sandboxes), g.Name)
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "  error: %s\n", e)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force destruction without confirmation")
	return cmd
}

func groupStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <group>",
		Short: "Show status of all sandboxes in a group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := loadGroup(args[0])
			if err != nil {
				return err
			}

			if len(g.Sandboxes) == 0 {
				fmt.Printf("Group %q has no sandboxes.\n", g.Name)
				return nil
			}

			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return err
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "GROUP: %s\n", g.Name)
			fmt.Fprintln(w, "NAME\tSTATUS\tVCPUS\tMEMORY\tIP")

			running, stopped, missing := 0, 0, 0
			for _, name := range g.Sandboxes {
				st, err := manager.Status(name)
				if err != nil {
					fmt.Fprintf(w, "%s\tnot found\t-\t-\t-\n", name)
					missing++
					continue
				}
				status := string(st.Status)
				vcpus := fmt.Sprintf("%d", st.VCPUs)
				mem := fmt.Sprintf("%d MB", st.MemoryMB)
				ip := st.IP
				if ip == "" {
					ip = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, status, vcpus, mem, ip)

				switch st.Status {
				case models.VMStatusRunning:
					running++
				case models.VMStatusStopped:
					stopped++
				}
			}
			w.Flush()

			fmt.Printf("\nSummary: %d running, %d stopped, %d missing (total %d)\n",
				running, stopped, missing, len(g.Sandboxes))
			return nil
		},
	}
}
