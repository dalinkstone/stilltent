// Package virtio provides virtio device emulation for microVMs.
// This file implements virtio-net — a network device that bridges guest
// network traffic to a host TAP device.
//
// Reference: Virtual I/O Device (VIRTIO) Version 1.1, Section 5.1
package virtio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

// Virtio net header size (without mergeable rx buffers)
const virtioNetHeaderSize = 10

// VirtioNetHeader is prepended to every packet in both directions.
type VirtioNetHeader struct {
	Flags      uint8
	GSOType    uint8
	HdrLen     uint16
	GSOSize    uint16
	CSumStart  uint16
	CSumOffset uint16
}

// GSO types
const (
	VirtioNetHdrGSONone  uint8 = 0
	VirtioNetHdrGSOTCPv4 uint8 = 1
	VirtioNetHdrGSOUDP   uint8 = 3
	VirtioNetHdrGSOTCPv6 uint8 = 4
)

// VirtioNetConfig holds the device configuration (Section 5.1.4).
type VirtioNetConfig struct {
	MAC    [6]byte // Device MAC address
	Status uint16  // Link status
	// MaxVirtqueuePairs for multiqueue
	MaxVirtqueuePairs uint16
}

// TAPReadWriter abstracts a TAP device for sending/receiving Ethernet frames.
type TAPReadWriter interface {
	// Read reads a packet from the TAP device
	Read(buf []byte) (int, error)
	// Write writes a packet to the TAP device
	Write(buf []byte) (int, error)
	// Close closes the TAP device
	Close() error
}

// VirtioNet implements a virtio network device connected to a TAP interface.
type VirtioNet struct {
	mu sync.Mutex

	deviceID string
	mac      net.HardwareAddr

	// TAP device for packet I/O
	tap TAPReadWriter

	// Virtqueues: 0 = receiveq (device writes packets to guest),
	//             1 = transmitq (guest sends packets to device)
	rxQueue *Virtqueue
	txQueue *Virtqueue

	// Device state
	running atomic.Bool
	stopCh  chan struct{}
	wg      sync.WaitGroup

	// Stats
	rxPackets atomic.Uint64
	txPackets atomic.Uint64
	rxBytes   atomic.Uint64
	txBytes   atomic.Uint64
}

// VirtioNetOpts holds configuration for creating a VirtioNet device.
type VirtioNetOpts struct {
	DeviceID string
	MAC      net.HardwareAddr
	TAP      TAPReadWriter
	RxQueue  *Virtqueue
	TxQueue  *Virtqueue
}

// NewVirtioNet creates a new virtio-net device.
func NewVirtioNet(opts VirtioNetOpts) (*VirtioNet, error) {
	if opts.TAP == nil {
		return nil, errors.New("virtio-net: TAP device is required")
	}

	mac := opts.MAC
	if mac == nil {
		// Generate a locally-administered MAC address
		mac = generateMAC()
	}

	dev := &VirtioNet{
		deviceID: opts.DeviceID,
		mac:      mac,
		tap:      opts.TAP,
		rxQueue:  opts.RxQueue,
		txQueue:  opts.TxQueue,
		stopCh:   make(chan struct{}),
	}

	return dev, nil
}

// Type returns DeviceTypeNet.
func (d *VirtioNet) Type() DeviceType {
	return DeviceTypeNet
}

// ID returns the device identifier.
func (d *VirtioNet) ID() string {
	return d.deviceID
}

// MAC returns the device MAC address.
func (d *VirtioNet) MAC() net.HardwareAddr {
	return d.mac
}

// GetConfig returns the device configuration.
func (d *VirtioNet) GetConfig() VirtioNetConfig {
	var cfg VirtioNetConfig
	copy(cfg.MAC[:], d.mac)
	cfg.Status = 1 // Link up
	cfg.MaxVirtqueuePairs = 1
	return cfg
}

// Start begins packet processing goroutines.
func (d *VirtioNet) Start() error {
	if d.running.Load() {
		return errors.New("virtio-net: already running")
	}
	d.running.Store(true)

	// Start RX loop: read from TAP, inject into guest via rxQueue
	d.wg.Add(1)
	go d.rxLoop()

	return nil
}

// Stop halts packet processing.
func (d *VirtioNet) Stop() error {
	if !d.running.CompareAndSwap(true, false) {
		return nil
	}
	close(d.stopCh)
	d.wg.Wait()

	if d.tap != nil {
		return d.tap.Close()
	}
	return nil
}

// Configure applies configuration parameters.
func (d *VirtioNet) Configure(config map[string]string) error {
	return nil
}

// ProcessTx handles a transmit request from the guest.
// The descriptor chain contains: [virtio-net header + packet data (readable)]
func (d *VirtioNet) ProcessTx(chain *DescriptorChain) (uint32, error) {
	if !d.running.Load() {
		return 0, errors.New("virtio-net: device not running")
	}

	if d.txQueue == nil || d.txQueue.memRead == nil {
		return 0, errors.New("virtio-net: no tx queue or memory accessor")
	}

	// Gather all readable data from the chain
	var packet []byte
	for _, link := range chain.Readable {
		buf, err := d.txQueue.memRead(link.Addr, link.Len)
		if err != nil {
			return 0, fmt.Errorf("virtio-net: failed to read tx data: %w", err)
		}
		packet = append(packet, buf...)
	}

	if len(packet) < virtioNetHeaderSize {
		return 0, errors.New("virtio-net: packet too short for header")
	}

	// Skip the virtio-net header, send the Ethernet frame to TAP
	frame := packet[virtioNetHeaderSize:]
	if len(frame) == 0 {
		return 0, nil
	}

	n, err := d.tap.Write(frame)
	if err != nil {
		return 0, fmt.Errorf("virtio-net: tap write failed: %w", err)
	}

	d.txPackets.Add(1)
	d.txBytes.Add(uint64(n))
	return 0, nil
}

// rxLoop reads packets from the TAP device and injects them into the guest.
func (d *VirtioNet) rxLoop() {
	defer d.wg.Done()

	buf := make([]byte, 65536) // Max Ethernet frame + headroom

	for {
		select {
		case <-d.stopCh:
			return
		default:
		}

		n, err := d.tap.Read(buf)
		if err != nil {
			if !d.running.Load() {
				return
			}
			continue
		}

		if n == 0 {
			continue
		}

		if err := d.injectRxPacket(buf[:n]); err != nil {
			// Drop packet if we can't inject it
			continue
		}

		d.rxPackets.Add(1)
		d.rxBytes.Add(uint64(n))
	}
}

// injectRxPacket places a received packet into the guest via the rx virtqueue.
func (d *VirtioNet) injectRxPacket(frame []byte) error {
	if d.rxQueue == nil {
		return errors.New("virtio-net: no rx queue")
	}

	if !d.rxQueue.HasAvailable() {
		return errors.New("virtio-net: rx queue full")
	}

	chain, err := d.rxQueue.PopAvailable()
	if err != nil {
		return err
	}

	if len(chain.Writable) == 0 {
		return errors.New("virtio-net: no writable descriptors in rx chain")
	}

	// Build virtio-net header (all zeros for a simple received packet)
	hdr := make([]byte, virtioNetHeaderSize)

	// Write header + frame into the writable descriptors
	payload := append(hdr, frame...)
	var offset int
	var totalWritten uint32

	for _, desc := range chain.Writable {
		if offset >= len(payload) {
			break
		}
		end := offset + int(desc.Len)
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[offset:end]
		if err := d.rxQueue.memWrite(desc.Addr, chunk); err != nil {
			return err
		}
		totalWritten += uint32(len(chunk))
		offset = end
	}

	return d.rxQueue.PushUsed(chain.HeadIndex, totalWritten)
}

// Stats returns device packet statistics.
func (d *VirtioNet) Stats() VirtioNetStats {
	return VirtioNetStats{
		RxPackets: d.rxPackets.Load(),
		TxPackets: d.txPackets.Load(),
		RxBytes:   d.rxBytes.Load(),
		TxBytes:   d.txBytes.Load(),
	}
}

// VirtioNetStats holds device packet statistics.
type VirtioNetStats struct {
	RxPackets uint64
	TxPackets uint64
	RxBytes   uint64
	TxBytes   uint64
}

// SerializeNetConfig serializes the VirtioNetConfig to bytes.
func SerializeNetConfig(cfg VirtioNetConfig) []byte {
	buf := make([]byte, 12)
	copy(buf[0:6], cfg.MAC[:])
	binary.LittleEndian.PutUint16(buf[6:8], cfg.Status)
	binary.LittleEndian.PutUint16(buf[8:10], cfg.MaxVirtqueuePairs)
	return buf
}

// generateMAC creates a locally-administered MAC address with the tent OUI prefix.
func generateMAC() net.HardwareAddr {
	// Use 02:xx:xx for locally administered, unicast
	// Prefix: 02:74:6E ("tn" for tent)
	return net.HardwareAddr{0x02, 0x74, 0x6E, 0x00, 0x00, 0x01}
}
