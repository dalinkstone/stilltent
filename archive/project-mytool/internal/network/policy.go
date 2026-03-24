package network

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// NetworkCondition defines simulated network conditions for a sandbox.
// Used to test workloads under degraded network scenarios (high latency,
// packet loss, jitter). All values of 0 mean no simulation (normal conditions).
type NetworkCondition struct {
	LatencyMs   uint32  `yaml:"latency_ms,omitempty"`   // added latency in milliseconds (0 = none)
	JitterMs    uint32  `yaml:"jitter_ms,omitempty"`    // latency jitter in milliseconds (0 = none)
	PacketLoss  float64 `yaml:"packet_loss,omitempty"`  // packet loss percentage 0.0-100.0 (0 = none)
	Corrupt     float64 `yaml:"corrupt,omitempty"`      // packet corruption percentage 0.0-100.0 (0 = none)
	Reorder     float64 `yaml:"reorder,omitempty"`      // packet reorder percentage 0.0-100.0 (0 = none)
	Duplicate   float64 `yaml:"duplicate,omitempty"`    // packet duplicate percentage 0.0-100.0 (0 = none)
	RateLimitKB uint32  `yaml:"rate_limit_kb,omitempty"` // rate limit in KB/s for condition shaping (0 = unlimited)
	Preset      string  `yaml:"preset,omitempty"`       // named preset: "3g", "satellite", "lossy-wifi", "edge", "perfect"
}

// HasConditions returns true if any network condition simulation is configured.
func (nc *NetworkCondition) HasConditions() bool {
	return nc != nil && (nc.LatencyMs > 0 || nc.JitterMs > 0 || nc.PacketLoss > 0 ||
		nc.Corrupt > 0 || nc.Reorder > 0 || nc.Duplicate > 0 || nc.RateLimitKB > 0)
}

// ApplyPreset fills in condition values from a named preset.
func (nc *NetworkCondition) ApplyPreset(preset string) error {
	switch strings.ToLower(preset) {
	case "3g":
		nc.LatencyMs = 150
		nc.JitterMs = 30
		nc.PacketLoss = 1.5
		nc.RateLimitKB = 384
		nc.Preset = "3g"
	case "satellite":
		nc.LatencyMs = 600
		nc.JitterMs = 50
		nc.PacketLoss = 2.0
		nc.RateLimitKB = 512
		nc.Preset = "satellite"
	case "lossy-wifi":
		nc.LatencyMs = 10
		nc.JitterMs = 20
		nc.PacketLoss = 5.0
		nc.Corrupt = 0.5
		nc.Preset = "lossy-wifi"
	case "edge":
		nc.LatencyMs = 300
		nc.JitterMs = 100
		nc.PacketLoss = 3.0
		nc.RateLimitKB = 128
		nc.Preset = "edge"
	case "perfect", "none", "":
		*nc = NetworkCondition{}
	default:
		return fmt.Errorf("unknown preset %q: use 3g, satellite, lossy-wifi, edge, or perfect", preset)
	}
	return nil
}

// FormatDuration formats milliseconds as a human-readable duration string.
func FormatDuration(ms uint32) string {
	if ms == 0 {
		return "0ms"
	}
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000.0)
	}
	return fmt.Sprintf("%dms", ms)
}

// FormatPercent formats a percentage value.
func FormatPercent(pct float64) string {
	if pct == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", pct)
}

// BandwidthLimit defines rate limits for sandbox network traffic.
// Rates are specified in bits per second. A value of 0 means unlimited.
type BandwidthLimit struct {
	IngressRate  uint64 `yaml:"ingress_rate,omitempty"`  // max inbound bits/sec (0 = unlimited)
	EgressRate   uint64 `yaml:"egress_rate,omitempty"`   // max outbound bits/sec (0 = unlimited)
	IngressBurst uint64 `yaml:"ingress_burst,omitempty"` // burst size in bytes for inbound (0 = auto)
	EgressBurst  uint64 `yaml:"egress_burst,omitempty"`  // burst size in bytes for outbound (0 = auto)
}

// HasLimits returns true if any rate limit is configured.
func (b *BandwidthLimit) HasLimits() bool {
	return b != nil && (b.IngressRate > 0 || b.EgressRate > 0)
}

// FormatRate formats a bits-per-second value as a human-readable string.
func FormatRate(bps uint64) string {
	switch {
	case bps == 0:
		return "unlimited"
	case bps >= 1_000_000_000:
		return fmt.Sprintf("%.1f Gbps", float64(bps)/1_000_000_000)
	case bps >= 1_000_000:
		return fmt.Sprintf("%.1f Mbps", float64(bps)/1_000_000)
	case bps >= 1_000:
		return fmt.Sprintf("%.1f Kbps", float64(bps)/1_000)
	default:
		return fmt.Sprintf("%d bps", bps)
	}
}

// ParseRate parses a human-readable rate string (e.g., "10mbps", "1gbps", "500kbps")
// into bits per second.
func ParseRate(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || strings.EqualFold(s, "unlimited") {
		return 0, nil
	}

	lower := strings.ToLower(s)

	// Try suffixes from longest to shortest
	suffixes := []struct {
		suffix     string
		multiplier uint64
	}{
		{"gbps", 1_000_000_000},
		{"mbps", 1_000_000},
		{"kbps", 1_000},
		{"bps", 1},
		{"g", 1_000_000_000},
		{"m", 1_000_000},
		{"k", 1_000},
	}

	for _, sf := range suffixes {
		if strings.HasSuffix(lower, sf.suffix) {
			numStr := strings.TrimSpace(s[:len(s)-len(sf.suffix)])
			val, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid rate value %q: %w", numStr, err)
			}
			if val < 0 {
				return 0, fmt.Errorf("rate cannot be negative: %s", s)
			}
			return uint64(val * float64(sf.multiplier)), nil
		}
	}

	// Try plain number (interpreted as bps)
	val, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid rate %q: expected number with optional suffix (kbps, mbps, gbps)", s)
	}
	return val, nil
}

// Policy represents network policy for a sandbox
type Policy struct {
	Name      string            `yaml:"name"`
	Allowed   []string          `yaml:"allowed"`
	Denied    []string          `yaml:"denied"`
	Bandwidth *BandwidthLimit   `yaml:"bandwidth,omitempty"`
	Condition *NetworkCondition `yaml:"condition,omitempty"`
	Proxy     *ProxySettings    `yaml:"proxy,omitempty"`
	CreatedAt int64             `yaml:"created_at"`
	UpdatedAt int64             `yaml:"updated_at"`
}

// ProxySettings stores HTTP/HTTPS proxy configuration for a sandbox
type ProxySettings struct {
	HTTPProxy  string   `yaml:"http_proxy,omitempty"`
	HTTPSProxy string   `yaml:"https_proxy,omitempty"`
	NoProxy    []string `yaml:"no_proxy,omitempty"`
	Enabled    bool     `yaml:"enabled"`
}

// PolicyManager manages network policies for sandboxes
type PolicyManager struct {
	baseDir string
	policies map[string]*Policy
	mu      sync.Mutex
}

// NewPolicyManager creates a new network policy manager
func NewPolicyManager(baseDir string) (*PolicyManager, error) {
	pm := &PolicyManager{
		baseDir:  baseDir,
		policies: make(map[string]*Policy),
	}

	// Load existing policies
	if err := pm.loadPolicies(); err != nil {
		return nil, fmt.Errorf("failed to load policies: %w", err)
	}

	return pm, nil
}

// loadPolicies loads all saved policies from disk
func (pm *PolicyManager) loadPolicies() error {
	policiesDir := pm.getPoliciesDir()
	if _, err := os.Stat(policiesDir); os.IsNotExist(err) {
		// Create directory if it doesn't exist
		if err := os.MkdirAll(policiesDir, 0755); err != nil {
			return fmt.Errorf("failed to create policies directory: %w", err)
		}
		return nil
	}

	// Read all policy files
	entries, err := os.ReadDir(policiesDir)
	if err != nil {
		return fmt.Errorf("failed to read policies directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".yaml" {
			if err := pm.loadPolicy(entry.Name()); err != nil {
				// Skip failed loads but log them
				fmt.Printf("Warning: failed to load policy %s: %v\n", entry.Name(), err)
			}
		}
	}

	return nil
}

// loadPolicy loads a single policy from file
func (pm *PolicyManager) loadPolicy(filename string) error {
	policyPath := filepath.Join(pm.getPoliciesDir(), filename)
	data, err := os.ReadFile(policyPath)
	if err != nil {
		return fmt.Errorf("failed to read policy file: %w", err)
	}

	var policy Policy
	if err := yaml.Unmarshal(data, &policy); err != nil {
		return fmt.Errorf("failed to parse policy YAML: %w", err)
	}

	pm.mu.Lock()
	pm.policies[policy.Name] = &policy
	pm.mu.Unlock()

	return nil
}

// getPoliciesDir returns the policies directory path
func (pm *PolicyManager) getPoliciesDir() string {
	return filepath.Join(pm.baseDir, "network-policies")
}

// SavePolicy saves a policy to disk
func (pm *PolicyManager) SavePolicy(policy *Policy) error {
	pm.mu.Lock()
	pm.policies[policy.Name] = policy
	pm.mu.Unlock()

	// Ensure directory exists
	policiesDir := pm.getPoliciesDir()
	if err := os.MkdirAll(policiesDir, 0755); err != nil {
		return fmt.Errorf("failed to create policies directory: %w", err)
	}

	// Marshal to YAML
	data, err := yaml.Marshal(policy)
	if err != nil {
		return fmt.Errorf("failed to marshal policy: %w", err)
	}

	// Save to file
	policyPath := filepath.Join(policiesDir, fmt.Sprintf("%s.yaml", policy.Name))
	if err := os.WriteFile(policyPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write policy file: %w", err)
	}

	return nil
}

// GetPolicy retrieves policy for a sandbox
func (pm *PolicyManager) GetPolicy(name string) (*Policy, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		return nil, fmt.Errorf("no policy found for sandbox %s", name)
	}

	return policy, nil
}

// SetPolicy creates or updates a policy for a sandbox
func (pm *PolicyManager) SetPolicy(name string, allowed, denied []string) (*Policy, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	ts := time.Now().Unix()
	createdAt := ts

	// Preserve existing CreatedAt if this is an update
	if existing, exists := pm.policies[name]; exists {
		createdAt = existing.CreatedAt
	}

	policy := &Policy{
		Name:      name,
		Allowed:   allowed,
		Denied:    denied,
		CreatedAt: createdAt,
		UpdatedAt: ts,
	}

	pm.policies[name] = policy
	return policy, nil
}

// AddAllowedEndpoint adds an endpoint to the allowed list
func (pm *PolicyManager) AddAllowedEndpoint(name, endpoint string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		// Create new policy
		policy = &Policy{
			Name:      name,
			Allowed:   []string{},
			Denied:    []string{},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		pm.policies[name] = policy
	} else {
		policy.UpdatedAt = time.Now().Unix()
	}

	// Check if endpoint already exists
	for _, ep := range policy.Allowed {
		if ep == endpoint {
			return nil // Already exists
		}
	}

	// Add endpoint
	policy.Allowed = append(policy.Allowed, endpoint)
	policy.UpdatedAt = time.Now().Unix()

	return nil
}

// RemoveAllowedEndpoint removes an endpoint from the allowed list
func (pm *PolicyManager) RemoveAllowedEndpoint(name, endpoint string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		return fmt.Errorf("no policy found for sandbox %s", name)
	}

	// Find and remove endpoint
	var newAllowed []string
	for _, ep := range policy.Allowed {
		if ep != endpoint {
			newAllowed = append(newAllowed, ep)
		}
	}

	if len(newAllowed) == len(policy.Allowed) {
		return fmt.Errorf("endpoint %s not in allowed list", endpoint)
	}

	policy.Allowed = newAllowed
	policy.UpdatedAt = time.Now().Unix()

	return nil
}

// AddDeniedEndpoint adds an endpoint to the denied list
func (pm *PolicyManager) AddDeniedEndpoint(name, endpoint string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		// Create new policy
		policy = &Policy{
			Name:      name,
			Allowed:   []string{},
			Denied:    []string{},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		pm.policies[name] = policy
	} else {
		policy.UpdatedAt = time.Now().Unix()
	}

	// Check if endpoint already exists
	for _, ep := range policy.Denied {
		if ep == endpoint {
			return nil // Already exists
		}
	}

	// Add endpoint
	policy.Denied = append(policy.Denied, endpoint)
	policy.UpdatedAt = time.Now().Unix()

	return nil
}

// RemoveDeniedEndpoint removes an endpoint from the denied list
func (pm *PolicyManager) RemoveDeniedEndpoint(name, endpoint string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		return fmt.Errorf("no policy found for sandbox %s", name)
	}

	// Find and remove endpoint
	var newDenied []string
	for _, ep := range policy.Denied {
		if ep != endpoint {
			newDenied = append(newDenied, ep)
		}
	}

	if len(newDenied) == len(policy.Denied) {
		return fmt.Errorf("endpoint %s not in denied list", endpoint)
	}

	policy.Denied = newDenied
	policy.UpdatedAt = time.Now().Unix()

	return nil
}

// ListPolicies returns all policies
func (pm *PolicyManager) ListPolicies() ([]*Policy, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policies := make([]*Policy, 0, len(pm.policies))
	for _, policy := range pm.policies {
		policies = append(policies, policy)
	}

	return policies, nil
}

// DefaultAIAllowlist returns the default set of AI API endpoints that are
// allowed by default for AI workloads. This implements tent's "AI-native
// defaults" — sandboxes can reach common model APIs out of the box.
func DefaultAIAllowlist() []string {
	return []string{
		"api.anthropic.com",
		"api.openai.com",
		"openrouter.ai",
		"api.openrouter.ai",
		"generativelanguage.googleapis.com",
		"localhost:12434", // Docker Model Runner
	}
}

// EnsureDefaultPolicy creates a policy with the default AI allowlist if no
// policy exists yet for the given sandbox. Returns the (possibly new) policy.
func (pm *PolicyManager) EnsureDefaultPolicy(name string) (*Policy, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if policy, exists := pm.policies[name]; exists {
		return policy, nil
	}

	policy := &Policy{
		Name:      name,
		Allowed:   DefaultAIAllowlist(),
		Denied:    []string{},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	pm.policies[name] = policy
	return policy, nil
}

// SetBandwidthLimit configures bandwidth limits for a sandbox.
func (pm *PolicyManager) SetBandwidthLimit(name string, limit *BandwidthLimit) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		policy = &Policy{
			Name:      name,
			Allowed:   []string{},
			Denied:    []string{},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		pm.policies[name] = policy
	}

	policy.Bandwidth = limit
	policy.UpdatedAt = time.Now().Unix()
	return nil
}

// GetBandwidthLimit returns the bandwidth limit for a sandbox.
func (pm *PolicyManager) GetBandwidthLimit(name string) (*BandwidthLimit, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		return nil, fmt.Errorf("no policy found for sandbox %s", name)
	}

	if policy.Bandwidth == nil {
		return &BandwidthLimit{}, nil
	}
	return policy.Bandwidth, nil
}

// RemoveBandwidthLimit clears bandwidth limits for a sandbox.
func (pm *PolicyManager) RemoveBandwidthLimit(name string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		return fmt.Errorf("no policy found for sandbox %s", name)
	}

	policy.Bandwidth = nil
	policy.UpdatedAt = time.Now().Unix()
	return nil
}

// SetNetworkCondition configures network condition simulation for a sandbox.
func (pm *PolicyManager) SetNetworkCondition(name string, cond *NetworkCondition) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		policy = &Policy{
			Name:      name,
			Allowed:   []string{},
			Denied:    []string{},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		pm.policies[name] = policy
	}

	policy.Condition = cond
	policy.UpdatedAt = time.Now().Unix()
	return nil
}

// GetNetworkCondition returns the network condition simulation for a sandbox.
func (pm *PolicyManager) GetNetworkCondition(name string) (*NetworkCondition, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		return nil, fmt.Errorf("no policy found for sandbox %s", name)
	}

	if policy.Condition == nil {
		return &NetworkCondition{}, nil
	}
	return policy.Condition, nil
}

// RemoveNetworkCondition clears network condition simulation for a sandbox.
func (pm *PolicyManager) RemoveNetworkCondition(name string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		return fmt.Errorf("no policy found for sandbox %s", name)
	}

	policy.Condition = nil
	policy.UpdatedAt = time.Now().Unix()
	return nil
}

// ListPresets returns all available network condition presets with descriptions.
func ListPresets() map[string]string {
	return map[string]string{
		"3g":         "3G mobile (150ms latency, 30ms jitter, 1.5% loss, 384KB/s)",
		"satellite":  "Satellite link (600ms latency, 50ms jitter, 2% loss, 512KB/s)",
		"lossy-wifi": "Lossy WiFi (10ms latency, 20ms jitter, 5% loss, 0.5% corrupt)",
		"edge":       "Edge/2G (300ms latency, 100ms jitter, 3% loss, 128KB/s)",
		"perfect":    "No simulation (clear all conditions)",
	}
}

// IsEndpointAllowed checks if an endpoint is allowed for a sandbox
func (pm *PolicyManager) IsEndpointAllowed(name, endpoint string) (bool, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		// Default: all endpoints blocked unless allowed
		return false, nil
	}

	// Check if explicitly denied (deny list takes precedence)
	for _, ep := range policy.Denied {
		if ep == endpoint {
			return false, nil
		}
	}

	// Check if explicitly allowed
	for _, ep := range policy.Allowed {
		if ep == endpoint {
			return true, nil
		}
	}

	// Default: blocked (security by default)
	return false, nil
}

// SetProxy configures proxy settings for a sandbox.
func (pm *PolicyManager) SetProxy(name string, proxy *ProxySettings) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		policy = &Policy{
			Name:      name,
			Allowed:   []string{},
			Denied:    []string{},
			CreatedAt: time.Now().Unix(),
			UpdatedAt: time.Now().Unix(),
		}
		pm.policies[name] = policy
	}

	policy.Proxy = proxy
	policy.UpdatedAt = time.Now().Unix()
	return nil
}

// GetProxy returns the proxy settings for a sandbox.
func (pm *PolicyManager) GetProxy(name string) (*ProxySettings, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		return nil, fmt.Errorf("no policy found for sandbox %s", name)
	}

	if policy.Proxy == nil {
		return &ProxySettings{}, nil
	}
	return policy.Proxy, nil
}

// RemoveProxy clears proxy settings for a sandbox.
func (pm *PolicyManager) RemoveProxy(name string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	policy, exists := pm.policies[name]
	if !exists {
		return fmt.Errorf("no policy found for sandbox %s", name)
	}

	policy.Proxy = nil
	policy.UpdatedAt = time.Now().Unix()
	return nil
}

// ProxyEnvVars returns environment variables for the proxy configuration.
// These can be injected into the sandbox guest environment.
func (ps *ProxySettings) ProxyEnvVars() map[string]string {
	if ps == nil || !ps.Enabled {
		return nil
	}

	vars := make(map[string]string)
	if ps.HTTPProxy != "" {
		vars["http_proxy"] = ps.HTTPProxy
		vars["HTTP_PROXY"] = ps.HTTPProxy
	}
	if ps.HTTPSProxy != "" {
		vars["https_proxy"] = ps.HTTPSProxy
		vars["HTTPS_PROXY"] = ps.HTTPSProxy
	}
	if len(ps.NoProxy) > 0 {
		noProxy := ""
		for i, np := range ps.NoProxy {
			if i > 0 {
				noProxy += ","
			}
			noProxy += np
		}
		vars["no_proxy"] = noProxy
		vars["NO_PROXY"] = noProxy
	}
	return vars
}
