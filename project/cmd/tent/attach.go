package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

// attachCmd creates the attach command for interactive console access
func attachCmd() *cobra.Command {
	var (
		detachKeys string
		noStdin    bool
	)

	cmd := &cobra.Command{
		Use:   "attach <name>",
		Short: "Attach to a running sandbox's console",
		Long: `Attach to a running sandbox's serial console for interactive access.

This connects your terminal to the sandbox's virtio-console device,
allowing interactive shell access and real-time console output.

Use the detach key sequence (default: Ctrl+P, Ctrl+Q) to detach
without stopping the sandbox.

Examples:
  tent attach mybox
  tent attach mybox --no-stdin
  tent attach mybox --detach-keys ctrl-c`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

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

			// Verify sandbox exists and is running
			vmState, err := manager.Status(name)
			if err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}

			if vmState.Status != "running" {
				return fmt.Errorf("sandbox %q is not running (status: %s)", name, vmState.Status)
			}

			// Parse detach key sequence
			detachSeq := parseDetachKeys(detachKeys)

			fmt.Fprintf(os.Stderr, "Attaching to sandbox %q console...\n", name)
			if len(detachSeq) > 0 {
				fmt.Fprintf(os.Stderr, "Detach with: %s\n", detachKeys)
			}

			// Set up signal handling
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			doneCh := make(chan struct{})

			// Stream console output to stdout
			go func() {
				err := manager.FollowConsoleLogs(name, 20, os.Stdout, doneCh)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\nConsole error: %v\n", err)
				}
			}()

			if noStdin {
				// Wait for signal only
				<-sigCh
				close(doneCh)
				fmt.Fprintf(os.Stderr, "\nDetached from %q\n", name)
				return nil
			}

			// Read stdin and watch for detach sequence
			go func() {
				buf := make([]byte, 256)
				detachIdx := 0
				for {
					n, err := os.Stdin.Read(buf)
					if err != nil {
						return
					}

					for i := 0; i < n; i++ {
						if len(detachSeq) > 0 && buf[i] == detachSeq[detachIdx] {
							detachIdx++
							if detachIdx == len(detachSeq) {
								// Detach sequence matched
								close(doneCh)
								return
							}
						} else {
							detachIdx = 0
						}
					}

					// Forward input to sandbox console
					if err := manager.WriteToConsole(name, buf[:n]); err != nil {
						// Ignore write errors — sandbox may have stopped
						continue
					}
				}
			}()

			// Wait for either signal or detach
			select {
			case <-sigCh:
				close(doneCh)
			case <-doneCh:
			}

			fmt.Fprintf(os.Stderr, "\nDetached from %q\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&detachKeys, "detach-keys", "ctrl-p,ctrl-q", "Key sequence to detach from console")
	cmd.Flags().BoolVar(&noStdin, "no-stdin", false, "Do not attach stdin (output only)")

	return cmd
}

// parseDetachKeys parses a detach key specification like "ctrl-p,ctrl-q"
// into a byte sequence.
func parseDetachKeys(spec string) []byte {
	if spec == "" {
		return nil
	}

	var seq []byte
	keys := splitDetachKeys(spec)

	for _, key := range keys {
		switch key {
		case "ctrl-a":
			seq = append(seq, 1)
		case "ctrl-b":
			seq = append(seq, 2)
		case "ctrl-c":
			seq = append(seq, 3)
		case "ctrl-d":
			seq = append(seq, 4)
		case "ctrl-e":
			seq = append(seq, 5)
		case "ctrl-f":
			seq = append(seq, 6)
		case "ctrl-p":
			seq = append(seq, 16)
		case "ctrl-q":
			seq = append(seq, 17)
		case "ctrl-z":
			seq = append(seq, 26)
		case "ctrl-\\":
			seq = append(seq, 28)
		default:
			if len(key) == 1 {
				seq = append(seq, key[0])
			}
		}
	}

	return seq
}

// splitDetachKeys splits a comma-separated detach key spec
func splitDetachKeys(spec string) []string {
	var keys []string
	current := ""
	for i := 0; i < len(spec); i++ {
		if spec[i] == ',' {
			if current != "" {
				keys = append(keys, current)
				current = ""
			}
		} else {
			current += string(spec[i])
		}
	}
	if current != "" {
		keys = append(keys, current)
	}
	return keys
}
