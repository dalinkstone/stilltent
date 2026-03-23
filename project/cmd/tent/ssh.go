package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/sandbox"
)

// ConfigureSshCmd creates a new ssh command with optional dependencies.
// Deprecated: Use `tent shell` instead. SSH support will be removed in a future release.
func ConfigureSshCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	cmd := &cobra.Command{
		Use:   "ssh <name>",
		Short: "SSH into a running microVM (deprecated: use 'tent shell' instead)",
		Long: `SSH into a running microVM. This command is deprecated.

Use 'tent shell' instead for vsock-based access that doesn't require
SSH server installation, key generation, or IP discovery.

See also: tent shell, tent exec`,
		Example: `  # Open a shell via SSH (deprecated)
  tent ssh mybox

  # Preferred: use tent shell instead
  tent shell mybox`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			fmt.Fprintln(os.Stderr, "NOTICE: 'tent ssh' is deprecated. Use 'tent shell' for faster, SSH-free access via vsock.")

			// Create VM manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			// Get platform-specific hypervisor backend if not provided
			hvBackend := opts.Hypervisor
			if hvBackend == nil {
				var err error
				hvBackend, err = vm.NewPlatformBackend(baseDir)
				if err != nil {
					return fmt.Errorf("failed to create hypervisor backend: %w", err)
				}
			}

			// Create manager with dependencies
			manager, err := vm.NewManager(baseDir, opts.StateManager, hvBackend, opts.NetworkMgr, opts.StorageMgr)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Build SSH arguments with per-sandbox key
			sshArgs, err := manager.GetSSHArgs(name)
			if err != nil {
				return err
			}

			// SSH into the VM interactively
			sshCmd := exec.Command("ssh", sshArgs...)
			sshCmd.Stdin = os.Stdin
			sshCmd.Stdout = os.Stdout
			sshCmd.Stderr = os.Stderr

			if err := sshCmd.Run(); err != nil {
				return fmt.Errorf("SSH failed: %w", err)
			}

			return nil
		},
	}

	return cmd
}

// sshCmd is a convenience function that uses default dependencies
func sshCmd() *cobra.Command {
	return ConfigureSshCmd()
}

// ConfigureShellCmd creates the `tent shell` command that opens an interactive
// shell via the vsock-based guest agent. No SSH, no IP discovery needed.
func ConfigureShellCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	for _, opt := range options {
		opt(opts)
	}

	var shellBin string

	cmd := &cobra.Command{
		Use:   "shell <name>",
		Short: "Open an interactive shell inside a running microVM via vsock",
		Long: `Open an interactive shell inside a running microVM using the vsock-based
guest agent. This does not require SSH, IP discovery, or any network setup.

The shell communicates directly with the tent-agent running inside the guest
over virtio-vsock, providing sub-second connection times.

Examples:
  tent shell mybox
  tent shell mybox --shell /bin/sh`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Create VM manager
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			// Get platform-specific hypervisor backend if not provided
			hvBackend := opts.Hypervisor
			if hvBackend == nil {
				var err error
				hvBackend, err = vm.NewPlatformBackend(baseDir)
				if err != nil {
					return fmt.Errorf("failed to create hypervisor backend: %w", err)
				}
			}

			// Create manager with dependencies
			manager, err := vm.NewManager(baseDir, opts.StateManager, hvBackend, opts.NetworkMgr, opts.StorageMgr)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Open interactive shell via vsock agent
			exitCode, err := manager.ExecShell(name, shellBin)
			if err != nil {
				return fmt.Errorf("failed to open shell: %w", err)
			}

			if exitCode != 0 {
				os.Exit(exitCode)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&shellBin, "shell", "/bin/bash", "Shell binary to use (default: /bin/bash)")

	return cmd
}

// shellCmd is a convenience function that uses default dependencies
func shellCmd() *cobra.Command {
	return ConfigureShellCmd()
}
