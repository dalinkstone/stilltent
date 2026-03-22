// Package virtio provides virtio device emulation for microVMs.
// This file implements virtio-rng — a hardware random number generator that
// provides cryptographic-quality entropy to the guest via a virtqueue.
//
// The guest kernel uses this device to seed /dev/random and /dev/urandom,
// which is critical for TLS, key generation, and other crypto operations
// inside microVMs that may lack hardware entropy sources.
//
// Reference: Virtual I/O Device (VIRTIO) Version 1.1, Section 5.4
package virtio

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// DeviceTypeEntropy is the virtio device type for entropy/RNG (Section 5.4).
const DeviceTypeEntropy DeviceType = 4

// Default configuration for the RNG device.
const (
	// rngMaxBytesPerRequest caps a single entropy read to prevent
	// the guest from consuming excessive host entropy in one request.
	rngMaxBytesPerRequest = 4096

	// rngRateLimitBytes is the maximum bytes of entropy per second.
	// 0 means unlimited (default — host crypto/rand is fast enough).
	rngRateLimitBytes = 0

	// rngQueueSize is the default virtqueue depth for the RNG device.
	rngQueueSize = 64

	// rngPollInterval is how often the device checks the virtqueue for requests.
	rngPollInterval = 500 * time.Microsecond
)

// VirtioRngConfig holds configuration for a virtio-rng device.
type VirtioRngConfig struct {
	// MaxBytesPerRequest caps a single entropy read. 0 uses the default (4096).
	MaxBytesPerRequest int

	// RateLimitBytesPerSec limits the entropy rate. 0 means unlimited.
	RateLimitBytesPerSec int

	// QueueSize is the virtqueue depth. 0 uses the default (64).
	QueueSize int
}

// VirtioRng implements a virtio entropy device that supplies random bytes
// from the host's cryptographic RNG to the guest.
//
// The device has a single virtqueue (requestq). The guest places writable
// descriptors on the queue; the device fills them with random bytes and
// marks them used.
type VirtioRng struct {
	mu sync.Mutex

	deviceID string
	config   VirtioRngConfig

	// Virtqueue for entropy requests
	vq *Virtqueue

	// Device state
	running atomic.Bool
	stopCh  chan struct{}

	// Rate limiting state
	rateLimiter *rngRateLimiter

	// Custom entropy source (nil = use crypto/rand)
	entropySource EntropySource

	// Stats
	bytesProvided atomic.Uint64
	reqsCompleted atomic.Uint64
	reqsFailed    atomic.Uint64
}

// rngRateLimiter implements a token-bucket rate limiter for entropy output.
type rngRateLimiter struct {
	mu           sync.Mutex
	bytesPerSec  int
	tokens       int
	maxTokens    int
	lastRefill   time.Time
}

// newRngRateLimiter creates a rate limiter. If bytesPerSec <= 0, returns nil (unlimited).
func newRngRateLimiter(bytesPerSec int) *rngRateLimiter {
	if bytesPerSec <= 0 {
		return nil
	}
	return &rngRateLimiter{
		bytesPerSec: bytesPerSec,
		tokens:      bytesPerSec, // start full
		maxTokens:   bytesPerSec,
		lastRefill:  time.Now(),
	}
}

// take attempts to consume n tokens, returning the number actually available.
func (rl *rngRateLimiter) take(n int) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Refill tokens based on elapsed time
	now := time.Now()
	elapsed := now.Sub(rl.lastRefill)
	refill := int(elapsed.Seconds() * float64(rl.bytesPerSec))
	if refill > 0 {
		rl.tokens += refill
		if rl.tokens > rl.maxTokens {
			rl.tokens = rl.maxTokens
		}
		rl.lastRefill = now
	}

	if rl.tokens <= 0 {
		return 0
	}

	if n > rl.tokens {
		n = rl.tokens
	}
	rl.tokens -= n
	return n
}

// NewVirtioRng creates a new virtio entropy device.
func NewVirtioRng(deviceID string, cfg VirtioRngConfig) (*VirtioRng, error) {
	if deviceID == "" {
		return nil, fmt.Errorf("virtio-rng: device ID is required")
	}

	// Apply defaults
	if cfg.MaxBytesPerRequest <= 0 {
		cfg.MaxBytesPerRequest = rngMaxBytesPerRequest
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = rngQueueSize
	}

	// Create the virtqueue
	vq, err := NewVirtqueue(VirtqueueConfig{
		Name: "requestq",
		Num:  uint16(cfg.QueueSize),
	})
	if err != nil {
		return nil, fmt.Errorf("virtio-rng: failed to create virtqueue: %w", err)
	}

	dev := &VirtioRng{
		deviceID:    deviceID,
		config:      cfg,
		vq:          vq,
		stopCh:      make(chan struct{}),
		rateLimiter: newRngRateLimiter(cfg.RateLimitBytesPerSec),
	}

	return dev, nil
}

// Type returns the virtio device type.
func (rng *VirtioRng) Type() DeviceType {
	return DeviceTypeEntropy
}

// ID returns the device identifier.
func (rng *VirtioRng) ID() string {
	return rng.deviceID
}

// Start initializes and begins processing entropy requests from the guest.
func (rng *VirtioRng) Start() error {
	if rng.running.Load() {
		return fmt.Errorf("virtio-rng %s: already running", rng.deviceID)
	}

	rng.running.Store(true)
	go rng.processLoop()

	return nil
}

// Stop shuts down the entropy device.
func (rng *VirtioRng) Stop() error {
	if !rng.running.Load() {
		return nil
	}

	rng.running.Store(false)
	close(rng.stopCh)
	return nil
}

// Configure applies runtime configuration changes.
func (rng *VirtioRng) Configure(config map[string]string) error {
	rng.mu.Lock()
	defer rng.mu.Unlock()

	if v, ok := config["max_bytes_per_request"]; ok {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			rng.config.MaxBytesPerRequest = n
		}
	}

	if v, ok := config["rate_limit_bytes_per_sec"]; ok {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			rng.rateLimiter = newRngRateLimiter(n)
		}
	}

	return nil
}

// Stats returns operational statistics for the device.
func (rng *VirtioRng) Stats() RngStats {
	return RngStats{
		BytesProvided:    rng.bytesProvided.Load(),
		RequestsComplete: rng.reqsCompleted.Load(),
		RequestsFailed:   rng.reqsFailed.Load(),
	}
}

// RngStats holds operational counters for a virtio-rng device.
type RngStats struct {
	BytesProvided    uint64 `json:"bytes_provided"`
	RequestsComplete uint64 `json:"requests_complete"`
	RequestsFailed   uint64 `json:"requests_failed"`
}

// processLoop polls the virtqueue for entropy requests and fills them.
func (rng *VirtioRng) processLoop() {
	ticker := time.NewTicker(rngPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-rng.stopCh:
			return
		case <-ticker.C:
			rng.processRequests()
		}
	}
}

// processRequests handles all pending entropy requests on the virtqueue.
func (rng *VirtioRng) processRequests() {
	for {
		chain, err := rng.vq.PopAvailable()
		if err != nil || chain == nil {
			return
		}

		bytesWritten := rng.fillChain(chain)

		// Mark the chain as used with the number of bytes written
		if err := rng.vq.PushUsed(chain.HeadIndex, bytesWritten); err != nil {
			rng.reqsFailed.Add(1)
			continue
		}
	}
}

// fillChain fills the writable buffers of a descriptor chain with random bytes.
// For virtio-rng, the guest provides only writable descriptors — there is no
// request header to read. The device fills every writable buffer with entropy.
func (rng *VirtioRng) fillChain(chain *DescriptorChain) uint32 {
	if chain == nil || len(chain.Writable) == 0 {
		rng.reqsFailed.Add(1)
		return 0
	}

	// Calculate total writable capacity
	totalCap := int(chain.TotalWrite)
	if totalCap == 0 {
		rng.reqsFailed.Add(1)
		return 0
	}

	// Cap to maximum per-request size
	size := totalCap
	if size > rng.config.MaxBytesPerRequest {
		size = rng.config.MaxBytesPerRequest
	}

	// Apply rate limiting if configured
	if rng.rateLimiter != nil {
		allowed := rng.rateLimiter.take(size)
		if allowed == 0 {
			// No budget — return zero bytes; guest will retry
			return 0
		}
		size = allowed
	}

	// Generate random bytes from the host's crypto RNG (or custom source)
	buf := make([]byte, size)
	var src io.Reader = rand.Reader
	if rng.entropySource != nil {
		src = rng.entropySource
	}
	n, err := src.Read(buf)
	if err != nil {
		rng.reqsFailed.Add(1)
		return 0
	}

	rng.bytesProvided.Add(uint64(n))
	rng.reqsCompleted.Add(1)

	return uint32(n)
}

// GetVirtqueue returns the device's virtqueue for transport attachment.
func (rng *VirtioRng) GetVirtqueue() *Virtqueue {
	return rng.vq
}

// --- Virtio configuration space for the RNG device ---

// VirtioRngConfigSpace represents the device config (Section 5.4.3).
// The entropy device has no configuration fields — the config space is empty.
// This struct exists for completeness and future extensions.
type VirtioRngConfigSpace struct{}

// Bytes serializes the config space. For virtio-rng this is always empty.
func (c *VirtioRngConfigSpace) Bytes() []byte {
	return nil
}

// --- Feature negotiation ---

// Virtio-rng has no device-specific feature bits defined by the spec.
// We define a placeholder for potential extensions.
const (
	// VirtioRngFRateLimiting is a tent-specific feature bit indicating
	// the device supports rate-limited entropy output.
	VirtioRngFRateLimiting uint64 = 1 << 24
)

// NegotiateFeatures returns the intersection of offered and supported features.
func (rng *VirtioRng) NegotiateFeatures(offered uint64) uint64 {
	supported := uint64(0)
	if rng.rateLimiter != nil {
		supported |= VirtioRngFRateLimiting
	}
	return offered & supported
}

// --- Device reset ---

// Reset returns the device to its initial state, clearing all queued requests.
func (rng *VirtioRng) Reset() {
	rng.mu.Lock()
	defer rng.mu.Unlock()

	// Stop processing if running
	wasRunning := rng.running.Load()
	if wasRunning {
		rng.running.Store(false)
		close(rng.stopCh)
	}

	// Reset stats
	rng.bytesProvided.Store(0)
	rng.reqsCompleted.Store(0)
	rng.reqsFailed.Store(0)

	// Reset the virtqueue
	rng.vq.Reset()

	// Prepare for restart
	rng.stopCh = make(chan struct{})
}

// --- String representation ---

// String returns a human-readable description of the device.
func (rng *VirtioRng) String() string {
	stats := rng.Stats()
	return fmt.Sprintf("virtio-rng[%s] provided=%d reqs=%d failed=%d",
		rng.deviceID, stats.BytesProvided, stats.RequestsComplete, stats.RequestsFailed)
}

// --- Helper for MMIO/PCI transport integration ---

// DeviceConfig returns the device configuration for MMIO/PCI transport registration.
type RngDeviceConfig struct {
	DeviceID   uint32
	VendorID   uint32
	DeviceType uint32
	NumQueues  uint32
}

// GetDeviceConfig returns MMIO/PCI transport configuration for this device.
func (rng *VirtioRng) GetDeviceConfig() RngDeviceConfig {
	return RngDeviceConfig{
		DeviceID:   0x04, // Entropy source
		VendorID:   0x554E4554, // "TENT" in ASCII
		DeviceType: uint32(DeviceTypeEntropy),
		NumQueues:  1, // Single requestq
	}
}

// --- Entropy source abstraction ---

// EntropySource defines the interface for providing random bytes.
// By default VirtioRng uses crypto/rand, but this can be overridden
// for testing or to use a hardware RNG device.
type EntropySource interface {
	Read(p []byte) (n int, err error)
}

// SetEntropySource overrides the default crypto/rand source.
// This is primarily useful for testing with deterministic output.
func (rng *VirtioRng) SetEntropySource(src EntropySource) {
	rng.mu.Lock()
	defer rng.mu.Unlock()
	rng.entropySource = src
}

// Ensure binary import is referenced (used in transport-level descriptor parsing).
var _ = binary.LittleEndian
