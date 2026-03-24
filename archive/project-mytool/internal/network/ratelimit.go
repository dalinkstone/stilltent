// Package network provides cross-platform networking for microVMs.
// This file implements a token-bucket rate limiter and throttled I/O wrappers
// for enforcing bandwidth limits on sandbox network connections.
package network

import (
	"io"
	"sync"
	"time"
)

// TokenBucket implements a thread-safe token bucket rate limiter.
// Tokens represent bytes; the bucket fills at a steady rate (bytesPerSec)
// up to a maximum burst capacity.
type TokenBucket struct {
	mu           sync.Mutex
	tokens       float64   // current available tokens (bytes)
	maxTokens    float64   // burst capacity (bytes)
	refillRate   float64   // tokens added per second (bytes/sec)
	lastRefill   time.Time // last time tokens were added
}

// NewTokenBucket creates a rate limiter that allows bytesPerSec throughput
// with the given burst capacity. If burstBytes is 0, it defaults to
// bytesPerSec (one second of burst).
func NewTokenBucket(bytesPerSec, burstBytes uint64) *TokenBucket {
	if burstBytes == 0 {
		burstBytes = bytesPerSec
		if burstBytes < 4096 {
			burstBytes = 4096
		}
	}
	return &TokenBucket{
		tokens:     float64(burstBytes),
		maxTokens:  float64(burstBytes),
		refillRate: float64(bytesPerSec),
		lastRefill: time.Now(),
	}
}

// refill adds tokens based on elapsed time since last refill.
// Must be called with mu held.
func (tb *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.maxTokens {
		tb.tokens = tb.maxTokens
	}
	tb.lastRefill = now
}

// Take attempts to consume n tokens. It returns the number of tokens
// that can be consumed immediately (may be less than n) and the
// duration to wait before trying again if fewer tokens were available.
func (tb *TokenBucket) Take(n int) (int, time.Duration) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.refill()

	if tb.tokens >= float64(n) {
		tb.tokens -= float64(n)
		return n, 0
	}

	// Partial take: consume what's available
	available := int(tb.tokens)
	if available > 0 {
		tb.tokens -= float64(available)
		return available, 0
	}

	// No tokens: compute wait time for at least 1 token
	waitSec := 1.0 / tb.refillRate
	return 0, time.Duration(waitSec * float64(time.Second))
}

// WaitForTokens blocks until n tokens are available or returns immediately
// if the rate is unlimited (refillRate == 0).
func (tb *TokenBucket) WaitForTokens(n int) {
	remaining := n
	for remaining > 0 {
		taken, wait := tb.Take(remaining)
		remaining -= taken
		if remaining > 0 && wait > 0 {
			time.Sleep(wait)
		}
	}
}

// ThrottledReader wraps an io.Reader and limits read throughput
// using a token bucket rate limiter.
type ThrottledReader struct {
	reader io.Reader
	bucket *TokenBucket
}

// NewThrottledReader creates a reader that limits throughput to bytesPerSec.
// If bytesPerSec is 0, reads are not throttled.
func NewThrottledReader(r io.Reader, bytesPerSec, burstBytes uint64) io.Reader {
	if bytesPerSec == 0 {
		return r
	}
	return &ThrottledReader{
		reader: r,
		bucket: NewTokenBucket(bytesPerSec, burstBytes),
	}
}

func (tr *ThrottledReader) Read(p []byte) (int, error) {
	// Limit read size to available tokens to avoid long stalls
	maxRead := len(p)
	if maxRead > 65536 {
		maxRead = 65536
	}

	tr.bucket.WaitForTokens(maxRead)
	return tr.reader.Read(p[:maxRead])
}

// ThrottledWriter wraps an io.Writer and limits write throughput
// using a token bucket rate limiter.
type ThrottledWriter struct {
	writer io.Writer
	bucket *TokenBucket
}

// NewThrottledWriter creates a writer that limits throughput to bytesPerSec.
// If bytesPerSec is 0, writes are not throttled.
func NewThrottledWriter(w io.Writer, bytesPerSec, burstBytes uint64) io.Writer {
	if bytesPerSec == 0 {
		return w
	}
	return &ThrottledWriter{
		writer: w,
		bucket: NewTokenBucket(bytesPerSec, burstBytes),
	}
}

func (tw *ThrottledWriter) Write(p []byte) (int, error) {
	totalWritten := 0
	remaining := p

	for len(remaining) > 0 {
		chunk := len(remaining)
		if chunk > 65536 {
			chunk = 65536
		}

		tw.bucket.WaitForTokens(chunk)

		n, err := tw.writer.Write(remaining[:chunk])
		totalWritten += n
		if err != nil {
			return totalWritten, err
		}
		remaining = remaining[n:]
	}

	return totalWritten, nil
}

// ThrottledConn wraps a net.Conn (or any io.ReadWriteCloser) with
// separate ingress and egress rate limiters.
type ThrottledConn struct {
	reader io.Reader
	writer io.Writer
	closer io.Closer
}

// NewThrottledConn wraps a connection with bandwidth limits.
// Rates are in bits per second (matching BandwidthLimit). A rate of 0 means unlimited.
func NewThrottledConn(conn io.ReadWriteCloser, limit *BandwidthLimit) io.ReadWriteCloser {
	if limit == nil || !limit.HasLimits() {
		return conn
	}

	// Convert bits/sec to bytes/sec
	ingressBPS := limit.IngressRate / 8
	egressBPS := limit.EgressRate / 8
	ingressBurst := limit.IngressBurst
	egressBurst := limit.EgressBurst

	var reader io.Reader = conn
	var writer io.Writer = conn

	if ingressBPS > 0 {
		reader = NewThrottledReader(conn, ingressBPS, ingressBurst)
	}
	if egressBPS > 0 {
		writer = NewThrottledWriter(conn, egressBPS, egressBurst)
	}

	return &ThrottledConn{
		reader: reader,
		writer: writer,
		closer: conn,
	}
}

func (tc *ThrottledConn) Read(p []byte) (int, error) {
	return tc.reader.Read(p)
}

func (tc *ThrottledConn) Write(p []byte) (int, error) {
	return tc.writer.Write(p)
}

func (tc *ThrottledConn) Close() error {
	return tc.closer.Close()
}

// BandwidthTracker tracks cumulative bandwidth usage for a sandbox.
type BandwidthTracker struct {
	mu          sync.Mutex
	rxBytes     uint64
	txBytes     uint64
	rxPackets   uint64
	txPackets   uint64
	startTime   time.Time
}

// NewBandwidthTracker creates a tracker that records cumulative usage.
func NewBandwidthTracker() *BandwidthTracker {
	return &BandwidthTracker{
		startTime: time.Now(),
	}
}

// RecordRx records received (ingress) bytes and increments the packet count.
func (bt *BandwidthTracker) RecordRx(bytes int) {
	bt.mu.Lock()
	bt.rxBytes += uint64(bytes)
	bt.rxPackets++
	bt.mu.Unlock()
}

// RecordTx records transmitted (egress) bytes and increments the packet count.
func (bt *BandwidthTracker) RecordTx(bytes int) {
	bt.mu.Lock()
	bt.txBytes += uint64(bytes)
	bt.txPackets++
	bt.mu.Unlock()
}

// BandwidthStats holds a snapshot of bandwidth usage.
type BandwidthStats struct {
	RxBytes       uint64  `json:"rx_bytes"`
	TxBytes       uint64  `json:"tx_bytes"`
	RxPackets     uint64  `json:"rx_packets"`
	TxPackets     uint64  `json:"tx_packets"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	AvgRxBPS      float64 `json:"avg_rx_bps"` // average ingress bits/sec
	AvgTxBPS      float64 `json:"avg_tx_bps"` // average egress bits/sec
}

// Stats returns a snapshot of bandwidth usage.
func (bt *BandwidthTracker) Stats() BandwidthStats {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	uptime := time.Since(bt.startTime).Seconds()
	var avgRx, avgTx float64
	if uptime > 0 {
		avgRx = float64(bt.rxBytes*8) / uptime
		avgTx = float64(bt.txBytes*8) / uptime
	}

	return BandwidthStats{
		RxBytes:       bt.rxBytes,
		TxBytes:       bt.txBytes,
		RxPackets:     bt.rxPackets,
		TxPackets:     bt.txPackets,
		UptimeSeconds: uptime,
		AvgRxBPS:      avgRx,
		AvgTxBPS:      avgTx,
	}
}

// TrackingReader wraps a reader and records bytes read to a tracker.
type TrackingReader struct {
	reader  io.Reader
	tracker *BandwidthTracker
}

// NewTrackingReader wraps a reader with bandwidth tracking.
func NewTrackingReader(r io.Reader, tracker *BandwidthTracker) io.Reader {
	return &TrackingReader{reader: r, tracker: tracker}
}

func (tr *TrackingReader) Read(p []byte) (int, error) {
	n, err := tr.reader.Read(p)
	if n > 0 {
		tr.tracker.RecordRx(n)
	}
	return n, err
}

// TrackingWriter wraps a writer and records bytes written to a tracker.
type TrackingWriter struct {
	writer  io.Writer
	tracker *BandwidthTracker
}

// NewTrackingWriter wraps a writer with bandwidth tracking.
func NewTrackingWriter(w io.Writer, tracker *BandwidthTracker) io.Writer {
	return &TrackingWriter{writer: w, tracker: tracker}
}

func (tw *TrackingWriter) Write(p []byte) (int, error) {
	n, err := tw.writer.Write(p)
	if n > 0 {
		tw.tracker.RecordTx(n)
	}
	return n, err
}
