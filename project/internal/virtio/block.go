// Package virtio provides virtio device emulation for microVMs.
// This file implements virtio-blk — a block storage device that processes
// read/write/flush requests from the guest via a virtqueue.
//
// Reference: Virtual I/O Device (VIRTIO) Version 1.1, Section 5.2
package virtio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// Virtio block request types (Section 5.2.6)
const (
	VirtioBlkTIn          uint32 = 0 // Read from device
	VirtioBlkTOut         uint32 = 1 // Write to device
	VirtioBlkTFlush       uint32 = 5 // Flush (write barrier)
	VirtioBlkTGetID       uint32 = 8 // Get device ID string
	VirtioBlkTGetLifetime uint32 = 10
	VirtioBlkTDiscard     uint32 = 11
	VirtioBlkTWriteZeroes uint32 = 13
)

// Virtio block status values (Section 5.2.6)
const (
	VirtioBlkSOK        uint8 = 0
	VirtioBlkSIOErr     uint8 = 1
	VirtioBlkSUnsupport uint8 = 2
)

// VirtioBlkReqHeader is the header of a virtio-blk request (16 bytes).
// Sent by the guest as the first readable descriptor in the chain.
type VirtioBlkReqHeader struct {
	Type   uint32 // Request type (VirtioBlkT*)
	_      uint32 // Reserved
	Sector uint64 // Starting sector (512-byte units)
}

const virtioBlkReqHeaderSize = 16
const virtioBlkSectorSize = 512

// VirtioBlkConfig represents the device configuration space (Section 5.2.4).
type VirtioBlkConfig struct {
	Capacity   uint64 // Device capacity in 512-byte sectors
	SizeMax    uint32 // Max segment size
	SegMax     uint32 // Max number of segments
	Cylinders  uint16
	Heads      uint8
	Sectors    uint8
	BlockSize  uint32 // Block size (usually 512)
	PhysBlkExp uint8  // Physical block exponent
}

// VirtioBlk implements a virtio block device backed by a file or raw disk image.
type VirtioBlk struct {
	mu sync.Mutex

	deviceID string
	readOnly bool

	// Backing store
	file     *os.File
	filePath string
	capacity uint64 // Size in bytes

	// Virtqueue for request processing
	vq *Virtqueue

	// Device state
	running atomic.Bool
	stopCh  chan struct{}

	// Stats
	readsCompleted  atomic.Uint64
	writesCompleted atomic.Uint64
	bytesRead       atomic.Uint64
	bytesWritten    atomic.Uint64
}

// VirtioBlkConfig holds options for creating a VirtioBlk device.
type VirtioBlkOpts struct {
	DeviceID string
	FilePath string
	ReadOnly bool
	Queue    *Virtqueue
}

// NewVirtioBlk creates a new virtio-blk device backed by the given file.
func NewVirtioBlk(opts VirtioBlkOpts) (*VirtioBlk, error) {
	if opts.FilePath == "" {
		return nil, errors.New("virtio-blk: file path is required")
	}

	flags := os.O_RDWR
	if opts.ReadOnly {
		flags = os.O_RDONLY
	}

	f, err := os.OpenFile(opts.FilePath, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("virtio-blk: failed to open backing file: %w", err)
	}

	// Get file size for capacity
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("virtio-blk: failed to stat backing file: %w", err)
	}

	dev := &VirtioBlk{
		deviceID: opts.DeviceID,
		readOnly: opts.ReadOnly,
		file:     f,
		filePath: opts.FilePath,
		capacity: uint64(info.Size()),
		vq:       opts.Queue,
		stopCh:   make(chan struct{}),
	}

	return dev, nil
}

// Type returns DeviceTypeBlock.
func (d *VirtioBlk) Type() DeviceType {
	return DeviceTypeBlock
}

// ID returns the device identifier.
func (d *VirtioBlk) ID() string {
	return d.deviceID
}

// Capacity returns the device capacity in bytes.
func (d *VirtioBlk) Capacity() uint64 {
	return d.capacity
}

// SectorCount returns the number of 512-byte sectors.
func (d *VirtioBlk) SectorCount() uint64 {
	return d.capacity / virtioBlkSectorSize
}

// GetConfig returns the device configuration.
func (d *VirtioBlk) GetConfig() VirtioBlkConfig {
	return VirtioBlkConfig{
		Capacity:  d.SectorCount(),
		SizeMax:   1 << 20, // 1 MiB max segment
		SegMax:    128,
		BlockSize: virtioBlkSectorSize,
	}
}

// Start begins processing virtqueue requests.
func (d *VirtioBlk) Start() error {
	if d.running.Load() {
		return errors.New("virtio-blk: already running")
	}
	d.running.Store(true)
	return nil
}

// Stop halts request processing and closes the backing file.
func (d *VirtioBlk) Stop() error {
	if !d.running.CompareAndSwap(true, false) {
		return nil
	}
	close(d.stopCh)

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.file != nil {
		return d.file.Close()
	}
	return nil
}

// Configure applies configuration parameters.
func (d *VirtioBlk) Configure(config map[string]string) error {
	return nil
}

// ProcessRequest handles a single virtio-blk request from a descriptor chain.
// The chain layout is: [header (readable)] [data (readable or writable)] [status (writable, 1 byte)]
func (d *VirtioBlk) ProcessRequest(chain *DescriptorChain) (uint32, error) {
	if !d.running.Load() {
		return 0, errors.New("virtio-blk: device not running")
	}

	// Need at least one readable descriptor (header) and one writable (status)
	if len(chain.Readable) < 1 {
		return 0, errors.New("virtio-blk: no readable descriptors for header")
	}
	if len(chain.Writable) < 1 {
		return 0, errors.New("virtio-blk: no writable descriptor for status")
	}

	// Parse the request header from the first readable buffer
	if chain.Readable[0].Len < virtioBlkReqHeaderSize {
		return 0, fmt.Errorf("virtio-blk: header too small (%d bytes)", chain.Readable[0].Len)
	}

	header, status, bytesWritten, err := d.processChain(chain)
	if err != nil {
		return 0, err
	}

	_ = header
	_ = status
	return bytesWritten, nil
}

// processChain processes a descriptor chain and returns (header, status, bytesWritten, error).
func (d *VirtioBlk) processChain(chain *DescriptorChain) (VirtioBlkReqHeader, uint8, uint32, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// We use the virtqueue's memRead to get header bytes
	if d.vq == nil || d.vq.memRead == nil {
		return VirtioBlkReqHeader{}, VirtioBlkSIOErr, 0, errors.New("virtio-blk: no memory accessor")
	}

	headerBuf, err := d.vq.memRead(chain.Readable[0].Addr, virtioBlkReqHeaderSize)
	if err != nil {
		return VirtioBlkReqHeader{}, VirtioBlkSIOErr, 0, fmt.Errorf("virtio-blk: failed to read header: %w", err)
	}

	header := VirtioBlkReqHeader{
		Type:   binary.LittleEndian.Uint32(headerBuf[0:4]),
		Sector: binary.LittleEndian.Uint64(headerBuf[8:16]),
	}

	var status uint8
	var totalWritten uint32

	switch header.Type {
	case VirtioBlkTIn:
		status, totalWritten = d.handleRead(chain, header.Sector)
	case VirtioBlkTOut:
		status = d.handleWrite(chain, header.Sector)
	case VirtioBlkTFlush:
		status = d.handleFlush()
	case VirtioBlkTGetID:
		status, totalWritten = d.handleGetID(chain)
	default:
		status = VirtioBlkSUnsupport
	}

	// Write status byte to the last writable descriptor
	lastWritable := chain.Writable[len(chain.Writable)-1]
	statusBuf := []byte{status}
	if err := d.vq.memWrite(lastWritable.Addr+uint64(lastWritable.Len)-1, statusBuf); err != nil {
		return header, VirtioBlkSIOErr, 0, fmt.Errorf("virtio-blk: failed to write status: %w", err)
	}

	// Total bytes written includes data + status byte
	totalWritten++

	return header, status, totalWritten, nil
}

// handleRead reads sectors from the backing file into writable descriptors.
func (d *VirtioBlk) handleRead(chain *DescriptorChain, sector uint64) (uint8, uint32) {
	offset := int64(sector * virtioBlkSectorSize)
	var totalWritten uint32

	// Write data to all writable descriptors except the last one (status byte)
	dataDescs := chain.Writable
	if len(dataDescs) > 1 {
		dataDescs = dataDescs[:len(dataDescs)-1]
	} else {
		// Only status descriptor, no data to read
		return VirtioBlkSOK, 0
	}

	for _, desc := range dataDescs {
		buf := make([]byte, desc.Len)
		n, err := d.file.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			return VirtioBlkSIOErr, totalWritten
		}

		if err := d.vq.memWrite(desc.Addr, buf[:n]); err != nil {
			return VirtioBlkSIOErr, totalWritten
		}

		totalWritten += uint32(n)
		offset += int64(n)
	}

	d.readsCompleted.Add(1)
	d.bytesRead.Add(uint64(totalWritten))
	return VirtioBlkSOK, totalWritten
}

// handleWrite writes data from readable descriptors to the backing file.
func (d *VirtioBlk) handleWrite(chain *DescriptorChain, sector uint64) uint8 {
	if d.readOnly {
		return VirtioBlkSIOErr
	}

	offset := int64(sector * virtioBlkSectorSize)

	// Data comes from readable descriptors after the header
	dataDescs := chain.Readable
	if len(dataDescs) > 1 {
		dataDescs = dataDescs[1:] // Skip header
	} else {
		return VirtioBlkSOK // No data to write
	}

	var totalWritten uint64
	for _, desc := range dataDescs {
		buf, err := d.vq.memRead(desc.Addr, desc.Len)
		if err != nil {
			return VirtioBlkSIOErr
		}

		n, err := d.file.WriteAt(buf, offset)
		if err != nil {
			return VirtioBlkSIOErr
		}

		totalWritten += uint64(n)
		offset += int64(n)
	}

	d.writesCompleted.Add(1)
	d.bytesWritten.Add(totalWritten)
	return VirtioBlkSOK
}

// handleFlush syncs the backing file to disk.
func (d *VirtioBlk) handleFlush() uint8 {
	if err := d.file.Sync(); err != nil {
		return VirtioBlkSIOErr
	}
	return VirtioBlkSOK
}

// handleGetID returns the device serial/ID string.
func (d *VirtioBlk) handleGetID(chain *DescriptorChain) (uint8, uint32) {
	// Device ID is up to 20 bytes, zero-padded
	id := make([]byte, 20)
	copy(id, []byte(d.deviceID))

	if len(chain.Writable) > 1 {
		desc := chain.Writable[0]
		writeLen := uint32(len(id))
		if writeLen > desc.Len {
			writeLen = desc.Len
		}
		if err := d.vq.memWrite(desc.Addr, id[:writeLen]); err != nil {
			return VirtioBlkSIOErr, 0
		}
		return VirtioBlkSOK, writeLen
	}
	return VirtioBlkSOK, 0
}

// Stats returns device I/O statistics.
func (d *VirtioBlk) Stats() VirtioBlkStats {
	return VirtioBlkStats{
		ReadsCompleted:  d.readsCompleted.Load(),
		WritesCompleted: d.writesCompleted.Load(),
		BytesRead:       d.bytesRead.Load(),
		BytesWritten:    d.bytesWritten.Load(),
	}
}

// VirtioBlkStats holds device I/O statistics.
type VirtioBlkStats struct {
	ReadsCompleted  uint64
	WritesCompleted uint64
	BytesRead       uint64
	BytesWritten    uint64
}
