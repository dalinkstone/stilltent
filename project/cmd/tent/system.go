package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/image"
	"github.com/dalinkstone/tent/internal/network"
	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

func systemCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "system",
		Short: "System-level commands",
	}

	cmd.AddCommand(systemInfoCmd())
	cmd.AddCommand(systemDfCmd())
	cmd.AddCommand(systemPruneCmd())

	return cmd
}

// SystemInfo holds comprehensive system information
type SystemInfo struct {
	Version      string            `json:"version"`
	Commit       string            `json:"commit"`
	BuildDate    string            `json:"build_date"`
	GoVersion    string            `json:"go_version"`
	Platform     string            `json:"platform"`
	Arch         string            `json:"arch"`
	NumCPU       int               `json:"num_cpu"`
	Hypervisor   string            `json:"hypervisor"`
	BaseDir      string            `json:"base_dir"`
	Sandboxes    SandboxSummary    `json:"sandboxes"`
	Images       ImageSummary      `json:"images"`
	DiskUsage    DiskUsageSummary  `json:"disk_usage"`
}

// SandboxSummary summarizes sandbox counts by status
type SandboxSummary struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Stopped int `json:"stopped"`
	Paused  int `json:"paused"`
}

// ImageSummary summarizes image counts
type ImageSummary struct {
	Total int `json:"total"`
}

// DiskUsageSummary shows disk usage by category
type DiskUsageSummary struct {
	SandboxesBytes int64  `json:"sandboxes_bytes"`
	ImagesBytes    int64  `json:"images_bytes"`
	TotalBytes     int64  `json:"total_bytes"`
	TotalHuman     string `json:"total_human"`
}

func systemInfoCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Display system-wide information",
		Long: `Show tent system information including version, hypervisor, platform details,
sandbox and image counts, and disk usage.

Examples:
  tent system info
  tent system info --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			info := SystemInfo{
				Version:   version,
				Commit:    commit,
				BuildDate: buildDate,
				GoVersion: runtime.Version(),
				Platform:  runtime.GOOS,
				Arch:      runtime.GOARCH,
				NumCPU:    runtime.NumCPU(),
				BaseDir:   baseDir,
			}

			// Determine hypervisor
			switch runtime.GOOS {
			case "darwin":
				info.Hypervisor = "Apple Hypervisor.framework (HVF)"
			case "linux":
				if _, err := os.Stat("/dev/kvm"); err == nil {
					info.Hypervisor = "KVM"
				} else {
					info.Hypervisor = "KVM (not available)"
				}
			default:
				info.Hypervisor = "none"
			}

			// Count sandboxes by status
			info.Sandboxes = countSandboxes(baseDir)

			// Count images
			info.Images = countImages(baseDir)

			// Calculate disk usage
			info.DiskUsage = calculateDiskUsage(baseDir)

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}

			printSystemInfo(info)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

// DfEntry represents a disk usage entry for the df command
type DfEntry struct {
	Type       string `json:"type"`
	Name       string `json:"name"`
	SizeBytes  int64  `json:"size_bytes"`
	SizeHuman  string `json:"size_human"`
	Created    string `json:"created,omitempty"`
}

func systemDfCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "df",
		Short: "Show disk usage by sandboxes and images",
		Long: `Show detailed disk space usage broken down by individual sandboxes and images.

Examples:
  tent system df
  tent system df --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			var entries []DfEntry

			// Sandbox disk usage
			sandboxDir := filepath.Join(baseDir, "sandboxes")
			if dirs, err := os.ReadDir(sandboxDir); err == nil {
				for _, d := range dirs {
					if !d.IsDir() {
						continue
					}
					path := filepath.Join(sandboxDir, d.Name())
					size := dirSize(path)
					entry := DfEntry{
						Type:      "sandbox",
						Name:      d.Name(),
						SizeBytes: size,
						SizeHuman: humanSize(size),
					}
					if info, err := d.Info(); err == nil {
						entry.Created = info.ModTime().Format(time.RFC3339)
					}
					entries = append(entries, entry)
				}
			}

			// Image disk usage
			imageDir := filepath.Join(baseDir, "images")
			if dirs, err := os.ReadDir(imageDir); err == nil {
				for _, d := range dirs {
					if !d.IsDir() {
						continue
					}
					path := filepath.Join(imageDir, d.Name())
					size := dirSize(path)
					entry := DfEntry{
						Type:      "image",
						Name:      d.Name(),
						SizeBytes: size,
						SizeHuman: humanSize(size),
					}
					if info, err := d.Info(); err == nil {
						entry.Created = info.ModTime().Format(time.RFC3339)
					}
					entries = append(entries, entry)
				}
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}

			if len(entries) == 0 {
				fmt.Println("No sandboxes or images found.")
				return nil
			}

			// Print sandboxes
			var sandboxEntries, imageEntries []DfEntry
			var sandboxTotal, imageTotal int64
			for _, e := range entries {
				switch e.Type {
				case "sandbox":
					sandboxEntries = append(sandboxEntries, e)
					sandboxTotal += e.SizeBytes
				case "image":
					imageEntries = append(imageEntries, e)
					imageTotal += e.SizeBytes
				}
			}

			if len(sandboxEntries) > 0 {
				fmt.Println("SANDBOXES:")
				fmt.Printf("  %-30s %10s\n", "NAME", "SIZE")
				for _, e := range sandboxEntries {
					fmt.Printf("  %-30s %10s\n", e.Name, e.SizeHuman)
				}
				fmt.Printf("  %-30s %10s\n", "Total", humanSize(sandboxTotal))
				fmt.Println()
			}

			if len(imageEntries) > 0 {
				fmt.Println("IMAGES:")
				fmt.Printf("  %-30s %10s\n", "NAME", "SIZE")
				for _, e := range imageEntries {
					fmt.Printf("  %-30s %10s\n", e.Name, e.SizeHuman)
				}
				fmt.Printf("  %-30s %10s\n", "Total", humanSize(imageTotal))
				fmt.Println()
			}

			fmt.Printf("Total disk usage: %s\n", humanSize(sandboxTotal+imageTotal))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func systemPruneCmd() *cobra.Command {
	var (
		force      bool
		all        bool
		volumes    bool
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove unused sandboxes, images, and networks",
		Long: `Remove stopped sandboxes, unused images, and empty custom networks in one operation.

By default, only stopped/errored sandboxes and images not referenced by any
sandbox are removed. Use --all to also remove paused sandboxes. Use --volumes
to also remove snapshot data associated with pruned sandboxes.

Examples:
  tent system prune
  tent system prune --force
  tent system prune --all --volumes
  tent system prune --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			// Phase 1: Identify stopped sandboxes
			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create sandbox manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup sandbox manager: %w", err)
			}

			vms, err := manager.List()
			if err != nil {
				return fmt.Errorf("failed to list sandboxes: %w", err)
			}

			var sandboxCandidates []*models.VMState
			activeImageRefs := make(map[string]bool)

			for _, v := range vms {
				removable := v.Status == models.VMStatusStopped ||
					v.Status == models.VMStatusCreated ||
					v.Status == models.VMStatusError
				if all && v.Status == models.VMStatusPaused {
					removable = true
				}

				if removable {
					sandboxCandidates = append(sandboxCandidates, v)
				} else {
					// Track images in use by non-removable sandboxes
					if v.ImageRef != "" {
						activeImageRefs[v.ImageRef] = true
					}
				}
			}

			// Phase 2: Identify unused images
			imgMgr, err := image.NewManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create image manager: %w", err)
			}

			allImages, err := imgMgr.ListImages()
			if err != nil {
				return fmt.Errorf("failed to list images: %w", err)
			}

			var unusedImages []string
			for _, img := range allImages {
				if !activeImageRefs[img.Name] {
					unusedImages = append(unusedImages, img.Name)
				}
			}

			// Phase 3: Identify empty custom networks
			netStore, err := network.NewNetworkStore(baseDir)
			if err != nil {
				return fmt.Errorf("failed to load network store: %w", err)
			}

			allNets := netStore.ListNetworks()
			var emptyNets []string
			for _, n := range allNets {
				if len(n.Sandboxes) == 0 {
					emptyNets = append(emptyNets, n.Name)
				}
			}

			// Nothing to do?
			if len(sandboxCandidates) == 0 && len(unusedImages) == 0 && len(emptyNets) == 0 {
				if jsonOutput {
					fmt.Println("{}")
				} else {
					fmt.Println("Nothing to prune.")
				}
				return nil
			}

			// Confirm
			if !force {
				fmt.Println("The following resources will be removed:")
				if len(sandboxCandidates) > 0 {
					fmt.Printf("\n  Sandboxes (%d):\n", len(sandboxCandidates))
					for _, v := range sandboxCandidates {
						fmt.Printf("    - %s (%s)\n", v.Name, v.Status)
					}
				}
				if len(unusedImages) > 0 {
					fmt.Printf("\n  Images (%d):\n", len(unusedImages))
					for _, name := range unusedImages {
						fmt.Printf("    - %s\n", name)
					}
				}
				if len(emptyNets) > 0 {
					fmt.Printf("\n  Networks (%d):\n", len(emptyNets))
					for _, name := range emptyNets {
						fmt.Printf("    - %s\n", name)
					}
				}
				if volumes {
					fmt.Println("\n  (Snapshot data for pruned sandboxes will also be removed)")
				}
				fmt.Print("\nProceed? [y/N] ")

				var response string
				fmt.Scanln(&response)
				if response != "y" && response != "Y" && response != "yes" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			// Execute pruning
			result := PruneResult{}

			// Remove sandboxes
			for _, v := range sandboxCandidates {
				size := dirSize(filepath.Join(baseDir, "sandboxes", v.Name))
				if err := manager.Destroy(v.Name); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("sandbox %s: %v", v.Name, err))
				} else {
					result.SandboxesRemoved = append(result.SandboxesRemoved, v.Name)
					result.SpaceReclaimed += size
				}

				// Remove snapshot data if --volumes
				if volumes {
					snapshotDir := filepath.Join(baseDir, "snapshots", v.Name)
					snapSize := dirSize(snapshotDir)
					if snapSize > 0 {
						os.RemoveAll(snapshotDir)
						result.SpaceReclaimed += snapSize
					}
				}
			}

			// Remove unused images
			for _, name := range unusedImages {
				imgPath := filepath.Join(baseDir, "images", name)
				size := dirSize(imgPath)
				if err := imgMgr.RemoveImage(name); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("image %s: %v", name, err))
				} else {
					result.ImagesRemoved = append(result.ImagesRemoved, name)
					result.SpaceReclaimed += size
				}
			}

			// Remove empty networks
			for _, name := range emptyNets {
				if err := netStore.DeleteNetwork(name); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("network %s: %v", name, err))
				} else {
					result.NetworksRemoved = append(result.NetworksRemoved, name)
				}
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			// Print results
			if len(result.SandboxesRemoved) > 0 {
				fmt.Printf("Removed %d sandbox(es):\n", len(result.SandboxesRemoved))
				for _, name := range result.SandboxesRemoved {
					fmt.Printf("  - %s\n", name)
				}
			}
			if len(result.ImagesRemoved) > 0 {
				fmt.Printf("Removed %d image(s):\n", len(result.ImagesRemoved))
				for _, name := range result.ImagesRemoved {
					fmt.Printf("  - %s\n", name)
				}
			}
			if len(result.NetworksRemoved) > 0 {
				fmt.Printf("Removed %d network(s):\n", len(result.NetworksRemoved))
				for _, name := range result.NetworksRemoved {
					fmt.Printf("  - %s\n", name)
				}
			}
			if len(result.Errors) > 0 {
				fmt.Printf("\n%d error(s):\n", len(result.Errors))
				for _, e := range result.Errors {
					fmt.Printf("  ! %s\n", e)
				}
			}
			fmt.Printf("\nTotal reclaimed space: %s\n", humanSize(result.SpaceReclaimed))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&all, "all", false, "Also remove paused sandboxes")
	cmd.Flags().BoolVar(&volumes, "volumes", false, "Also remove snapshot data for pruned sandboxes")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output results in JSON format")
	return cmd
}

// PruneResult contains the results of a system prune operation.
type PruneResult struct {
	SandboxesRemoved []string `json:"sandboxes_removed,omitempty"`
	ImagesRemoved    []string `json:"images_removed,omitempty"`
	NetworksRemoved  []string `json:"networks_removed,omitempty"`
	SpaceReclaimed   int64    `json:"space_reclaimed_bytes"`
	Errors           []string `json:"errors,omitempty"`
}

func printSystemInfo(info SystemInfo) {
	fmt.Printf("tent version:    %s\n", info.Version)
	fmt.Printf("  Commit:        %s\n", info.Commit)
	fmt.Printf("  Built:         %s\n", info.BuildDate)
	fmt.Printf("  Go version:    %s\n", info.GoVersion)
	fmt.Printf("Platform:        %s/%s\n", info.Platform, info.Arch)
	fmt.Printf("  CPUs:          %d\n", info.NumCPU)
	fmt.Printf("  Hypervisor:    %s\n", info.Hypervisor)
	fmt.Printf("Base directory:  %s\n", info.BaseDir)
	fmt.Printf("Sandboxes:       %d (%d running, %d stopped, %d paused)\n",
		info.Sandboxes.Total, info.Sandboxes.Running,
		info.Sandboxes.Stopped, info.Sandboxes.Paused)
	fmt.Printf("Images:          %d\n", info.Images.Total)
	fmt.Printf("Disk usage:      %s\n", info.DiskUsage.TotalHuman)
	if info.DiskUsage.SandboxesBytes > 0 || info.DiskUsage.ImagesBytes > 0 {
		fmt.Printf("  Sandboxes:     %s\n", humanSize(info.DiskUsage.SandboxesBytes))
		fmt.Printf("  Images:        %s\n", humanSize(info.DiskUsage.ImagesBytes))
	}
}

func countSandboxes(baseDir string) SandboxSummary {
	summary := SandboxSummary{}

	hvBackend, err := vm.NewPlatformBackend(baseDir)
	if err != nil {
		return summary
	}

	manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
	if err != nil {
		return summary
	}

	if err := manager.Setup(); err != nil {
		return summary
	}

	sandboxes, err := manager.List()
	if err != nil {
		return summary
	}

	for _, sb := range sandboxes {
		summary.Total++
		status := strings.ToLower(string(sb.Status))
		switch {
		case status == "running":
			summary.Running++
		case status == "paused":
			summary.Paused++
		default:
			summary.Stopped++
		}
	}
	return summary
}

func countImages(baseDir string) ImageSummary {
	summary := ImageSummary{}

	mgr, err := image.NewManager(baseDir)
	if err != nil {
		return summary
	}

	images, err := mgr.ListImages()
	if err != nil {
		return summary
	}

	summary.Total = len(images)
	return summary
}

func calculateDiskUsage(baseDir string) DiskUsageSummary {
	du := DiskUsageSummary{}

	sandboxDir := filepath.Join(baseDir, "sandboxes")
	du.SandboxesBytes = dirSize(sandboxDir)

	imageDir := filepath.Join(baseDir, "images")
	du.ImagesBytes = dirSize(imageDir)

	du.TotalBytes = du.SandboxesBytes + du.ImagesBytes
	du.TotalHuman = humanSize(du.TotalBytes)
	return du
}

func dirSize(path string) int64 {
	var size int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size
}

func humanSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
