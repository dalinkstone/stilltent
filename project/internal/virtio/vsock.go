// Package virtio provides virtio device emulation for microVMs.
// This file implements virtio-vsock — a host/guest communication transport
// using the VM sockets (AF_VSOCK) protocol over virtio.
//
// Reference: Virtual I/O Device (VIRTIO) Version 1.1, Section 5.10
package virtio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Virtio-vsock device type ID (Section 5.10)
const DeviceTypeVsock DeviceType = 19

// VSOCK operation types (Section 5.10.6)
const (
	VsockOpInvalid  uint16 = 0
	VsockOpRequest  uint16 = 1 // Connection request
	VsockOpResponse uint16 = 2 // Connection response
	VsockOpRst      uint16 = 3 // Connection reset
	VsockOpShutdown uint16 = 4 // Graceful shutdown
	VsockOpRW       uint16 = 5 // Data read/write
	VsockOpCredit   uint16 = 6 // Credit update (flow control)
)

// VSOCK shutdown flags
const (
	VsockShutdownFlagRecv uint32 = 1
	VsockShutdownFlagSend uint32 = 2
)

// VSOCK transport type
const (
	VsockTypeStream uint16 = 1 // Stream socket (SOCK_STREAM equivalent)
)

// Well-known CIDs
const (
	VsockCIDHypervisor uint64 = 0 // Reserved
	VsockCIDLocal      uint64 = 1 // Loopback (deprecated)
	VsockCIDHost       uint64 = 2 // Host CID
)

// Guest agent port — our convention for the tent guest agent service
const VsockPortGuestAgent uint32 = 1024

// VsockHeader is the virtio-vsock packet header (44 bytes).
// Every packet in either direction carries this header.
type VsockHeader struct {
	SrcCID  uint64 // Source context ID
	DstCID  uint64 // Destination context ID
	SrcPort uint32 // Source port
	DstPort uint32 // Destination port
	Len     uint32 // Payload length
	Type    uint16 // Socket type (VsockTypeStream)
	Op      uint16 // Operation (VsockOp*)
	Flags   uint32 // Operation-specific flags
	BufSize uint32 // Receive buffer size (for flow control)
	FwdCnt  uint32 // Forward count (bytes consumed, for flow control)
}

const vsockHeaderSize = 44

// VsockConnection tracks the state of a single vsock connection.
type VsockConnection struct {
	mu sync.Mutex

	// Connection identity
	LocalCID  uint64
	LocalPort uint32
	PeerCID   uint64
	PeerPort  uint32

	// Flow control
	BufSize  uint32 // Our receive buffer size
	FwdCnt   uint32 // Bytes we've consumed (reported to peer)
	PeerBuf  uint32 // Peer's receive buffer size
	PeerFwd  uint32 // Peer's forward count
	TxCnt    uint32 // Total bytes we've sent

	// Receive buffer
	recvBuf []byte

	// State
	State    VsockConnState
	ShutdownFlags uint32
}

// VsockConnState represents a connection's lifecycle state.
type VsockConnState uint8

const (
	VsockConnStateClosed     VsockConnState = 0
	VsockConnStateListening  VsockConnState = 1
	VsockConnStateConnecting VsockConnState = 2
	VsockConnStateConnected  VsockConnState = 3
	VsockConnStateClosing    VsockConnState = 4
)

// PeerCredit returns how many bytes we can still send to the peer.
func (c *VsockConnection) PeerCredit() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	peerFree := c.PeerBuf - (c.TxCnt - c.PeerFwd)
	return peerFree
}

// Enqueue appends data to the connection's receive buffer.
func (c *VsockConnection) Enqueue(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recvBuf = append(c.recvBuf, data...)
}

// Dequeue reads up to n bytes from the connection's receive buffer.
func (c *VsockConnection) Dequeue(n int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.recvBuf) == 0 {
		return nil
	}
	if n > len(c.recvBuf) {
		n = len(c.recvBuf)
	}
	data := make([]byte, n)
	copy(data, c.recvBuf[:n])
	c.recvBuf = c.recvBuf[n:]
	c.FwdCnt += uint32(n)
	return data
}

// connKey uniquely identifies a vsock connection.
type connKey struct {
	localPort uint32
	peerCID   uint64
	peerPort  uint32
}

// VirtioVsock implements a virtio-vsock device for host-guest communication.
type VirtioVsock struct {
	mu sync.Mutex

	deviceID string
	guestCID uint64 // CID assigned to the guest

	// Virtqueues: rx (host→guest), tx (guest→host), event
	rxVq    *Virtqueue
	txVq    *Virtqueue
	eventVq *Virtqueue

	// Active connections
	conns map[connKey]*VsockConnection

	// Listener callbacks — host-side listeners waiting for guest connections
	listeners map[uint32]VsockListenFunc

	// Outbound packet queue (host→guest)
	txQueue []VsockPacket

	// Device state
	running atomic.Bool
	stopCh  chan struct{}

	// Stats
	packetsRx atomic.Uint64
	packetsTx atomic.Uint64
	bytesRx   atomic.Uint64
	bytesTx   atomic.Uint64
}

// VsockPacket is a complete vsock packet (header + payload).
type VsockPacket struct {
	Header  VsockHeader
	Payload []byte
}

// VsockListenFunc is called when a guest connects to a host-side port.
type VsockListenFunc func(conn *VsockConnection) error

// VirtioVsockOpts holds options for creating a VirtioVsock device.
type VirtioVsockOpts struct {
	DeviceID string
	GuestCID uint64
	RxQueue  *Virtqueue
	TxQueue  *Virtqueue
	EventQueue *Virtqueue
}

// NewVirtioVsock creates a new virtio-vsock device.
func NewVirtioVsock(opts VirtioVsockOpts) (*VirtioVsock, error) {
	if opts.GuestCID < 3 {
		return nil, errors.New("virtio-vsock: guest CID must be >= 3 (0-2 are reserved)")
	}

	dev := &VirtioVsock{
		deviceID:  opts.DeviceID,
		guestCID:  opts.GuestCID,
		rxVq:      opts.RxQueue,
		txVq:      opts.TxQueue,
		eventVq:   opts.EventQueue,
		conns:     make(map[connKey]*VsockConnection),
		listeners: make(map[uint32]VsockListenFunc),
		stopCh:    make(chan struct{}),
	}

	return dev, nil
}

// Type returns DeviceTypeVsock.
func (d *VirtioVsock) Type() DeviceType {
	return DeviceTypeVsock
}

// ID returns the device identifier.
func (d *VirtioVsock) ID() string {
	return d.deviceID
}

// GuestCID returns the CID assigned to the guest.
func (d *VirtioVsock) GuestCID() uint64 {
	return d.guestCID
}

// Start begins processing vsock packets.
func (d *VirtioVsock) Start() error {
	if d.running.Load() {
		return errors.New("virtio-vsock: already running")
	}
	d.running.Store(true)
	return nil
}

// Stop halts packet processing and closes all connections.
func (d *VirtioVsock) Stop() error {
	if !d.running.CompareAndSwap(true, false) {
		return nil
	}
	close(d.stopCh)

	d.mu.Lock()
	defer d.mu.Unlock()

	// Reset all connections
	for k, conn := range d.conns {
		conn.mu.Lock()
		conn.State = VsockConnStateClosed
		conn.mu.Unlock()
		delete(d.conns, k)
	}

	return nil
}

// Configure applies configuration parameters.
func (d *VirtioVsock) Configure(config map[string]string) error {
	return nil
}

// Listen registers a host-side listener on the given port.
// When the guest connects to this port, the callback is invoked.
func (d *VirtioVsock) Listen(port uint32, handler VsockListenFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.listeners[port] = handler
}

// Connect initiates a connection from the host to the guest.
func (d *VirtioVsock) Connect(guestPort uint32, hostPort uint32) (*VsockConnection, error) {
	if !d.running.Load() {
		return nil, errors.New("virtio-vsock: device not running")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	key := connKey{
		localPort: hostPort,
		peerCID:   d.guestCID,
		peerPort:  guestPort,
	}

	if _, exists := d.conns[key]; exists {
		return nil, fmt.Errorf("virtio-vsock: connection already exists on port %d", hostPort)
	}

	conn := &VsockConnection{
		LocalCID:  VsockCIDHost,
		LocalPort: hostPort,
		PeerCID:   d.guestCID,
		PeerPort:  guestPort,
		BufSize:   65536, // 64 KiB receive buffer
		State:     VsockConnStateConnecting,
	}

	d.conns[key] = conn

	// Queue a connection request packet to the guest
	d.enqueueTxPacket(VsockPacket{
		Header: VsockHeader{
			SrcCID:  VsockCIDHost,
			DstCID:  d.guestCID,
			SrcPort: hostPort,
			DstPort: guestPort,
			Type:    VsockTypeStream,
			Op:      VsockOpRequest,
			BufSize: conn.BufSize,
		},
	})

	return conn, nil
}

// SendData sends data to an established connection.
func (d *VirtioVsock) SendData(conn *VsockConnection, data []byte) error {
	if !d.running.Load() {
		return errors.New("virtio-vsock: device not running")
	}

	conn.mu.Lock()
	if conn.State != VsockConnStateConnected {
		conn.mu.Unlock()
		return errors.New("virtio-vsock: connection not established")
	}
	conn.TxCnt += uint32(len(data))
	conn.mu.Unlock()

	d.mu.Lock()
	defer d.mu.Unlock()

	d.enqueueTxPacket(VsockPacket{
		Header: VsockHeader{
			SrcCID:  VsockCIDHost,
			DstCID:  conn.PeerCID,
			SrcPort: conn.LocalPort,
			DstPort: conn.PeerPort,
			Len:     uint32(len(data)),
			Type:    VsockTypeStream,
			Op:      VsockOpRW,
			BufSize: conn.BufSize,
			FwdCnt:  conn.FwdCnt,
		},
		Payload: data,
	})

	d.bytesTx.Add(uint64(len(data)))
	return nil
}

// CloseConnection gracefully closes a vsock connection.
func (d *VirtioVsock) CloseConnection(conn *VsockConnection) error {
	conn.mu.Lock()
	if conn.State == VsockConnStateClosed {
		conn.mu.Unlock()
		return nil
	}
	conn.State = VsockConnStateClosing
	conn.mu.Unlock()

	d.mu.Lock()
	defer d.mu.Unlock()

	d.enqueueTxPacket(VsockPacket{
		Header: VsockHeader{
			SrcCID:  VsockCIDHost,
			DstCID:  conn.PeerCID,
			SrcPort: conn.LocalPort,
			DstPort: conn.PeerPort,
			Type:    VsockTypeStream,
			Op:      VsockOpShutdown,
			Flags:   VsockShutdownFlagSend | VsockShutdownFlagRecv,
		},
	})

	// Send RST to finalize
	d.enqueueTxPacket(VsockPacket{
		Header: VsockHeader{
			SrcCID:  VsockCIDHost,
			DstCID:  conn.PeerCID,
			SrcPort: conn.LocalPort,
			DstPort: conn.PeerPort,
			Type:    VsockTypeStream,
			Op:      VsockOpRst,
		},
	})

	// Remove from connection table
	key := connKey{
		localPort: conn.LocalPort,
		peerCID:   conn.PeerCID,
		peerPort:  conn.PeerPort,
	}
	delete(d.conns, key)

	conn.mu.Lock()
	conn.State = VsockConnStateClosed
	conn.mu.Unlock()

	return nil
}

// ProcessGuestPacket processes a packet received from the guest (via tx virtqueue).
func (d *VirtioVsock) ProcessGuestPacket(headerBuf []byte, payload []byte) error {
	if !d.running.Load() {
		return errors.New("virtio-vsock: device not running")
	}
	if len(headerBuf) < vsockHeaderSize {
		return fmt.Errorf("virtio-vsock: header too small (%d bytes)", len(headerBuf))
	}

	hdr := decodeVsockHeader(headerBuf)

	d.packetsRx.Add(1)
	d.bytesRx.Add(uint64(hdr.Len))

	d.mu.Lock()
	defer d.mu.Unlock()

	switch hdr.Op {
	case VsockOpRequest:
		return d.handleGuestConnect(hdr)
	case VsockOpResponse:
		return d.handleGuestResponse(hdr)
	case VsockOpRW:
		return d.handleGuestData(hdr, payload)
	case VsockOpShutdown:
		return d.handleGuestShutdown(hdr)
	case VsockOpRst:
		return d.handleGuestRst(hdr)
	case VsockOpCredit:
		return d.handleGuestCredit(hdr)
	default:
		return fmt.Errorf("virtio-vsock: unknown op %d", hdr.Op)
	}
}

// handleGuestConnect processes a connection request from the guest.
func (d *VirtioVsock) handleGuestConnect(hdr VsockHeader) error {
	handler, ok := d.listeners[hdr.DstPort]
	if !ok {
		// No listener — send RST
		d.enqueueTxPacket(VsockPacket{
			Header: VsockHeader{
				SrcCID:  VsockCIDHost,
				DstCID:  hdr.SrcCID,
				SrcPort: hdr.DstPort,
				DstPort: hdr.SrcPort,
				Type:    VsockTypeStream,
				Op:      VsockOpRst,
			},
		})
		return nil
	}

	key := connKey{
		localPort: hdr.DstPort,
		peerCID:   hdr.SrcCID,
		peerPort:  hdr.SrcPort,
	}

	conn := &VsockConnection{
		LocalCID:  VsockCIDHost,
		LocalPort: hdr.DstPort,
		PeerCID:   hdr.SrcCID,
		PeerPort:  hdr.SrcPort,
		BufSize:   65536,
		PeerBuf:   hdr.BufSize,
		PeerFwd:   hdr.FwdCnt,
		State:     VsockConnStateConnected,
	}

	d.conns[key] = conn

	// Send response accepting the connection
	d.enqueueTxPacket(VsockPacket{
		Header: VsockHeader{
			SrcCID:  VsockCIDHost,
			DstCID:  hdr.SrcCID,
			SrcPort: hdr.DstPort,
			DstPort: hdr.SrcPort,
			Type:    VsockTypeStream,
			Op:      VsockOpResponse,
			BufSize: conn.BufSize,
			FwdCnt:  conn.FwdCnt,
		},
	})

	// Notify listener
	if err := handler(conn); err != nil {
		d.enqueueTxPacket(VsockPacket{
			Header: VsockHeader{
				SrcCID:  VsockCIDHost,
				DstCID:  hdr.SrcCID,
				SrcPort: hdr.DstPort,
				DstPort: hdr.SrcPort,
				Type:    VsockTypeStream,
				Op:      VsockOpRst,
			},
		})
		delete(d.conns, key)
	}

	return nil
}

// handleGuestResponse processes a connection response from the guest.
func (d *VirtioVsock) handleGuestResponse(hdr VsockHeader) error {
	key := connKey{
		localPort: hdr.DstPort,
		peerCID:   hdr.SrcCID,
		peerPort:  hdr.SrcPort,
	}

	conn, ok := d.conns[key]
	if !ok {
		return fmt.Errorf("virtio-vsock: response for unknown connection (port %d)", hdr.DstPort)
	}

	conn.mu.Lock()
	conn.State = VsockConnStateConnected
	conn.PeerBuf = hdr.BufSize
	conn.PeerFwd = hdr.FwdCnt
	conn.mu.Unlock()

	return nil
}

// handleGuestData processes incoming data from the guest.
func (d *VirtioVsock) handleGuestData(hdr VsockHeader, payload []byte) error {
	key := connKey{
		localPort: hdr.DstPort,
		peerCID:   hdr.SrcCID,
		peerPort:  hdr.SrcPort,
	}

	conn, ok := d.conns[key]
	if !ok {
		return fmt.Errorf("virtio-vsock: data for unknown connection (port %d)", hdr.DstPort)
	}

	if len(payload) > 0 {
		conn.Enqueue(payload)
	}

	// Update peer's flow control info
	conn.mu.Lock()
	conn.PeerBuf = hdr.BufSize
	conn.PeerFwd = hdr.FwdCnt
	conn.mu.Unlock()

	return nil
}

// handleGuestShutdown processes a shutdown request from the guest.
func (d *VirtioVsock) handleGuestShutdown(hdr VsockHeader) error {
	key := connKey{
		localPort: hdr.DstPort,
		peerCID:   hdr.SrcCID,
		peerPort:  hdr.SrcPort,
	}

	conn, ok := d.conns[key]
	if !ok {
		return nil
	}

	conn.mu.Lock()
	conn.ShutdownFlags |= hdr.Flags
	if conn.ShutdownFlags == (VsockShutdownFlagRecv | VsockShutdownFlagSend) {
		conn.State = VsockConnStateClosing
	}
	conn.mu.Unlock()

	// Acknowledge with RST
	d.enqueueTxPacket(VsockPacket{
		Header: VsockHeader{
			SrcCID:  VsockCIDHost,
			DstCID:  hdr.SrcCID,
			SrcPort: hdr.DstPort,
			DstPort: hdr.SrcPort,
			Type:    VsockTypeStream,
			Op:      VsockOpRst,
		},
	})

	delete(d.conns, key)

	conn.mu.Lock()
	conn.State = VsockConnStateClosed
	conn.mu.Unlock()

	return nil
}

// handleGuestRst processes a reset from the guest.
func (d *VirtioVsock) handleGuestRst(hdr VsockHeader) error {
	key := connKey{
		localPort: hdr.DstPort,
		peerCID:   hdr.SrcCID,
		peerPort:  hdr.SrcPort,
	}

	conn, ok := d.conns[key]
	if !ok {
		return nil
	}

	conn.mu.Lock()
	conn.State = VsockConnStateClosed
	conn.mu.Unlock()

	delete(d.conns, key)
	return nil
}

// handleGuestCredit processes a credit update from the guest.
func (d *VirtioVsock) handleGuestCredit(hdr VsockHeader) error {
	key := connKey{
		localPort: hdr.DstPort,
		peerCID:   hdr.SrcCID,
		peerPort:  hdr.SrcPort,
	}

	conn, ok := d.conns[key]
	if !ok {
		return nil
	}

	conn.mu.Lock()
	conn.PeerBuf = hdr.BufSize
	conn.PeerFwd = hdr.FwdCnt
	conn.mu.Unlock()

	return nil
}

// enqueueTxPacket adds a packet to the outbound queue (host→guest).
// Caller must hold d.mu.
func (d *VirtioVsock) enqueueTxPacket(pkt VsockPacket) {
	d.txQueue = append(d.txQueue, pkt)
	d.packetsTx.Add(1)
}

// DrainTxQueue returns and clears all pending outbound packets.
func (d *VirtioVsock) DrainTxQueue() []VsockPacket {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.txQueue) == 0 {
		return nil
	}

	pkts := d.txQueue
	d.txQueue = nil
	return pkts
}

// ActiveConnections returns the number of active connections.
func (d *VirtioVsock) ActiveConnections() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.conns)
}

// GetConnection looks up a connection by local port and peer address.
func (d *VirtioVsock) GetConnection(localPort uint32, peerCID uint64, peerPort uint32) *VsockConnection {
	d.mu.Lock()
	defer d.mu.Unlock()

	key := connKey{
		localPort: localPort,
		peerCID:   peerCID,
		peerPort:  peerPort,
	}
	return d.conns[key]
}

// Stats returns device packet statistics.
func (d *VirtioVsock) Stats() VsockStats {
	return VsockStats{
		PacketsRx: d.packetsRx.Load(),
		PacketsTx: d.packetsTx.Load(),
		BytesRx:   d.bytesRx.Load(),
		BytesTx:   d.bytesTx.Load(),
	}
}

// VsockStats holds device packet statistics.
type VsockStats struct {
	PacketsRx uint64
	PacketsTx uint64
	BytesRx   uint64
	BytesTx   uint64
}

// decodeVsockHeader parses a 44-byte virtio-vsock header from a byte buffer.
func decodeVsockHeader(buf []byte) VsockHeader {
	return VsockHeader{
		SrcCID:  binary.LittleEndian.Uint64(buf[0:8]),
		DstCID:  binary.LittleEndian.Uint64(buf[8:16]),
		SrcPort: binary.LittleEndian.Uint32(buf[16:20]),
		DstPort: binary.LittleEndian.Uint32(buf[20:24]),
		Len:     binary.LittleEndian.Uint32(buf[24:28]),
		Type:    binary.LittleEndian.Uint16(buf[28:30]),
		Op:      binary.LittleEndian.Uint16(buf[30:32]),
		Flags:   binary.LittleEndian.Uint32(buf[32:36]),
		BufSize: binary.LittleEndian.Uint32(buf[36:40]),
		FwdCnt:  binary.LittleEndian.Uint32(buf[40:44]),
	}
}

// EncodeVsockHeader serializes a VsockHeader into a 44-byte buffer.
func EncodeVsockHeader(hdr VsockHeader) []byte {
	buf := make([]byte, vsockHeaderSize)
	binary.LittleEndian.PutUint64(buf[0:8], hdr.SrcCID)
	binary.LittleEndian.PutUint64(buf[8:16], hdr.DstCID)
	binary.LittleEndian.PutUint32(buf[16:20], hdr.SrcPort)
	binary.LittleEndian.PutUint32(buf[20:24], hdr.DstPort)
	binary.LittleEndian.PutUint32(buf[24:28], hdr.Len)
	binary.LittleEndian.PutUint16(buf[28:30], hdr.Type)
	binary.LittleEndian.PutUint16(buf[30:32], hdr.Op)
	binary.LittleEndian.PutUint32(buf[32:36], hdr.Flags)
	binary.LittleEndian.PutUint32(buf[36:40], hdr.BufSize)
	binary.LittleEndian.PutUint32(buf[40:44], hdr.FwdCnt)
	return buf
}
