package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dalinkstone/tent/pkg/models"
)

// TTLEntry represents a sandbox TTL record.
type TTLEntry struct {
	Sandbox   string    `json:"sandbox"`
	TTL       string    `json:"ttl"`
	SetAt     time.Time `json:"set_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Action    string    `json:"action"` // "stop" or "destroy"
}

// TTLManager manages sandbox time-to-live policies.
type TTLManager struct {
	mu      sync.Mutex
	baseDir string
}

// NewTTLManager creates a new TTL manager.
func NewTTLManager(baseDir string) *TTLManager {
	return &TTLManager{baseDir: baseDir}
}

// Set sets a TTL on a sandbox. The TTL duration string supports Go durations
// plus "d" for days (e.g., "30m", "2h", "1d", "7d").
// Action specifies what happens on expiry: "stop" or "destroy" (default: "destroy").
func (t *TTLManager) Set(name, ttlStr, action string) (*TTLEntry, error) {
	if action == "" {
		action = "destroy"
	}
	if action != "stop" && action != "destroy" {
		return nil, fmt.Errorf("invalid action %q: must be \"stop\" or \"destroy\"", action)
	}

	dur, err := parseTTLDuration(ttlStr)
	if err != nil {
		return nil, fmt.Errorf("invalid TTL duration %q: %w", ttlStr, err)
	}

	now := time.Now().UTC()
	entry := &TTLEntry{
		Sandbox:   name,
		TTL:       ttlStr,
		SetAt:     now,
		ExpiresAt: now.Add(dur),
		Action:    action,
	}

	if err := t.saveEntry(entry); err != nil {
		return nil, err
	}

	return entry, nil
}

// Get returns the TTL entry for a sandbox, or nil if none is set.
func (t *TTLManager) Get(name string) (*TTLEntry, error) {
	return t.loadEntry(name)
}

// Remove removes the TTL from a sandbox.
func (t *TTLManager) Remove(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	path := t.entryPath(name)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no TTL set for sandbox %q", name)
		}
		return err
	}
	return nil
}

// List returns all active TTL entries.
func (t *TTLManager) List() ([]*TTLEntry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	dir := t.ttlDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var result []*TTLEntry
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			continue
		}
		var entry TTLEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}
		result = append(result, &entry)
	}

	return result, nil
}

// Expired returns all TTL entries that have expired.
func (t *TTLManager) Expired() ([]*TTLEntry, error) {
	all, err := t.List()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var expired []*TTLEntry
	for _, e := range all {
		if now.After(e.ExpiresAt) {
			expired = append(expired, e)
		}
	}
	return expired, nil
}

// Enforce checks all TTL entries and performs the configured action on expired sandboxes.
// Returns the list of sandboxes that were acted on.
func (t *TTLManager) Enforce(manager *VMManager) ([]string, error) {
	expired, err := t.Expired()
	if err != nil {
		return nil, fmt.Errorf("failed to check expired TTLs: %w", err)
	}

	var acted []string
	for _, entry := range expired {
		// Check sandbox exists and is actionable
		vmState, err := manager.stateManager.GetVM(entry.Sandbox)
		if err != nil {
			// Sandbox gone, clean up the TTL entry
			_ = t.Remove(entry.Sandbox)
			continue
		}

		switch entry.Action {
		case "stop":
			if vmState.Status == models.VMStatusRunning {
				if err := manager.Stop(entry.Sandbox); err != nil {
					continue
				}
				acted = append(acted, entry.Sandbox)
			}
			_ = t.Remove(entry.Sandbox)
		case "destroy":
			if vmState.Status == models.VMStatusRunning {
				_ = manager.Stop(entry.Sandbox)
			}
			if err := manager.Destroy(entry.Sandbox); err != nil {
				continue
			}
			_ = t.Remove(entry.Sandbox)
			acted = append(acted, entry.Sandbox)
		}
	}

	return acted, nil
}

// TimeRemaining returns how long until a sandbox's TTL expires.
func (t *TTLManager) TimeRemaining(name string) (time.Duration, error) {
	entry, err := t.loadEntry(name)
	if err != nil {
		return 0, err
	}
	if entry == nil {
		return 0, fmt.Errorf("no TTL set for sandbox %q", name)
	}

	remaining := time.Until(entry.ExpiresAt)
	if remaining < 0 {
		return 0, nil
	}
	return remaining, nil
}

func (t *TTLManager) ttlDir() string {
	return filepath.Join(t.baseDir, "ttl")
}

func (t *TTLManager) entryPath(name string) string {
	return filepath.Join(t.ttlDir(), name+".json")
}

func (t *TTLManager) saveEntry(entry *TTLEntry) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	dir := t.ttlDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create TTL directory: %w", err)
	}

	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(t.entryPath(entry.Sandbox), data, 0644)
}

func (t *TTLManager) loadEntry(name string) (*TTLEntry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	data, err := os.ReadFile(t.entryPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var entry TTLEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

// parseTTLDuration handles standard Go durations plus "d" for days.
func parseTTLDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		trimmed := strings.TrimSuffix(s, "d")
		var days int
		if _, err := fmt.Sscanf(trimmed, "%d", &days); err != nil {
			return 0, fmt.Errorf("invalid duration: %s", s)
		}
		if days <= 0 {
			return 0, fmt.Errorf("duration must be positive")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return d, nil
}
