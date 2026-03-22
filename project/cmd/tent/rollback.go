package main

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func rollbackCmd() *cobra.Command {
	var (
		target   string
		dryRun   bool
		fromType string
	)

	cmd := &cobra.Command{
		Use:   "rollback <name>",
		Short: "Roll back a sandbox to its most recent snapshot or checkpoint",
		Long: `Quickly revert a sandbox to a known-good state by restoring the most recent
snapshot or checkpoint. This is a convenience command for AI agent workloads
where you want to reset execution state without remembering specific tag names.

By default, rollback chooses the most recent restore point across both snapshots
and checkpoints. Use --from to prefer a specific type, or --target to specify
an exact tag.

Examples:
  tent rollback mybox                          # Restore most recent snapshot or checkpoint
  tent rollback mybox --from snapshot          # Only consider snapshots
  tent rollback mybox --from checkpoint        # Only consider checkpoints
  tent rollback mybox --target v1              # Restore a specific tag
  tent rollback mybox --dry-run                # Show what would be restored without doing it`,
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

			// Verify sandbox exists
			if _, err := manager.Status(name); err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}

			if target != "" {
				return rollbackToTarget(manager, name, target, fromType, dryRun)
			}

			return rollbackToLatest(manager, name, fromType, dryRun)
		},
	}

	cmd.Flags().StringVarP(&target, "target", "t", "", "Restore a specific snapshot/checkpoint tag")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be restored without performing the rollback")
	cmd.Flags().StringVar(&fromType, "from", "", "Restore point type: 'snapshot', 'checkpoint', or '' for either (default)")

	return cmd
}

// restorePoint represents either a snapshot or checkpoint as a unified type
type restorePoint struct {
	Tag       string
	Timestamp time.Time
	Kind      string // "snapshot" or "checkpoint"
	SizeMB    int64
	Desc      string
}

func collectRestorePoints(manager *vm.VMManager, name, fromType string) ([]restorePoint, error) {
	var points []restorePoint

	if fromType == "" || fromType == "snapshot" {
		snapshots, err := manager.ListSnapshots(name)
		if err == nil {
			for _, s := range snapshots {
				ts := parseTimestamp(s.Timestamp)
				points = append(points, restorePoint{
					Tag:       s.Tag,
					Timestamp: ts,
					Kind:      "snapshot",
					SizeMB:    int64(s.SizeMB),
				})
			}
		}
	}

	if fromType == "" || fromType == "checkpoint" {
		checkpoints, err := manager.ListCheckpoints(name)
		if err == nil {
			for _, c := range checkpoints {
				ts := parseTimestamp(c.Timestamp)
				points = append(points, restorePoint{
					Tag:       c.Tag,
					Timestamp: ts,
					Kind:      "checkpoint",
					SizeMB:    c.SizeMB,
					Desc:      c.Description,
				})
			}
		}
	}

	// Sort by timestamp descending — most recent first
	sort.Slice(points, func(i, j int) bool {
		return points[i].Timestamp.After(points[j].Timestamp)
	})

	return points, nil
}

func parseTimestamp(s string) time.Time {
	// Try common formats
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func findPointByTag(points []restorePoint, tag string) *restorePoint {
	for i := range points {
		if points[i].Tag == tag {
			return &points[i]
		}
	}
	return nil
}

func rollbackToTarget(manager *vm.VMManager, name, tag, fromType string, dryRun bool) error {
	points, err := collectRestorePoints(manager, name, fromType)
	if err != nil {
		return err
	}

	p := findPointByTag(points, tag)
	if p == nil {
		return fmt.Errorf("no snapshot or checkpoint with tag %q found for sandbox %q", tag, name)
	}

	return performRollback(manager, name, p, dryRun)
}

func rollbackToLatest(manager *vm.VMManager, name, fromType string, dryRun bool) error {
	points, err := collectRestorePoints(manager, name, fromType)
	if err != nil {
		return err
	}

	if len(points) == 0 {
		typeStr := "snapshots or checkpoints"
		if fromType != "" {
			typeStr = fromType + "s"
		}
		return fmt.Errorf("no %s found for sandbox %q", typeStr, name)
	}

	latest := &points[0]

	if dryRun {
		fmt.Printf("Would rollback sandbox %q to %s %q\n", name, latest.Kind, latest.Tag)
		fmt.Printf("  Type:      %s\n", latest.Kind)
		fmt.Printf("  Tag:       %s\n", latest.Tag)
		if !latest.Timestamp.IsZero() {
			fmt.Printf("  Created:   %s\n", latest.Timestamp.Local().Format("2006-01-02 15:04:05"))
		}
		fmt.Printf("  Size:      %d MB\n", latest.SizeMB)
		if latest.Desc != "" {
			fmt.Printf("  Desc:      %s\n", latest.Desc)
		}

		if len(points) > 1 {
			fmt.Printf("\nOther available restore points (%d total):\n", len(points))
			limit := len(points)
			if limit > 6 {
				limit = 6
			}
			for i := 1; i < limit; i++ {
				p := points[i]
				age := formatAge(p.Timestamp)
				fmt.Printf("  %-12s  %-20s  %s  %d MB\n", p.Kind, p.Tag, age, p.SizeMB)
			}
			if len(points) > 6 {
				fmt.Printf("  ... and %d more\n", len(points)-6)
			}
		}
		return nil
	}

	return performRollback(manager, name, latest, false)
}

func performRollback(manager *vm.VMManager, name string, p *restorePoint, dryRun bool) error {
	if dryRun {
		fmt.Printf("Would restore %s %q for sandbox %q\n", p.Kind, p.Tag, name)
		return nil
	}

	tsStr := ""
	if !p.Timestamp.IsZero() {
		tsStr = fmt.Sprintf(" (created %s)", p.Timestamp.Local().Format("2006-01-02 15:04:05"))
	}
	fmt.Printf("Rolling back sandbox %q to %s %q%s...\n", name, p.Kind, p.Tag, tsStr)

	var err error
	if p.Kind == "snapshot" {
		err = manager.RestoreSnapshot(name, p.Tag)
	} else {
		err = manager.RestoreCheckpoint(name, p.Tag)
	}

	if err != nil {
		return fmt.Errorf("failed to restore %s: %w", p.Kind, err)
	}

	fmt.Fprintf(os.Stdout, "Successfully rolled back sandbox %q to %s %q\n", name, p.Kind, p.Tag)
	return nil
}

func formatAge(t time.Time) string {
	if t.IsZero() {
		return "unknown age"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
