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
	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func systemCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "system",
		Short: "System-level commands",
	}

	cmd.AddCommand(systemInfoCmd())
	cmd.AddCommand(systemDfCmd())

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
