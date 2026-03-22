package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// PluginMeta holds metadata about a discovered plugin.
type PluginMeta struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	Source      string `json:"source"` // "path", "installed"
}

func pluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage tent plugins",
		Long: `Discover, list, install, and run external tent plugins.

Plugins are executable files named "tent-<name>" found in $PATH or the
tent plugins directory (~/.tent/plugins/). When you run "tent <name>",
tent will look for a matching plugin if no built-in command matches.

Plugins receive sandbox context via environment variables:
  TENT_BASE_DIR    - Base directory for tent data
  TENT_PLUGIN_NAME - The plugin name as invoked

Examples:
  tent plugin list                    # List discovered plugins
  tent plugin install ./my-plugin     # Install a plugin
  tent plugin install ./my-plugin --name custom-name
  tent plugin info <name>             # Show plugin details
  tent plugin remove <name>           # Remove an installed plugin
  tent plugin run <name> [args...]    # Run a plugin explicitly`,
	}

	cmd.AddCommand(pluginListCmd())
	cmd.AddCommand(pluginInstallCmd())
	cmd.AddCommand(pluginInfoCmd())
	cmd.AddCommand(pluginRemoveCmd())
	cmd.AddCommand(pluginRunCmd())

	return cmd
}

func pluginDir() string {
	base := os.Getenv("TENT_BASE_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".tent")
	}
	return filepath.Join(base, "plugins")
}

// discoverPlugins finds all tent-* executables in PATH and the plugins directory.
func discoverPlugins() []PluginMeta {
	seen := make(map[string]bool)
	var plugins []PluginMeta

	// Check installed plugins directory first
	pdir := pluginDir()
	if entries, err := os.ReadDir(pdir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasPrefix(name, "tent-") {
				continue
			}
			pluginName := strings.TrimPrefix(name, "tent-")
			fullPath := filepath.Join(pdir, name)
			info, err := e.Info()
			if err != nil {
				continue
			}
			if !isExecutable(info) {
				continue
			}
			meta := PluginMeta{
				Name:   pluginName,
				Path:   fullPath,
				Source: "installed",
			}
			// Try to get description from plugin
			if desc := queryPluginDescription(fullPath); desc != "" {
				meta.Description = desc
			}
			if ver := queryPluginVersion(fullPath); ver != "" {
				meta.Version = ver
			}
			seen[pluginName] = true
			plugins = append(plugins, meta)
		}
	}

	// Scan PATH for tent-* executables
	pathEnv := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(pathEnv) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasPrefix(name, "tent-") {
				continue
			}
			pluginName := strings.TrimPrefix(name, "tent-")
			if seen[pluginName] {
				continue
			}
			fullPath := filepath.Join(dir, name)
			info, err := e.Info()
			if err != nil {
				continue
			}
			if !isExecutable(info) {
				continue
			}
			meta := PluginMeta{
				Name:   pluginName,
				Path:   fullPath,
				Source: "path",
			}
			if desc := queryPluginDescription(fullPath); desc != "" {
				meta.Description = desc
			}
			if ver := queryPluginVersion(fullPath); ver != "" {
				meta.Version = ver
			}
			seen[pluginName] = true
			plugins = append(plugins, meta)
		}
	}

	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].Name < plugins[j].Name
	})
	return plugins
}

func isExecutable(info fs.FileInfo) bool {
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(info.Name()))
		return ext == ".exe" || ext == ".bat" || ext == ".cmd"
	}
	return info.Mode()&0111 != 0
}

// queryPluginDescription runs "tent-<name> --tent-describe" to get a description.
func queryPluginDescription(path string) string {
	cmd := exec.Command(path, "--tent-describe")
	cmd.Env = append(os.Environ(), "TENT_PLUGIN_QUERY=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// queryPluginVersion runs "tent-<name> --tent-version" to get the version.
func queryPluginVersion(path string) string {
	cmd := exec.Command(path, "--tent-version")
	cmd.Env = append(os.Environ(), "TENT_PLUGIN_QUERY=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func pluginListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List discovered plugins",
		Long:  "Scan PATH and the tent plugins directory for tent-* executables.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			plugins := discoverPlugins()

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(plugins)
			}

			if len(plugins) == 0 {
				fmt.Println("No plugins found.")
				fmt.Println()
				fmt.Println("Install plugins by placing tent-<name> executables in:")
				fmt.Printf("  %s\n", pluginDir())
				fmt.Println("  or anywhere in your PATH")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tVERSION\tSOURCE\tDESCRIPTION")
			for _, p := range plugins {
				ver := p.Version
				if ver == "" {
					ver = "-"
				}
				desc := p.Description
				if desc == "" {
					desc = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, ver, p.Source, desc)
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func pluginInstallCmd() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "install <path>",
		Short: "Install a plugin from a local path",
		Long: `Copy an executable into the tent plugins directory.

The source must be an executable file. By default, the plugin name is derived
from the filename (stripping any "tent-" prefix). Use --name to override.

Examples:
  tent plugin install ./my-tool              # Installed as tent-my-tool
  tent plugin install ./analyzer --name scan # Installed as tent-scan`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			srcPath := args[0]

			// Resolve to absolute path
			absSrc, err := filepath.Abs(srcPath)
			if err != nil {
				return fmt.Errorf("failed to resolve path: %w", err)
			}

			info, err := os.Stat(absSrc)
			if err != nil {
				return fmt.Errorf("cannot access %q: %w", srcPath, err)
			}
			if info.IsDir() {
				return fmt.Errorf("%q is a directory, not an executable", srcPath)
			}
			if !isExecutable(info) {
				return fmt.Errorf("%q is not executable (chmod +x it first)", srcPath)
			}

			// Determine plugin name
			pluginName := name
			if pluginName == "" {
				pluginName = filepath.Base(absSrc)
				pluginName = strings.TrimPrefix(pluginName, "tent-")
			}

			// Ensure plugins directory exists
			pdir := pluginDir()
			if err := os.MkdirAll(pdir, 0o755); err != nil {
				return fmt.Errorf("failed to create plugins directory: %w", err)
			}

			destPath := filepath.Join(pdir, "tent-"+pluginName)

			// Read source and write to destination
			data, err := os.ReadFile(absSrc)
			if err != nil {
				return fmt.Errorf("failed to read %q: %w", srcPath, err)
			}

			if err := os.WriteFile(destPath, data, 0o755); err != nil {
				return fmt.Errorf("failed to install plugin: %w", err)
			}

			fmt.Printf("Installed plugin %q at %s\n", pluginName, destPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Override the plugin name")
	return cmd
}

func pluginInfoCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "info <name>",
		Short: "Show details about a plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pluginName := args[0]
			plugins := discoverPlugins()

			var found *PluginMeta
			for i, p := range plugins {
				if p.Name == pluginName {
					found = &plugins[i]
					break
				}
			}

			if found == nil {
				return fmt.Errorf("plugin %q not found", pluginName)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(found)
			}

			fmt.Printf("Name:        %s\n", found.Name)
			fmt.Printf("Path:        %s\n", found.Path)
			fmt.Printf("Source:      %s\n", found.Source)
			if found.Version != "" {
				fmt.Printf("Version:     %s\n", found.Version)
			}
			if found.Description != "" {
				fmt.Printf("Description: %s\n", found.Description)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func pluginRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed plugin",
		Long:  "Remove a plugin from the tent plugins directory. Plugins found via PATH cannot be removed this way.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pluginName := args[0]
			pdir := pluginDir()
			target := filepath.Join(pdir, "tent-"+pluginName)

			if _, err := os.Stat(target); os.IsNotExist(err) {
				return fmt.Errorf("plugin %q is not installed in %s", pluginName, pdir)
			}

			if err := os.Remove(target); err != nil {
				return fmt.Errorf("failed to remove plugin: %w", err)
			}

			fmt.Printf("Removed plugin %q\n", pluginName)
			return nil
		},
	}

	return cmd
}

func pluginRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <name> [args...]",
		Short: "Run a plugin explicitly",
		Long: `Run a discovered plugin by name, passing any additional arguments.

The plugin receives sandbox context via environment variables:
  TENT_BASE_DIR    - Base directory for tent data
  TENT_PLUGIN_NAME - The plugin name as invoked

Examples:
  tent plugin run my-tool --flag value
  tent plugin run analyzer sandbox1`,
		Args:              cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			pluginName := args[0]
			pluginArgs := args[1:]

			return runPlugin(pluginName, pluginArgs)
		},
	}

	return cmd
}

// runPlugin finds and executes a plugin by name.
func runPlugin(name string, args []string) error {
	plugins := discoverPlugins()

	var pluginPath string
	for _, p := range plugins {
		if p.Name == name {
			pluginPath = p.Path
			break
		}
	}

	if pluginPath == "" {
		return fmt.Errorf("plugin %q not found. Run 'tent plugin list' to see available plugins", name)
	}

	// Set up environment
	baseDir := os.Getenv("TENT_BASE_DIR")
	if baseDir == "" {
		home, _ := os.UserHomeDir()
		baseDir = filepath.Join(home, ".tent")
	}

	pluginCmd := exec.Command(pluginPath, args...)
	pluginCmd.Stdin = os.Stdin
	pluginCmd.Stdout = os.Stdout
	pluginCmd.Stderr = os.Stderr
	pluginCmd.Env = append(os.Environ(),
		"TENT_BASE_DIR="+baseDir,
		"TENT_PLUGIN_NAME="+name,
	)

	if err := pluginCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("failed to run plugin %q: %w", name, err)
	}

	return nil
}

// FindPlugin looks up a plugin by name, returning its path or empty string.
func FindPlugin(name string) string {
	plugins := discoverPlugins()
	for _, p := range plugins {
		if p.Name == name {
			return p.Path
		}
	}
	return ""
}
