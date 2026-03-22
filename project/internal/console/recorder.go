// Package console provides VM console log capture and retrieval.
// This file implements session recording and playback with timing data,
// similar to script/scriptreplay but for sandbox console sessions.
package console

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RecordEvent represents a single recorded console I/O event.
type RecordEvent struct {
	// Delay is the time elapsed since the previous event (or session start).
	Delay time.Duration `json:"delay_ns"`
	// Direction is "i" for input, "o" for output.
	Direction string `json:"dir"`
	// Data is the raw bytes captured.
	Data []byte `json:"data"`
}

// Recording is a complete recorded console session.
type Recording struct {
	// SandboxName is the name of the sandbox that was recorded.
	SandboxName string `json:"sandbox"`
	// StartedAt is when the recording began.
	StartedAt time.Time `json:"started_at"`
	// Duration is the total length of the recording.
	Duration time.Duration `json:"duration_ns"`
	// Events is the ordered list of I/O events.
	Events []RecordEvent `json:"events"`
	// Metadata holds optional key-value annotations.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Recorder captures console I/O events with timing information.
type Recorder struct {
	mu          sync.Mutex
	sandboxName string
	startTime   time.Time
	lastEvent   time.Time
	events      []RecordEvent
	metadata    map[string]string
	closed      bool
}

// NewRecorder creates a new session recorder for the named sandbox.
func NewRecorder(sandboxName string) *Recorder {
	now := time.Now()
	return &Recorder{
		sandboxName: sandboxName,
		startTime:   now,
		lastEvent:   now,
		events:      make([]RecordEvent, 0, 256),
		metadata:    make(map[string]string),
	}
}

// SetMetadata adds an annotation to the recording.
func (r *Recorder) SetMetadata(key, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metadata[key] = value
}

// RecordOutput records output data (sandbox → user) with timing.
func (r *Recorder) RecordOutput(data []byte) {
	r.record("o", data)
}

// RecordInput records input data (user → sandbox) with timing.
func (r *Recorder) RecordInput(data []byte) {
	r.record("i", data)
}

func (r *Recorder) record(dir string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(data) == 0 {
		return
	}

	now := time.Now()
	delay := now.Sub(r.lastEvent)
	r.lastEvent = now

	// Copy data to avoid aliasing
	buf := make([]byte, len(data))
	copy(buf, data)

	r.events = append(r.events, RecordEvent{
		Delay:     delay,
		Direction: dir,
		Data:      buf,
	})
}

// Finish finalises the recording and returns it. The recorder cannot be used after this.
func (r *Recorder) Finish() *Recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true

	return &Recording{
		SandboxName: r.sandboxName,
		StartedAt:   r.startTime,
		Duration:    time.Since(r.startTime),
		Events:      r.events,
		Metadata:    r.metadata,
	}
}

// SaveRecording writes a recording to a JSON file in the recordings directory.
func SaveRecording(baseDir string, rec *Recording, tag string) (string, error) {
	dir := filepath.Join(baseDir, "recordings", rec.SandboxName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create recordings directory: %w", err)
	}

	if tag == "" {
		tag = rec.StartedAt.Format("20060102-150405")
	}
	filename := fmt.Sprintf("%s.json", tag)
	path := filepath.Join(dir, filename)

	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("failed to create recording file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rec); err != nil {
		return "", fmt.Errorf("failed to encode recording: %w", err)
	}

	return path, nil
}

// LoadRecording loads a recording from a JSON file.
func LoadRecording(path string) (*Recording, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open recording: %w", err)
	}
	defer f.Close()

	var rec Recording
	if err := json.NewDecoder(f).Decode(&rec); err != nil {
		return nil, fmt.Errorf("failed to decode recording: %w", err)
	}
	return &rec, nil
}

// ListRecordings returns all recording files for a sandbox.
func ListRecordings(baseDir, sandboxName string) ([]RecordingInfo, error) {
	dir := filepath.Join(baseDir, "recordings", sandboxName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read recordings directory: %w", err)
	}

	var infos []RecordingInfo
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		rec, err := LoadRecording(path)
		if err != nil {
			continue // skip corrupt files
		}
		fi, _ := entry.Info()
		infos = append(infos, RecordingInfo{
			Tag:       entry.Name()[:len(entry.Name())-5], // strip .json
			Path:      path,
			Sandbox:   rec.SandboxName,
			StartedAt: rec.StartedAt,
			Duration:  rec.Duration,
			Events:    len(rec.Events),
			Size:      fi.Size(),
		})
	}
	return infos, nil
}

// ListAllRecordings returns recordings across all sandboxes.
func ListAllRecordings(baseDir string) ([]RecordingInfo, error) {
	recDir := filepath.Join(baseDir, "recordings")
	entries, err := os.ReadDir(recDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read recordings directory: %w", err)
	}

	var all []RecordingInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		infos, err := ListRecordings(baseDir, entry.Name())
		if err != nil {
			continue
		}
		all = append(all, infos...)
	}
	return all, nil
}

// RecordingInfo holds summary information about a recording.
type RecordingInfo struct {
	Tag       string        `json:"tag"`
	Path      string        `json:"path"`
	Sandbox   string        `json:"sandbox"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration_ns"`
	Events    int           `json:"events"`
	Size      int64         `json:"size"`
}

// DeleteRecording removes a recording file.
func DeleteRecording(baseDir, sandboxName, tag string) error {
	path := filepath.Join(baseDir, "recordings", sandboxName, tag+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("recording %q not found for sandbox %q", tag, sandboxName)
		}
		return fmt.Errorf("failed to delete recording: %w", err)
	}
	return nil
}

// Replay plays back a recording to the given writer, honoring timing delays.
// The speed parameter multiplies playback speed (1.0 = realtime, 2.0 = 2x, etc.).
// Set maxDelay to cap the maximum delay between events (0 = no cap).
// The done channel can be used to cancel playback.
func Replay(rec *Recording, out io.Writer, speed float64, maxDelay time.Duration, done <-chan struct{}) error {
	if speed <= 0 {
		speed = 1.0
	}

	w := bufio.NewWriter(out)
	defer w.Flush()

	for _, evt := range rec.Events {
		// Only replay output events by default
		if evt.Direction != "o" {
			continue
		}

		// Calculate delay
		delay := time.Duration(float64(evt.Delay) / speed)
		if maxDelay > 0 && delay > maxDelay {
			delay = maxDelay
		}

		if delay > 0 {
			select {
			case <-done:
				return nil
			case <-time.After(delay):
			}
		}

		if _, err := w.Write(evt.Data); err != nil {
			return fmt.Errorf("write error during replay: %w", err)
		}
		w.Flush()
	}
	return nil
}

// ExportAsciicast exports a recording in asciicast v2 format (compatible with asciinema).
func ExportAsciicast(rec *Recording, out io.Writer) error {
	w := bufio.NewWriter(out)
	defer w.Flush()

	// Write header
	header := map[string]interface{}{
		"version":   2,
		"width":     120,
		"height":    40,
		"timestamp": rec.StartedAt.Unix(),
		"title":     fmt.Sprintf("tent session: %s", rec.SandboxName),
		"env": map[string]string{
			"TERM":  "xterm-256color",
			"SHELL": "/bin/bash",
		},
	}

	headerBytes, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("failed to encode header: %w", err)
	}
	w.Write(headerBytes)
	w.WriteByte('\n')

	// Write events
	var elapsed float64
	for _, evt := range rec.Events {
		if evt.Direction != "o" {
			continue
		}
		elapsed += evt.Delay.Seconds()
		// asciicast v2 format: [time, type, data]
		line, err := json.Marshal([]interface{}{elapsed, "o", string(evt.Data)})
		if err != nil {
			continue
		}
		w.Write(line)
		w.WriteByte('\n')
	}

	return nil
}
