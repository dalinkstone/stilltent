package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

// BackupMeta holds metadata for a single backup entry.
type BackupMeta struct {
	ID        string    `json:"id"`
	Sandbox   string    `json:"sandbox"`
	CreatedAt time.Time `json:"created_at"`
	SizeBytes int64     `json:"size_bytes"`
	Checksum  string    `json:"checksum"`
	Tags      []string  `json:"tags,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// BackupIndex holds the list of all backups in a backup repository.
type BackupIndex struct {
	Version int          `json:"version"`
	Backups []BackupMeta `json:"backups"`
}

func backupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Manage sandbox backups with versioning and retention",
		Long: `Create and manage versioned backups of sandboxes in a local backup
repository. Unlike 'tent export' which creates a single archive, backups
are tracked with metadata, checksums, and retention policies.

Subcommands:
  create   - Create a new backup of a sandbox
  list     - List all backups (optionally filtered by sandbox)
  restore  - Restore a sandbox from a backup
  delete   - Delete a specific backup
  prune    - Remove old backups based on retention policy
  info     - Show detailed information about a backup

The backup repository defaults to ~/.tent/backups/ and can be overridden
with the TENT_BACKUP_DIR environment variable.

Examples:
  tent backup create mybox
  tent backup create mybox --note "before migration"
  tent backup list
  tent backup list --sandbox mybox
  tent backup restore mybox abc123
  tent backup prune --keep 5
  tent backup prune --older-than 30d`,
	}

	cmd.AddCommand(backupCreateCmd())
	cmd.AddCommand(backupListCmd())
	cmd.AddCommand(backupRestoreCmd())
	cmd.AddCommand(backupDeleteCmd())
	cmd.AddCommand(backupPruneCmd())
	cmd.AddCommand(backupInfoCmd())

	return cmd
}

func backupDir() string {
	if dir := os.Getenv("TENT_BACKUP_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tent", "backups")
}

func loadBackupIndex(dir string) (*BackupIndex, error) {
	indexPath := filepath.Join(dir, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &BackupIndex{Version: 1}, nil
		}
		return nil, fmt.Errorf("failed to read backup index: %w", err)
	}
	var idx BackupIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("failed to parse backup index: %w", err)
	}
	return &idx, nil
}

func saveBackupIndex(dir string, idx *BackupIndex) error {
	indexPath := filepath.Join(dir, "index.json")
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal backup index: %w", err)
	}
	return os.WriteFile(indexPath, data, 0644)
}

func generateBackupID() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return hex.EncodeToString(h[:])[:12]
}

func checksumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func backupCreateCmd() *cobra.Command {
	var (
		note    string
		tags    []string
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "create <sandbox>",
		Short: "Create a backup of a sandbox",
		Long: `Create a versioned backup of a sandbox's data. The sandbox must be stopped.
Each backup is stored with a unique ID and checksum for integrity verification.

Examples:
  tent backup create mybox
  tent backup create mybox --note "pre-upgrade"
  tent backup create mybox --tag v1.0 --tag stable`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = filepath.Join(home, ".tent")
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

			bdir := backupDir()
			dataDir := filepath.Join(bdir, "data")
			if err := os.MkdirAll(dataDir, 0755); err != nil {
				return fmt.Errorf("failed to create backup directory: %w", err)
			}

			idx, err := loadBackupIndex(bdir)
			if err != nil {
				return err
			}

			id := generateBackupID()
			archivePath := filepath.Join(dataDir, fmt.Sprintf("%s-%s.tar.gz", name, id))

			if !jsonOut {
				fmt.Printf("Creating backup of sandbox %q...\n", name)
			}

			if err := manager.Export(name, archivePath); err != nil {
				return fmt.Errorf("backup failed: %w", err)
			}

			checksum, err := checksumFile(archivePath)
			if err != nil {
				return fmt.Errorf("failed to compute checksum: %w", err)
			}

			var sizeBytes int64
			if info, err := os.Stat(archivePath); err == nil {
				sizeBytes = info.Size()
			}

			meta := BackupMeta{
				ID:        id,
				Sandbox:   name,
				CreatedAt: time.Now().UTC(),
				SizeBytes: sizeBytes,
				Checksum:  checksum,
				Tags:      tags,
				Note:      note,
			}

			idx.Backups = append(idx.Backups, meta)
			if err := saveBackupIndex(bdir, idx); err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(meta)
			}

			sizeMB := float64(sizeBytes) / (1024 * 1024)
			fmt.Printf("Backup created: %s\n", id)
			if sizeMB >= 1 {
				fmt.Printf("  Size: %.1f MB\n", sizeMB)
			} else {
				fmt.Printf("  Size: %d bytes\n", sizeBytes)
			}
			fmt.Printf("  Checksum: %s\n", checksum[:16]+"...")
			if note != "" {
				fmt.Printf("  Note: %s\n", note)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&note, "note", "", "Add a note to the backup")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "Add tags to the backup (can be repeated)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func backupListCmd() *cobra.Command {
	var (
		sandbox string
		jsonOut bool
		tag     string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all backups",
		Long: `List backups in the backup repository, optionally filtered by sandbox or tag.

Examples:
  tent backup list
  tent backup list --sandbox mybox
  tent backup list --tag stable
  tent backup list --json`,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, err := loadBackupIndex(backupDir())
			if err != nil {
				return err
			}

			var filtered []BackupMeta
			for _, b := range idx.Backups {
				if sandbox != "" && b.Sandbox != sandbox {
					continue
				}
				if tag != "" {
					found := false
					for _, t := range b.Tags {
						if t == tag {
							found = true
							break
						}
					}
					if !found {
						continue
					}
				}
				filtered = append(filtered, b)
			}

			// Sort by creation time descending (newest first)
			sort.Slice(filtered, func(i, j int) bool {
				return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
			})

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(filtered)
			}

			if len(filtered) == 0 {
				fmt.Println("No backups found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "ID\tSANDBOX\tCREATED\tSIZE\tNOTE\n")
			for _, b := range filtered {
				age := backupFormatDuration(time.Since(b.CreatedAt))
				size := backupFormatSize(b.SizeBytes)
				noteStr := b.Note
				if len(noteStr) > 40 {
					noteStr = noteStr[:37] + "..."
				}
				fmt.Fprintf(w, "%s\t%s\t%s ago\t%s\t%s\n", b.ID, b.Sandbox, age, size, noteStr)
			}
			return w.Flush()
		},
	}

	cmd.Flags().StringVar(&sandbox, "sandbox", "", "Filter by sandbox name")
	cmd.Flags().StringVar(&tag, "tag", "", "Filter by tag")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func backupRestoreCmd() *cobra.Command {
	var (
		force   bool
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "restore <sandbox> <backup-id>",
		Short: "Restore a sandbox from a backup",
		Long: `Restore a sandbox from a previously created backup. If the sandbox already
exists, use --force to overwrite it. The backup's integrity is verified
before restoration.

Examples:
  tent backup restore mybox abc123def456
  tent backup restore mybox abc123def456 --force`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			backupID := args[1]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = filepath.Join(home, ".tent")
			}

			bdir := backupDir()
			idx, err := loadBackupIndex(bdir)
			if err != nil {
				return err
			}

			// Find the backup
			var meta *BackupMeta
			for i := range idx.Backups {
				if idx.Backups[i].ID == backupID && idx.Backups[i].Sandbox == name {
					meta = &idx.Backups[i]
					break
				}
			}
			// Also try matching by ID only (partial match)
			if meta == nil {
				for i := range idx.Backups {
					if strings.HasPrefix(idx.Backups[i].ID, backupID) {
						meta = &idx.Backups[i]
						break
					}
				}
			}

			if meta == nil {
				return fmt.Errorf("backup %q not found for sandbox %q", backupID, name)
			}

			archivePath := filepath.Join(bdir, "data", fmt.Sprintf("%s-%s.tar.gz", meta.Sandbox, meta.ID))
			if _, err := os.Stat(archivePath); os.IsNotExist(err) {
				return fmt.Errorf("backup archive not found at %s", archivePath)
			}

			// Verify checksum
			if !jsonOut {
				fmt.Printf("Verifying backup integrity...\n")
			}
			checksum, err := checksumFile(archivePath)
			if err != nil {
				return fmt.Errorf("failed to verify checksum: %w", err)
			}
			if checksum != meta.Checksum {
				return fmt.Errorf("checksum mismatch: backup may be corrupted")
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

			// Check if sandbox already exists
			if _, getErr := manager.Status(name); getErr == nil {
				if !force {
					return fmt.Errorf("sandbox %q already exists (use --force to overwrite)", name)
				}
				if !jsonOut {
					fmt.Printf("Destroying existing sandbox %q...\n", name)
				}
				if err := manager.Destroy(name); err != nil {
					return fmt.Errorf("failed to destroy existing sandbox: %w", err)
				}
			}

			if !jsonOut {
				fmt.Printf("Restoring sandbox %q from backup %s...\n", name, meta.ID)
			}

			if err := manager.Import(archivePath, name); err != nil {
				return fmt.Errorf("restore failed: %w", err)
			}

			if jsonOut {
				result := map[string]interface{}{
					"status":    "restored",
					"sandbox":   name,
					"backup_id": meta.ID,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			fmt.Printf("Restored sandbox %q from backup %s\n", name, meta.ID)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing sandbox")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func backupDeleteCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "delete <backup-id>",
		Short: "Delete a specific backup",
		Long: `Delete a backup by its ID. Both the archive file and the index entry are removed.

Examples:
  tent backup delete abc123def456`,
		Aliases: []string{"rm"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backupID := args[0]

			bdir := backupDir()
			idx, err := loadBackupIndex(bdir)
			if err != nil {
				return err
			}

			found := -1
			for i := range idx.Backups {
				if idx.Backups[i].ID == backupID || strings.HasPrefix(idx.Backups[i].ID, backupID) {
					found = i
					break
				}
			}

			if found < 0 {
				return fmt.Errorf("backup %q not found", backupID)
			}

			meta := idx.Backups[found]
			archivePath := filepath.Join(bdir, "data", fmt.Sprintf("%s-%s.tar.gz", meta.Sandbox, meta.ID))

			// Remove archive file
			if err := os.Remove(archivePath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to delete backup archive: %w", err)
			}

			// Remove from index
			idx.Backups = append(idx.Backups[:found], idx.Backups[found+1:]...)
			if err := saveBackupIndex(bdir, idx); err != nil {
				return err
			}

			if jsonOut {
				result := map[string]interface{}{
					"status":    "deleted",
					"backup_id": meta.ID,
					"sandbox":   meta.Sandbox,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			fmt.Printf("Deleted backup %s (sandbox: %s)\n", meta.ID, meta.Sandbox)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func backupPruneCmd() *cobra.Command {
	var (
		keep     int
		olderStr string
		sandbox  string
		dryRun   bool
		jsonOut  bool
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove old backups based on retention policy",
		Long: `Prune backups using a retention policy. You can keep a fixed number of
recent backups per sandbox, remove backups older than a duration, or both.

Duration format: Nd (days), e.g. 7d, 30d, 90d.

Examples:
  tent backup prune --keep 5
  tent backup prune --older-than 30d
  tent backup prune --keep 3 --sandbox mybox
  tent backup prune --older-than 7d --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if keep <= 0 && olderStr == "" {
				return fmt.Errorf("specify --keep <n> and/or --older-than <duration>")
			}

			var olderThan time.Duration
			if olderStr != "" {
				d, err := parseDurationDays(olderStr)
				if err != nil {
					return fmt.Errorf("invalid duration %q: %w", olderStr, err)
				}
				olderThan = d
			}

			bdir := backupDir()
			idx, err := loadBackupIndex(bdir)
			if err != nil {
				return err
			}

			now := time.Now().UTC()
			var toDelete []BackupMeta
			var remaining []BackupMeta

			// Group backups by sandbox
			groups := make(map[string][]BackupMeta)
			for _, b := range idx.Backups {
				groups[b.Sandbox] = append(groups[b.Sandbox], b)
			}

			for sbx, backups := range groups {
				if sandbox != "" && sbx != sandbox {
					remaining = append(remaining, backups...)
					continue
				}

				// Sort newest first
				sort.Slice(backups, func(i, j int) bool {
					return backups[i].CreatedAt.After(backups[j].CreatedAt)
				})

				for i, b := range backups {
					shouldDelete := false

					if olderThan > 0 && now.Sub(b.CreatedAt) > olderThan {
						shouldDelete = true
					}

					if keep > 0 && i >= keep {
						shouldDelete = true
					}

					if shouldDelete {
						toDelete = append(toDelete, b)
					} else {
						remaining = append(remaining, b)
					}
				}
			}

			if jsonOut {
				result := map[string]interface{}{
					"dry_run":  dryRun,
					"pruned":   len(toDelete),
					"retained": len(remaining),
					"deleted":  toDelete,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			if len(toDelete) == 0 {
				fmt.Println("No backups to prune.")
				return nil
			}

			if dryRun {
				fmt.Printf("Would prune %d backup(s):\n", len(toDelete))
				for _, b := range toDelete {
					fmt.Printf("  %s  %s  %s ago\n", b.ID, b.Sandbox, backupFormatDuration(now.Sub(b.CreatedAt)))
				}
				return nil
			}

			var totalFreed int64
			for _, b := range toDelete {
				archivePath := filepath.Join(bdir, "data", fmt.Sprintf("%s-%s.tar.gz", b.Sandbox, b.ID))
				if err := os.Remove(archivePath); err != nil && !os.IsNotExist(err) {
					fmt.Fprintf(os.Stderr, "Warning: failed to delete %s: %v\n", archivePath, err)
				}
				totalFreed += b.SizeBytes
			}

			idx.Backups = remaining
			if err := saveBackupIndex(bdir, idx); err != nil {
				return err
			}

			fmt.Printf("Pruned %d backup(s), freed %s\n", len(toDelete), backupFormatSize(totalFreed))
			return nil
		},
	}

	cmd.Flags().IntVar(&keep, "keep", 0, "Keep N most recent backups per sandbox")
	cmd.Flags().StringVar(&olderStr, "older-than", "", "Remove backups older than duration (e.g. 30d)")
	cmd.Flags().StringVar(&sandbox, "sandbox", "", "Only prune backups for a specific sandbox")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be pruned without deleting")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func backupInfoCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "info <backup-id>",
		Short: "Show detailed information about a backup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backupID := args[0]

			bdir := backupDir()
			idx, err := loadBackupIndex(bdir)
			if err != nil {
				return err
			}

			var meta *BackupMeta
			for i := range idx.Backups {
				if idx.Backups[i].ID == backupID || strings.HasPrefix(idx.Backups[i].ID, backupID) {
					meta = &idx.Backups[i]
					break
				}
			}

			if meta == nil {
				return fmt.Errorf("backup %q not found", backupID)
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(meta)
			}

			archivePath := filepath.Join(bdir, "data", fmt.Sprintf("%s-%s.tar.gz", meta.Sandbox, meta.ID))
			exists := "yes"
			if _, err := os.Stat(archivePath); os.IsNotExist(err) {
				exists = "no (missing)"
			}

			fmt.Printf("Backup ID:    %s\n", meta.ID)
			fmt.Printf("Sandbox:      %s\n", meta.Sandbox)
			fmt.Printf("Created:      %s\n", meta.CreatedAt.Format(time.RFC3339))
			fmt.Printf("Size:         %s\n", backupFormatSize(meta.SizeBytes))
			fmt.Printf("Checksum:     %s\n", meta.Checksum)
			fmt.Printf("Archive:      %s\n", exists)
			if len(meta.Tags) > 0 {
				fmt.Printf("Tags:         %s\n", strings.Join(meta.Tags, ", "))
			}
			if meta.Note != "" {
				fmt.Printf("Note:         %s\n", meta.Note)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

func parseDurationDays(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if !strings.HasSuffix(s, "d") {
		return 0, fmt.Errorf("expected format like '30d' (days)")
	}
	numStr := strings.TrimSuffix(s, "d")
	var days int
	if _, err := fmt.Sscanf(numStr, "%d", &days); err != nil {
		return 0, fmt.Errorf("invalid number: %s", numStr)
	}
	if days <= 0 {
		return 0, fmt.Errorf("days must be positive")
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

func backupFormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd", days)
}

func backupFormatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(bytes)/(1024*1024*1024))
}
