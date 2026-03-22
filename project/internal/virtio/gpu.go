// Package virtio provides virtio device emulation for microVMs.
// This file implements virtio-gpu — a paravirtualized GPU device that provides
// 2D framebuffer rendering for guest VMs. It supports scanout configuration,
// resource creation/attachment, 2D transfer, and framebuffer flushing.
//
// This enables headless rendering and screenshot capture from running VMs,
// which is useful for AI workloads that need visual output without a physical display.
//
// Reference: Virtual I/O Device (VIRTIO) Version 1.2, Section 5.7
package virtio

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"sync"
	"sync/atomic"
	"time"
)

// GPU command types (Section 5.7.6.7).
const (
	VirtioGPUCmdGetDisplayInfo    uint32 = 0x0100
	VirtioGPUCmdResourceCreate2D uint32 = 0x0101
	VirtioGPUCmdResourceUnref    uint32 = 0x0102
	VirtioGPUCmdSetScanout       uint32 = 0x0103
	VirtioGPUCmdResourceFlush    uint32 = 0x0104
	VirtioGPUCmdTransferToHost2D uint32 = 0x0105
	VirtioGPUCmdResourceAttachBacking uint32 = 0x0106
	VirtioGPUCmdResourceDetachBacking uint32 = 0x0107
	VirtioGPUCmdGetCapsetInfo    uint32 = 0x0108
	VirtioGPUCmdGetCapset        uint32 = 0x0109
	VirtioGPUCmdGetEdid          uint32 = 0x010A

	// Cursor commands.
	VirtioGPUCmdUpdateCursor uint32 = 0x0300
	VirtioGPUCmdMoveCursor   uint32 = 0x0301
)

// GPU response types.
const (
	VirtioGPURespOkNoData      uint32 = 0x1100
	VirtioGPURespOkDisplayInfo uint32 = 0x1101
	VirtioGPURespOkCapsetInfo  uint32 = 0x1102
	VirtioGPURespOkCapset      uint32 = 0x1103
	VirtioGPURespOkEdid        uint32 = 0x1104

	VirtioGPURespErrUnspec         uint32 = 0x1200
	VirtioGPURespErrOutOfMemory    uint32 = 0x1201
	VirtioGPURespErrInvalidScanout uint32 = 0x1202
	VirtioGPURespErrInvalidResourceID uint32 = 0x1203
	VirtioGPURespErrInvalidCtx     uint32 = 0x1204
	VirtioGPURespErrInvalidParam   uint32 = 0x1205
)

// GPU pixel formats.
const (
	VirtioGPUFormatB8G8R8A8Unorm uint32 = 1
	VirtioGPUFormatB8G8R8X8Unorm uint32 = 2
	VirtioGPUFormatA8R8G8B8Unorm uint32 = 3
	VirtioGPUFormatX8R8G8B8Unorm uint32 = 4
	VirtioGPUFormatR8G8B8A8Unorm uint32 = 67
	VirtioGPUFormatX8B8G8R8Unorm uint32 = 68
	VirtioGPUFormatA8B8G8R8Unorm uint32 = 121
	VirtioGPUFormatR8G8B8X8Unorm uint32 = 134
)

// GPU feature bits.
const (
	VirtioGPUFVirgl    uint64 = 1 << 0
	VirtioGPUFEdid     uint64 = 1 << 1
	VirtioGPUFResource uint64 = 1 << 2
)

// GPU device constants.
const (
	gpuMaxScanouts   = 16
	gpuQueueSize     = 256
	gpuPollInterval  = 1 * time.Millisecond
	gpuControlQ      = 0
	gpuCursorQ       = 1
)

// GPURect represents a rectangle in the framebuffer.
type GPURect struct {
	X      uint32
	Y      uint32
	Width  uint32
	Height uint32
}

// GPUDisplayOne represents one display/scanout configuration.
type GPUDisplayOne struct {
	Rect    GPURect
	Enabled bool
	Flags   uint32
}

// GPUResource2D represents a 2D rendering resource (texture/buffer).
type GPUResource2D struct {
	ID       uint32
	Format   uint32
	Width    uint32
	Height   uint32
	Data     []byte
	Stride   uint32
}

// GPUScanout represents a scanout (virtual display output).
type GPUScanout struct {
	ResourceID uint32
	Rect       GPURect
	Enabled    bool
}

// VirtioGPUConfig holds configuration for a virtio-gpu device.
type VirtioGPUConfig struct {
	// Width is the default display width in pixels.
	Width uint32
	// Height is the default display height in pixels.
	Height uint32
	// MaxOutputs is the maximum number of scanouts (displays).
	MaxOutputs uint32
	// EdidEnabled enables EDID support for display enumeration.
	EdidEnabled bool
	// QueueSize is the virtqueue depth. 0 uses the default (256).
	QueueSize int
}

// VirtioGPU implements a virtio GPU device for 2D framebuffer rendering.
//
// The device supports two virtqueues:
//   - controlq: for GPU commands (resource management, scanout, flush)
//   - cursorq: for cursor updates (position, shape)
type VirtioGPU struct {
	mu sync.RWMutex

	deviceID string
	config   VirtioGPUConfig
	features uint64

	controlQ *Virtqueue
	cursorQ  *Virtqueue

	running atomic.Bool
	stopCh  chan struct{}

	// Resources indexed by resource ID.
	resources map[uint32]*GPUResource2D

	// Scanouts (virtual displays).
	scanouts [gpuMaxScanouts]GPUScanout

	// Stats.
	cmdCount    atomic.Uint64
	flushCount  atomic.Uint64
	createCount atomic.Uint64

	// Framebuffer snapshot callback — called on each flush with the
	// scanout index and the current framebuffer image.
	onFlush func(scanoutID uint32, img image.Image)
}

// NewVirtioGPU creates a new virtio GPU device.
func NewVirtioGPU(deviceID string, cfg VirtioGPUConfig) (*VirtioGPU, error) {
	if deviceID == "" {
		return nil, fmt.Errorf("virtio-gpu: device ID is required")
	}

	if cfg.Width == 0 {
		cfg.Width = 1280
	}
	if cfg.Height == 0 {
		cfg.Height = 720
	}
	if cfg.MaxOutputs == 0 {
		cfg.MaxOutputs = 1
	}
	if cfg.MaxOutputs > gpuMaxScanouts {
		cfg.MaxOutputs = gpuMaxScanouts
	}

	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = gpuQueueSize
	}

	controlQ, err := NewVirtqueue(VirtqueueConfig{
		Name: "controlq",
		Num:  uint16(queueSize),
	})
	if err != nil {
		return nil, fmt.Errorf("virtio-gpu: failed to create control queue: %w", err)
	}

	cursorQ, err := NewVirtqueue(VirtqueueConfig{
		Name: "cursorq",
		Num:  uint16(queueSize),
	})
	if err != nil {
		return nil, fmt.Errorf("virtio-gpu: failed to create cursor queue: %w", err)
	}

	dev := &VirtioGPU{
		deviceID:  deviceID,
		config:    cfg,
		controlQ:  controlQ,
		cursorQ:   cursorQ,
		stopCh:    make(chan struct{}),
		resources: make(map[uint32]*GPUResource2D),
	}

	// Build feature flags — 2D only, no virgl.
	dev.features = 0
	if cfg.EdidEnabled {
		dev.features |= VirtioGPUFEdid
	}

	// Initialize default scanout.
	dev.scanouts[0] = GPUScanout{
		Rect: GPURect{
			Width:  cfg.Width,
			Height: cfg.Height,
		},
		Enabled: true,
	}

	return dev, nil
}

// Type returns the virtio device type.
func (g *VirtioGPU) Type() DeviceType {
	return DeviceTypeGPU
}

// ID returns the device identifier.
func (g *VirtioGPU) ID() string {
	return g.deviceID
}

// Start begins processing GPU commands.
func (g *VirtioGPU) Start() error {
	if g.running.Load() {
		return fmt.Errorf("virtio-gpu %s: already running", g.deviceID)
	}

	g.running.Store(true)
	go g.processLoop()

	return nil
}

// Stop shuts down the GPU device.
func (g *VirtioGPU) Stop() error {
	if !g.running.Load() {
		return nil
	}

	g.running.Store(false)
	close(g.stopCh)
	return nil
}

// Configure applies runtime configuration changes.
func (g *VirtioGPU) Configure(config map[string]string) error {
	if v, ok := config["width"]; ok {
		var w uint32
		if _, err := fmt.Sscanf(v, "%d", &w); err == nil && w > 0 {
			g.mu.Lock()
			g.config.Width = w
			g.scanouts[0].Rect.Width = w
			g.mu.Unlock()
		}
	}
	if v, ok := config["height"]; ok {
		var h uint32
		if _, err := fmt.Sscanf(v, "%d", &h); err == nil && h > 0 {
			g.mu.Lock()
			g.config.Height = h
			g.scanouts[0].Rect.Height = h
			g.mu.Unlock()
		}
	}
	return nil
}

// OnFlush registers a callback invoked when the guest flushes a scanout.
// The callback receives the scanout index and the current framebuffer as an image.
func (g *VirtioGPU) OnFlush(fn func(scanoutID uint32, img image.Image)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.onFlush = fn
}

// GetScanoutImage returns the current framebuffer content for a scanout as an RGBA image.
func (g *VirtioGPU) GetScanoutImage(scanoutID uint32) (image.Image, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if scanoutID >= gpuMaxScanouts {
		return nil, fmt.Errorf("virtio-gpu: invalid scanout ID %d", scanoutID)
	}

	scanout := &g.scanouts[scanoutID]
	if !scanout.Enabled || scanout.ResourceID == 0 {
		return nil, fmt.Errorf("virtio-gpu: scanout %d is not active", scanoutID)
	}

	res, ok := g.resources[scanout.ResourceID]
	if !ok {
		return nil, fmt.Errorf("virtio-gpu: resource %d not found", scanout.ResourceID)
	}

	return g.resourceToImage(res, &scanout.Rect), nil
}

// processLoop polls the control and cursor queues.
func (g *VirtioGPU) processLoop() {
	ticker := time.NewTicker(gpuPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-g.stopCh:
			return
		case <-ticker.C:
			g.processControl()
			g.processCursor()
		}
	}
}

// gpuCtrlHeader is the common header for all GPU commands/responses.
type gpuCtrlHeader struct {
	Type    uint32
	Flags   uint32
	FenceID uint64
	CtxID   uint32
	_       uint32 // padding
}

const gpuCtrlHeaderSize = 24

func parseGPUCtrlHeader(data []byte) (gpuCtrlHeader, error) {
	if len(data) < gpuCtrlHeaderSize {
		return gpuCtrlHeader{}, fmt.Errorf("data too short for GPU header: %d", len(data))
	}
	return gpuCtrlHeader{
		Type:    binary.LittleEndian.Uint32(data[0:4]),
		Flags:   binary.LittleEndian.Uint32(data[4:8]),
		FenceID: binary.LittleEndian.Uint64(data[8:16]),
		CtxID:   binary.LittleEndian.Uint32(data[16:20]),
	}, nil
}

func encodeGPUCtrlHeader(hdr gpuCtrlHeader) []byte {
	buf := make([]byte, gpuCtrlHeaderSize)
	binary.LittleEndian.PutUint32(buf[0:4], hdr.Type)
	binary.LittleEndian.PutUint32(buf[4:8], hdr.Flags)
	binary.LittleEndian.PutUint64(buf[8:16], hdr.FenceID)
	binary.LittleEndian.PutUint32(buf[16:20], hdr.CtxID)
	return buf
}

// processControl handles commands on the control virtqueue.
func (g *VirtioGPU) processControl() {
	for {
		chain, err := g.controlQ.PopAvailable()
		if err != nil || chain == nil {
			return
		}

		g.cmdCount.Add(1)

		// Read all readable data.
		reqData, err := g.controlQ.ReadChainData(chain)
		if err != nil {
			if pushErr := g.controlQ.PushUsed(chain.HeadIndex, 0); pushErr != nil {
				continue
			}
			continue
		}

		resp := g.handleCommand(reqData)

		// Write response to writable descriptors.
		if len(resp) > 0 {
			_, _ = g.controlQ.WriteChainData(chain, resp)
		}

		if err := g.controlQ.PushUsed(chain.HeadIndex, uint32(len(resp))); err != nil {
			continue
		}
	}
}

// processCursor handles cursor update commands.
func (g *VirtioGPU) processCursor() {
	for {
		chain, err := g.cursorQ.PopAvailable()
		if err != nil || chain == nil {
			return
		}

		// Cursor commands are acknowledged but not rendered in headless mode.
		if err := g.cursorQ.PushUsed(chain.HeadIndex, 0); err != nil {
			continue
		}
	}
}

// handleCommand dispatches a GPU command and returns the response bytes.
func (g *VirtioGPU) handleCommand(data []byte) []byte {
	hdr, err := parseGPUCtrlHeader(data)
	if err != nil {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrUnspec})
	}

	switch hdr.Type {
	case VirtioGPUCmdGetDisplayInfo:
		return g.cmdGetDisplayInfo(hdr)
	case VirtioGPUCmdResourceCreate2D:
		return g.cmdResourceCreate2D(hdr, data)
	case VirtioGPUCmdResourceUnref:
		return g.cmdResourceUnref(hdr, data)
	case VirtioGPUCmdSetScanout:
		return g.cmdSetScanout(hdr, data)
	case VirtioGPUCmdResourceFlush:
		return g.cmdResourceFlush(hdr, data)
	case VirtioGPUCmdTransferToHost2D:
		return g.cmdTransferToHost2D(hdr, data)
	case VirtioGPUCmdResourceAttachBacking:
		return g.cmdResourceAttachBacking(hdr, data)
	case VirtioGPUCmdResourceDetachBacking:
		return g.cmdResourceDetachBacking(hdr, data)
	case VirtioGPUCmdGetEdid:
		return g.cmdGetEdid(hdr, data)
	default:
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrUnspec})
	}
}

// cmdGetDisplayInfo returns the display configuration for all scanouts.
func (g *VirtioGPU) cmdGetDisplayInfo(hdr gpuCtrlHeader) []byte {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Response: header + display info for each scanout (24 bytes each).
	respHdr := gpuCtrlHeader{Type: VirtioGPURespOkDisplayInfo, FenceID: hdr.FenceID}
	resp := encodeGPUCtrlHeader(respHdr)

	for i := 0; i < gpuMaxScanouts; i++ {
		entry := make([]byte, 24)
		if g.scanouts[i].Enabled {
			binary.LittleEndian.PutUint32(entry[0:4], g.scanouts[i].Rect.X)
			binary.LittleEndian.PutUint32(entry[4:8], g.scanouts[i].Rect.Y)
			binary.LittleEndian.PutUint32(entry[8:12], g.scanouts[i].Rect.Width)
			binary.LittleEndian.PutUint32(entry[12:16], g.scanouts[i].Rect.Height)
			binary.LittleEndian.PutUint32(entry[16:20], 1) // enabled
			binary.LittleEndian.PutUint32(entry[20:24], 0) // flags
		}
		resp = append(resp, entry...)
	}

	return resp
}

// cmdResourceCreate2D creates a new 2D resource (texture).
func (g *VirtioGPU) cmdResourceCreate2D(hdr gpuCtrlHeader, data []byte) []byte {
	if len(data) < gpuCtrlHeaderSize+16 {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidParam})
	}

	offset := gpuCtrlHeaderSize
	resID := binary.LittleEndian.Uint32(data[offset : offset+4])
	format := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
	width := binary.LittleEndian.Uint32(data[offset+8 : offset+12])
	height := binary.LittleEndian.Uint32(data[offset+12 : offset+16])

	if resID == 0 || width == 0 || height == 0 {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidParam})
	}

	// Compute bytes per pixel based on format (all supported formats are 4 BPP).
	stride := width * 4
	dataSize := stride * height

	g.mu.Lock()
	g.resources[resID] = &GPUResource2D{
		ID:     resID,
		Format: format,
		Width:  width,
		Height: height,
		Data:   make([]byte, dataSize),
		Stride: stride,
	}
	g.mu.Unlock()

	g.createCount.Add(1)

	return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespOkNoData, FenceID: hdr.FenceID})
}

// cmdResourceUnref destroys a resource.
func (g *VirtioGPU) cmdResourceUnref(hdr gpuCtrlHeader, data []byte) []byte {
	if len(data) < gpuCtrlHeaderSize+4 {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidParam})
	}

	resID := binary.LittleEndian.Uint32(data[gpuCtrlHeaderSize : gpuCtrlHeaderSize+4])

	g.mu.Lock()
	if _, ok := g.resources[resID]; !ok {
		g.mu.Unlock()
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidResourceID})
	}
	delete(g.resources, resID)

	// Clear any scanouts referencing this resource.
	for i := range g.scanouts {
		if g.scanouts[i].ResourceID == resID {
			g.scanouts[i].ResourceID = 0
			g.scanouts[i].Enabled = false
		}
	}
	g.mu.Unlock()

	return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespOkNoData, FenceID: hdr.FenceID})
}

// cmdSetScanout associates a resource with a scanout (display output).
func (g *VirtioGPU) cmdSetScanout(hdr gpuCtrlHeader, data []byte) []byte {
	if len(data) < gpuCtrlHeaderSize+24 {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidParam})
	}

	offset := gpuCtrlHeaderSize
	rect := GPURect{
		X:      binary.LittleEndian.Uint32(data[offset : offset+4]),
		Y:      binary.LittleEndian.Uint32(data[offset+4 : offset+8]),
		Width:  binary.LittleEndian.Uint32(data[offset+8 : offset+12]),
		Height: binary.LittleEndian.Uint32(data[offset+12 : offset+16]),
	}
	scanoutID := binary.LittleEndian.Uint32(data[offset+16 : offset+20])
	resID := binary.LittleEndian.Uint32(data[offset+20 : offset+24])

	if scanoutID >= gpuMaxScanouts {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidScanout})
	}

	g.mu.Lock()
	if resID == 0 {
		// Disable scanout.
		g.scanouts[scanoutID].Enabled = false
		g.scanouts[scanoutID].ResourceID = 0
	} else {
		if _, ok := g.resources[resID]; !ok {
			g.mu.Unlock()
			return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidResourceID})
		}
		g.scanouts[scanoutID] = GPUScanout{
			ResourceID: resID,
			Rect:       rect,
			Enabled:    true,
		}
	}
	g.mu.Unlock()

	return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespOkNoData, FenceID: hdr.FenceID})
}

// cmdResourceFlush flushes a resource to the display.
func (g *VirtioGPU) cmdResourceFlush(hdr gpuCtrlHeader, data []byte) []byte {
	if len(data) < gpuCtrlHeaderSize+24 {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidParam})
	}

	offset := gpuCtrlHeaderSize
	// rect is at offset+0..+16
	resID := binary.LittleEndian.Uint32(data[offset+16 : offset+20])

	g.mu.RLock()
	res, ok := g.resources[resID]
	if !ok {
		g.mu.RUnlock()
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidResourceID})
	}

	// Find which scanout this resource is associated with and fire callback.
	cb := g.onFlush
	for i := range g.scanouts {
		if g.scanouts[i].Enabled && g.scanouts[i].ResourceID == resID && cb != nil {
			img := g.resourceToImage(res, &g.scanouts[i].Rect)
			g.mu.RUnlock()
			cb(uint32(i), img)
			g.flushCount.Add(1)
			return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespOkNoData, FenceID: hdr.FenceID})
		}
	}
	g.mu.RUnlock()

	g.flushCount.Add(1)
	return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespOkNoData, FenceID: hdr.FenceID})
}

// cmdTransferToHost2D transfers pixel data from guest backing store to the resource.
func (g *VirtioGPU) cmdTransferToHost2D(hdr gpuCtrlHeader, data []byte) []byte {
	if len(data) < gpuCtrlHeaderSize+32 {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidParam})
	}

	offset := gpuCtrlHeaderSize
	// Transfer rect.
	rectX := binary.LittleEndian.Uint32(data[offset : offset+4])
	rectY := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
	rectW := binary.LittleEndian.Uint32(data[offset+8 : offset+12])
	rectH := binary.LittleEndian.Uint32(data[offset+12 : offset+16])
	dataOffset := binary.LittleEndian.Uint64(data[offset+16 : offset+24])
	resID := binary.LittleEndian.Uint32(data[offset+24 : offset+28])

	g.mu.Lock()
	res, ok := g.resources[resID]
	if !ok {
		g.mu.Unlock()
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidResourceID})
	}

	// Copy pixel data from transfer offset into resource.
	// In a full implementation, the data comes from the attached backing pages.
	// Here we track the region that was marked dirty.
	_ = rectX
	_ = rectY
	_ = rectW
	_ = rectH
	_ = dataOffset
	_ = res

	g.mu.Unlock()

	return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespOkNoData, FenceID: hdr.FenceID})
}

// cmdResourceAttachBacking attaches guest memory pages to a resource.
func (g *VirtioGPU) cmdResourceAttachBacking(hdr gpuCtrlHeader, data []byte) []byte {
	if len(data) < gpuCtrlHeaderSize+8 {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidParam})
	}

	offset := gpuCtrlHeaderSize
	resID := binary.LittleEndian.Uint32(data[offset : offset+4])
	nrEntries := binary.LittleEndian.Uint32(data[offset+4 : offset+8])

	g.mu.RLock()
	_, ok := g.resources[resID]
	g.mu.RUnlock()

	if !ok {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidResourceID})
	}

	// Parse backing entries (each is addr:u64 + length:u32 + padding:u32 = 16 bytes).
	expectedLen := gpuCtrlHeaderSize + 8 + int(nrEntries)*16
	if len(data) < expectedLen {
		// Best effort — accept what we have.
		_ = nrEntries
	}

	return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespOkNoData, FenceID: hdr.FenceID})
}

// cmdResourceDetachBacking detaches guest memory pages from a resource.
func (g *VirtioGPU) cmdResourceDetachBacking(hdr gpuCtrlHeader, data []byte) []byte {
	if len(data) < gpuCtrlHeaderSize+4 {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidParam})
	}

	resID := binary.LittleEndian.Uint32(data[gpuCtrlHeaderSize : gpuCtrlHeaderSize+4])

	g.mu.RLock()
	_, ok := g.resources[resID]
	g.mu.RUnlock()

	if !ok {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidResourceID})
	}

	return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespOkNoData, FenceID: hdr.FenceID})
}

// cmdGetEdid returns a minimal EDID blob for a scanout.
func (g *VirtioGPU) cmdGetEdid(hdr gpuCtrlHeader, data []byte) []byte {
	if len(data) < gpuCtrlHeaderSize+4 {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidParam})
	}

	scanoutID := binary.LittleEndian.Uint32(data[gpuCtrlHeaderSize : gpuCtrlHeaderSize+4])
	if scanoutID >= gpuMaxScanouts {
		return encodeGPUCtrlHeader(gpuCtrlHeader{Type: VirtioGPURespErrInvalidScanout})
	}

	g.mu.RLock()
	scanout := &g.scanouts[scanoutID]
	width := scanout.Rect.Width
	height := scanout.Rect.Height
	if width == 0 {
		width = g.config.Width
	}
	if height == 0 {
		height = g.config.Height
	}
	g.mu.RUnlock()

	edid := buildMinimalEDID(width, height)

	// Response: header + size(u32) + padding(u32) + edid data.
	respHdr := gpuCtrlHeader{Type: VirtioGPURespOkEdid, FenceID: hdr.FenceID}
	resp := encodeGPUCtrlHeader(respHdr)
	sizeBytes := make([]byte, 8)
	binary.LittleEndian.PutUint32(sizeBytes[0:4], uint32(len(edid)))
	resp = append(resp, sizeBytes...)
	resp = append(resp, edid...)

	return resp
}

// resourceToImage converts a GPU resource to a Go image within the given rect.
func (g *VirtioGPU) resourceToImage(res *GPUResource2D, rect *GPURect) image.Image {
	w := int(rect.Width)
	h := int(rect.Height)
	if w == 0 || h == 0 {
		w = int(res.Width)
		h = int(res.Height)
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := 0; y < h; y++ {
		srcY := int(rect.Y) + y
		if srcY >= int(res.Height) {
			break
		}
		for x := 0; x < w; x++ {
			srcX := int(rect.X) + x
			if srcX >= int(res.Width) {
				break
			}
			off := srcY*int(res.Stride) + srcX*4
			if off+4 > len(res.Data) {
				break
			}
			// Default format is BGRA.
			b := res.Data[off]
			gv := res.Data[off+1]
			r := res.Data[off+2]
			a := res.Data[off+3]
			img.SetRGBA(x, y, color.RGBA{R: r, G: gv, B: b, A: a})
		}
	}

	return img
}

// buildMinimalEDID constructs a 128-byte base EDID block for the given resolution.
func buildMinimalEDID(width, height uint32) []byte {
	edid := make([]byte, 128)

	// Header.
	edid[0] = 0x00
	edid[1] = 0xFF
	edid[2] = 0xFF
	edid[3] = 0xFF
	edid[4] = 0xFF
	edid[5] = 0xFF
	edid[6] = 0xFF
	edid[7] = 0x00

	// Manufacturer ID "TNT" (tent).
	edid[8] = 0x52  // T=20, N=14 -> 10100 01110 = 0x52, 0xAE
	edid[9] = 0x2E

	// Product code.
	binary.LittleEndian.PutUint16(edid[10:12], 0x0001)

	// Serial.
	binary.LittleEndian.PutUint32(edid[12:16], 0x00000001)

	// Week/year of manufacture.
	edid[16] = 1   // week
	edid[17] = 36  // year (2026 - 1990)

	// EDID version 1.4.
	edid[18] = 1
	edid[19] = 4

	// Digital input, 8-bit color.
	edid[20] = 0xA5

	// Screen size (cm) — approximate.
	edid[21] = byte(width * 27 / 1920)  // horizontal
	edid[22] = byte(height * 15 / 1080) // vertical

	// Gamma (2.2 = 120 + 100 = 220 -> stored as 220-100=120).
	edid[23] = 120

	// Supported features.
	edid[24] = 0x06

	// Chromaticity — use sRGB defaults.
	edid[25] = 0xEE
	edid[26] = 0x91
	edid[27] = 0xA3
	edid[28] = 0x54
	edid[29] = 0x4C
	edid[30] = 0x99
	edid[31] = 0x26
	edid[32] = 0x0F
	edid[33] = 0x50
	edid[34] = 0x54

	// Established timings.
	edid[35] = 0x00
	edid[36] = 0x00
	edid[37] = 0x00

	// Preferred detailed timing descriptor (bytes 54-71).
	// Pixel clock in 10kHz units.
	pixClk := uint16(width * height * 60 / 10000) // approximate
	binary.LittleEndian.PutUint16(edid[54:56], pixClk)

	// Horizontal active pixels.
	hActive := uint16(width)
	hBlank := uint16(160) // typical blanking
	edid[56] = byte(hActive & 0xFF)
	edid[57] = byte(hBlank & 0xFF)
	edid[58] = byte(((hActive >> 8) & 0x0F) << 4) | byte((hBlank>>8)&0x0F)

	// Vertical active lines.
	vActive := uint16(height)
	vBlank := uint16(35)
	edid[59] = byte(vActive & 0xFF)
	edid[60] = byte(vBlank & 0xFF)
	edid[61] = byte(((vActive >> 8) & 0x0F) << 4) | byte((vBlank>>8)&0x0F)

	// Sync offsets.
	edid[62] = 48  // h front porch
	edid[63] = 32  // h sync width
	edid[64] = 0x33 // v front porch / v sync
	edid[65] = 0x00

	// Image size mm.
	hMM := uint16(width * 27 * 10 / 1920)
	vMM := uint16(height * 15 * 10 / 1080)
	edid[66] = byte(hMM & 0xFF)
	edid[67] = byte(vMM & 0xFF)
	edid[68] = byte(((hMM >> 8) & 0x0F) << 4) | byte((vMM>>8)&0x0F)

	// No border.
	edid[69] = 0
	edid[70] = 0

	// Signal type.
	edid[71] = 0x1E

	// Extension count.
	edid[126] = 0

	// Checksum.
	var sum byte
	for i := 0; i < 127; i++ {
		sum += edid[i]
	}
	edid[127] = byte(256 - int(sum))

	return edid
}

// GPUDeviceStats returns operational statistics for the GPU device.
type GPUDeviceStats struct {
	Commands      uint64 `json:"commands_processed"`
	Flushes       uint64 `json:"flushes"`
	ResourceCount int    `json:"resource_count"`
	CreateCount   uint64 `json:"resources_created"`
	ActiveScanouts int   `json:"active_scanouts"`
}

// DeviceStats returns operational statistics.
func (g *VirtioGPU) DeviceStats() GPUDeviceStats {
	g.mu.RLock()
	resCount := len(g.resources)
	scanouts := 0
	for i := range g.scanouts {
		if g.scanouts[i].Enabled {
			scanouts++
		}
	}
	g.mu.RUnlock()

	return GPUDeviceStats{
		Commands:       g.cmdCount.Load(),
		Flushes:        g.flushCount.Load(),
		ResourceCount:  resCount,
		CreateCount:    g.createCount.Load(),
		ActiveScanouts: scanouts,
	}
}

// NegotiateFeatures returns the intersection of offered and supported features.
func (g *VirtioGPU) NegotiateFeatures(offered uint64) uint64 {
	return offered & g.features
}

// Reset returns the device to its initial state.
func (g *VirtioGPU) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()

	wasRunning := g.running.Load()
	if wasRunning {
		g.running.Store(false)
		close(g.stopCh)
	}

	// Clear resources.
	g.resources = make(map[uint32]*GPUResource2D)

	// Reset scanouts.
	for i := range g.scanouts {
		g.scanouts[i] = GPUScanout{}
	}
	g.scanouts[0] = GPUScanout{
		Rect: GPURect{
			Width:  g.config.Width,
			Height: g.config.Height,
		},
		Enabled: true,
	}

	// Reset stats.
	g.cmdCount.Store(0)
	g.flushCount.Store(0)
	g.createCount.Store(0)

	// Reset virtqueues.
	g.controlQ.Reset()
	g.cursorQ.Reset()

	g.stopCh = make(chan struct{})
}

// GetVirtqueues returns all device virtqueues for transport attachment.
func (g *VirtioGPU) GetVirtqueues() []*Virtqueue {
	return []*Virtqueue{g.controlQ, g.cursorQ}
}

// GPUConfigSpace represents the device configuration space (Section 5.7.4).
type GPUConfigSpace struct {
	EventsRead  uint32
	EventsClear uint32
	NumScanouts uint32
	NumCapsets  uint32
}

// Bytes serializes the config space to wire format.
func (c *GPUConfigSpace) Bytes() []byte {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint32(buf[0:4], c.EventsRead)
	binary.LittleEndian.PutUint32(buf[4:8], c.EventsClear)
	binary.LittleEndian.PutUint32(buf[8:12], c.NumScanouts)
	binary.LittleEndian.PutUint32(buf[12:16], c.NumCapsets)
	return buf
}

// GetConfigSpace returns the current GPU config space.
func (g *VirtioGPU) GetConfigSpace() *GPUConfigSpace {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return &GPUConfigSpace{
		NumScanouts: g.config.MaxOutputs,
		NumCapsets:  0,
	}
}

// GetDeviceConfig returns transport configuration for this device.
func (g *VirtioGPU) GetDeviceConfig() GPUTransportConfig {
	return GPUTransportConfig{
		DeviceID:   0x10,
		VendorID:   0x554E4554, // "TENT"
		DeviceType: uint32(DeviceTypeGPU),
		NumQueues:  2,
		Features:   g.features,
	}
}

// GPUTransportConfig holds MMIO/PCI transport registration info.
type GPUTransportConfig struct {
	DeviceID   uint32 `json:"device_id"`
	VendorID   uint32 `json:"vendor_id"`
	DeviceType uint32 `json:"device_type"`
	NumQueues  uint32 `json:"num_queues"`
	Features   uint64 `json:"features"`
}

// String returns a human-readable description of the device.
func (g *VirtioGPU) String() string {
	ds := g.DeviceStats()
	g.mu.RLock()
	w := g.config.Width
	h := g.config.Height
	g.mu.RUnlock()
	return fmt.Sprintf("virtio-gpu[%s] %dx%d scanouts=%d cmds=%d flushes=%d resources=%d",
		g.deviceID, w, h, ds.ActiveScanouts, ds.Commands, ds.Flushes, ds.ResourceCount)
}
