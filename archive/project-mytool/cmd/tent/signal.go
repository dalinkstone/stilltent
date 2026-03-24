package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

// signalNameMap maps signal names to their numeric values.
var signalNameMap = map[string]int{
	"SIGHUP":    1,
	"SIGINT":    2,
	"SIGQUIT":   3,
	"SIGKILL":   9,
	"SIGUSR1":   10,
	"SIGUSR2":   12,
	"SIGTERM":   15,
	"SIGCONT":   18,
	"SIGSTOP":   19,
	"HUP":       1,
	"INT":       2,
	"QUIT":      3,
	"KILL":      9,
	"USR1":      10,
	"USR2":      12,
	"TERM":      15,
	"CONT":      18,
	"STOP":      19,
}

func signalCmd() *cobra.Command {
	var (
		pid     int
		all     bool
		dryRun  bool
	)

	cmd := &cobra.Command{
		Use:   "signal <name> <signal>",
		Short: "Send a signal to processes inside a sandbox",
		Long: `Send an OS signal to a process running inside a sandbox.

The signal can be specified by name (SIGTERM, SIGKILL, SIGHUP, etc.)
or by number (15, 9, 1, etc.). Signal names may omit the SIG prefix.

By default, the signal is sent to PID 1 (the init process). Use --pid
to target a specific process, or --all to signal all processes.

Examples:
  tent signal mybox SIGTERM
  tent signal mybox SIGKILL --pid 42
  tent signal mybox HUP --all
  tent signal mybox 15
  tent signal mybox USR1 --pid 100`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			sigArg := args[1]

			sigNum, sigName, err := resolveSignal(sigArg)
			if err != nil {
				return err
			}

			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return err
			}

			// Verify sandbox is running
			state, err := manager.Status(name)
			if err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}
			if state.Status != models.VMStatusRunning {
				return fmt.Errorf("sandbox %q is not running (status: %s)", name, state.Status)
			}

			if dryRun {
				if all {
					fmt.Printf("Would send %s (%d) to all processes in sandbox %q\n", sigName, sigNum, name)
				} else {
					fmt.Printf("Would send %s (%d) to PID %d in sandbox %q\n", sigName, sigNum, pid, name)
				}
				return nil
			}

			// Build the kill command to execute inside the sandbox
			var killCmd string
			if all {
				// Send to all processes (kill -<sig> -1 sends to all except PID 1,
				// so we send to PID 1 explicitly as well)
				killCmd = fmt.Sprintf("kill -%d -1 2>/dev/null; kill -%d 1 2>/dev/null", sigNum, sigNum)
			} else {
				killCmd = fmt.Sprintf("kill -%d %d", sigNum, pid)
			}

			output, exitCode, err := manager.Exec(name, []string{"sh", "-c", killCmd})
			if err != nil {
				return fmt.Errorf("failed to send signal: %w", err)
			}

			if exitCode != 0 && !all {
				// For --all mode, some processes may not accept the signal; that's OK
				trimmed := strings.TrimSpace(output)
				if trimmed != "" {
					return fmt.Errorf("signal failed (exit %d): %s", exitCode, trimmed)
				}
				return fmt.Errorf("signal failed with exit code %d (PID %d may not exist)", exitCode, pid)
			}

			if all {
				fmt.Printf("Sent %s (%d) to all processes in sandbox %q\n", sigName, sigNum, name)
			} else {
				fmt.Printf("Sent %s (%d) to PID %d in sandbox %q\n", sigName, sigNum, pid, name)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&pid, "pid", 1, "Target process ID (default: 1, the init process)")
	cmd.Flags().BoolVar(&all, "all", false, "Send signal to all processes in the sandbox")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without sending the signal")

	return cmd
}

// resolveSignal parses a signal argument (name or number) and returns
// the numeric signal value and its canonical name.
func resolveSignal(s string) (int, string, error) {
	// Try numeric first
	if num, err := strconv.Atoi(s); err == nil {
		if num < 1 || num > 64 {
			return 0, "", fmt.Errorf("signal number %d out of range (1-64)", num)
		}
		// Find canonical name
		name := fmt.Sprintf("SIG%d", num)
		for n, v := range signalNameMap {
			if v == num && strings.HasPrefix(n, "SIG") {
				name = n
				break
			}
		}
		return num, name, nil
	}

	// Try name lookup (case insensitive)
	upper := strings.ToUpper(s)
	if num, ok := signalNameMap[upper]; ok {
		// Normalize to SIG-prefixed name
		canonical := upper
		if !strings.HasPrefix(canonical, "SIG") {
			canonical = "SIG" + canonical
		}
		return num, canonical, nil
	}

	return 0, "", fmt.Errorf("unknown signal %q (use name like SIGTERM or number like 15)", s)
}
