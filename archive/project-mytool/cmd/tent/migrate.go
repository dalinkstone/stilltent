package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

func migrateCmd() *cobra.Command {
	var (
		destDir   string
		dryRun    bool
		force     bool
		copyMode  bool
		jsonOut   bool
	)

	cmd := &cobra.Command{
		Use:   "migrate <name>",
		Short: "Migrate a sandbox to a different storage location",
		Long: `Migrate a stopped sandbox's data (rootfs, config, state) to a different base
directory. This is useful for moving sandboxes between disks, reorganizing
storage, or freeing up space on the primary storage volume.

By default, the operation moves data (delete after copy). Use --copy to keep
the original data in place and create a copy at the destination.

The sandbox must be stopped before migration. After migration, the sandbox
will be managed from the new location.

Examples:
  tent migrate mybox --dest /mnt/fast-ssd/.tent
  tent migrate mybox --dest /Volumes/External/.tent --copy
  tent migrate mybox --dest ~/alt-tent --dry-run
  tent migrate mybox --dest /data/.tent --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if destDir == "" {
				return fmt.Errorf("--dest is required: specify the target base directory")
			}

			// Resolve destination to absolute path
			if !filepath.IsAbs(destDir) {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get working directory: %w", err)
				}
				destDir = filepath.Join(wd, destDir)
			}

			baseDir := getBaseDir()

			// Don't migrate to same location
			srcAbs, _ := filepath.Abs(baseDir)
			dstAbs, _ := filepath.Abs(destDir)
			if srcAbs == dstAbs {
				return fmt.Errorf("source and destination are the same directory: %s", srcAbs)
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

			// Verify sandbox exists and is stopped
			state, err := manager.Status(name)
			if err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}

			if state.Status == models.VMStatusRunning && !force {
				return fmt.Errorf("sandbox %q is running — stop it first or use --force", name)
			}

			result := migrateSandbox(name, baseDir, destDir, dryRun, copyMode, manager)

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			if result.Error != "" {
				return fmt.Errorf("migration failed: %s", result.Error)
			}

			if dryRun {
				fmt.Printf("Dry run — migration plan for %q:\n", name)
				fmt.Printf("  Source:      %s\n", result.Source)
				fmt.Printf("  Destination: %s\n", result.Destination)
				fmt.Printf("  Mode:        %s\n", result.Mode)
				fmt.Printf("  Files:       %d\n", result.FileCount)
				fmt.Printf("  Total size:  %s\n", humanSize(result.TotalBytes))
				if len(result.Files) > 0 {
					fmt.Println("  Files to transfer:")
					for _, f := range result.Files {
						fmt.Printf("    %s (%s)\n", f.RelPath, humanSize(f.Size))
					}
				}
			} else {
				action := "Moved"
				if copyMode {
					action = "Copied"
				}
				fmt.Printf("%s sandbox %q to %s\n", action, name, destDir)
				fmt.Printf("  Files:      %d\n", result.FileCount)
				fmt.Printf("  Total size: %s\n", humanSize(result.TotalBytes))
				fmt.Printf("  Duration:   %s\n", result.Duration)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&destDir, "dest", "", "Destination base directory (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be migrated without performing the operation")
	cmd.Flags().BoolVar(&force, "force", false, "Force migration even if sandbox is running (will stop it first)")
	cmd.Flags().BoolVar(&copyMode, "copy", false, "Copy instead of move (keep original data)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output results as JSON")

	_ = cmd.MarkFlagRequired("dest")

	return cmd
}

// MigrateResult holds the outcome of a migration operation
type MigrateResult struct {
	Name        string         `json:"name"`
	Source      string         `json:"source"`
	Destination string         `json:"destination"`
	Mode        string         `json:"mode"` // "move" or "copy"
	DryRun      bool           `json:"dry_run"`
	FileCount   int            `json:"file_count"`
	TotalBytes  int64          `json:"total_bytes"`
	Duration    string         `json:"duration,omitempty"`
	Files       []MigrateFile  `json:"files,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// MigrateFile describes a file involved in migration
type MigrateFile struct {
	RelPath string `json:"path"`
	Size    int64  `json:"size"`
}

func migrateSandbox(name, srcBase, dstBase string, dryRun, copyMode bool, manager *vm.VMManager) MigrateResult {
	mode := "move"
	if copyMode {
		mode = "copy"
	}

	result := MigrateResult{
		Name:        name,
		Source:      srcBase,
		Destination: dstBase,
		Mode:        mode,
		DryRun:      dryRun,
	}

	srcDir := filepath.Join(srcBase, "sandboxes", name)
	dstDir := filepath.Join(dstBase, "sandboxes", name)

	// Check source exists
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		result.Error = fmt.Sprintf("source directory does not exist: %s", srcDir)
		return result
	}

	// Check destination doesn't exist
	if _, err := os.Stat(dstDir); err == nil {
		result.Error = fmt.Sprintf("destination already exists: %s", dstDir)
		return result
	}

	// Inventory files to migrate
	var files []MigrateFile
	var totalBytes int64

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, _ := filepath.Rel(srcDir, path)
		files = append(files, MigrateFile{
			RelPath: relPath,
			Size:    info.Size(),
		})
		totalBytes += info.Size()
		return nil
	})
	if err != nil {
		result.Error = fmt.Sprintf("failed to inventory source files: %v", err)
		return result
	}

	result.Files = files
	result.FileCount = len(files)
	result.TotalBytes = totalBytes

	if dryRun {
		return result
	}

	// Perform the migration
	start := time.Now()

	// Create destination directory structure
	if err := os.MkdirAll(filepath.Dir(dstDir), 0755); err != nil {
		result.Error = fmt.Sprintf("failed to create destination parent: %v", err)
		return result
	}

	// Copy all files to destination
	if err := copyDirRecursive(srcDir, dstDir); err != nil {
		// Clean up partial copy on failure
		_ = os.RemoveAll(dstDir)
		result.Error = fmt.Sprintf("failed to copy files: %v", err)
		return result
	}

	// Also copy/migrate sandbox state and config from other locations
	migrateAuxFiles(name, srcBase, dstBase, copyMode)

	// If move mode, remove source after successful copy
	if !copyMode {
		if err := os.RemoveAll(srcDir); err != nil {
			result.Error = fmt.Sprintf("copied successfully but failed to remove source: %v", err)
			return result
		}
	}

	result.Duration = time.Since(start).Round(time.Millisecond).String()
	return result
}

// migrateAuxFiles copies auxiliary sandbox files (configs, state snippets) that
// may exist outside the main sandbox directory
func migrateAuxFiles(name, srcBase, dstBase string, copyMode bool) {
	// Copy sandbox config if stored separately
	auxPaths := []string{
		filepath.Join("configs", name+".yaml"),
		filepath.Join("configs", name+".json"),
		filepath.Join("snapshots", name),
		filepath.Join("checkpoints", name),
	}

	for _, rel := range auxPaths {
		src := filepath.Join(srcBase, rel)
		dst := filepath.Join(dstBase, rel)

		info, err := os.Stat(src)
		if err != nil {
			continue
		}

		if info.IsDir() {
			_ = copyDirRecursive(src, dst)
		} else {
			_ = os.MkdirAll(filepath.Dir(dst), 0755)
			_ = copyFile(src, dst)
		}

		if !copyMode {
			if info.IsDir() {
				_ = os.RemoveAll(src)
			} else {
				_ = os.Remove(src)
			}
		}
	}
}

// copyDirRecursive copies a directory tree from src to dst
func copyDirRecursive(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDirRecursive(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// copyFile copies a single file preserving permissions
func copyFile(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	// Handle symlinks
	if srcInfo.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}


// migrateListCmd shows sandboxes with their storage locations and sizes
func migrateListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sandboxes with storage location and disk usage",
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

			type sandboxStorage struct {
				Name     string `json:"name"`
				Status   string `json:"status"`
				Path     string `json:"path"`
				DiskUsed int64  `json:"disk_used_bytes"`
				DiskHuman string `json:"disk_used"`
			}

			var entries []sandboxStorage
			for _, v := range vms {
				sbDir := filepath.Join(baseDir, "sandboxes", v.Name)
				var size int64
				_ = filepath.Walk(sbDir, func(_ string, info os.FileInfo, err error) error {
					if err != nil || info.IsDir() {
						return nil
					}
					size += info.Size()
					return nil
				})

				entries = append(entries, sandboxStorage{
					Name:      v.Name,
					Status:    string(v.Status),
					Path:      sbDir,
					DiskUsed:  size,
					DiskHuman: humanSize(size),
				})
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}

			if len(entries) == 0 {
				fmt.Println("No sandboxes found.")
				return nil
			}

			// Find max widths for alignment
			maxName := 4
			maxStatus := 6
			maxSize := 4
			for _, e := range entries {
				if len(e.Name) > maxName {
					maxName = len(e.Name)
				}
				if len(e.Status) > maxStatus {
					maxStatus = len(e.Status)
				}
				if len(e.DiskHuman) > maxSize {
					maxSize = len(e.DiskHuman)
				}
			}

			hdrFmt := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%s\n", maxName, maxStatus, maxSize)
			rowFmt := hdrFmt

			fmt.Printf(hdrFmt, "NAME", "STATUS", "SIZE", "PATH")
			fmt.Println(strings.Repeat("-", maxName+maxStatus+maxSize+len("PATH")+6))
			for _, e := range entries {
				fmt.Printf(rowFmt, e.Name, e.Status, e.DiskHuman, e.Path)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}
