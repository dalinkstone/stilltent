package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func dependCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "depend",
		Short: "Manage inter-sandbox dependencies",
		Long: `Declare and inspect dependencies between sandboxes. Dependencies enforce
start order: a sandbox will not start until all its dependencies are running.

This enables multi-service setups where, for example, an app sandbox depends
on a database sandbox being available first.

Examples:
  tent depend add app --on db
  tent depend add app --on db --on cache
  tent depend remove app --on db
  tent depend list app
  tent depend tree
  tent depend check app`,
	}

	cmd.AddCommand(dependAddCmd())
	cmd.AddCommand(dependRemoveCmd())
	cmd.AddCommand(dependListCmd())
	cmd.AddCommand(dependTreeCmd())
	cmd.AddCommand(dependCheckCmd())

	return cmd
}

func dependAddCmd() *cobra.Command {
	var deps []string

	cmd := &cobra.Command{
		Use:   "add <sandbox>",
		Short: "Add dependencies to a sandbox",
		Long: `Declare that a sandbox depends on one or more other sandboxes.
The dependent sandbox will not start unless all its dependencies are running.

Examples:
  tent depend add app --on db
  tent depend add app --on db --on cache --on redis`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if len(deps) == 0 {
				return fmt.Errorf("at least one --on flag is required")
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

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("sandbox '%s' not found: %w", name, err)
			}

			// Validate that dependency targets exist
			for _, dep := range deps {
				if dep == name {
					return fmt.Errorf("sandbox cannot depend on itself")
				}
				if _, err := manager.LoadConfig(dep); err != nil {
					return fmt.Errorf("dependency sandbox '%s' not found: %w", dep, err)
				}
			}

			// Check for circular dependencies
			allConfigs := make(map[string][]string)
			allConfigs[name] = append(config.DependsOn, deps...)
			if err := detectCycle(name, allConfigs, manager); err != nil {
				return err
			}

			// Add new dependencies (dedup)
			existing := make(map[string]bool)
			for _, d := range config.DependsOn {
				existing[d] = true
			}

			added := 0
			for _, dep := range deps {
				if !existing[dep] {
					config.DependsOn = append(config.DependsOn, dep)
					existing[dep] = true
					added++
					fmt.Printf("Added dependency: %s -> %s\n", name, dep)
				} else {
					fmt.Printf("Dependency already exists: %s -> %s\n", name, dep)
				}
			}

			if added > 0 {
				if err := manager.UpdateConfig(name, config); err != nil {
					return fmt.Errorf("failed to update config: %w", err)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringArrayVar(&deps, "on", nil, "Sandbox to depend on (can be repeated)")
	_ = cmd.MarkFlagRequired("on")

	return cmd
}

func dependRemoveCmd() *cobra.Command {
	var deps []string
	var all bool

	cmd := &cobra.Command{
		Use:   "remove <sandbox>",
		Short: "Remove dependencies from a sandbox",
		Long: `Remove one or more dependency declarations from a sandbox.

Examples:
  tent depend remove app --on db
  tent depend remove app --all`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !all && len(deps) == 0 {
				return fmt.Errorf("specify --on flags or --all")
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

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("sandbox '%s' not found: %w", name, err)
			}

			if all {
				config.DependsOn = nil
				fmt.Printf("Removed all dependencies from sandbox '%s'\n", name)
			} else {
				toRemove := make(map[string]bool)
				for _, d := range deps {
					toRemove[d] = true
				}
				var remaining []string
				removed := 0
				for _, d := range config.DependsOn {
					if toRemove[d] {
						fmt.Printf("Removed dependency: %s -> %s\n", name, d)
						removed++
					} else {
						remaining = append(remaining, d)
					}
				}
				if removed == 0 {
					fmt.Println("No matching dependencies found")
					return nil
				}
				config.DependsOn = remaining
			}

			if err := manager.UpdateConfig(name, config); err != nil {
				return fmt.Errorf("failed to update config: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringArrayVar(&deps, "on", nil, "Dependency to remove (can be repeated)")
	cmd.Flags().BoolVar(&all, "all", false, "Remove all dependencies")

	return cmd
}

func dependListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list <sandbox>",
		Short: "List dependencies of a sandbox",
		Long: `Show all sandboxes that the given sandbox depends on, along with their
current status.

Examples:
  tent depend list app
  tent depend list app --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("sandbox '%s' not found: %w", name, err)
			}

			if len(config.DependsOn) == 0 {
				if jsonOutput {
					fmt.Println("[]")
				} else {
					fmt.Printf("Sandbox '%s' has no dependencies\n", name)
				}
				return nil
			}

			type depInfo struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			}

			var infos []depInfo
			for _, dep := range config.DependsOn {
				status := "unknown"
				st, err := manager.Status(dep)
				if err == nil {
					status = string(st.Status)
				} else {
					status = "not-found"
				}
				infos = append(infos, depInfo{Name: dep, Status: status})
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(infos)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "DEPENDENCY\tSTATUS\n")
			for _, info := range infos {
				fmt.Fprintf(w, "%s\t%s\n", info.Name, info.Status)
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func dependTreeCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Display the full dependency tree of all sandboxes",
		Long: `Show the dependency graph across all sandboxes as an indented tree.
Sandboxes with no dependencies are shown as roots, and their dependents
are nested below them.

Examples:
  tent depend tree
  tent depend tree --json`,
		Args: cobra.NoArgs,
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

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			vms, err := manager.List()
			if err != nil {
				return fmt.Errorf("failed to list sandboxes: %w", err)
			}

			// Build dependency graph: sandbox -> its dependencies
			deps := make(map[string][]string)
			// Build reverse graph: sandbox -> sandboxes that depend on it
			dependents := make(map[string][]string)
			allNames := make(map[string]bool)

			for _, v := range vms {
				allNames[v.Name] = true
				cfg, err := manager.LoadConfig(v.Name)
				if err != nil {
					continue
				}
				deps[v.Name] = cfg.DependsOn
				for _, d := range cfg.DependsOn {
					dependents[d] = append(dependents[d], v.Name)
				}
			}

			if jsonOutput {
				type treeNode struct {
					Name       string   `json:"name"`
					DependsOn  []string `json:"depends_on,omitempty"`
					DependedBy []string `json:"depended_by,omitempty"`
				}
				var nodes []treeNode
				for _, v := range vms {
					nodes = append(nodes, treeNode{
						Name:       v.Name,
						DependsOn:  deps[v.Name],
						DependedBy: dependents[v.Name],
					})
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(nodes)
			}

			// Find roots: sandboxes with no dependencies
			var roots []string
			for name := range allNames {
				if len(deps[name]) == 0 {
					roots = append(roots, name)
				}
			}

			// Also show sandboxes that only appear as dependencies but have deps themselves
			if len(roots) == 0 && len(allNames) > 0 {
				fmt.Println("All sandboxes have dependencies (possible circular dependency)")
				for name := range allNames {
					fmt.Printf("  %s -> [%s]\n", name, strings.Join(deps[name], ", "))
				}
				return nil
			}

			printed := make(map[string]bool)
			for _, root := range roots {
				printDependTree(root, dependents, printed, "", true)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func printDependTree(name string, dependents map[string][]string, printed map[string]bool, prefix string, isLast bool) {
	connector := "├── "
	if isLast {
		connector = "└── "
	}
	if prefix == "" {
		fmt.Println(name)
	} else {
		fmt.Printf("%s%s%s\n", prefix, connector, name)
	}

	if printed[name] {
		return // prevent infinite loops
	}
	printed[name] = true

	children := dependents[name]
	for i, child := range children {
		newPrefix := prefix
		if prefix == "" {
			newPrefix = ""
		} else if isLast {
			newPrefix = prefix + "    "
		} else {
			newPrefix = prefix + "│   "
		}
		printDependTree(child, dependents, printed, newPrefix, i == len(children)-1)
	}
}

func dependCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check <sandbox>",
		Short: "Check if all dependencies of a sandbox are satisfied",
		Long: `Verify that all dependencies of a sandbox are currently running.
Returns exit code 0 if all dependencies are satisfied, 1 otherwise.

Examples:
  tent depend check app
  tent depend check app && tent start app`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			config, err := manager.LoadConfig(name)
			if err != nil {
				return fmt.Errorf("sandbox '%s' not found: %w", name, err)
			}

			if len(config.DependsOn) == 0 {
				fmt.Printf("Sandbox '%s' has no dependencies — ready to start\n", name)
				return nil
			}

			allSatisfied := true
			for _, dep := range config.DependsOn {
				st, err := manager.Status(dep)
				if err != nil {
					fmt.Printf("  x %s — not found\n", dep)
					allSatisfied = false
					continue
				}
				if st.Status == "running" {
					fmt.Printf("  + %s — running\n", dep)
				} else {
					fmt.Printf("  x %s — %s\n", dep, st.Status)
					allSatisfied = false
				}
			}

			if !allSatisfied {
				return fmt.Errorf("not all dependencies are satisfied for sandbox '%s'", name)
			}

			fmt.Printf("\nAll dependencies satisfied for sandbox '%s'\n", name)
			return nil
		},
	}

	return cmd
}

// detectCycle checks for circular dependencies starting from the given sandbox.
func detectCycle(start string, overrides map[string][]string, manager *vm.VMManager) error {
	visited := make(map[string]bool)
	path := make(map[string]bool)

	var visit func(name string) error
	visit = func(name string) error {
		if path[name] {
			return fmt.Errorf("circular dependency detected involving sandbox '%s'", name)
		}
		if visited[name] {
			return nil
		}
		visited[name] = true
		path[name] = true

		var deps []string
		if d, ok := overrides[name]; ok {
			deps = d
		} else {
			cfg, err := manager.LoadConfig(name)
			if err != nil {
				return nil // missing sandbox, skip
			}
			deps = cfg.DependsOn
		}

		for _, dep := range deps {
			if err := visit(dep); err != nil {
				return err
			}
		}

		path[name] = false
		return nil
	}

	return visit(start)
}
