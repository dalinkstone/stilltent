// Package virtio provides virtio device emulation for microVMs.
// This file implements a virtio-watchdog device that monitors guest VM health.
//
// The watchdog device provides a hardware timer that must be periodically "pet"
// (reset) by the guest. If the guest fails to pet the watchdog within the
// configured timeout, the host takes a configured action (reset, shutdown,
// poweroff, or pause). This detects guest hangs, kernel panics, and other
// unresponsive states that the hypervisor alone cannot observe.
//
// The guest writes a "heartbeat" command to the controlq virtqueue at regular
// intervals. The host tracks the last heartbeat timestamp and fires a timer
// expiry callback when the deadline passes.
//
// This implements a simplified i6300esb-style watchdog exposed via virtio transport.
package virtio

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// DeviceTypeWatchdog is the virtio device type for the watchdog timer.
// Using a vendor-specific device type in the reserved range.
const DeviceTypeWatchdog DeviceType = 35

// Watchdog device constants.
const (
	// watchdogDefaultTimeout is the default watchdog timeout.
	watchdogDefaultTimeout = 30 * time.Second

	// watchdogMinTimeout is the minimum allowed timeout.
	watchdogMinTimeout = 1 * time.Second

	// watchdogMaxTimeout is the maximum allowed timeout.
	watchdogMaxTimeout = 10 * time.Minute

	// watchdogQueueSize is the default virtqueue depth.
	watchdogQueueSize = 16

	// watchdogPollInterval is how often the device checks the virtqueue.
	watchdogPollInterval = 100 * time.Millisecond
)

// Watchdog command codes sent by the guest via the controlq.
const (
	// WatchdogCmdHeartbeat resets the watchdog timer (guest is alive).
	WatchdogCmdHeartbeat uint32 = 1

	// WatchdogCmdSetTimeout changes the timeout (value in seconds follows).
	WatchdogCmdSetTimeout uint32 = 2

	// WatchdogCmdEnable enables the watchdog timer.
	WatchdogCmdEnable uint32 = 3

	// WatchdogCmdDisable disables the watchdog timer (guest shutting down cleanly).
	WatchdogCmdDisable uint32 = 4

	// WatchdogCmdGetStatus requests current watchdog status.
	WatchdogCmdGetStatus uint32 = 5
)

// WatchdogAction defines what happens when the watchdog timer expires.
type WatchdogAction int

const (
	// WatchdogActionReset reboots the guest VM.
	WatchdogActionReset WatchdogAction = iota
	// WatchdogActionShutdown performs a graceful shutdown.
	WatchdogActionShutdown
	// WatchdogActionPoweroff immediately powers off the VM.
	WatchdogActionPoweroff
	// WatchdogActionPause pauses (freezes) the VM for inspection.
	WatchdogActionPause
	// WatchdogActionNone logs the expiry but takes no action.
	WatchdogActionNone
)

// String returns the human-readable name of the action.
func (a WatchdogAction) String() string {
	switch a {
	case WatchdogActionReset:
		return "reset"
	case WatchdogActionShutdown:
		return "shutdown"
	case WatchdogActionPoweroff:
		return "poweroff"
	case WatchdogActionPause:
		return "pause"
	case WatchdogActionNone:
		return "none"
	default:
		return "unknown"
	}
}

// ParseWatchdogAction converts a string to a WatchdogAction.
func ParseWatchdogAction(s string) (WatchdogAction, error) {
	switch s {
	case "reset":
		return WatchdogActionReset, nil
	case "shutdown":
		return WatchdogActionShutdown, nil
	case "poweroff":
		return WatchdogActionPoweroff, nil
	case "pause":
		return WatchdogActionPause, nil
	case "none":
		return WatchdogActionNone, nil
	default:
		return WatchdogActionNone, fmt.Errorf("unknown watchdog action: %s", s)
	}
}

// WatchdogExpiryHandler is called when the watchdog timer expires.
// The handler receives the device ID and configured action.
type WatchdogExpiryHandler func(deviceID string, action WatchdogAction)

// VirtioWatchdogConfig holds configuration for a virtio-watchdog device.
type VirtioWatchdogConfig struct {
	// Timeout is the watchdog timeout duration. Guest must heartbeat within this period.
	// Default: 30s. Min: 1s. Max: 10m.
	Timeout time.Duration

	// Action defines what happens on timer expiry.
	// Default: WatchdogActionReset.
	Action WatchdogAction

	// QueueSize is the virtqueue depth. 0 uses the default (16).
	QueueSize int

	// AutoStart controls whether the watchdog timer starts immediately
	// or waits for an explicit enable command from the guest.
	AutoStart bool

	// ExpiryHandler is called when the watchdog fires. If nil, the expiry
	// is logged but no action is taken beyond updating stats.
	ExpiryHandler WatchdogExpiryHandler
}

// VirtioWatchdog implements a watchdog timer device that monitors guest health.
//
// The device has a single virtqueue (controlq). The guest writes command
// structs to the queue; the most common command is a heartbeat that resets
// the timer. If the timer expires, the configured expiry action fires.
type VirtioWatchdog struct {
	mu sync.Mutex

	deviceID string
	config   VirtioWatchdogConfig

	// Virtqueue for watchdog commands from guest
	vq *Virtqueue

	// Device state
	running atomic.Bool
	stopCh  chan struct{}

	// Timer state
	enabled       bool
	timer         *time.Timer
	lastHeartbeat time.Time
	expiryCount   atomic.Uint64

	// Stats
	heartbeats atomic.Uint64
	commands   atomic.Uint64
	expiries   atomic.Uint64
}

// WatchdogStats holds operational counters for a virtio-watchdog device.
type WatchdogStats struct {
	Heartbeats   uint64        `json:"heartbeats"`
	Commands     uint64        `json:"commands"`
	Expiries     uint64        `json:"expiries"`
	Enabled      bool          `json:"enabled"`
	Timeout      time.Duration `json:"timeout"`
	Action       string        `json:"action"`
	LastPet      time.Time     `json:"last_pet,omitempty"`
	TimeToExpiry time.Duration `json:"time_to_expiry,omitempty"`
}

// NewVirtioWatchdog creates a new virtio watchdog timer device.
func NewVirtioWatchdog(deviceID string, cfg VirtioWatchdogConfig) (*VirtioWatchdog, error) {
	if deviceID == "" {
		return nil, fmt.Errorf("virtio-watchdog: device ID is required")
	}

	// Apply defaults
	if cfg.Timeout <= 0 {
		cfg.Timeout = watchdogDefaultTimeout
	}
	if cfg.Timeout < watchdogMinTimeout {
		cfg.Timeout = watchdogMinTimeout
	}
	if cfg.Timeout > watchdogMaxTimeout {
		cfg.Timeout = watchdogMaxTimeout
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = watchdogQueueSize
	}

	// Create the virtqueue
	vq, err := NewVirtqueue(VirtqueueConfig{
		Name: "controlq",
		Num:  uint16(cfg.QueueSize),
	})
	if err != nil {
		return nil, fmt.Errorf("virtio-watchdog: failed to create virtqueue: %w", err)
	}

	dev := &VirtioWatchdog{
		deviceID: deviceID,
		config:   cfg,
		vq:       vq,
		stopCh:   make(chan struct{}),
		enabled:  cfg.AutoStart,
	}

	return dev, nil
}

// Type returns the virtio device type.
func (wd *VirtioWatchdog) Type() DeviceType {
	return DeviceTypeWatchdog
}

// ID returns the device identifier.
func (wd *VirtioWatchdog) ID() string {
	return wd.deviceID
}

// Start initializes the watchdog device and begins processing commands.
func (wd *VirtioWatchdog) Start() error {
	if wd.running.Load() {
		return fmt.Errorf("virtio-watchdog %s: already running", wd.deviceID)
	}

	wd.running.Store(true)

	// If auto-start is enabled, arm the timer now
	if wd.config.AutoStart {
		wd.mu.Lock()
		wd.armTimer()
		wd.mu.Unlock()
	}

	go wd.processLoop()
	return nil
}

// Stop shuts down the watchdog device.
func (wd *VirtioWatchdog) Stop() error {
	if !wd.running.CompareAndSwap(true, false) {
		return nil
	}

	close(wd.stopCh)

	wd.mu.Lock()
	if wd.timer != nil {
		wd.timer.Stop()
		wd.timer = nil
	}
	wd.mu.Unlock()

	return nil
}

// Configure applies runtime configuration changes.
func (wd *VirtioWatchdog) Configure(config map[string]string) error {
	wd.mu.Lock()
	defer wd.mu.Unlock()

	if v, ok := config["timeout"]; ok {
		d, err := time.ParseDuration(v)
		if err == nil && d >= watchdogMinTimeout && d <= watchdogMaxTimeout {
			wd.config.Timeout = d
			if wd.enabled {
				wd.armTimer()
			}
		}
	}

	if v, ok := config["action"]; ok {
		action, err := ParseWatchdogAction(v)
		if err == nil {
			wd.config.Action = action
		}
	}

	if v, ok := config["enabled"]; ok {
		switch v {
		case "true", "1":
			if !wd.enabled {
				wd.enabled = true
				wd.armTimer()
			}
		case "false", "0":
			wd.enabled = false
			if wd.timer != nil {
				wd.timer.Stop()
				wd.timer = nil
			}
		}
	}

	return nil
}

// Stats returns operational statistics for the device.
func (wd *VirtioWatchdog) Stats() WatchdogStats {
	wd.mu.Lock()
	defer wd.mu.Unlock()

	stats := WatchdogStats{
		Heartbeats: wd.heartbeats.Load(),
		Commands:   wd.commands.Load(),
		Expiries:   wd.expiries.Load(),
		Enabled:    wd.enabled,
		Timeout:    wd.config.Timeout,
		Action:     wd.config.Action.String(),
		LastPet:    wd.lastHeartbeat,
	}

	if wd.enabled && !wd.lastHeartbeat.IsZero() {
		deadline := wd.lastHeartbeat.Add(wd.config.Timeout)
		remaining := time.Until(deadline)
		if remaining > 0 {
			stats.TimeToExpiry = remaining
		}
	}

	return stats
}

// IsEnabled returns whether the watchdog timer is currently armed.
func (wd *VirtioWatchdog) IsEnabled() bool {
	wd.mu.Lock()
	defer wd.mu.Unlock()
	return wd.enabled
}

// Pet manually sends a heartbeat (used by the host-side for testing/management).
func (wd *VirtioWatchdog) Pet() {
	wd.mu.Lock()
	defer wd.mu.Unlock()
	wd.handleHeartbeat()
}

// SetExpiryHandler sets the callback invoked when the watchdog fires.
func (wd *VirtioWatchdog) SetExpiryHandler(handler WatchdogExpiryHandler) {
	wd.mu.Lock()
	defer wd.mu.Unlock()
	wd.config.ExpiryHandler = handler
}

// processLoop polls the virtqueue for watchdog commands.
func (wd *VirtioWatchdog) processLoop() {
	ticker := time.NewTicker(watchdogPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-wd.stopCh:
			return
		case <-ticker.C:
			wd.processCommands()
		}
	}
}

// processCommands handles all pending commands on the virtqueue.
func (wd *VirtioWatchdog) processCommands() {
	for {
		chain, err := wd.vq.PopAvailable()
		if err != nil || chain == nil {
			return
		}

		wd.handleCommand(chain)
	}
}

// handleCommand processes a single watchdog command from the guest.
func (wd *VirtioWatchdog) handleCommand(chain *DescriptorChain) {
	wd.commands.Add(1)

	// Read the command code from the first readable descriptor
	if len(chain.Readable) == 0 || chain.TotalRead < 4 {
		// No data — treat as a heartbeat (simple ping)
		wd.mu.Lock()
		wd.handleHeartbeat()
		wd.mu.Unlock()
		_ = wd.vq.PushUsed(chain.HeadIndex, 0)
		return
	}

	// Read command data from the chain
	cmdBytes, err := wd.vq.ReadChainData(chain)
	if err != nil || len(cmdBytes) < 4 {
		_ = wd.vq.PushUsed(chain.HeadIndex, 0)
		return
	}
	cmd := binary.LittleEndian.Uint32(cmdBytes[:4])

	wd.mu.Lock()
	switch cmd {
	case WatchdogCmdHeartbeat:
		wd.handleHeartbeat()

	case WatchdogCmdSetTimeout:
		if len(cmdBytes) >= 8 {
			secs := binary.LittleEndian.Uint32(cmdBytes[4:8])
			timeout := time.Duration(secs) * time.Second
			if timeout >= watchdogMinTimeout && timeout <= watchdogMaxTimeout {
				wd.config.Timeout = timeout
				if wd.enabled {
					wd.armTimer()
				}
			}
		}

	case WatchdogCmdEnable:
		if !wd.enabled {
			wd.enabled = true
			wd.armTimer()
		}

	case WatchdogCmdDisable:
		wd.enabled = false
		if wd.timer != nil {
			wd.timer.Stop()
			wd.timer = nil
		}

	case WatchdogCmdGetStatus:
		// Write status to writable descriptors if available
		if len(chain.Writable) > 0 && chain.TotalWrite >= 16 {
			buf := make([]byte, 16)
			wd.writeStatus(buf)
			_, _ = wd.vq.WriteChainData(chain, buf)
		}
	}
	wd.mu.Unlock()

	_ = wd.vq.PushUsed(chain.HeadIndex, 0)
}

// handleHeartbeat processes a heartbeat (must be called with mu held).
func (wd *VirtioWatchdog) handleHeartbeat() {
	wd.lastHeartbeat = time.Now()
	wd.heartbeats.Add(1)

	if wd.enabled {
		wd.armTimer()
	}
}

// armTimer resets the watchdog timer (must be called with mu held).
func (wd *VirtioWatchdog) armTimer() {
	if wd.timer != nil {
		wd.timer.Stop()
	}

	wd.timer = time.AfterFunc(wd.config.Timeout, func() {
		wd.onExpiry()
	})
}

// onExpiry fires when the watchdog timer expires without a heartbeat.
func (wd *VirtioWatchdog) onExpiry() {
	wd.expiries.Add(1)

	wd.mu.Lock()
	action := wd.config.Action
	handler := wd.config.ExpiryHandler
	enabled := wd.enabled
	wd.mu.Unlock()

	if !enabled {
		return
	}

	if handler != nil {
		handler(wd.deviceID, action)
	}
}

// writeStatus writes the current watchdog status to a buffer (must be called with mu held).
func (wd *VirtioWatchdog) writeStatus(buf []byte) {
	if len(buf) < 16 {
		return
	}

	// Status layout (16 bytes):
	//   [0:4]   enabled (0 or 1)
	//   [4:8]   timeout in seconds
	//   [8:12]  seconds until expiry (0 if not armed)
	//   [12:16] total expiry count

	var enabledVal uint32
	if wd.enabled {
		enabledVal = 1
	}
	binary.LittleEndian.PutUint32(buf[0:4], enabledVal)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(wd.config.Timeout.Seconds()))

	var remaining uint32
	if wd.enabled && !wd.lastHeartbeat.IsZero() {
		deadline := wd.lastHeartbeat.Add(wd.config.Timeout)
		rem := time.Until(deadline)
		if rem > 0 {
			remaining = uint32(rem.Seconds())
		}
	}
	binary.LittleEndian.PutUint32(buf[8:12], remaining)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(wd.expiries.Load()))
}

// GetVirtqueue returns the device's virtqueue for transport attachment.
func (wd *VirtioWatchdog) GetVirtqueue() *Virtqueue {
	return wd.vq
}

// --- Virtio configuration space for the watchdog device ---

// VirtioWatchdogConfigSpace represents the device configuration space.
type VirtioWatchdogConfigSpace struct {
	// TimeoutSecs is the current timeout in seconds (read by guest).
	TimeoutSecs uint32
	// Action is the current expiry action code.
	Action uint32
}

// Bytes serializes the config space to bytes.
func (c *VirtioWatchdogConfigSpace) Bytes() []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf[0:4], c.TimeoutSecs)
	binary.LittleEndian.PutUint32(buf[4:8], c.Action)
	return buf
}

// GetConfigSpace returns the current device configuration space.
func (wd *VirtioWatchdog) GetConfigSpace() *VirtioWatchdogConfigSpace {
	wd.mu.Lock()
	defer wd.mu.Unlock()
	return &VirtioWatchdogConfigSpace{
		TimeoutSecs: uint32(wd.config.Timeout.Seconds()),
		Action:      uint32(wd.config.Action),
	}
}

// --- Feature negotiation ---

const (
	// VirtioWatchdogFSetTimeout allows the guest to change the timeout at runtime.
	VirtioWatchdogFSetTimeout uint64 = 1 << 0

	// VirtioWatchdogFGuestDisable allows the guest to disable the watchdog.
	VirtioWatchdogFGuestDisable uint64 = 1 << 1

	// VirtioWatchdogFStatus allows the guest to query watchdog status.
	VirtioWatchdogFStatus uint64 = 1 << 2
)

// NegotiateFeatures returns the intersection of offered and supported features.
func (wd *VirtioWatchdog) NegotiateFeatures(offered uint64) uint64 {
	supported := VirtioWatchdogFSetTimeout | VirtioWatchdogFGuestDisable | VirtioWatchdogFStatus
	return offered & supported
}

// --- Device reset ---

// Reset returns the device to its initial state.
func (wd *VirtioWatchdog) Reset() {
	wd.mu.Lock()
	defer wd.mu.Unlock()

	wasRunning := wd.running.Load()
	if wasRunning {
		wd.running.Store(false)
		close(wd.stopCh)
	}

	if wd.timer != nil {
		wd.timer.Stop()
		wd.timer = nil
	}

	wd.enabled = wd.config.AutoStart
	wd.lastHeartbeat = time.Time{}
	wd.heartbeats.Store(0)
	wd.commands.Store(0)
	wd.expiries.Store(0)

	wd.vq.Reset()
	wd.stopCh = make(chan struct{})
}

// --- String representation ---

// String returns a human-readable description of the device.
func (wd *VirtioWatchdog) String() string {
	stats := wd.Stats()
	return fmt.Sprintf("virtio-watchdog[%s] enabled=%v timeout=%s action=%s heartbeats=%d expiries=%d",
		wd.deviceID, stats.Enabled, stats.Timeout, stats.Action,
		stats.Heartbeats, stats.Expiries)
}

// --- MMIO/PCI transport configuration ---

// WatchdogDeviceConfig holds MMIO/PCI transport configuration.
type WatchdogDeviceConfig struct {
	DeviceID   uint32
	VendorID   uint32
	DeviceType uint32
	NumQueues  uint32
}

// GetDeviceConfig returns MMIO/PCI transport configuration for this device.
func (wd *VirtioWatchdog) GetDeviceConfig() WatchdogDeviceConfig {
	return WatchdogDeviceConfig{
		DeviceID:   0x23, // Watchdog
		VendorID:   0x554E4554, // "TENT" in ASCII
		DeviceType: uint32(DeviceTypeWatchdog),
		NumQueues:  1, // Single controlq
	}
}
