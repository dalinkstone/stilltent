package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// UsageRecord tracks cumulative resource usage for a single sandbox.
type UsageRecord struct {
	Sandbox      string        `json:"sandbox"`
	CPUSeconds   float64       `json:"cpu_seconds"`     // Total vCPU-seconds consumed
	MemoryMBHrs  float64       `json:"memory_mb_hours"` // Memory-MB * hours allocated
	NetTxBytes   uint64        `json:"net_tx_bytes"`    // Total bytes transmitted
	NetRxBytes   uint64        `json:"net_rx_bytes"`    // Total bytes received
	DiskReadOps  uint64        `json:"disk_read_ops"`   // Total disk read operations
	DiskWriteOps uint64        `json:"disk_write_ops"`  // Total disk write operations
	DiskUsedMB   int64         `json:"disk_used_mb"`    // Current disk usage
	UptimeSec    float64       `json:"uptime_seconds"`  // Total running time in seconds
	VCPUs        int           `json:"vcpus"`           // Current vCPU count
	MemoryMB     int           `json:"memory_mb"`       // Current memory allocation
	Sessions     int           `json:"sessions"`        // Number of start/stop cycles
	LastStart    *time.Time    `json:"last_start,omitempty"`
	LastStop     *time.Time    `json:"last_stop,omitempty"`
	UpdatedAt    time.Time     `json:"updated_at"`
	Intervals    []UsageInterval `json:"intervals,omitempty"` // Hourly usage buckets
}

// UsageInterval represents resource usage within a time bucket.
type UsageInterval struct {
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	CPUSeconds  float64   `json:"cpu_seconds"`
	MemoryMBHrs float64   `json:"memory_mb_hours"`
	NetTxBytes  uint64    `json:"net_tx_bytes"`
	NetRxBytes  uint64    `json:"net_rx_bytes"`
}

// UsageReport summarizes resource usage across sandboxes.
type UsageReport struct {
	GeneratedAt   time.Time              `json:"generated_at"`
	PeriodStart   time.Time              `json:"period_start"`
	PeriodEnd     time.Time              `json:"period_end"`
	TotalCPUSec   float64                `json:"total_cpu_seconds"`
	TotalMemMBHrs float64                `json:"total_memory_mb_hours"`
	TotalNetTx    uint64                 `json:"total_net_tx_bytes"`
	TotalNetRx    uint64                 `json:"total_net_rx_bytes"`
	TotalDiskMB   int64                  `json:"total_disk_used_mb"`
	Sandboxes     map[string]*UsageRecord `json:"sandboxes"`
}

// AccountingManager tracks resource usage for all sandboxes.
type AccountingManager struct {
	baseDir string
	records map[string]*UsageRecord
	mu      sync.Mutex
}

// NewAccountingManager creates a new accounting manager.
func NewAccountingManager(baseDir string) (*AccountingManager, error) {
	am := &AccountingManager{
		baseDir: baseDir,
		records: make(map[string]*UsageRecord),
	}
	if err := am.load(); err != nil {
		// Start fresh if we can't load
		am.records = make(map[string]*UsageRecord)
	}
	return am, nil
}

// accountingPath returns the path to the accounting data file.
func (am *AccountingManager) accountingPath() string {
	return filepath.Join(am.baseDir, "accounting.json")
}

// load reads accounting records from disk.
func (am *AccountingManager) load() error {
	data, err := os.ReadFile(am.accountingPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &am.records)
}

// save persists accounting records to disk.
func (am *AccountingManager) save() error {
	dir := filepath.Dir(am.accountingPath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(am.records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(am.accountingPath(), data, 0644)
}

// RecordStart records that a sandbox has started running.
func (am *AccountingManager) RecordStart(name string, vcpus int, memoryMB int) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	rec := am.getOrCreate(name)
	now := time.Now().UTC()
	rec.LastStart = &now
	rec.VCPUs = vcpus
	rec.MemoryMB = memoryMB
	rec.Sessions++
	rec.UpdatedAt = now

	return am.save()
}

// RecordStop records that a sandbox has stopped, updating cumulative usage.
func (am *AccountingManager) RecordStop(name string) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	rec := am.getOrCreate(name)
	now := time.Now().UTC()
	rec.LastStop = &now

	if rec.LastStart != nil {
		duration := now.Sub(*rec.LastStart)
		seconds := duration.Seconds()

		rec.UptimeSec += seconds
		rec.CPUSeconds += seconds * float64(rec.VCPUs)
		rec.MemoryMBHrs += float64(rec.MemoryMB) * (seconds / 3600.0)

		// Add interval
		rec.Intervals = append(rec.Intervals, UsageInterval{
			Start:       *rec.LastStart,
			End:         now,
			CPUSeconds:  seconds * float64(rec.VCPUs),
			MemoryMBHrs: float64(rec.MemoryMB) * (seconds / 3600.0),
		})

		// Keep only the most recent 720 intervals (30 days of hourly)
		if len(rec.Intervals) > 720 {
			rec.Intervals = rec.Intervals[len(rec.Intervals)-720:]
		}
	}

	rec.UpdatedAt = now
	return am.save()
}

// RecordNetworkIO adds network I/O counters.
func (am *AccountingManager) RecordNetworkIO(name string, txBytes, rxBytes uint64) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	rec := am.getOrCreate(name)
	rec.NetTxBytes += txBytes
	rec.NetRxBytes += rxBytes
	rec.UpdatedAt = time.Now().UTC()

	return am.save()
}

// RecordDiskIO adds disk I/O counters.
func (am *AccountingManager) RecordDiskIO(name string, readOps, writeOps uint64) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	rec := am.getOrCreate(name)
	rec.DiskReadOps += readOps
	rec.DiskWriteOps += writeOps
	rec.UpdatedAt = time.Now().UTC()

	return am.save()
}

// UpdateDiskUsage records current disk usage for a sandbox.
func (am *AccountingManager) UpdateDiskUsage(name string, usedMB int64) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	rec := am.getOrCreate(name)
	rec.DiskUsedMB = usedMB
	rec.UpdatedAt = time.Now().UTC()

	return am.save()
}

// GetRecord returns the usage record for a sandbox.
func (am *AccountingManager) GetRecord(name string) (*UsageRecord, error) {
	am.mu.Lock()
	defer am.mu.Unlock()

	rec, ok := am.records[name]
	if !ok {
		return nil, fmt.Errorf("no usage record for sandbox %q", name)
	}

	// If the sandbox is currently running, include in-flight usage
	result := *rec
	if rec.LastStart != nil && (rec.LastStop == nil || rec.LastStart.After(*rec.LastStop)) {
		duration := time.Since(*rec.LastStart).Seconds()
		result.CPUSeconds += duration * float64(rec.VCPUs)
		result.MemoryMBHrs += float64(rec.MemoryMB) * (duration / 3600.0)
		result.UptimeSec += duration
	}

	return &result, nil
}

// ListRecords returns usage records for all sandboxes sorted by name.
func (am *AccountingManager) ListRecords() []*UsageRecord {
	am.mu.Lock()
	defer am.mu.Unlock()

	records := make([]*UsageRecord, 0, len(am.records))
	for _, r := range am.records {
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Sandbox < records[j].Sandbox
	})
	return records
}

// GenerateReport creates a usage report for a time period.
func (am *AccountingManager) GenerateReport(start, end time.Time) *UsageReport {
	am.mu.Lock()
	defer am.mu.Unlock()

	report := &UsageReport{
		GeneratedAt: time.Now().UTC(),
		PeriodStart: start,
		PeriodEnd:   end,
		Sandboxes:   make(map[string]*UsageRecord),
	}

	for name, rec := range am.records {
		// Filter intervals to the requested period
		filtered := &UsageRecord{
			Sandbox:  name,
			VCPUs:    rec.VCPUs,
			MemoryMB: rec.MemoryMB,
			DiskUsedMB: rec.DiskUsedMB,
		}

		for _, iv := range rec.Intervals {
			// Include interval if it overlaps with the report period
			if iv.End.Before(start) || iv.Start.After(end) {
				continue
			}

			// Clamp to period boundaries
			ivStart := iv.Start
			ivEnd := iv.End
			if ivStart.Before(start) {
				ivStart = start
			}
			if ivEnd.After(end) {
				ivEnd = end
			}

			// Scale proportionally if clamped
			originalDuration := iv.End.Sub(iv.Start).Seconds()
			clampedDuration := ivEnd.Sub(ivStart).Seconds()
			if originalDuration <= 0 {
				continue
			}
			ratio := clampedDuration / originalDuration

			filtered.CPUSeconds += iv.CPUSeconds * ratio
			filtered.MemoryMBHrs += iv.MemoryMBHrs * ratio
			filtered.NetTxBytes += uint64(float64(iv.NetTxBytes) * ratio)
			filtered.NetRxBytes += uint64(float64(iv.NetRxBytes) * ratio)
			filtered.UptimeSec += clampedDuration
		}

		if filtered.CPUSeconds > 0 || filtered.UptimeSec > 0 {
			report.Sandboxes[name] = filtered
			report.TotalCPUSec += filtered.CPUSeconds
			report.TotalMemMBHrs += filtered.MemoryMBHrs
			report.TotalNetTx += filtered.NetTxBytes
			report.TotalNetRx += filtered.NetRxBytes
			report.TotalDiskMB += filtered.DiskUsedMB
		}
	}

	return report
}

// DeleteRecord removes the usage record for a sandbox.
func (am *AccountingManager) DeleteRecord(name string) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	delete(am.records, name)
	return am.save()
}

// ResetRecord clears cumulative usage for a sandbox while keeping the record.
func (am *AccountingManager) ResetRecord(name string) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	rec, ok := am.records[name]
	if !ok {
		return fmt.Errorf("no usage record for sandbox %q", name)
	}

	rec.CPUSeconds = 0
	rec.MemoryMBHrs = 0
	rec.NetTxBytes = 0
	rec.NetRxBytes = 0
	rec.DiskReadOps = 0
	rec.DiskWriteOps = 0
	rec.UptimeSec = 0
	rec.Sessions = 0
	rec.Intervals = nil
	rec.UpdatedAt = time.Now().UTC()

	return am.save()
}

// getOrCreate returns the record for a sandbox, creating one if needed.
func (am *AccountingManager) getOrCreate(name string) *UsageRecord {
	rec, ok := am.records[name]
	if !ok {
		rec = &UsageRecord{
			Sandbox:   name,
			UpdatedAt: time.Now().UTC(),
		}
		am.records[name] = rec
	}
	return rec
}

// FormatBytes returns a human-readable byte count.
func FormatBytes(b uint64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)
	switch {
	case b >= TB:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(TB))
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// FormatDuration returns a human-readable duration string.
func FormatDuration(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%.1fm", seconds/60)
	}
	if seconds < 86400 {
		return fmt.Sprintf("%.1fh", seconds/3600)
	}
	return fmt.Sprintf("%.1fd", seconds/86400)
}
