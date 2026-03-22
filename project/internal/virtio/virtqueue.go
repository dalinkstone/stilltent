// Package virtio provides virtio device emulation for microVMs.
// This file implements the virtio split virtqueue (vring) — the shared-memory
// ring buffer that underpins all virtio device I/O between host and guest.
//
// Reference: Virtual I/O Device (VIRTIO) Version 1.1, Section 2.6
package virtio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

// Virtqueue constants from the VIRTIO spec
const (
	// VringDescFNext indicates the descriptor chains via next field
	VringDescFNext uint16 = 1
	// VringDescFWrite marks a descriptor as device-writable (vs device-readable)
	VringDescFWrite uint16 = 2
	// VringDescFIndirect indicates the buffer contains a table of descriptors
	VringDescFIndirect uint16 = 4

	// VringUsedFNoNotify tells the driver not to send notifications
	VringUsedFNoNotify uint16 = 1
	// VringAvailFNoInterrupt tells the device not to send interrupts
	VringAvailFNoInterrupt uint16 = 1

	// DefaultQueueSize is the default number of descriptors in a virtqueue
	DefaultQueueSize uint16 = 256
	// MaxQueueSize is the maximum number of descriptors
	MaxQueueSize uint16 = 32768
)

// VringDesc is a single descriptor in the descriptor table.
// Each descriptor points to a guest-physical memory buffer.
type VringDesc struct {
	Addr  uint64 // Guest-physical address of the buffer
	Len   uint32 // Length of the buffer in bytes
	Flags uint16 // VringDescF* flags
	Next  uint16 // Index of next descriptor if VringDescFNext is set
}

// VringDescSize is the size of a VringDesc in bytes
const VringDescSize = int(unsafe.Sizeof(VringDesc{}))

// VringAvail is the available ring — written by the driver (guest),
// read by the device (host). The driver places descriptor chain heads here.
type VringAvail struct {
	Flags uint16   // VringAvailF* flags
	Idx   uint16   // Next index the driver will write to (wraps)
	Ring  []uint16 // Array of descriptor chain head indices
}

// VringUsedElem is a single element in the used ring
type VringUsedElem struct {
	ID  uint32 // Index of the descriptor chain head
	Len uint32 // Total bytes written to the descriptor chain
}

// VringUsed is the used ring — written by the device (host),
// read by the driver (guest). The device places completed descriptor chains here.
type VringUsed struct {
	Flags uint16          // VringUsedF* flags
	Idx   uint16          // Next index the device will write to (wraps)
	Ring  []VringUsedElem // Array of used elements
}

// DescriptorChain represents a parsed chain of descriptors for one I/O request.
type DescriptorChain struct {
	HeadIndex   uint16       // Index of the first descriptor in the chain
	Readable    []ChainLink  // Device-readable buffers (data from guest)
	Writable    []ChainLink  // Device-writable buffers (data to guest)
	TotalRead   uint32       // Total readable bytes
	TotalWrite  uint32       // Total writable bytes
}

// ChainLink is a single buffer within a descriptor chain
type ChainLink struct {
	Addr uint64
	Len  uint32
}

// Virtqueue implements a virtio split virtqueue with descriptor table,
// available ring, and used ring. It provides the host-side logic for
// processing I/O requests from the guest.
type Virtqueue struct {
	mu sync.Mutex

	// Queue configuration
	num      uint16 // Number of descriptors (must be power of 2)
	maxNum   uint16
	ready    bool

	// Descriptor table
	descs []VringDesc

	// Available ring (guest -> host)
	avail VringAvail

	// Used ring (host -> guest)
	used VringUsed

	// Shadow of the last seen available index — tracks what the host has consumed
	lastAvailIdx uint16

	// Guest memory accessor — maps guest-physical addresses to host buffers
	memRead  func(gpa uint64, size uint32) ([]byte, error)
	memWrite func(gpa uint64, data []byte) error

	// Notification callback — called when the device has placed items in used ring
	notifyGuest func()

	// Name for debugging
	name string
}

// VirtqueueConfig holds configuration for creating a new Virtqueue
type VirtqueueConfig struct {
	Name        string
	Num         uint16 // Queue size (number of descriptors)
	MemRead     func(gpa uint64, size uint32) ([]byte, error)
	MemWrite    func(gpa uint64, data []byte) error
	NotifyGuest func()
}

// NewVirtqueue creates a new virtqueue with the given configuration.
func NewVirtqueue(cfg VirtqueueConfig) (*Virtqueue, error) {
	num := cfg.Num
	if num == 0 {
		num = DefaultQueueSize
	}

	// Queue size must be a power of 2
	if num&(num-1) != 0 {
		return nil, fmt.Errorf("queue size %d is not a power of 2", num)
	}
	if num > MaxQueueSize {
		return nil, fmt.Errorf("queue size %d exceeds maximum %d", num, MaxQueueSize)
	}

	vq := &Virtqueue{
		num:    num,
		maxNum: num,
		name:   cfg.Name,
		descs:  make([]VringDesc, num),
		avail: VringAvail{
			Ring: make([]uint16, num),
		},
		used: VringUsed{
			Ring: make([]VringUsedElem, num),
		},
		memRead:     cfg.MemRead,
		memWrite:    cfg.MemWrite,
		notifyGuest: cfg.NotifyGuest,
	}

	return vq, nil
}

// Reset returns the virtqueue to its initial state
func (vq *Virtqueue) Reset() {
	vq.mu.Lock()
	defer vq.mu.Unlock()

	vq.ready = false
	vq.lastAvailIdx = 0

	for i := range vq.descs {
		vq.descs[i] = VringDesc{}
	}
	vq.avail.Flags = 0
	vq.avail.Idx = 0
	for i := range vq.avail.Ring {
		vq.avail.Ring[i] = 0
	}
	vq.used.Flags = 0
	vq.used.Idx = 0
	for i := range vq.used.Ring {
		vq.used.Ring[i] = VringUsedElem{}
	}
}

// SetReady marks the virtqueue as ready for I/O
func (vq *Virtqueue) SetReady(ready bool) {
	vq.mu.Lock()
	defer vq.mu.Unlock()
	vq.ready = ready
}

// IsReady returns whether the virtqueue is ready
func (vq *Virtqueue) IsReady() bool {
	vq.mu.Lock()
	defer vq.mu.Unlock()
	return vq.ready
}

// Size returns the number of descriptors in the queue
func (vq *Virtqueue) Size() uint16 {
	return vq.num
}

// Name returns the queue name
func (vq *Virtqueue) Name() string {
	return vq.name
}

// Errors
var (
	ErrQueueNotReady   = errors.New("virtqueue not ready")
	ErrQueueEmpty      = errors.New("no available descriptors")
	ErrChainTooLong    = errors.New("descriptor chain too long (possible loop)")
	ErrInvalidDescIdx  = errors.New("descriptor index out of range")
)

// HasAvailable returns true if there are pending descriptors in the available ring
func (vq *Virtqueue) HasAvailable() bool {
	vq.mu.Lock()
	defer vq.mu.Unlock()
	return vq.lastAvailIdx != vq.avail.Idx
}

// PopAvailable removes and returns the next available descriptor chain.
// Returns ErrQueueEmpty if no descriptors are available.
func (vq *Virtqueue) PopAvailable() (*DescriptorChain, error) {
	vq.mu.Lock()
	defer vq.mu.Unlock()

	if !vq.ready {
		return nil, ErrQueueNotReady
	}

	if vq.lastAvailIdx == vq.avail.Idx {
		return nil, ErrQueueEmpty
	}

	// Get the descriptor chain head index from the available ring
	ringIdx := vq.lastAvailIdx % vq.num
	headIdx := vq.avail.Ring[ringIdx]
	vq.lastAvailIdx++

	if headIdx >= vq.num {
		return nil, ErrInvalidDescIdx
	}

	// Walk the descriptor chain
	chain := &DescriptorChain{
		HeadIndex: headIdx,
	}

	idx := headIdx
	seen := make(map[uint16]bool)

	for {
		if idx >= vq.num {
			return nil, ErrInvalidDescIdx
		}
		if seen[idx] {
			return nil, ErrChainTooLong
		}
		seen[idx] = true

		desc := vq.descs[idx]
		link := ChainLink{
			Addr: desc.Addr,
			Len:  desc.Len,
		}

		if desc.Flags&VringDescFWrite != 0 {
			chain.Writable = append(chain.Writable, link)
			chain.TotalWrite += desc.Len
		} else {
			chain.Readable = append(chain.Readable, link)
			chain.TotalRead += desc.Len
		}

		if desc.Flags&VringDescFNext == 0 {
			break
		}
		idx = desc.Next
	}

	return chain, nil
}

// PushUsed places a completed descriptor chain into the used ring and
// optionally notifies the guest.
func (vq *Virtqueue) PushUsed(headIdx uint16, bytesWritten uint32) error {
	vq.mu.Lock()
	defer vq.mu.Unlock()

	if !vq.ready {
		return ErrQueueNotReady
	}

	ringIdx := vq.used.Idx % vq.num
	vq.used.Ring[ringIdx] = VringUsedElem{
		ID:  uint32(headIdx),
		Len: bytesWritten,
	}
	vq.used.Idx++

	// Notify guest if notifications aren't suppressed
	if vq.avail.Flags&VringAvailFNoInterrupt == 0 && vq.notifyGuest != nil {
		vq.notifyGuest()
	}

	return nil
}

// SetSuppressNotifications controls whether the device asks the driver
// to suppress notifications (kicks)
func (vq *Virtqueue) SetSuppressNotifications(suppress bool) {
	vq.mu.Lock()
	defer vq.mu.Unlock()
	if suppress {
		vq.used.Flags |= VringUsedFNoNotify
	} else {
		vq.used.Flags &^= VringUsedFNoNotify
	}
}

// --- Guest memory layout serialization ---
// These methods allow reading/writing the virtqueue state from/to
// guest physical memory, as would happen with real VM shared memory.

// LoadDescriptor loads a descriptor from a byte slice (16 bytes per the spec)
func LoadDescriptor(data []byte) VringDesc {
	return VringDesc{
		Addr:  binary.LittleEndian.Uint64(data[0:8]),
		Len:   binary.LittleEndian.Uint32(data[8:12]),
		Flags: binary.LittleEndian.Uint16(data[12:14]),
		Next:  binary.LittleEndian.Uint16(data[14:16]),
	}
}

// StoreDescriptor serializes a descriptor to a byte slice
func StoreDescriptor(desc VringDesc) []byte {
	data := make([]byte, 16)
	binary.LittleEndian.PutUint64(data[0:8], desc.Addr)
	binary.LittleEndian.PutUint32(data[8:12], desc.Len)
	binary.LittleEndian.PutUint16(data[12:14], desc.Flags)
	binary.LittleEndian.PutUint16(data[14:16], desc.Next)
	return data
}

// SetDescriptor sets a descriptor at the given index (used for testing and
// direct memory-mapped access)
func (vq *Virtqueue) SetDescriptor(idx uint16, desc VringDesc) error {
	vq.mu.Lock()
	defer vq.mu.Unlock()
	if idx >= vq.num {
		return ErrInvalidDescIdx
	}
	vq.descs[idx] = desc
	return nil
}

// SetAvailIdx sets the available ring index (simulates guest writing)
func (vq *Virtqueue) SetAvailIdx(idx uint16) {
	vq.mu.Lock()
	defer vq.mu.Unlock()
	vq.avail.Idx = idx
}

// SetAvailRing sets a value in the available ring
func (vq *Virtqueue) SetAvailRing(pos uint16, val uint16) error {
	vq.mu.Lock()
	defer vq.mu.Unlock()
	if pos >= vq.num {
		return ErrInvalidDescIdx
	}
	vq.avail.Ring[pos] = val
	return nil
}

// UsedIdx returns the current used ring index
func (vq *Virtqueue) UsedIdx() uint16 {
	vq.mu.Lock()
	defer vq.mu.Unlock()
	return vq.used.Idx
}

// UsedRing returns the used ring element at the given position
func (vq *Virtqueue) UsedRing(pos uint16) (VringUsedElem, error) {
	vq.mu.Lock()
	defer vq.mu.Unlock()
	if pos >= vq.num {
		return VringUsedElem{}, ErrInvalidDescIdx
	}
	return vq.used.Ring[pos], nil
}

// VirtqueueAlignSize calculates the aligned size for virtqueue memory layout
// as per the VIRTIO spec. The descriptor table and available ring are
// aligned to 16 bytes, the used ring is aligned to 4096 bytes.
func VirtqueueAlignSize(num uint16) (descTableSize, availRingSize, usedRingSize int) {
	n := int(num)
	descTableSize = n * 16           // 16 bytes per descriptor
	availRingSize = 4 + 2*n + 2      // flags(2) + idx(2) + ring(2*n) + used_event(2)
	usedRingSize = 4 + 8*n + 2       // flags(2) + idx(2) + ring(8*n) + avail_event(2)
	return
}

// TotalVirtqueueSize returns the total memory footprint of a virtqueue
// with proper alignment as per the VIRTIO spec
func TotalVirtqueueSize(num uint16) int {
	desc, avail, used := VirtqueueAlignSize(num)
	// Available ring is placed right after desc table, then aligned to 4096 for used ring
	firstPart := desc + avail
	// Align to 4096
	firstPartAligned := (firstPart + 4095) & ^4095
	return firstPartAligned + used
}
