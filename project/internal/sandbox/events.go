package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EventType represents the type of sandbox lifecycle event
type EventType string

const (
	EventCreate          EventType = "create"
	EventStart           EventType = "start"
	EventStop            EventType = "stop"
	EventDestroy         EventType = "destroy"
	EventRestart         EventType = "restart"
	EventSnapshotCreate  EventType = "snapshot.create"
	EventSnapshotRestore EventType = "snapshot.restore"
	EventSnapshotDelete  EventType = "snapshot.delete"
	EventExport          EventType = "export"
	EventImport          EventType = "import"
	EventRename          EventType = "rename"
	EventUpdate          EventType = "update"
	EventPrune           EventType = "prune"
	EventNetworkAllow    EventType = "network.allow"
	EventNetworkDeny     EventType = "network.deny"
	EventClone           EventType = "clone"
	EventPause           EventType = "pause"
	EventUnpause         EventType = "unpause"
	EventCommit          EventType = "commit"
	EventHookRun            EventType = "hook.run"
	EventHookError          EventType = "hook.error"
	EventCheckpointCreate   EventType = "checkpoint.create"
	EventCheckpointRestore  EventType = "checkpoint.restore"
	EventCheckpointDelete   EventType = "checkpoint.delete"
)

// Event represents a single sandbox lifecycle event
type Event struct {
	Timestamp time.Time         `json:"timestamp"`
	Type      EventType         `json:"type"`
	Sandbox   string            `json:"sandbox"`
	Details   map[string]string `json:"details,omitempty"`
}

// EventLogger handles writing and reading sandbox events
type EventLogger struct {
	logPath string
	mu      sync.Mutex
}

// NewEventLogger creates an event logger for the given base directory
func NewEventLogger(baseDir string) *EventLogger {
	return &EventLogger{
		logPath: filepath.Join(baseDir, "events.log"),
	}
}

// Log writes an event to the event log
func (el *EventLogger) Log(eventType EventType, sandbox string, details map[string]string) error {
	el.mu.Lock()
	defer el.mu.Unlock()

	event := Event{
		Timestamp: time.Now().UTC(),
		Type:      eventType,
		Sandbox:   sandbox,
		Details:   details,
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(el.logPath), 0755); err != nil {
		return fmt.Errorf("failed to create events directory: %w", err)
	}

	f, err := os.OpenFile(el.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open event log: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to write event: %w", err)
	}

	return nil
}

// EventFilter controls which events to return
type EventFilter struct {
	Sandbox string
	Type    EventType
	Since   time.Time
	Limit   int
}

// Query reads events from the log, applying optional filters
func (el *EventLogger) Query(filter EventFilter) ([]Event, error) {
	el.mu.Lock()
	defer el.mu.Unlock()

	data, err := os.ReadFile(el.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read event log: %w", err)
	}

	var events []Event
	// Parse newline-delimited JSON
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			line := data[start:i]
			start = i + 1
			if len(line) == 0 {
				continue
			}
			var event Event
			if err := json.Unmarshal(line, &event); err != nil {
				continue // skip malformed lines
			}

			// Apply filters
			if filter.Sandbox != "" && event.Sandbox != filter.Sandbox {
				continue
			}
			if filter.Type != "" && event.Type != filter.Type {
				continue
			}
			if !filter.Since.IsZero() && event.Timestamp.Before(filter.Since) {
				continue
			}
			events = append(events, event)
		}
	}

	// Apply limit (return last N events)
	if filter.Limit > 0 && len(events) > filter.Limit {
		events = events[len(events)-filter.Limit:]
	}

	return events, nil
}

// WatchEvent represents an event delivered via the Watch channel
type WatchEvent struct {
	Event Event
	Err   error
}

// Watch tails the event log file, sending new events on the returned channel.
// It polls the file for new lines at the given interval. Close the done channel to stop.
func (el *EventLogger) Watch(filter EventFilter, interval time.Duration, done <-chan struct{}) <-chan WatchEvent {
	ch := make(chan WatchEvent, 16)

	go func() {
		defer close(ch)

		var offset int64

		// Start from end of file
		if info, err := os.Stat(el.logPath); err == nil {
			offset = info.Size()
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				newEvents, newOffset := el.readNewEvents(offset, filter)
				if newOffset > offset {
					offset = newOffset
				}
				for _, ev := range newEvents {
					select {
					case ch <- WatchEvent{Event: ev}:
					case <-done:
						return
					}
				}
			}
		}
	}()

	return ch
}

// readNewEvents reads events appended after the given byte offset
func (el *EventLogger) readNewEvents(offset int64, filter EventFilter) ([]Event, int64) {
	f, err := os.Open(el.logPath)
	if err != nil {
		return nil, offset
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() <= offset {
		return nil, offset
	}

	if _, err := f.Seek(offset, 0); err != nil {
		return nil, offset
	}

	buf := make([]byte, info.Size()-offset)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return nil, offset
	}

	var events []Event
	start := 0
	for i := 0; i < n; i++ {
		if buf[i] == '\n' {
			line := buf[start:i]
			start = i + 1
			if len(line) == 0 {
				continue
			}
			var event Event
			if err := json.Unmarshal(line, &event); err != nil {
				continue
			}
			if filter.Sandbox != "" && event.Sandbox != filter.Sandbox {
				continue
			}
			if filter.Type != "" && event.Type != filter.Type {
				continue
			}
			events = append(events, event)
		}
	}

	return events, offset + int64(start)
}
