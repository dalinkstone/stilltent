package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEntry represents a single audit log record.
type AuditEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Action    string            `json:"action"`
	Sandbox   string            `json:"sandbox,omitempty"`
	User      string            `json:"user,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
	Success   bool              `json:"success"`
	Error     string            `json:"error,omitempty"`
}

// AuditLog manages an append-only audit trail of sandbox operations.
type AuditLog struct {
	mu   sync.Mutex
	path string
}

// NewAuditLog creates a new audit log in the given data directory.
func NewAuditLog(dataDir string) (*AuditLog, error) {
	if dataDir == "" {
		dataDir = "~/.tent"
	}

	if dataDir[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dataDir = filepath.Join(home, dataDir[1:])
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	return &AuditLog{
		path: filepath.Join(dataDir, "audit.log"),
	}, nil
}

// Log appends an audit entry to the log file.
func (a *AuditLog) Log(action, sandbox string, details map[string]string, success bool, errMsg string) error {
	entry := AuditEntry{
		Timestamp: time.Now().UTC(),
		Action:    action,
		Sandbox:   sandbox,
		User:      currentUser(),
		Details:   details,
		Success:   success,
		Error:     errMsg,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()

	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}

// AuditFilter specifies criteria for querying audit entries.
type AuditFilter struct {
	Sandbox string
	Action  string
	Since   time.Time
	Until   time.Time
	Limit   int
	Success *bool
}

// Query reads and filters audit entries. Results are returned newest-first.
func (a *AuditLog) Query(filter AuditFilter) ([]AuditEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	data, err := os.ReadFile(a.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var all []AuditEntry
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip malformed lines
		}
		all = append(all, entry)
	}

	// Filter
	var filtered []AuditEntry
	for _, e := range all {
		if filter.Sandbox != "" && e.Sandbox != filter.Sandbox {
			continue
		}
		if filter.Action != "" && e.Action != filter.Action {
			continue
		}
		if !filter.Since.IsZero() && e.Timestamp.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && e.Timestamp.After(filter.Until) {
			continue
		}
		if filter.Success != nil && e.Success != *filter.Success {
			continue
		}
		filtered = append(filtered, e)
	}

	// Reverse for newest-first
	for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}

	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}

	return filtered, nil
}

// Clear removes all audit entries.
func (a *AuditLog) Clear() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	return os.Remove(a.path)
}

// Stats returns aggregate counts per action.
func (a *AuditLog) Stats() (map[string]int, int, error) {
	entries, err := a.Query(AuditFilter{})
	if err != nil {
		return nil, 0, err
	}

	counts := make(map[string]int)
	for _, e := range entries {
		counts[e.Action]++
	}

	return counts, len(entries), nil
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	return "unknown"
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// FormatEntry returns a human-readable string for an audit entry.
func FormatEntry(e AuditEntry) string {
	status := "OK"
	if !e.Success {
		status = fmt.Sprintf("FAIL: %s", e.Error)
	}

	s := fmt.Sprintf("[%s] %-12s %-20s user=%-10s %s",
		e.Timestamp.Format("2006-01-02 15:04:05"),
		e.Action,
		e.Sandbox,
		e.User,
		status,
	)

	if len(e.Details) > 0 {
		for k, v := range e.Details {
			s += fmt.Sprintf(" %s=%s", k, v)
		}
	}

	return s
}
