package vm

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// WebhookConfig defines a webhook subscription for sandbox events.
type WebhookConfig struct {
	ID        string    `json:"id" yaml:"id"`
	URL       string    `json:"url" yaml:"url"`
	Secret    string    `json:"secret,omitempty" yaml:"secret,omitempty"`
	Events    []string  `json:"events" yaml:"events"`
	Sandboxes []string  `json:"sandboxes,omitempty" yaml:"sandboxes,omitempty"`
	Active    bool      `json:"active" yaml:"active"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
}

// WebhookPayload is the JSON body sent to webhook endpoints.
type WebhookPayload struct {
	ID        string            `json:"id"`
	Timestamp time.Time         `json:"timestamp"`
	Event     EventType         `json:"event"`
	Sandbox   string            `json:"sandbox"`
	Details   map[string]string `json:"details,omitempty"`
}

// WebhookDelivery records the result of a webhook delivery attempt.
type WebhookDelivery struct {
	WebhookID  string    `json:"webhook_id"`
	Event      EventType `json:"event"`
	Sandbox    string    `json:"sandbox"`
	StatusCode int       `json:"status_code"`
	Error      string    `json:"error,omitempty"`
	Duration   string    `json:"duration"`
	Timestamp  time.Time `json:"timestamp"`
}

// WebhookManager manages webhook subscriptions and event delivery.
type WebhookManager struct {
	baseDir    string
	configPath string
	logPath    string
	client     *http.Client
	mu         sync.RWMutex
}

// NewWebhookManager creates a new webhook manager.
func NewWebhookManager(baseDir string) *WebhookManager {
	return &WebhookManager{
		baseDir:    baseDir,
		configPath: filepath.Join(baseDir, "webhooks.json"),
		logPath:    filepath.Join(baseDir, "webhook_deliveries.log"),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Add registers a new webhook subscription.
func (wm *WebhookManager) Add(cfg WebhookConfig) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	hooks, err := wm.loadLocked()
	if err != nil {
		return err
	}

	for _, h := range hooks {
		if h.ID == cfg.ID {
			return fmt.Errorf("webhook %q already exists", cfg.ID)
		}
	}

	cfg.CreatedAt = time.Now().UTC()
	hooks = append(hooks, cfg)
	return wm.saveLocked(hooks)
}

// Remove deletes a webhook subscription by ID.
func (wm *WebhookManager) Remove(id string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	hooks, err := wm.loadLocked()
	if err != nil {
		return err
	}

	found := false
	var result []WebhookConfig
	for _, h := range hooks {
		if h.ID == id {
			found = true
			continue
		}
		result = append(result, h)
	}

	if !found {
		return fmt.Errorf("webhook %q not found", id)
	}

	return wm.saveLocked(result)
}

// List returns all webhook subscriptions.
func (wm *WebhookManager) List() ([]WebhookConfig, error) {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	return wm.loadLocked()
}

// Get returns a specific webhook by ID.
func (wm *WebhookManager) Get(id string) (*WebhookConfig, error) {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	hooks, err := wm.loadLocked()
	if err != nil {
		return nil, err
	}

	for _, h := range hooks {
		if h.ID == id {
			return &h, nil
		}
	}
	return nil, fmt.Errorf("webhook %q not found", id)
}

// SetActive enables or disables a webhook.
func (wm *WebhookManager) SetActive(id string, active bool) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	hooks, err := wm.loadLocked()
	if err != nil {
		return err
	}

	found := false
	for i, h := range hooks {
		if h.ID == id {
			hooks[i].Active = active
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("webhook %q not found", id)
	}

	return wm.saveLocked(hooks)
}

// Deliver sends an event to all matching webhooks. Called by the event system.
func (wm *WebhookManager) Deliver(event Event) {
	wm.mu.RLock()
	hooks, err := wm.loadLocked()
	wm.mu.RUnlock()

	if err != nil || len(hooks) == 0 {
		return
	}

	for _, hook := range hooks {
		if !hook.Active {
			continue
		}
		if !wm.matchesEvent(hook, event) {
			continue
		}
		// Fire-and-forget delivery in goroutine
		go wm.deliverOne(hook, event)
	}
}

// DeliveryLog returns recent webhook deliveries.
func (wm *WebhookManager) DeliveryLog(webhookID string, limit int) ([]WebhookDelivery, error) {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	data, err := os.ReadFile(wm.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read delivery log: %w", err)
	}

	var deliveries []WebhookDelivery
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			line := data[start:i]
			start = i + 1
			if len(line) == 0 {
				continue
			}
			var d WebhookDelivery
			if err := json.Unmarshal(line, &d); err != nil {
				continue
			}
			if webhookID != "" && d.WebhookID != webhookID {
				continue
			}
			deliveries = append(deliveries, d)
		}
	}

	if limit > 0 && len(deliveries) > limit {
		deliveries = deliveries[len(deliveries)-limit:]
	}

	return deliveries, nil
}

// Test sends a test event to a webhook and returns the delivery result.
func (wm *WebhookManager) Test(id string) (*WebhookDelivery, error) {
	hook, err := wm.Get(id)
	if err != nil {
		return nil, err
	}

	testEvent := Event{
		Timestamp: time.Now().UTC(),
		Type:      "webhook.test",
		Sandbox:   "test-sandbox",
		Details:   map[string]string{"message": "webhook test delivery"},
	}

	return wm.deliverOne(*hook, testEvent), nil
}

func (wm *WebhookManager) matchesEvent(hook WebhookConfig, event Event) bool {
	// Check sandbox filter
	if len(hook.Sandboxes) > 0 {
		matched := false
		for _, s := range hook.Sandboxes {
			if s == event.Sandbox || s == "*" {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Check event type filter
	if len(hook.Events) > 0 {
		matched := false
		for _, e := range hook.Events {
			if e == string(event.Type) || e == "*" {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

func (wm *WebhookManager) deliverOne(hook WebhookConfig, event Event) *WebhookDelivery {
	delivery := &WebhookDelivery{
		WebhookID: hook.ID,
		Event:     event.Type,
		Sandbox:   event.Sandbox,
		Timestamp: time.Now().UTC(),
	}

	payload := WebhookPayload{
		ID:        fmt.Sprintf("%s-%d", hook.ID, time.Now().UnixNano()),
		Timestamp: event.Timestamp,
		Event:     event.Type,
		Sandbox:   event.Sandbox,
		Details:   event.Details,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		delivery.Error = fmt.Sprintf("marshal error: %v", err)
		wm.logDelivery(delivery)
		return delivery
	}

	req, err := http.NewRequest(http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		delivery.Error = fmt.Sprintf("request error: %v", err)
		wm.logDelivery(delivery)
		return delivery
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tent-webhook/1.0")
	req.Header.Set("X-Tent-Event", string(event.Type))
	req.Header.Set("X-Tent-Delivery", payload.ID)

	if hook.Secret != "" {
		sig := computeHMAC(body, []byte(hook.Secret))
		req.Header.Set("X-Tent-Signature", "sha256="+sig)
	}

	start := time.Now()
	resp, err := wm.client.Do(req)
	delivery.Duration = time.Since(start).String()

	if err != nil {
		delivery.Error = fmt.Sprintf("delivery failed: %v", err)
		wm.logDelivery(delivery)
		return delivery
	}
	defer resp.Body.Close()

	delivery.StatusCode = resp.StatusCode
	if resp.StatusCode >= 400 {
		delivery.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	wm.logDelivery(delivery)
	return delivery
}

func (wm *WebhookManager) logDelivery(d *WebhookDelivery) {
	data, err := json.Marshal(d)
	if err != nil {
		return
	}

	if err := os.MkdirAll(filepath.Dir(wm.logPath), 0755); err != nil {
		return
	}

	f, err := os.OpenFile(wm.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = f.Write(append(data, '\n'))
}

func (wm *WebhookManager) loadLocked() ([]WebhookConfig, error) {
	data, err := os.ReadFile(wm.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read webhooks: %w", err)
	}

	var hooks []WebhookConfig
	if err := json.Unmarshal(data, &hooks); err != nil {
		return nil, fmt.Errorf("failed to parse webhooks: %w", err)
	}

	return hooks, nil
}

func (wm *WebhookManager) saveLocked(hooks []WebhookConfig) error {
	if err := os.MkdirAll(filepath.Dir(wm.configPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal webhooks: %w", err)
	}

	return os.WriteFile(wm.configPath, data, 0600)
}

func computeHMAC(message, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}
