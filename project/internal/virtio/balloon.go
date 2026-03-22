// Package virtio provides virtio device emulation for microVMs.
// This file implements virtio-balloon — a memory balloon device that allows
// the host to dynamically reclaim and return memory from a running guest VM.
//
// The balloon driver in the guest inflates (returns pages to host) or deflates
// (reclaims pages from host) based on commands from the VMM. This enables
// dynamic memory management without stopping the VM.
//
// Reference: Virtual I/O Device (VIRTIO) Version 1.1, Section 5.5
package virtio

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// DeviceTypeBalloon is the virtio device type for memory balloon (Section 5.5).
const DeviceTypeBalloon DeviceType = 5

// Balloon device constants.
const (
	// balloonPageSize is the size of a balloon page (4KB, matching guest page size).
	balloonPageSize = 4096

	// balloonQueueSize is the default virtqueue depth.
	balloonQueueSize = 128

	// balloonPollInterval is how often the device checks virtqueues.
	balloonPollInterval = 1 * time.Millisecond

	// balloonStatsPollInterval is how often memory stats are requested.
	balloonStatsPollInterval = 2 * time.Second
)

// Balloon virtqueue indices per the spec.
const (
	balloonInflateQ = 0 // Guest->host: pages returned to host
	balloonDeflateQ = 1 // Guest->host: pages reclaimed by guest
	balloonStatsQ   = 2 // Guest->host: memory statistics
)

// Balloon feature bits (Section 5.5.3).
const (
	// VirtioBalloonFMustTellHost requires guest to tell host before using freed pages.
	VirtioBalloonFMustTellHost uint64 = 1 << 0

	// VirtioBalloonFStatsVQ enables the stats virtqueue for memory statistics.
	VirtioBalloonFStatsVQ uint64 = 1 << 1

	// VirtioBalloonFDeflateOnOOM allows the guest to deflate the balloon on OOM.
	VirtioBalloonFDeflateOnOOM uint64 = 1 << 2

	// VirtioBalloonFFreePageHint supports free page hinting for better host memory management.
	VirtioBalloonFFreePageHint uint64 = 1 << 3
)

// BalloonStatTag identifies a memory statistic reported by the guest.
type BalloonStatTag uint16

const (
	// BalloonStatSwapIn — amount of memory swapped in (bytes).
	BalloonStatSwapIn BalloonStatTag = 0
	// BalloonStatSwapOut — amount of memory swapped out (bytes).
	BalloonStatSwapOut BalloonStatTag = 1
	// BalloonStatMajorFaults — number of major page faults.
	BalloonStatMajorFaults BalloonStatTag = 2
	// BalloonStatMinorFaults — number of minor page faults.
	BalloonStatMinorFaults BalloonStatTag = 3
	// BalloonStatFreeMemory — amount of free memory (bytes).
	BalloonStatFreeMemory BalloonStatTag = 4
	// BalloonStatTotalMemory — total amount of memory (bytes).
	BalloonStatTotalMemory BalloonStatTag = 5
	// BalloonStatAvailableMemory — available memory as reported by the guest.
	BalloonStatAvailableMemory BalloonStatTag = 6
	// BalloonStatDiskCaches — amount of memory used by disk caches.
	BalloonStatDiskCaches BalloonStatTag = 7
	// BalloonStatHugetlbAllocations — number of hugetlb allocations.
	BalloonStatHugetlbAllocations BalloonStatTag = 8
	// BalloonStatHugetlbFailures — number of failed hugetlb allocations.
	BalloonStatHugetlbFailures BalloonStatTag = 9
)

// BalloonStat represents a single memory statistic from the guest.
type BalloonStat struct {
	Tag   BalloonStatTag `json:"tag"`
	Value uint64         `json:"value"`
}

// BalloonStats holds all reported memory statistics from the guest.
type BalloonStats struct {
	SwapIn              uint64 `json:"swap_in_bytes,omitempty"`
	SwapOut             uint64 `json:"swap_out_bytes,omitempty"`
	MajorFaults         uint64 `json:"major_faults,omitempty"`
	MinorFaults         uint64 `json:"minor_faults,omitempty"`
	FreeMemory          uint64 `json:"free_memory_bytes,omitempty"`
	TotalMemory         uint64 `json:"total_memory_bytes,omitempty"`
	AvailableMemory     uint64 `json:"available_memory_bytes,omitempty"`
	DiskCaches          uint64 `json:"disk_caches_bytes,omitempty"`
	HugetlbAllocations  uint64 `json:"hugetlb_allocations,omitempty"`
	HugetlbFailures     uint64 `json:"hugetlb_failures,omitempty"`
	LastUpdated         time.Time `json:"last_updated"`
}

// VirtioBalloonConfig holds configuration for a virtio-balloon device.
type VirtioBalloonConfig struct {
	// DeflateOnOOM allows the guest balloon driver to release pages when
	// the guest is under memory pressure (OOM).
	DeflateOnOOM bool

	// StatsEnabled enables the stats virtqueue for guest memory reporting.
	StatsEnabled bool

	// FreePageHinting enables free page hinting to the host.
	FreePageHinting bool

	// QueueSize is the virtqueue depth. 0 uses the default (128).
	QueueSize int
}

// VirtioBalloon implements a virtio memory balloon device that enables
// dynamic memory management between the host and guest.
//
// The device has up to three virtqueues:
//   - inflateq: guest sends page frame numbers (PFNs) of pages returned to host
//   - deflateq: guest sends PFNs of pages being reclaimed from host
//   - statsq:   guest sends memory statistics (optional, if StatsEnabled)
type VirtioBalloon struct {
	mu sync.RWMutex

	deviceID string
	config   VirtioBalloonConfig
	features uint64

	// Virtqueues
	inflateQ *Virtqueue
	deflateQ *Virtqueue
	statsQ   *Virtqueue // nil if stats not enabled

	// Device state
	running atomic.Bool
	stopCh  chan struct{}

	// Target number of balloon pages (set by host, read by guest)
	targetPages atomic.Uint32

	// Actual number of inflated pages
	actualPages atomic.Uint32

	// Memory statistics from guest
	stats     BalloonStats
	statsOnce sync.Once

	// Inflated page tracking — maps PFN to whether it's currently inflated
	inflatedPFNs map[uint32]bool
	pfnMu        sync.Mutex

	// Stats counters
	inflateOps atomic.Uint64
	deflateOps atomic.Uint64
	pagesIn    atomic.Uint64
	pagesOut   atomic.Uint64

	// Callback for page state changes
	onInflate func(pfns []uint32)
	onDeflate func(pfns []uint32)
}

// NewVirtioBalloon creates a new virtio memory balloon device.
func NewVirtioBalloon(deviceID string, cfg VirtioBalloonConfig) (*VirtioBalloon, error) {
	if deviceID == "" {
		return nil, fmt.Errorf("virtio-balloon: device ID is required")
	}

	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = balloonQueueSize
	}

	inflateQ, err := NewVirtqueue(VirtqueueConfig{
		Index:     balloonInflateQ,
		Size:      uint16(queueSize),
		QueueType: "inflateq",
	})
	if err != nil {
		return nil, fmt.Errorf("virtio-balloon: failed to create inflate queue: %w", err)
	}

	deflateQ, err := NewVirtqueue(VirtqueueConfig{
		Index:     balloonDeflateQ,
		Size:      uint16(queueSize),
		QueueType: "deflateq",
	})
	if err != nil {
		return nil, fmt.Errorf("virtio-balloon: failed to create deflate queue: %w", err)
	}

	dev := &VirtioBalloon{
		deviceID:     deviceID,
		config:       cfg,
		inflateQ:     inflateQ,
		deflateQ:     deflateQ,
		stopCh:       make(chan struct{}),
		inflatedPFNs: make(map[uint32]bool),
	}

	// Build feature flags
	dev.features = VirtioBalloonFMustTellHost
	if cfg.DeflateOnOOM {
		dev.features |= VirtioBalloonFDeflateOnOOM
	}
	if cfg.FreePageHinting {
		dev.features |= VirtioBalloonFFreePageHint
	}

	// Create stats queue if enabled
	if cfg.StatsEnabled {
		dev.features |= VirtioBalloonFStatsVQ
		statsQ, err := NewVirtqueue(VirtqueueConfig{
			Index:     balloonStatsQ,
			Size:      uint16(queueSize),
			QueueType: "statsq",
		})
		if err != nil {
			return nil, fmt.Errorf("virtio-balloon: failed to create stats queue: %w", err)
		}
		dev.statsQ = statsQ
	}

	return dev, nil
}

// Type returns the virtio device type.
func (b *VirtioBalloon) Type() DeviceType {
	return DeviceTypeBalloon
}

// ID returns the device identifier.
func (b *VirtioBalloon) ID() string {
	return b.deviceID
}

// Start initializes and begins processing balloon requests.
func (b *VirtioBalloon) Start() error {
	if b.running.Load() {
		return fmt.Errorf("virtio-balloon %s: already running", b.deviceID)
	}

	b.running.Store(true)
	go b.processLoop()

	return nil
}

// Stop shuts down the balloon device.
func (b *VirtioBalloon) Stop() error {
	if !b.running.Load() {
		return nil
	}

	b.running.Store(false)
	close(b.stopCh)
	return nil
}

// Configure applies runtime configuration changes.
func (b *VirtioBalloon) Configure(config map[string]string) error {
	if v, ok := config["target_mb"]; ok {
		var mb int
		if _, err := fmt.Sscanf(v, "%d", &mb); err == nil && mb >= 0 {
			pages := uint32(mb * 1024 * 1024 / balloonPageSize)
			b.SetTarget(pages)
		}
	}

	if v, ok := config["deflate_on_oom"]; ok {
		b.mu.Lock()
		b.config.DeflateOnOOM = v == "true" || v == "1"
		if b.config.DeflateOnOOM {
			b.features |= VirtioBalloonFDeflateOnOOM
		} else {
			b.features &^= VirtioBalloonFDeflateOnOOM
		}
		b.mu.Unlock()
	}

	return nil
}

// SetTarget sets the balloon target in pages. The guest driver will inflate
// or deflate to approach this target.
func (b *VirtioBalloon) SetTarget(pages uint32) {
	b.targetPages.Store(pages)
}

// SetTargetMB sets the balloon target in megabytes.
func (b *VirtioBalloon) SetTargetMB(mb uint32) {
	pages := mb * 1024 * 1024 / balloonPageSize
	b.targetPages.Store(pages)
}

// GetTarget returns the current balloon target in pages.
func (b *VirtioBalloon) GetTarget() uint32 {
	return b.targetPages.Load()
}

// GetTargetMB returns the current balloon target in megabytes.
func (b *VirtioBalloon) GetTargetMB() uint32 {
	pages := b.targetPages.Load()
	return pages * balloonPageSize / (1024 * 1024)
}

// GetActual returns the actual number of inflated pages.
func (b *VirtioBalloon) GetActual() uint32 {
	return b.actualPages.Load()
}

// GetActualMB returns the actual inflated memory in megabytes.
func (b *VirtioBalloon) GetActualMB() uint32 {
	pages := b.actualPages.Load()
	return pages * balloonPageSize / (1024 * 1024)
}

// GetStats returns the latest memory statistics from the guest.
func (b *VirtioBalloon) GetStats() BalloonStats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.stats
}

// OnInflate registers a callback invoked when the guest inflates (returns pages).
func (b *VirtioBalloon) OnInflate(fn func(pfns []uint32)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onInflate = fn
}

// OnDeflate registers a callback invoked when the guest deflates (reclaims pages).
func (b *VirtioBalloon) OnDeflate(fn func(pfns []uint32)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onDeflate = fn
}

// BalloonDeviceStats returns operational statistics for the device.
type BalloonDeviceStats struct {
	TargetPages  uint32 `json:"target_pages"`
	ActualPages  uint32 `json:"actual_pages"`
	TargetMB     uint32 `json:"target_mb"`
	ActualMB     uint32 `json:"actual_mb"`
	InflateOps   uint64 `json:"inflate_ops"`
	DeflateOps   uint64 `json:"deflate_ops"`
	PagesIn      uint64 `json:"pages_inflated_total"`
	PagesOut     uint64 `json:"pages_deflated_total"`
	TrackedPFNs  int    `json:"tracked_pfns"`
}

// DeviceStats returns operational statistics.
func (b *VirtioBalloon) DeviceStats() BalloonDeviceStats {
	b.pfnMu.Lock()
	pfnCount := len(b.inflatedPFNs)
	b.pfnMu.Unlock()

	return BalloonDeviceStats{
		TargetPages: b.targetPages.Load(),
		ActualPages: b.actualPages.Load(),
		TargetMB:    b.GetTargetMB(),
		ActualMB:    b.GetActualMB(),
		InflateOps:  b.inflateOps.Load(),
		DeflateOps:  b.deflateOps.Load(),
		PagesIn:     b.pagesIn.Load(),
		PagesOut:    b.pagesOut.Load(),
		TrackedPFNs: pfnCount,
	}
}

// processLoop polls virtqueues for inflate/deflate/stats requests.
func (b *VirtioBalloon) processLoop() {
	ticker := time.NewTicker(balloonPollInterval)
	defer ticker.Stop()

	var statsTicker *time.Ticker
	var statsCh <-chan time.Time
	if b.statsQ != nil {
		statsTicker = time.NewTicker(balloonStatsPollInterval)
		defer statsTicker.Stop()
		statsCh = statsTicker.C
	}

	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			b.processInflate()
			b.processDeflate()
		case <-statsCh:
			b.processStats()
		}
	}
}

// processInflate handles page frame numbers from the guest indicating
// pages that are being returned to the host.
func (b *VirtioBalloon) processInflate() {
	for {
		chain, err := b.inflateQ.PopAvailable()
		if err != nil || chain == nil {
			return
		}

		pfns := b.extractPFNs(chain)
		if len(pfns) == 0 {
			if err := b.inflateQ.PushUsed(chain.HeadIndex, 0); err != nil {
				continue
			}
			continue
		}

		// Track inflated pages
		b.pfnMu.Lock()
		for _, pfn := range pfns {
			b.inflatedPFNs[pfn] = true
		}
		b.pfnMu.Unlock()

		b.actualPages.Store(uint32(b.countInflated()))
		b.inflateOps.Add(1)
		b.pagesIn.Add(uint64(len(pfns)))

		// Notify callback
		b.mu.RLock()
		cb := b.onInflate
		b.mu.RUnlock()
		if cb != nil {
			cb(pfns)
		}

		if err := b.inflateQ.PushUsed(chain.HeadIndex, 0); err != nil {
			continue
		}
	}
}

// processDeflate handles page frame numbers from the guest indicating
// pages that are being reclaimed from the host.
func (b *VirtioBalloon) processDeflate() {
	for {
		chain, err := b.deflateQ.PopAvailable()
		if err != nil || chain == nil {
			return
		}

		pfns := b.extractPFNs(chain)
		if len(pfns) == 0 {
			if err := b.deflateQ.PushUsed(chain.HeadIndex, 0); err != nil {
				continue
			}
			continue
		}

		// Remove from tracked inflated pages
		b.pfnMu.Lock()
		for _, pfn := range pfns {
			delete(b.inflatedPFNs, pfn)
		}
		b.pfnMu.Unlock()

		b.actualPages.Store(uint32(b.countInflated()))
		b.deflateOps.Add(1)
		b.pagesOut.Add(uint64(len(pfns)))

		// Notify callback
		b.mu.RLock()
		cb := b.onDeflate
		b.mu.RUnlock()
		if cb != nil {
			cb(pfns)
		}

		if err := b.deflateQ.PushUsed(chain.HeadIndex, 0); err != nil {
			continue
		}
	}
}

// processStats reads memory statistics from the stats virtqueue.
// The guest places an array of BalloonStat (tag: u16, val: u64) on the queue.
func (b *VirtioBalloon) processStats() {
	if b.statsQ == nil {
		return
	}

	chain, err := b.statsQ.PopAvailable()
	if err != nil || chain == nil {
		return
	}

	// Parse stats from readable descriptors
	stats := b.parseStats(chain)

	b.mu.Lock()
	b.stats.LastUpdated = time.Now()
	for _, s := range stats {
		switch s.Tag {
		case BalloonStatSwapIn:
			b.stats.SwapIn = s.Value
		case BalloonStatSwapOut:
			b.stats.SwapOut = s.Value
		case BalloonStatMajorFaults:
			b.stats.MajorFaults = s.Value
		case BalloonStatMinorFaults:
			b.stats.MinorFaults = s.Value
		case BalloonStatFreeMemory:
			b.stats.FreeMemory = s.Value
		case BalloonStatTotalMemory:
			b.stats.TotalMemory = s.Value
		case BalloonStatAvailableMemory:
			b.stats.AvailableMemory = s.Value
		case BalloonStatDiskCaches:
			b.stats.DiskCaches = s.Value
		case BalloonStatHugetlbAllocations:
			b.stats.HugetlbAllocations = s.Value
		case BalloonStatHugetlbFailures:
			b.stats.HugetlbFailures = s.Value
		}
	}
	b.mu.Unlock()

	if err := b.statsQ.PushUsed(chain.HeadIndex, 0); err != nil {
		return
	}
}

// extractPFNs reads page frame numbers from a descriptor chain.
// Each PFN is a uint32 representing a 4KB-aligned guest page.
func (b *VirtioBalloon) extractPFNs(chain *DescriptorChain) []uint32 {
	if chain == nil {
		return nil
	}

	// PFNs come from readable descriptors (guest -> host)
	var allData []byte
	for _, desc := range chain.Readable {
		allData = append(allData, desc.Data...)
	}

	// Each PFN is 4 bytes (uint32, little-endian)
	count := len(allData) / 4
	if count == 0 {
		return nil
	}

	pfns := make([]uint32, 0, count)
	for i := 0; i < count; i++ {
		pfn := binary.LittleEndian.Uint32(allData[i*4 : (i+1)*4])
		pfns = append(pfns, pfn)
	}

	return pfns
}

// parseStats reads BalloonStat entries from a descriptor chain.
// Each entry is 10 bytes: u16 tag + u64 value.
func (b *VirtioBalloon) parseStats(chain *DescriptorChain) []BalloonStat {
	if chain == nil {
		return nil
	}

	var allData []byte
	for _, desc := range chain.Readable {
		allData = append(allData, desc.Data...)
	}

	// Each stat is 10 bytes: 2 (tag) + 8 (value)
	const statSize = 10
	count := len(allData) / statSize
	if count == 0 {
		return nil
	}

	stats := make([]BalloonStat, 0, count)
	for i := 0; i < count; i++ {
		offset := i * statSize
		tag := binary.LittleEndian.Uint16(allData[offset : offset+2])
		val := binary.LittleEndian.Uint64(allData[offset+2 : offset+10])
		stats = append(stats, BalloonStat{
			Tag:   BalloonStatTag(tag),
			Value: val,
		})
	}

	return stats
}

// countInflated returns the current count of inflated PFNs.
func (b *VirtioBalloon) countInflated() int {
	b.pfnMu.Lock()
	defer b.pfnMu.Unlock()
	return len(b.inflatedPFNs)
}

// NegotiateFeatures returns the intersection of offered and supported features.
func (b *VirtioBalloon) NegotiateFeatures(offered uint64) uint64 {
	return offered & b.features
}

// Reset returns the device to its initial state.
func (b *VirtioBalloon) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	wasRunning := b.running.Load()
	if wasRunning {
		b.running.Store(false)
		close(b.stopCh)
	}

	// Reset counters
	b.targetPages.Store(0)
	b.actualPages.Store(0)
	b.inflateOps.Store(0)
	b.deflateOps.Store(0)
	b.pagesIn.Store(0)
	b.pagesOut.Store(0)

	// Clear tracked PFNs
	b.pfnMu.Lock()
	b.inflatedPFNs = make(map[uint32]bool)
	b.pfnMu.Unlock()

	// Reset stats
	b.stats = BalloonStats{}

	// Reset virtqueues
	b.inflateQ.Reset()
	b.deflateQ.Reset()
	if b.statsQ != nil {
		b.statsQ.Reset()
	}

	// Prepare for restart
	b.stopCh = make(chan struct{})
}

// GetVirtqueues returns all device virtqueues for transport attachment.
func (b *VirtioBalloon) GetVirtqueues() []*Virtqueue {
	queues := []*Virtqueue{b.inflateQ, b.deflateQ}
	if b.statsQ != nil {
		queues = append(queues, b.statsQ)
	}
	return queues
}

// BalloonConfigSpace represents the device configuration space (Section 5.5.4).
// The config space contains two fields:
//   - num_pages (u32): number of pages host wants in the balloon
//   - actual (u32): number of pages actually in the balloon (set by guest)
type BalloonConfigSpace struct {
	NumPages uint32
	Actual   uint32
}

// Bytes serializes the config space to wire format (little-endian).
func (c *BalloonConfigSpace) Bytes() []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf[0:4], c.NumPages)
	binary.LittleEndian.PutUint32(buf[4:8], c.Actual)
	return buf
}

// ParseBalloonConfigSpace deserializes a config space from wire format.
func ParseBalloonConfigSpace(data []byte) (*BalloonConfigSpace, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("virtio-balloon: config space too small (%d bytes, need 8)", len(data))
	}
	return &BalloonConfigSpace{
		NumPages: binary.LittleEndian.Uint32(data[0:4]),
		Actual:   binary.LittleEndian.Uint32(data[4:8]),
	}, nil
}

// GetConfigSpace returns the current device config space.
func (b *VirtioBalloon) GetConfigSpace() *BalloonConfigSpace {
	return &BalloonConfigSpace{
		NumPages: b.targetPages.Load(),
		Actual:   b.actualPages.Load(),
	}
}

// GetDeviceConfig returns MMIO/PCI transport configuration for this device.
func (b *VirtioBalloon) GetDeviceConfig() BalloonTransportConfig {
	numQueues := uint32(2)
	if b.statsQ != nil {
		numQueues = 3
	}
	return BalloonTransportConfig{
		DeviceID:   0x05,
		VendorID:   0x554E4554, // "TENT"
		DeviceType: uint32(DeviceTypeBalloon),
		NumQueues:  numQueues,
		Features:   b.features,
	}
}

// BalloonTransportConfig holds MMIO/PCI transport registration info.
type BalloonTransportConfig struct {
	DeviceID   uint32 `json:"device_id"`
	VendorID   uint32 `json:"vendor_id"`
	DeviceType uint32 `json:"device_type"`
	NumQueues  uint32 `json:"num_queues"`
	Features   uint64 `json:"features"`
}

// String returns a human-readable description of the device.
func (b *VirtioBalloon) String() string {
	ds := b.DeviceStats()
	return fmt.Sprintf("virtio-balloon[%s] target=%dMB actual=%dMB inflate_ops=%d deflate_ops=%d",
		b.deviceID, ds.TargetMB, ds.ActualMB, ds.InflateOps, ds.DeflateOps)
}

// StatTagName returns the human-readable name for a balloon stat tag.
func StatTagName(tag BalloonStatTag) string {
	switch tag {
	case BalloonStatSwapIn:
		return "swap_in"
	case BalloonStatSwapOut:
		return "swap_out"
	case BalloonStatMajorFaults:
		return "major_faults"
	case BalloonStatMinorFaults:
		return "minor_faults"
	case BalloonStatFreeMemory:
		return "free_memory"
	case BalloonStatTotalMemory:
		return "total_memory"
	case BalloonStatAvailableMemory:
		return "available_memory"
	case BalloonStatDiskCaches:
		return "disk_caches"
	case BalloonStatHugetlbAllocations:
		return "hugetlb_allocations"
	case BalloonStatHugetlbFailures:
		return "hugetlb_failures"
	default:
		return fmt.Sprintf("unknown(%d)", tag)
	}
}
