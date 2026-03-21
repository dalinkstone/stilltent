package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/vm"
)

// ConfigureSshCmd creates a new ssh command with optional dependencies
func ConfigureSshCmd(options ...CommonCmdOption) *cobra.Command {
	opts := &CommonCmdOptions{}

	// Apply functional options
	for _, opt := range options {
		opt(opts)
	}

	cmd := &cobra.Command{
		Use:   "ssh <name>",
		Short: "SSH into a running microVM",
		Long:  `SSH into a running microVM.`,
		Args:  cobra.ExactArgs(1),
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

			// Get VM status to check if running and get IP
			vmState, err := manager.Status(name)
			if err != nil {
				return fmt.Errorf("failed to get VM status: %w", err)
			}

			if vmState.Status != "running" {
				return fmt.Errorf("VM %s is not running", name)
			}

			if vmState.IP == "" {
				return fmt.Errorf("VM %s has no IP address assigned", name)
			}

			// SSH into the VM
			sshCmd := exec.Command("ssh", "root@"+vmState.IP)
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
