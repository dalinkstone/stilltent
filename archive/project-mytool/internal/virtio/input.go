// Package virtio provides virtio device emulation for microVMs.
// This file implements virtio-input — a device for passing HID input events
// (keyboard, mouse, touchpad) from the host to the guest VM.
//
// Reference: Virtual I/O Device (VIRTIO) Version 1.2, Section 5.8
//
// The virtio-input device transports Linux evdev events between host and guest.
// Each event is an 8-byte structure: type (2), code (2), value (4).
// The host queues events into the eventq virtqueue; the guest reads them and
// injects into its input subsystem.
package virtio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// DeviceTypeInput is the virtio device type for input devices (Section 5.8).
const DeviceTypeInput DeviceType = 18

// Input event types (matching Linux input.h EV_* constants).
const (
	EvSyn      uint16 = 0x00 // Synchronization event
	EvKey      uint16 = 0x01 // Key/button event
	EvRel      uint16 = 0x02 // Relative axis (mouse movement)
	EvAbs      uint16 = 0x03 // Absolute axis (touchscreen/tablet)
	EvMsc      uint16 = 0x04 // Miscellaneous event
	EvSw       uint16 = 0x05 // Switch event
	EvLed      uint16 = 0x11 // LED event
	EvRep      uint16 = 0x14 // Autorepeat event
)

// Synchronization event codes.
const (
	SynReport    uint16 = 0 // End of event group
	SynConfig    uint16 = 1 // Configuration changed
	SynMTReport  uint16 = 2 // End of multi-touch group
	SynDropped   uint16 = 3 // Events dropped
)

// Common key codes (matching Linux input-event-codes.h KEY_* constants).
const (
	KeyReserved  uint16 = 0
	KeyEsc       uint16 = 1
	Key1         uint16 = 2
	Key2         uint16 = 3
	Key3         uint16 = 4
	Key4         uint16 = 5
	Key5         uint16 = 6
	Key6         uint16 = 7
	Key7         uint16 = 8
	Key8         uint16 = 9
	Key9         uint16 = 10
	Key0         uint16 = 11
	KeyMinus     uint16 = 12
	KeyEqual     uint16 = 13
	KeyBackspace uint16 = 14
	KeyTab       uint16 = 15
	KeyQ         uint16 = 16
	KeyW         uint16 = 17
	KeyE         uint16 = 18
	KeyR         uint16 = 19
	KeyT         uint16 = 20
	KeyY         uint16 = 21
	KeyU         uint16 = 22
	KeyI         uint16 = 23
	KeyO         uint16 = 24
	KeyP         uint16 = 25
	KeyLeftBrace uint16 = 26
	KeyRightBrace uint16 = 27
	KeyEnter     uint16 = 28
	KeyLeftCtrl  uint16 = 29
	KeyA         uint16 = 30
	KeyS         uint16 = 31
	KeyD         uint16 = 32
	KeyF         uint16 = 33
	KeyG         uint16 = 34
	KeyH         uint16 = 35
	KeyJ         uint16 = 36
	KeyK         uint16 = 37
	KeyL         uint16 = 38
	KeySemicolon uint16 = 39
	KeyApostrophe uint16 = 40
	KeyGrave     uint16 = 41
	KeyLeftShift uint16 = 42
	KeyBackslash uint16 = 43
	KeyZ         uint16 = 44
	KeyX         uint16 = 45
	KeyC         uint16 = 46
	KeyV         uint16 = 47
	KeyB         uint16 = 48
	KeyN         uint16 = 49
	KeyM         uint16 = 50
	KeyComma     uint16 = 51
	KeyDot       uint16 = 52
	KeySlash     uint16 = 53
	KeyRightShift uint16 = 54
	KeySpace     uint16 = 57
	KeyCapsLock  uint16 = 58
	KeyF1        uint16 = 59
	KeyF2        uint16 = 60
	KeyF3        uint16 = 61
	KeyF4        uint16 = 62
	KeyF5        uint16 = 63
	KeyF6        uint16 = 64
	KeyF7        uint16 = 65
	KeyF8        uint16 = 66
	KeyF9        uint16 = 67
	KeyF10       uint16 = 68
	KeyF11       uint16 = 87
	KeyF12       uint16 = 88
	KeyUp        uint16 = 103
	KeyLeft      uint16 = 105
	KeyRight     uint16 = 106
	KeyDown      uint16 = 108
	KeyDelete    uint16 = 111
)

// Relative axis codes (REL_*).
const (
	RelX     uint16 = 0x00 // X axis (mouse)
	RelY     uint16 = 0x01 // Y axis (mouse)
	RelZ     uint16 = 0x02 // Z axis
	RelWheel uint16 = 0x08 // Scroll wheel
	RelHWheel uint16 = 0x06 // Horizontal scroll wheel
)

// Absolute axis codes (ABS_*).
const (
	AbsX        uint16 = 0x00 // X coordinate
	AbsY        uint16 = 0x01 // Y coordinate
	AbsPressure uint16 = 0x18 // Pressure
	AbsMTSlot   uint16 = 0x2F // Multi-touch slot
	AbsMTPositionX uint16 = 0x35 // Multi-touch X
	AbsMTPositionY uint16 = 0x36 // Multi-touch Y
	AbsMTTrackingID uint16 = 0x39 // Multi-touch tracking ID
)

// Button codes (BTN_*).
const (
	BtnLeft    uint16 = 0x110 // Left mouse button
	BtnRight   uint16 = 0x111 // Right mouse button
	BtnMiddle  uint16 = 0x112 // Middle mouse button
	BtnTouch   uint16 = 0x14A // Touchscreen touch
)

// InputEvent represents a single Linux input event transported over virtio.
// This matches the struct input_event layout (without timestamp).
type InputEvent struct {
	Type  uint16 // Event type (EV_KEY, EV_REL, etc.)
	Code  uint16 // Event code (KEY_A, REL_X, etc.)
	Value int32  // Event value (1=press, 0=release for keys; delta for rel axes)
}

// InputEventSize is the wire size of a single input event (8 bytes).
const InputEventSize = 8

// InputDeviceSubtype specifies the type of input device being emulated.
type InputDeviceSubtype uint8

const (
	InputSubtypeKeyboard   InputDeviceSubtype = 1
	InputSubtypeMouse      InputDeviceSubtype = 2
	InputSubtypeTablet     InputDeviceSubtype = 3 // Absolute pointer
	InputSubtypeMultitouch InputDeviceSubtype = 4
)

// VirtioInputConfig describes the input device's identity and capabilities.
// This maps to the virtio_input_config structure (Section 5.8.4).
type VirtioInputConfig struct {
	// Device identity
	Name    string             // Human-readable name
	Serial  string             // Serial number
	Subtype InputDeviceSubtype // Device subtype

	// Capabilities — which event types and codes are supported
	SupportedEvents map[uint16][]uint16 // event type → list of supported codes

	// Absolute axis info (for tablets/touchscreens)
	AbsInfo map[uint16]*AbsAxisInfo // abs code → axis info
}

// AbsAxisInfo describes the range and resolution of an absolute axis.
type AbsAxisInfo struct {
	Min        int32
	Max        int32
	Fuzz       int32 // Noise filter
	Flat       int32 // Center deadzone
	Resolution int32
}

// VirtioInput implements a virtio-input device for HID input passthrough.
type VirtioInput struct {
	mu sync.Mutex

	deviceID string
	config   VirtioInputConfig

	// Virtqueues: eventq (host→guest), statusq (guest→host for LED/feedback)
	eventVq  *Virtqueue
	statusVq *Virtqueue

	// Pending events waiting to be delivered to the guest
	pendingEvents []InputEvent

	// Device state
	running atomic.Bool
	stopCh  chan struct{}

	// Statistics
	eventsIn  atomic.Uint64 // Events received from host
	eventsOut atomic.Uint64 // Events delivered to guest
	eventsDropped atomic.Uint64 // Events dropped (queue full)
}

// VirtioInputOpts holds options for creating a VirtioInput device.
type VirtioInputOpts struct {
	DeviceID   string
	Config     VirtioInputConfig
	EventQueue *Virtqueue
	StatusQueue *Virtqueue
}

// NewVirtioInput creates a new virtio-input device.
func NewVirtioInput(opts VirtioInputOpts) (*VirtioInput, error) {
	if opts.DeviceID == "" {
		return nil, errors.New("virtio-input: device ID is required")
	}
	if opts.Config.Name == "" {
		return nil, errors.New("virtio-input: device name is required")
	}

	dev := &VirtioInput{
		deviceID: opts.DeviceID,
		config:   opts.Config,
		eventVq:  opts.EventQueue,
		statusVq: opts.StatusQueue,
		stopCh:   make(chan struct{}),
	}

	return dev, nil
}

// NewKeyboard creates a virtio-input device configured as a standard keyboard.
func NewKeyboard(deviceID string, eventVq, statusVq *Virtqueue) (*VirtioInput, error) {
	allKeys := make([]uint16, 0, 128)
	for k := KeyEsc; k <= KeyF10; k++ {
		allKeys = append(allKeys, k)
	}
	allKeys = append(allKeys, KeyF11, KeyF12, KeyUp, KeyLeft, KeyRight, KeyDown, KeyDelete)

	cfg := VirtioInputConfig{
		Name:    "tent-virtio-keyboard",
		Serial:  "tent-kbd-001",
		Subtype: InputSubtypeKeyboard,
		SupportedEvents: map[uint16][]uint16{
			EvSyn: {SynReport},
			EvKey: allKeys,
			EvRep: {},
			EvLed: {},
		},
	}

	return NewVirtioInput(VirtioInputOpts{
		DeviceID:    deviceID,
		Config:      cfg,
		EventQueue:  eventVq,
		StatusQueue: statusVq,
	})
}

// NewMouse creates a virtio-input device configured as a relative pointer (mouse).
func NewMouse(deviceID string, eventVq, statusVq *Virtqueue) (*VirtioInput, error) {
	cfg := VirtioInputConfig{
		Name:    "tent-virtio-mouse",
		Serial:  "tent-mouse-001",
		Subtype: InputSubtypeMouse,
		SupportedEvents: map[uint16][]uint16{
			EvSyn: {SynReport},
			EvKey: {BtnLeft, BtnRight, BtnMiddle},
			EvRel: {RelX, RelY, RelWheel, RelHWheel},
		},
	}

	return NewVirtioInput(VirtioInputOpts{
		DeviceID:    deviceID,
		Config:      cfg,
		EventQueue:  eventVq,
		StatusQueue: statusVq,
	})
}

// NewTablet creates a virtio-input device configured as an absolute pointer (tablet).
func NewTablet(deviceID string, width, height int32, eventVq, statusVq *Virtqueue) (*VirtioInput, error) {
	if width <= 0 || height <= 0 {
		return nil, errors.New("virtio-input: tablet dimensions must be positive")
	}

	cfg := VirtioInputConfig{
		Name:    "tent-virtio-tablet",
		Serial:  "tent-tablet-001",
		Subtype: InputSubtypeTablet,
		SupportedEvents: map[uint16][]uint16{
			EvSyn: {SynReport},
			EvKey: {BtnLeft, BtnRight, BtnMiddle, BtnTouch},
			EvAbs: {AbsX, AbsY, AbsPressure},
		},
		AbsInfo: map[uint16]*AbsAxisInfo{
			AbsX: {Min: 0, Max: width - 1, Resolution: 1},
			AbsY: {Min: 0, Max: height - 1, Resolution: 1},
			AbsPressure: {Min: 0, Max: 255},
		},
	}

	return NewVirtioInput(VirtioInputOpts{
		DeviceID:    deviceID,
		Config:      cfg,
		EventQueue:  eventVq,
		StatusQueue: statusVq,
	})
}

// Type returns DeviceTypeInput.
func (d *VirtioInput) Type() DeviceType {
	return DeviceTypeInput
}

// ID returns the device identifier.
func (d *VirtioInput) ID() string {
	return d.deviceID
}

// Start begins the input device.
func (d *VirtioInput) Start() error {
	if d.running.Load() {
		return errors.New("virtio-input: already running")
	}
	d.running.Store(true)
	return nil
}

// Stop halts the input device and discards pending events.
func (d *VirtioInput) Stop() error {
	if !d.running.CompareAndSwap(true, false) {
		return nil
	}
	close(d.stopCh)

	d.mu.Lock()
	d.pendingEvents = nil
	d.mu.Unlock()

	return nil
}

// Configure applies configuration parameters.
func (d *VirtioInput) Configure(config map[string]string) error {
	return nil
}

// maxPendingEvents is the maximum number of events that can be queued.
const maxPendingEvents = 4096

// InjectEvent queues a single input event for delivery to the guest.
func (d *VirtioInput) InjectEvent(ev InputEvent) error {
	if !d.running.Load() {
		return errors.New("virtio-input: device not running")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.pendingEvents) >= maxPendingEvents {
		d.eventsDropped.Add(1)
		return fmt.Errorf("virtio-input: event queue full (%d events)", maxPendingEvents)
	}

	d.pendingEvents = append(d.pendingEvents, ev)
	d.eventsIn.Add(1)
	return nil
}

// InjectEvents queues multiple input events atomically.
func (d *VirtioInput) InjectEvents(events []InputEvent) error {
	if !d.running.Load() {
		return errors.New("virtio-input: device not running")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.pendingEvents)+len(events) > maxPendingEvents {
		d.eventsDropped.Add(uint64(len(events)))
		return fmt.Errorf("virtio-input: insufficient queue space (%d/%d)",
			len(d.pendingEvents), maxPendingEvents)
	}

	d.pendingEvents = append(d.pendingEvents, events...)
	d.eventsIn.Add(uint64(len(events)))
	return nil
}

// InjectKeyPress injects a key press and release with a SYN_REPORT after each.
func (d *VirtioInput) InjectKeyPress(code uint16) error {
	events := []InputEvent{
		{Type: EvKey, Code: code, Value: 1}, // Key down
		{Type: EvSyn, Code: SynReport, Value: 0},
		{Type: EvKey, Code: code, Value: 0}, // Key up
		{Type: EvSyn, Code: SynReport, Value: 0},
	}
	return d.InjectEvents(events)
}

// InjectMouseMove injects a relative mouse movement event.
func (d *VirtioInput) InjectMouseMove(dx, dy int32) error {
	events := []InputEvent{
		{Type: EvRel, Code: RelX, Value: dx},
		{Type: EvRel, Code: RelY, Value: dy},
		{Type: EvSyn, Code: SynReport, Value: 0},
	}
	return d.InjectEvents(events)
}

// InjectMouseButton injects a mouse button press or release.
// pressed: true for press, false for release.
func (d *VirtioInput) InjectMouseButton(button uint16, pressed bool) error {
	val := int32(0)
	if pressed {
		val = 1
	}
	events := []InputEvent{
		{Type: EvKey, Code: button, Value: val},
		{Type: EvSyn, Code: SynReport, Value: 0},
	}
	return d.InjectEvents(events)
}

// InjectMouseClick injects a mouse button click (press + release).
func (d *VirtioInput) InjectMouseClick(button uint16) error {
	events := []InputEvent{
		{Type: EvKey, Code: button, Value: 1},
		{Type: EvSyn, Code: SynReport, Value: 0},
		{Type: EvKey, Code: button, Value: 0},
		{Type: EvSyn, Code: SynReport, Value: 0},
	}
	return d.InjectEvents(events)
}

// InjectScroll injects a scroll wheel event.
func (d *VirtioInput) InjectScroll(delta int32) error {
	events := []InputEvent{
		{Type: EvRel, Code: RelWheel, Value: delta},
		{Type: EvSyn, Code: SynReport, Value: 0},
	}
	return d.InjectEvents(events)
}

// InjectAbsMove injects an absolute pointer movement (for tablets/touchscreens).
func (d *VirtioInput) InjectAbsMove(x, y int32) error {
	events := []InputEvent{
		{Type: EvAbs, Code: AbsX, Value: x},
		{Type: EvAbs, Code: AbsY, Value: y},
		{Type: EvSyn, Code: SynReport, Value: 0},
	}
	return d.InjectEvents(events)
}

// InjectText injects a sequence of key events to type a string.
// Only supports basic ASCII characters (a-z, 0-9, space, common punctuation).
func (d *VirtioInput) InjectText(text string) error {
	for _, ch := range text {
		code, shift := charToKeyCode(ch)
		if code == KeyReserved {
			continue // Skip unsupported characters
		}

		var events []InputEvent
		if shift {
			events = append(events, InputEvent{Type: EvKey, Code: KeyLeftShift, Value: 1})
			events = append(events, InputEvent{Type: EvSyn, Code: SynReport, Value: 0})
		}
		events = append(events, InputEvent{Type: EvKey, Code: code, Value: 1})
		events = append(events, InputEvent{Type: EvSyn, Code: SynReport, Value: 0})
		events = append(events, InputEvent{Type: EvKey, Code: code, Value: 0})
		events = append(events, InputEvent{Type: EvSyn, Code: SynReport, Value: 0})
		if shift {
			events = append(events, InputEvent{Type: EvKey, Code: KeyLeftShift, Value: 0})
			events = append(events, InputEvent{Type: EvSyn, Code: SynReport, Value: 0})
		}

		if err := d.InjectEvents(events); err != nil {
			return err
		}
	}
	return nil
}

// DrainEvents returns and clears all pending events.
func (d *VirtioInput) DrainEvents() []InputEvent {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.pendingEvents) == 0 {
		return nil
	}

	events := d.pendingEvents
	d.pendingEvents = nil
	d.eventsOut.Add(uint64(len(events)))
	return events
}

// EncodeEvents serializes a slice of input events into a byte buffer.
// Each event is encoded as 8 bytes: type(2) + code(2) + value(4), little-endian.
func EncodeEvents(events []InputEvent) []byte {
	buf := make([]byte, len(events)*InputEventSize)
	for i, ev := range events {
		off := i * InputEventSize
		binary.LittleEndian.PutUint16(buf[off:off+2], ev.Type)
		binary.LittleEndian.PutUint16(buf[off+2:off+4], ev.Code)
		binary.LittleEndian.PutUint32(buf[off+4:off+8], uint32(ev.Value))
	}
	return buf
}

// DecodeEvents deserializes input events from a byte buffer.
func DecodeEvents(buf []byte) ([]InputEvent, error) {
	if len(buf)%InputEventSize != 0 {
		return nil, fmt.Errorf("virtio-input: buffer size %d not a multiple of %d", len(buf), InputEventSize)
	}

	count := len(buf) / InputEventSize
	events := make([]InputEvent, count)
	for i := range events {
		off := i * InputEventSize
		events[i] = InputEvent{
			Type:  binary.LittleEndian.Uint16(buf[off : off+2]),
			Code:  binary.LittleEndian.Uint16(buf[off+2 : off+4]),
			Value: int32(binary.LittleEndian.Uint32(buf[off+4 : off+8])),
		}
	}
	return events, nil
}

// PendingCount returns the number of events waiting to be delivered.
func (d *VirtioInput) PendingCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pendingEvents)
}

// Config returns the device configuration.
func (d *VirtioInput) Config() VirtioInputConfig {
	return d.config
}

// InputStats holds device event statistics.
type InputStats struct {
	EventsIn      uint64    `json:"events_in"`
	EventsOut     uint64    `json:"events_out"`
	EventsDropped uint64    `json:"events_dropped"`
	Pending       int       `json:"pending"`
	Timestamp     time.Time `json:"timestamp"`
}

// Stats returns device event statistics.
func (d *VirtioInput) Stats() InputStats {
	return InputStats{
		EventsIn:      d.eventsIn.Load(),
		EventsOut:     d.eventsOut.Load(),
		EventsDropped: d.eventsDropped.Load(),
		Pending:       d.PendingCount(),
		Timestamp:     time.Now(),
	}
}

// charToKeyCode maps an ASCII character to a Linux key code and whether shift is needed.
func charToKeyCode(ch rune) (uint16, bool) {
	switch {
	case ch >= 'a' && ch <= 'z':
		return KeyA + uint16(ch-'a'), false
	case ch >= 'A' && ch <= 'Z':
		return KeyA + uint16(ch-'A'), true
	case ch >= '1' && ch <= '9':
		return Key1 + uint16(ch-'1'), false
	case ch == '0':
		return Key0, false
	case ch == ' ':
		return KeySpace, false
	case ch == '\n':
		return KeyEnter, false
	case ch == '\t':
		return KeyTab, false
	case ch == '-':
		return KeyMinus, false
	case ch == '=':
		return KeyEqual, false
	case ch == '[':
		return KeyLeftBrace, false
	case ch == ']':
		return KeyRightBrace, false
	case ch == '\\':
		return KeyBackslash, false
	case ch == ';':
		return KeySemicolon, false
	case ch == '\'':
		return KeyApostrophe, false
	case ch == '`':
		return KeyGrave, false
	case ch == ',':
		return KeyComma, false
	case ch == '.':
		return KeyDot, false
	case ch == '/':
		return KeySlash, false
	case ch == '!':
		return Key1, true
	case ch == '@':
		return Key2, true
	case ch == '#':
		return Key3, true
	case ch == '$':
		return Key4, true
	case ch == '%':
		return Key5, true
	case ch == '^':
		return Key6, true
	case ch == '&':
		return Key7, true
	case ch == '*':
		return Key8, true
	case ch == '(':
		return Key9, true
	case ch == ')':
		return Key0, true
	case ch == '_':
		return KeyMinus, true
	case ch == '+':
		return KeyEqual, true
	case ch == '{':
		return KeyLeftBrace, true
	case ch == '}':
		return KeyRightBrace, true
	case ch == '|':
		return KeyBackslash, true
	case ch == ':':
		return KeySemicolon, true
	case ch == '"':
		return KeyApostrophe, true
	case ch == '~':
		return KeyGrave, true
	case ch == '<':
		return KeyComma, true
	case ch == '>':
		return KeyDot, true
	case ch == '?':
		return KeySlash, true
	default:
		return KeyReserved, false
	}
}
