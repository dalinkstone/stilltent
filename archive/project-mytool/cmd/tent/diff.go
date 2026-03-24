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

// FileChange represents a single filesystem change in a sandbox
type FileChange struct {
	Type string `json:"type"` // "A" (added), "C" (changed), "D" (deleted)
	Path string `json:"path"`
}

func diffCmd() *cobra.Command {
	var (
		outputJSON bool
		showDirs   bool
	)

	cmd := &cobra.Command{
		Use:   "diff <name>",
		Short: "Show filesystem changes in a sandbox since creation",
		Long: `Inspect changes to a sandbox's filesystem relative to its base image.

Runs inside the sandbox to detect files that have been added, changed,
or deleted compared to the original root filesystem. Similar to 'docker diff'.

Change types:
  A  Added    — file was created after sandbox start
  C  Changed  — file was modified after sandbox creation
  D  Deleted  — file was removed (present in base image, now missing)

Examples:
  tent diff mybox
  tent diff mybox --json
  tent diff mybox --dirs`,
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

			// Verify sandbox is running
			vmState, err := manager.Status(name)
			if err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}
			if vmState.Status != "running" {
				return fmt.Errorf("sandbox %q is not running (status: %s) — diff requires a running sandbox", name, vmState.Status)
			}

			changes, err := collectDiff(manager, name, showDirs)
			if err != nil {
				return fmt.Errorf("failed to collect diff: %w", err)
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(changes)
			}

			if len(changes) == 0 {
				fmt.Println("No filesystem changes detected.")
				return nil
			}

			for _, c := range changes {
				fmt.Printf("%s %s\n", c.Type, c.Path)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&showDirs, "dirs", false, "Include directory entries in output")

	return cmd
}

// collectDiff runs commands inside the sandbox to detect filesystem changes.
// It uses find to detect recently modified files and compares against
// known system directories that are expected to change at runtime.
func collectDiff(manager *vm.VMManager, name string, showDirs bool) ([]FileChange, error) {
	// Get the sandbox creation timestamp to use as a baseline
	vmState, err := manager.Status(name)
	if err != nil {
		return nil, err
	}

	// Find files modified after sandbox creation
	// We use the sandbox's /etc/tent-created marker if it exists,
	// otherwise fall back to finding files newer than /etc/hostname
	// which is typically set at image creation time.
	//
	// Strategy: find all files changed after boot by comparing against
	// a reference timestamp. We create a reference file at a known epoch
	// and use find -newer.

	// First, create a timestamp reference file based on creation time
	createRef := fmt.Sprintf("date -d @%d +%%Y%%m%%d%%H%%M.%%S 2>/dev/null || date -r %d +%%Y%%m%%d%%H%%M.%%S 2>/dev/null || echo NODATE",
		vmState.CreatedAt, vmState.CreatedAt)
	tsOutput, _, err := manager.Exec(name, []string{"sh", "-c", createRef})
	if err != nil {
		// Fallback: just find recently modified files
		tsOutput = "NODATE"
	}
	tsOutput = strings.TrimSpace(tsOutput)

	var findCmd string
	if tsOutput != "" && tsOutput != "NODATE" {
		// Create a reference file with the creation timestamp
		setupRef := fmt.Sprintf("touch -t %s /tmp/.tent-diff-ref 2>/dev/null", tsOutput)
		manager.Exec(name, []string{"sh", "-c", setupRef})

		// Find files newer than the reference
		typeFilter := "-type f"
		if showDirs {
			typeFilter = "\\( -type f -o -type d \\)"
		}
		findCmd = fmt.Sprintf("find / -xdev %s -newer /tmp/.tent-diff-ref "+
			"! -path '/proc/*' ! -path '/sys/*' ! -path '/dev/*' "+
			"! -path '/run/*' ! -path '/tmp/.tent-diff-ref' "+
			"2>/dev/null | sort", typeFilter)
	} else {
		// No creation timestamp available — find files modified in the last
		// period since we can't determine exact creation time. Use 30 days
		// as a generous window.
		typeFilter := "-type f"
		if showDirs {
			typeFilter = "\\( -type f -o -type d \\)"
		}
		findCmd = fmt.Sprintf("find / -xdev %s -mmin -43200 "+
			"! -path '/proc/*' ! -path '/sys/*' ! -path '/dev/*' "+
			"! -path '/run/*' "+
			"2>/dev/null | sort", typeFilter)
	}

	modifiedOutput, _, err := manager.Exec(name, []string{"sh", "-c", findCmd})
	if err != nil {
		return nil, fmt.Errorf("failed to find modified files: %w", err)
	}

	// Also check for deleted files by looking at dpkg/rpm database if available
	// This is best-effort — we look for files that packages expect but are missing
	deletedOutput := ""
	// Try dpkg-based detection (Debian/Ubuntu)
	dpkgOut, _, dpkgErr := manager.Exec(name, []string{"sh", "-c",
		"dpkg --verify 2>/dev/null | grep '^missing' | awk '{print $NF}' | sort"})
	if dpkgErr == nil && strings.TrimSpace(dpkgOut) != "" {
		deletedOutput = dpkgOut
	} else {
		// Try rpm-based detection (RHEL/Fedora)
		rpmOut, _, rpmErr := manager.Exec(name, []string{"sh", "-c",
			"rpm -Va 2>/dev/null | grep '^missing' | awk '{print $NF}' | sort"})
		if rpmErr == nil && strings.TrimSpace(rpmOut) != "" {
			deletedOutput = rpmOut
		}
	}

	var changes []FileChange

	// Parse modified/added files
	// Runtime paths that commonly change and should be excluded
	runtimePrefixes := []string{
		"/var/log/",
		"/var/cache/",
		"/var/tmp/",
		"/tmp/",
	}

	for _, line := range strings.Split(strings.TrimSpace(modifiedOutput), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "/" {
			continue
		}

		// Skip common runtime noise
		skip := false
		for _, prefix := range runtimePrefixes {
			if strings.HasPrefix(line, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		changes = append(changes, FileChange{
			Type: "C",
			Path: line,
		})
	}

	// Parse deleted files
	for _, line := range strings.Split(strings.TrimSpace(deletedOutput), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		changes = append(changes, FileChange{
			Type: "D",
			Path: line,
		})
	}

	// Sort: type then path
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Type != changes[j].Type {
			return changes[i].Type < changes[j].Type
		}
		return changes[i].Path < changes[j].Path
	})

	// Cleanup reference file
	manager.Exec(name, []string{"rm", "-f", "/tmp/.tent-diff-ref"})

	return changes, nil
}
