package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// QuotaConfig defines global resource quotas for all sandboxes.
type QuotaConfig struct {
	// MaxSandboxes is the maximum number of sandboxes allowed (0 = unlimited).
	MaxSandboxes int `json:"max_sandboxes,omitempty"`
	// MaxTotalVCPUs is the maximum total vCPUs across all sandboxes (0 = unlimited).
	MaxTotalVCPUs int `json:"max_total_vcpus,omitempty"`
	// MaxTotalMemoryMB is the maximum total memory in MB across all sandboxes (0 = unlimited).
	MaxTotalMemoryMB int `json:"max_total_memory_mb,omitempty"`
	// MaxTotalDiskGB is the maximum total disk in GB across all sandboxes (0 = unlimited).
	MaxTotalDiskGB int `json:"max_total_disk_gb,omitempty"`
	// MaxVCPUsPerSandbox is the maximum vCPUs for a single sandbox (0 = unlimited).
	MaxVCPUsPerSandbox int `json:"max_vcpus_per_sandbox,omitempty"`
	// MaxMemoryPerSandboxMB is the maximum memory in MB for a single sandbox (0 = unlimited).
	MaxMemoryPerSandboxMB int `json:"max_memory_per_sandbox_mb,omitempty"`
	// MaxDiskPerSandboxGB is the maximum disk in GB for a single sandbox (0 = unlimited).
	MaxDiskPerSandboxGB int `json:"max_disk_per_sandbox_gb,omitempty"`
}

// QuotaUsage shows current resource usage vs quota limits.
type QuotaUsage struct {
	Sandboxes    QuotaItem `json:"sandboxes"`
	TotalVCPUs   QuotaItem `json:"total_vcpus"`
	TotalMemory  QuotaItem `json:"total_memory_mb"`
	TotalDisk    QuotaItem `json:"total_disk_gb"`
}

// QuotaItem represents a single quota metric with current/limit values.
type QuotaItem struct {
	Current int    `json:"current"`
	Limit   int    `json:"limit"`
	Status  string `json:"status"` // "ok", "warning", "exceeded"
}

// QuotaManager manages global resource quotas.
type QuotaManager struct {
	configPath string
	mu         sync.Mutex
}

// NewQuotaManager creates a new quota manager for the given base directory.
func NewQuotaManager(baseDir string) *QuotaManager {
	return &QuotaManager{
		configPath: filepath.Join(baseDir, "quota.json"),
	}
}

// Get returns the current quota configuration.
func (qm *QuotaManager) Get() (*QuotaConfig, error) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	data, err := os.ReadFile(qm.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &QuotaConfig{}, nil
		}
		return nil, fmt.Errorf("failed to read quota config: %w", err)
	}

	var cfg QuotaConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse quota config: %w", err)
	}

	return &cfg, nil
}

// Set saves a quota configuration.
func (qm *QuotaManager) Set(cfg *QuotaConfig) error {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	if err := cfg.Validate(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(qm.configPath), 0755); err != nil {
		return fmt.Errorf("failed to create quota directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal quota config: %w", err)
	}

	if err := os.WriteFile(qm.configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write quota config: %w", err)
	}

	return nil
}

// Reset removes all quota limits.
func (qm *QuotaManager) Reset() error {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	if err := os.Remove(qm.configPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove quota config: %w", err)
	}
	return nil
}

// CheckCreate validates that creating a new sandbox with the given resources
// would not exceed any quota limits. Pass the current list of VMs.
func (qm *QuotaManager) CheckCreate(cfg *QuotaConfig, currentVMs int, totalVCPUs, totalMemoryMB, totalDiskGB int, newVCPUs, newMemoryMB, newDiskGB int) []string {
	var violations []string

	if cfg.MaxSandboxes > 0 && currentVMs+1 > cfg.MaxSandboxes {
		violations = append(violations, fmt.Sprintf("sandbox count would exceed quota: %d/%d", currentVMs+1, cfg.MaxSandboxes))
	}

	if cfg.MaxTotalVCPUs > 0 && totalVCPUs+newVCPUs > cfg.MaxTotalVCPUs {
		violations = append(violations, fmt.Sprintf("total vCPUs would exceed quota: %d/%d", totalVCPUs+newVCPUs, cfg.MaxTotalVCPUs))
	}

	if cfg.MaxTotalMemoryMB > 0 && totalMemoryMB+newMemoryMB > cfg.MaxTotalMemoryMB {
		violations = append(violations, fmt.Sprintf("total memory would exceed quota: %dMB/%dMB", totalMemoryMB+newMemoryMB, cfg.MaxTotalMemoryMB))
	}

	if cfg.MaxTotalDiskGB > 0 && totalDiskGB+newDiskGB > cfg.MaxTotalDiskGB {
		violations = append(violations, fmt.Sprintf("total disk would exceed quota: %dGB/%dGB", totalDiskGB+newDiskGB, cfg.MaxTotalDiskGB))
	}

	if cfg.MaxVCPUsPerSandbox > 0 && newVCPUs > cfg.MaxVCPUsPerSandbox {
		violations = append(violations, fmt.Sprintf("sandbox vCPUs exceed per-sandbox limit: %d/%d", newVCPUs, cfg.MaxVCPUsPerSandbox))
	}

	if cfg.MaxMemoryPerSandboxMB > 0 && newMemoryMB > cfg.MaxMemoryPerSandboxMB {
		violations = append(violations, fmt.Sprintf("sandbox memory exceeds per-sandbox limit: %dMB/%dMB", newMemoryMB, cfg.MaxMemoryPerSandboxMB))
	}

	if cfg.MaxDiskPerSandboxGB > 0 && newDiskGB > cfg.MaxDiskPerSandboxGB {
		violations = append(violations, fmt.Sprintf("sandbox disk exceeds per-sandbox limit: %dGB/%dGB", newDiskGB, cfg.MaxDiskPerSandboxGB))
	}

	return violations
}

// ComputeUsage calculates current resource usage against quota limits.
func (qm *QuotaManager) ComputeUsage(cfg *QuotaConfig, sandboxCount, totalVCPUs, totalMemoryMB, totalDiskGB int) *QuotaUsage {
	usage := &QuotaUsage{
		Sandboxes: QuotaItem{
			Current: sandboxCount,
			Limit:   cfg.MaxSandboxes,
			Status:  quotaStatus(sandboxCount, cfg.MaxSandboxes),
		},
		TotalVCPUs: QuotaItem{
			Current: totalVCPUs,
			Limit:   cfg.MaxTotalVCPUs,
			Status:  quotaStatus(totalVCPUs, cfg.MaxTotalVCPUs),
		},
		TotalMemory: QuotaItem{
			Current: totalMemoryMB,
			Limit:   cfg.MaxTotalMemoryMB,
			Status:  quotaStatus(totalMemoryMB, cfg.MaxTotalMemoryMB),
		},
		TotalDisk: QuotaItem{
			Current: totalDiskGB,
			Limit:   cfg.MaxTotalDiskGB,
			Status:  quotaStatus(totalDiskGB, cfg.MaxTotalDiskGB),
		},
	}
	return usage
}

// quotaStatus returns the status string for a quota metric.
func quotaStatus(current, limit int) string {
	if limit <= 0 {
		return "unlimited"
	}
	if current > limit {
		return "exceeded"
	}
	// Warning at 80% utilization
	if current*100 >= limit*80 {
		return "warning"
	}
	return "ok"
}

// Validate checks quota values are non-negative.
func (cfg *QuotaConfig) Validate() error {
	if cfg.MaxSandboxes < 0 {
		return fmt.Errorf("max_sandboxes must be non-negative")
	}
	if cfg.MaxTotalVCPUs < 0 {
		return fmt.Errorf("max_total_vcpus must be non-negative")
	}
	if cfg.MaxTotalMemoryMB < 0 {
		return fmt.Errorf("max_total_memory_mb must be non-negative")
	}
	if cfg.MaxTotalDiskGB < 0 {
		return fmt.Errorf("max_total_disk_gb must be non-negative")
	}
	if cfg.MaxVCPUsPerSandbox < 0 {
		return fmt.Errorf("max_vcpus_per_sandbox must be non-negative")
	}
	if cfg.MaxMemoryPerSandboxMB < 0 {
		return fmt.Errorf("max_memory_per_sandbox_mb must be non-negative")
	}
	if cfg.MaxDiskPerSandboxGB < 0 {
		return fmt.Errorf("max_disk_per_sandbox_gb must be non-negative")
	}
	return nil
}
