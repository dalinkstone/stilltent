// Package vm provides the resource limiter for sandbox VMs.
// This implements CPU, memory, I/O, and process limits using platform-appropriate
// mechanisms: cgroups v2 on Linux, and process-level limits on macOS.
package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/dalinkstone/tent/pkg/models"
)

// ResourceLimiter manages resource constraints for sandbox VMs.
type ResourceLimiter struct {
	baseDir string
}

// NewResourceLimiter creates a new resource limiter.
func NewResourceLimiter(baseDir string) *ResourceLimiter {
	return &ResourceLimiter{baseDir: baseDir}
}

// AppliedLimits stores the computed limits that were applied to a sandbox.
type AppliedLimits struct {
	Name             string `json:"name"`
	CPUWeight        int    `json:"cpu_weight,omitempty"`
	CPUMaxPercent    int    `json:"cpu_max_percent,omitempty"`
	MemoryMaxBytes   int64  `json:"memory_max_bytes,omitempty"`
	SwapMaxBytes     int64  `json:"swap_max_bytes,omitempty"`
	IOReadBPS        int64  `json:"io_read_bps,omitempty"`
	IOWriteBPS       int64  `json:"io_write_bps,omitempty"`
	IOReadIOPS       int64  `json:"io_read_iops,omitempty"`
	IOWriteIOPS      int64  `json:"io_write_iops,omitempty"`
	NetworkBwMbps    int    `json:"network_bandwidth_mbps,omitempty"`
	PidsMax          int    `json:"pids_max,omitempty"`
	Platform         string `json:"platform"`
	EnforcementLevel string `json:"enforcement_level"`
}

// ApplyLimits computes and applies resource limits for a sandbox.
// On Linux with cgroups v2, limits are fully enforced.
// On macOS, limits are recorded and best-effort (hypervisor-level constraints).
func (rl *ResourceLimiter) ApplyLimits(name string, config *models.VMConfig) (*AppliedLimits, error) {
	if config.Resources == nil {
		return nil, nil
	}

	limits := config.Resources
	if err := limits.Validate(); err != nil {
		return nil, fmt.Errorf("invalid resource limits: %w", err)
	}

	applied := &AppliedLimits{
		Name:     name,
		Platform: runtime.GOOS,
	}

	// CPU weight (default 1024 if not specified)
	if limits.CPUWeight > 0 {
		applied.CPUWeight = limits.CPUWeight
	} else {
		applied.CPUWeight = 1024
	}

	// CPU max percent
	if limits.CPUMaxPercent > 0 {
		applied.CPUMaxPercent = limits.CPUMaxPercent
	}

	// Memory limits
	if limits.MemoryMaxMB > 0 {
		applied.MemoryMaxBytes = int64(limits.MemoryMaxMB) * 1024 * 1024
	} else if config.MemoryMB > 0 {
		// Default to VM memory allocation
		applied.MemoryMaxBytes = int64(config.MemoryMB) * 1024 * 1024
	}

	if limits.MemorySwapMaxMB > 0 {
		applied.SwapMaxBytes = int64(limits.MemorySwapMaxMB) * 1024 * 1024
	}

	// I/O limits
	applied.IOReadBPS = limits.IOReadBPS
	applied.IOWriteBPS = limits.IOWriteBPS
	applied.IOReadIOPS = limits.IOReadIOPS
	applied.IOWriteIOPS = limits.IOWriteIOPS

	// Network bandwidth
	applied.NetworkBwMbps = limits.NetworkBandwidthMbps

	// PID limit
	applied.PidsMax = limits.PidsMax

	// Platform-specific enforcement
	switch runtime.GOOS {
	case "linux":
		applied.EnforcementLevel = "full"
		if err := rl.applyLinuxCgroupLimits(name, applied); err != nil {
			// Degrade to advisory if cgroup setup fails
			applied.EnforcementLevel = "advisory"
			fmt.Fprintf(os.Stderr, "warning: cgroup enforcement unavailable for %s: %v\n", name, err)
		}
	case "darwin":
		applied.EnforcementLevel = "advisory"
		// macOS doesn't have cgroups — limits are enforced at the hypervisor level
		// via Virtualization.framework resource controls where available
	default:
		applied.EnforcementLevel = "advisory"
	}

	// Persist applied limits
	if err := rl.saveLimits(name, applied); err != nil {
		return applied, fmt.Errorf("failed to persist limits: %w", err)
	}

	return applied, nil
}

// RemoveLimits cleans up resource limits for a sandbox.
func (rl *ResourceLimiter) RemoveLimits(name string) error {
	if runtime.GOOS == "linux" {
		rl.cleanupLinuxCgroup(name)
	}

	limitsFile := filepath.Join(rl.baseDir, "sandboxes", name, "resource_limits.json")
	os.Remove(limitsFile)
	return nil
}

// GetLimits returns the currently applied limits for a sandbox.
func (rl *ResourceLimiter) GetLimits(name string) (*AppliedLimits, error) {
	limitsFile := filepath.Join(rl.baseDir, "sandboxes", name, "resource_limits.json")
	data, err := os.ReadFile(limitsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var applied AppliedLimits
	if err := json.Unmarshal(data, &applied); err != nil {
		return nil, fmt.Errorf("corrupt limits file: %w", err)
	}
	return &applied, nil
}

// saveLimits persists applied limits to disk.
func (rl *ResourceLimiter) saveLimits(name string, applied *AppliedLimits) error {
	dir := filepath.Join(rl.baseDir, "sandboxes", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(applied, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "resource_limits.json"), data, 0o644)
}

// FormatLimits returns a human-readable summary of applied limits.
func FormatLimits(al *AppliedLimits) string {
	if al == nil {
		return "  (no resource limits configured)"
	}

	s := fmt.Sprintf("  Enforcement: %s (%s)\n", al.EnforcementLevel, al.Platform)
	if al.CPUWeight != 0 {
		s += fmt.Sprintf("  CPU weight:  %d\n", al.CPUWeight)
	}
	if al.CPUMaxPercent > 0 {
		s += fmt.Sprintf("  CPU max:     %d%%\n", al.CPUMaxPercent)
	}
	if al.MemoryMaxBytes > 0 {
		s += fmt.Sprintf("  Memory max:  %d MB\n", al.MemoryMaxBytes/(1024*1024))
	}
	if al.SwapMaxBytes > 0 {
		s += fmt.Sprintf("  Swap max:    %d MB\n", al.SwapMaxBytes/(1024*1024))
	}
	if al.IOReadBPS > 0 {
		s += fmt.Sprintf("  IO read:     %d B/s\n", al.IOReadBPS)
	}
	if al.IOWriteBPS > 0 {
		s += fmt.Sprintf("  IO write:    %d B/s\n", al.IOWriteBPS)
	}
	if al.IOReadIOPS > 0 {
		s += fmt.Sprintf("  IO read:     %d IOPS\n", al.IOReadIOPS)
	}
	if al.IOWriteIOPS > 0 {
		s += fmt.Sprintf("  IO write:    %d IOPS\n", al.IOWriteIOPS)
	}
	if al.NetworkBwMbps > 0 {
		s += fmt.Sprintf("  Network:     %d Mbps\n", al.NetworkBwMbps)
	}
	if al.PidsMax > 0 {
		s += fmt.Sprintf("  PIDs max:    %d\n", al.PidsMax)
	}
	return s
}
