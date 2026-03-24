package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func cpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cp <src> <dst>",
		Short: "Copy files between host and sandbox",
		Long: `Copy files between host and a running sandbox.

Use <sandbox>:<path> to refer to a path inside a sandbox.
Use a plain path to refer to the host filesystem.

Examples:
  tent cp myfile.txt mybox:/tmp/myfile.txt    # host -> sandbox
  tent cp mybox:/var/log/app.log ./app.log    # sandbox -> host
  tent cp mybox:/etc/config/ ./config/        # directory copy`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := args[0]
			dst := args[1]

			srcSandbox, srcPath := parseCpArg(src)
			dstSandbox, dstPath := parseCpArg(dst)

			if srcSandbox != "" && dstSandbox != "" {
				return fmt.Errorf("cannot copy directly between two sandboxes; copy to host first")
			}
			if srcSandbox == "" && dstSandbox == "" {
				return fmt.Errorf("at least one argument must reference a sandbox (e.g. mybox:/path)")
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

			if dstSandbox != "" {
				// Host -> Sandbox
				if err := manager.CopyToGuest(dstSandbox, srcPath, dstPath); err != nil {
					return err
				}
				fmt.Printf("Copied %s -> %s:%s\n", srcPath, dstSandbox, dstPath)
			} else {
				// Sandbox -> Host
				if err := manager.CopyFromGuest(srcSandbox, srcPath, dstPath); err != nil {
					return err
				}
				fmt.Printf("Copied %s:%s -> %s\n", srcSandbox, srcPath, dstPath)
			}

			return nil
		},
	}

	return cmd
}

// parseCpArg splits a cp argument into sandbox name and path.
// If the argument contains a colon (and is not an absolute path on Windows),
// the part before the colon is the sandbox name and the part after is the path.
// Otherwise, the entire argument is treated as a local host path.
func parseCpArg(arg string) (sandbox, path string) {
	// Don't split on colon if it looks like a Windows drive letter (e.g. C:\)
	if len(arg) >= 2 && arg[1] == ':' && (arg[0] >= 'A' && arg[0] <= 'Z' || arg[0] >= 'a' && arg[0] <= 'z') {
		return "", arg
	}
	parts := strings.SplitN(arg, ":", 2)
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1]
	}
	return "", arg
}
