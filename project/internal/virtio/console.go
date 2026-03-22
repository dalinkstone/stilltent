// Package virtio provides virtio device emulation for microVMs.
// This file implements virtio-console — a serial console device for
// guest-host communication (boot logs, interactive shell).
//
// Reference: Virtual I/O Device (VIRTIO) Version 1.1, Section 5.3
package virtio

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// VirtioConsole implements a virtio console (serial port) device.
// It bridges guest console I/O to a host-side reader/writer (e.g., PTY, log file).
type VirtioConsole struct {
	mu sync.Mutex

	deviceID string

	// Host-side I/O
	input  io.Reader // Data from host -> guest (keyboard input)
	output io.Writer // Data from guest -> host (console output/logs)

	// Virtqueues: 0 = receiveq (host->guest), 1 = transmitq (guest->host)
	rxQueue *Virtqueue
	txQueue *Virtqueue

	// Device state
	running atomic.Bool
	stopCh  chan struct{}
	wg      sync.WaitGroup

	// Stats
	bytesIn  atomic.Uint64
	bytesOut atomic.Uint64
}

// VirtioConsoleOpts holds configuration for creating a VirtioConsole device.
type VirtioConsoleOpts struct {
	DeviceID string
	Input    io.Reader // Host input to guest (can be nil for output-only)
	Output   io.Writer // Guest output to host (required)
	RxQueue  *Virtqueue
	TxQueue  *Virtqueue
}

// NewVirtioConsole creates a new virtio-console device.
func NewVirtioConsole(opts VirtioConsoleOpts) (*VirtioConsole, error) {
	if opts.Output == nil {
		return nil, errors.New("virtio-console: output writer is required")
	}

	dev := &VirtioConsole{
		deviceID: opts.DeviceID,
		input:    opts.Input,
		output:   opts.Output,
		rxQueue:  opts.RxQueue,
		txQueue:  opts.TxQueue,
		stopCh:   make(chan struct{}),
	}

	return dev, nil
}

// Type returns DeviceTypeConsole.
func (d *VirtioConsole) Type() DeviceType {
	return DeviceTypeConsole
}

// ID returns the device identifier.
func (d *VirtioConsole) ID() string {
	return d.deviceID
}

// Start begins console I/O processing.
func (d *VirtioConsole) Start() error {
	if d.running.Load() {
		return errors.New("virtio-console: already running")
	}
	d.running.Store(true)

	// Start input loop if we have a reader
	if d.input != nil {
		d.wg.Add(1)
		go d.inputLoop()
	}

	return nil
}

// Stop halts console I/O.
func (d *VirtioConsole) Stop() error {
	if !d.running.CompareAndSwap(true, false) {
		return nil
	}
	close(d.stopCh)
	d.wg.Wait()
	return nil
}

// Configure applies configuration parameters.
func (d *VirtioConsole) Configure(config map[string]string) error {
	return nil
}

// ProcessTx handles a transmit request from the guest (guest -> host output).
// Each descriptor chain contains raw console data.
func (d *VirtioConsole) ProcessTx(chain *DescriptorChain) (uint32, error) {
	if !d.running.Load() {
		return 0, errors.New("virtio-console: device not running")
	}

	if d.txQueue == nil || d.txQueue.memRead == nil {
		return 0, errors.New("virtio-console: no tx queue or memory accessor")
	}

	// Gather all readable data from the chain
	for _, link := range chain.Readable {
		buf, err := d.txQueue.memRead(link.Addr, link.Len)
		if err != nil {
			return 0, fmt.Errorf("virtio-console: failed to read tx data: %w", err)
		}

		n, err := d.output.Write(buf)
		if err != nil {
			return 0, fmt.Errorf("virtio-console: output write failed: %w", err)
		}
		d.bytesOut.Add(uint64(n))
	}

	return 0, nil
}

// inputLoop reads from the host input and injects data into the guest via rxQueue.
func (d *VirtioConsole) inputLoop() {
	defer d.wg.Done()

	buf := make([]byte, 4096)

	for {
		select {
		case <-d.stopCh:
			return
		default:
		}

		n, err := d.input.Read(buf)
		if err != nil {
			if !d.running.Load() {
				return
			}
			if err == io.EOF {
				return
			}
			continue
		}

		if n == 0 {
			continue
		}

		if err := d.injectInput(buf[:n]); err != nil {
			continue // Drop data if queue is full
		}

		d.bytesIn.Add(uint64(n))
	}
}

// injectInput places host input data into the guest via the rx virtqueue.
func (d *VirtioConsole) injectInput(data []byte) error {
	if d.rxQueue == nil {
		return errors.New("virtio-console: no rx queue")
	}

	if !d.rxQueue.HasAvailable() {
		return errors.New("virtio-console: rx queue full")
	}

	chain, err := d.rxQueue.PopAvailable()
	if err != nil {
		return err
	}

	if len(chain.Writable) == 0 {
		return errors.New("virtio-console: no writable descriptors in rx chain")
	}

	var offset int
	var totalWritten uint32

	for _, desc := range chain.Writable {
		if offset >= len(data) {
			break
		}
		end := offset + int(desc.Len)
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		if err := d.rxQueue.memWrite(desc.Addr, chunk); err != nil {
			return err
		}
		totalWritten += uint32(len(chunk))
		offset = end
	}

	return d.rxQueue.PushUsed(chain.HeadIndex, totalWritten)
}

// WriteToGuest sends data from the host to the guest console.
// This is a convenience method for programmatic input.
func (d *VirtioConsole) WriteToGuest(data []byte) error {
	if !d.running.Load() {
		return errors.New("virtio-console: device not running")
	}
	return d.injectInput(data)
}

// Stats returns console I/O statistics.
func (d *VirtioConsole) Stats() VirtioConsoleStats {
	return VirtioConsoleStats{
		BytesIn:  d.bytesIn.Load(),
		BytesOut: d.bytesOut.Load(),
	}
}

// VirtioConsoleStats holds console I/O statistics.
type VirtioConsoleStats struct {
	BytesIn  uint64 // Host -> Guest bytes
	BytesOut uint64 // Guest -> Host bytes
}
