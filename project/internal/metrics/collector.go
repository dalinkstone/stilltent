// Package metrics provides sandbox resource metrics collection and export.
// It collects CPU, memory, disk, and network metrics per sandbox and supports
// Prometheus exposition format for integration with monitoring systems.
package metrics

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// MetricType represents the type of a metric.
type MetricType int

const (
	MetricGauge MetricType = iota
	MetricCounter
)

// Sample represents a single metric data point.
type Sample struct {
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels"`
	Value     float64           `json:"value"`
	Type      MetricType        `json:"type"`
	Help      string            `json:"help"`
	Timestamp time.Time         `json:"timestamp"`
}

// SandboxMetrics holds all metrics for a single sandbox.
type SandboxMetrics struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`

	// CPU metrics
	CPUUsagePercent float64 `json:"cpu_usage_percent"`
	VCPUs           int     `json:"vcpus"`

	// Memory metrics (bytes)
	MemoryUsedBytes  int64 `json:"memory_used_bytes"`
	MemoryTotalBytes int64 `json:"memory_total_bytes"`

	// Disk metrics (bytes)
	DiskUsedBytes  int64 `json:"disk_used_bytes"`
	DiskTotalBytes int64 `json:"disk_total_bytes"`
	DiskReadBytes  int64 `json:"disk_read_bytes"`
	DiskWriteBytes int64 `json:"disk_write_bytes"`

	// Network metrics (bytes)
	NetRxBytes   int64 `json:"net_rx_bytes"`
	NetTxBytes   int64 `json:"net_tx_bytes"`
	NetRxPackets int64 `json:"net_rx_packets"`
	NetTxPackets int64 `json:"net_tx_packets"`

	// Process count
	ProcessCount int `json:"process_count"`

	// Uptime
	UptimeSeconds float64 `json:"uptime_seconds"`
}

// Collector collects and stores sandbox metrics over time.
type Collector struct {
	mu       sync.RWMutex
	current  map[string]*SandboxMetrics // sandbox name -> latest metrics
	history  map[string][]*SandboxMetrics
	maxHist  int
	interval time.Duration
}

// NewCollector creates a new metrics collector.
func NewCollector(historySize int) *Collector {
	if historySize <= 0 {
		historySize = 60
	}
	return &Collector{
		current:  make(map[string]*SandboxMetrics),
		history:  make(map[string][]*SandboxMetrics),
		maxHist:  historySize,
		interval: 10 * time.Second,
	}
}

// Record stores metrics for a sandbox, appending to history.
func (c *Collector) Record(m *SandboxMetrics) {
	if m == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	m.Timestamp = time.Now()
	c.current[m.Name] = m

	hist := c.history[m.Name]
	hist = append(hist, m)
	if len(hist) > c.maxHist {
		hist = hist[len(hist)-c.maxHist:]
	}
	c.history[m.Name] = hist
}

// Get returns the latest metrics for a sandbox.
func (c *Collector) Get(name string) (*SandboxMetrics, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.current[name]
	return m, ok
}

// GetAll returns the latest metrics for all sandboxes.
func (c *Collector) GetAll() []*SandboxMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]*SandboxMetrics, 0, len(c.current))
	for _, m := range c.current {
		result = append(result, m)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// GetHistory returns the metrics history for a sandbox.
func (c *Collector) GetHistory(name string) []*SandboxMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()

	hist := c.history[name]
	out := make([]*SandboxMetrics, len(hist))
	copy(out, hist)
	return out
}

// Remove clears metrics for a sandbox.
func (c *Collector) Remove(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.current, name)
	delete(c.history, name)
}

// Samples converts all current metrics into a flat list of labeled samples,
// suitable for Prometheus export.
func (c *Collector) Samples() []Sample {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var samples []Sample
	for _, m := range c.current {
		labels := map[string]string{
			"sandbox": m.Name,
			"status":  m.Status,
		}

		add := func(name string, val float64, typ MetricType, help string) {
			samples = append(samples, Sample{
				Name:      fmt.Sprintf("tent_%s", name),
				Labels:    labels,
				Value:     val,
				Type:      typ,
				Help:      help,
				Timestamp: m.Timestamp,
			})
		}

		add("cpu_usage_percent", m.CPUUsagePercent, MetricGauge, "CPU usage percentage")
		add("vcpus", float64(m.VCPUs), MetricGauge, "Number of virtual CPUs")
		add("memory_used_bytes", float64(m.MemoryUsedBytes), MetricGauge, "Memory used in bytes")
		add("memory_total_bytes", float64(m.MemoryTotalBytes), MetricGauge, "Total memory in bytes")
		add("disk_used_bytes", float64(m.DiskUsedBytes), MetricGauge, "Disk used in bytes")
		add("disk_total_bytes", float64(m.DiskTotalBytes), MetricGauge, "Total disk in bytes")
		add("disk_read_bytes", float64(m.DiskReadBytes), MetricCounter, "Total disk bytes read")
		add("disk_write_bytes", float64(m.DiskWriteBytes), MetricCounter, "Total disk bytes written")
		add("net_rx_bytes", float64(m.NetRxBytes), MetricCounter, "Total network bytes received")
		add("net_tx_bytes", float64(m.NetTxBytes), MetricCounter, "Total network bytes transmitted")
		add("net_rx_packets", float64(m.NetRxPackets), MetricCounter, "Total network packets received")
		add("net_tx_packets", float64(m.NetTxPackets), MetricCounter, "Total network packets transmitted")
		add("process_count", float64(m.ProcessCount), MetricGauge, "Number of running processes")
		add("uptime_seconds", m.UptimeSeconds, MetricGauge, "Sandbox uptime in seconds")
	}

	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Name != samples[j].Name {
			return samples[i].Name < samples[j].Name
		}
		return samples[i].Labels["sandbox"] < samples[j].Labels["sandbox"]
	})

	return samples
}

// AggregateMetrics holds aggregate stats across all sandboxes.
type AggregateMetrics struct {
	TotalSandboxes int     `json:"total_sandboxes"`
	RunningSandboxes int   `json:"running_sandboxes"`
	TotalVCPUs     int     `json:"total_vcpus"`
	TotalMemoryBytes int64 `json:"total_memory_bytes"`
	TotalDiskBytes int64   `json:"total_disk_bytes"`
	AvgCPUPercent  float64 `json:"avg_cpu_percent"`
}

// Aggregate computes aggregate metrics across all sandboxes.
func (c *Collector) Aggregate() *AggregateMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()

	agg := &AggregateMetrics{}
	var cpuSum float64

	for _, m := range c.current {
		agg.TotalSandboxes++
		if m.Status == "running" {
			agg.RunningSandboxes++
		}
		agg.TotalVCPUs += m.VCPUs
		agg.TotalMemoryBytes += m.MemoryTotalBytes
		agg.TotalDiskBytes += m.DiskTotalBytes
		cpuSum += m.CPUUsagePercent
	}

	if agg.TotalSandboxes > 0 {
		agg.AvgCPUPercent = cpuSum / float64(agg.TotalSandboxes)
	}

	return agg
}
