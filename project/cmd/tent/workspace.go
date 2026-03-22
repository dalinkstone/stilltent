package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func workspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage shared workspace directories",
		Long: `Manage named workspace directories that can be mounted into multiple sandboxes.

Workspaces provide a way to define named host directories that are automatically
mounted into sandboxes at a consistent path. This is useful for AI workloads where
multiple sandboxes need access to the same code, data, or model files.

Examples:
  tent workspace create code --path ./src --mount /workspace/code
  tent workspace create data --path /data/models --readonly
  tent workspace list
  tent workspace attach code mysandbox
  tent workspace detach code mysandbox
  tent workspace inspect code
  tent workspace rm code`,
	}

	cmd.AddCommand(workspaceCreateCmd())
	cmd.AddCommand(workspaceListCmd())
	cmd.AddCommand(workspaceInspectCmd())
	cmd.AddCommand(workspaceRemoveCmd())
	cmd.AddCommand(workspaceAttachCmd())
	cmd.AddCommand(workspaceDetachCmd())

	return cmd
}

func newWorkspaceManager() (*vm.WorkspaceManager, error) {
	baseDir := os.Getenv("TENT_BASE_DIR")
	if baseDir == "" {
		home, _ := os.UserHomeDir()
		baseDir = home + "/.tent"
	}
	return vm.NewWorkspaceManager(baseDir)
}

func workspaceCreateCmd() *cobra.Command {
	var (
		hostPath    string
		mountPoint  string
		description string
		readonly    bool
	)

	cmd := &cobra.Command{
		Use:   "create <name> --path <host-dir>",
		Short: "Create a named workspace",
		Long: `Register a host directory as a named workspace that can be mounted into sandboxes.

The workspace name can be used with 'tent workspace attach' to mount it into
sandboxes, or referenced in compose files.

Examples:
  tent workspace create code --path ./my-project
  tent workspace create models --path /data/models --mount /models --readonly
  tent workspace create shared --path /tmp/shared --description "Shared temp files"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if hostPath == "" {
				return fmt.Errorf("--path is required: specify the host directory")
			}

			wm, err := newWorkspaceManager()
			if err != nil {
				return fmt.Errorf("initializing workspace manager: %w", err)
			}

			ws, err := wm.Create(name, hostPath, mountPoint, description, readonly)
			if err != nil {
				return err
			}

			fmt.Printf("Workspace %q created\n", ws.Name)
			fmt.Printf("  Host path:   %s\n", ws.HostPath)
			fmt.Printf("  Mount point: %s\n", ws.MountPoint)
			if ws.Readonly {
				fmt.Printf("  Mode:        readonly\n")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&hostPath, "path", "", "Host directory path (required)")
	cmd.Flags().StringVar(&mountPoint, "mount", "", "Guest mount point (default: /workspace/<name>)")
	cmd.Flags().StringVar(&description, "description", "", "Optional description")
	cmd.Flags().BoolVar(&readonly, "readonly", false, "Mount as read-only in sandboxes")

	return cmd
}

func workspaceListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all workspaces",
		Aliases: []string{"ls"},
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wm, err := newWorkspaceManager()
			if err != nil {
				return fmt.Errorf("initializing workspace manager: %w", err)
			}

			workspaces := wm.List()

			if jsonOut {
				data, err := json.MarshalIndent(workspaces, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			if len(workspaces) == 0 {
				fmt.Println("No workspaces registered. Use 'tent workspace create' to create one.")
				return nil
			}

			sort.Slice(workspaces, func(i, j int) bool {
				return workspaces[i].Name < workspaces[j].Name
			})

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tHOST PATH\tMOUNT POINT\tMODE\tSANDBOXES\tCREATED")
			for _, ws := range workspaces {
				mode := "rw"
				if ws.Readonly {
					mode = "ro"
				}
				sandboxes := "-"
				if len(ws.Sandboxes) > 0 {
					sandboxes = strings.Join(ws.Sandboxes, ",")
				}
				created := time.Unix(ws.CreatedAt, 0).Format("2006-01-02 15:04")
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					ws.Name, ws.HostPath, ws.MountPoint, mode, sandboxes, created)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func workspaceInspectCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "inspect <name>",
		Short: "Show workspace details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wm, err := newWorkspaceManager()
			if err != nil {
				return fmt.Errorf("initializing workspace manager: %w", err)
			}

			ws, err := wm.Get(args[0])
			if err != nil {
				return err
			}

			if jsonOut {
				data, err := json.MarshalIndent(ws, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			// Check if host path still exists
			pathStatus := "ok"
			if _, statErr := os.Stat(ws.HostPath); statErr != nil {
				pathStatus = "missing"
			}

			fmt.Printf("Name:        %s\n", ws.Name)
			fmt.Printf("Host path:   %s (%s)\n", ws.HostPath, pathStatus)
			fmt.Printf("Mount point: %s\n", ws.MountPoint)
			mode := "read-write"
			if ws.Readonly {
				mode = "read-only"
			}
			fmt.Printf("Mode:        %s\n", mode)
			if ws.Description != "" {
				fmt.Printf("Description: %s\n", ws.Description)
			}
			fmt.Printf("Created:     %s\n", time.Unix(ws.CreatedAt, 0).Format(time.RFC3339))
			fmt.Printf("Updated:     %s\n", time.Unix(ws.UpdatedAt, 0).Format(time.RFC3339))

			if len(ws.Sandboxes) > 0 {
				fmt.Printf("Sandboxes:\n")
				for _, s := range ws.Sandboxes {
					fmt.Printf("  - %s\n", s)
				}
			} else {
				fmt.Printf("Sandboxes:   (none)\n")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func workspaceRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "rm <name>",
		Short:   "Remove a workspace registration",
		Aliases: []string{"remove"},
		Long: `Remove a named workspace registration. This does NOT delete the host directory.

If the workspace is attached to sandboxes, use --force to remove it anyway.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			wm, err := newWorkspaceManager()
			if err != nil {
				return fmt.Errorf("initializing workspace manager: %w", err)
			}

			if force {
				err = wm.ForceRemove(name)
			} else {
				err = wm.Remove(name)
			}
			if err != nil {
				return err
			}

			fmt.Printf("Workspace %q removed\n", name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Remove even if attached to sandboxes")
	return cmd
}

func workspaceAttachCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attach <workspace> <sandbox>",
		Short: "Attach a workspace to a sandbox",
		Long: `Attach a named workspace to a sandbox. The workspace's host directory will be
mounted at the configured mount point when the sandbox starts.

The mount is applied on the next sandbox start/restart.

Examples:
  tent workspace attach code mybox
  tent workspace attach models agent-sandbox`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceName := args[0]
			sandboxName := args[1]

			wm, err := newWorkspaceManager()
			if err != nil {
				return fmt.Errorf("initializing workspace manager: %w", err)
			}

			if err := wm.Attach(workspaceName, sandboxName); err != nil {
				return err
			}

			ws, _ := wm.Get(workspaceName)
			fmt.Printf("Workspace %q attached to sandbox %q\n", workspaceName, sandboxName)
			fmt.Printf("  Mount: %s -> %s\n", ws.HostPath, ws.MountPoint)
			return nil
		},
	}
	return cmd
}

func workspaceDetachCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "detach <workspace> <sandbox>",
		Short: "Detach a workspace from a sandbox",
		Long: `Remove a workspace mount from a sandbox. The change takes effect on the next
sandbox start/restart.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceName := args[0]
			sandboxName := args[1]

			wm, err := newWorkspaceManager()
			if err != nil {
				return fmt.Errorf("initializing workspace manager: %w", err)
			}

			if err := wm.Detach(workspaceName, sandboxName); err != nil {
				return err
			}

			fmt.Printf("Workspace %q detached from sandbox %q\n", workspaceName, sandboxName)
			return nil
		},
	}
	return cmd
}
