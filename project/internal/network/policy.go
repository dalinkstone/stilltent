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
	Name      string          `yaml:"name"`
	Allowed   []string        `yaml:"allowed"`
	Denied    []string        `yaml:"denied"`
	Bandwidth *BandwidthLimit `yaml:"bandwidth,omitempty"`
	CreatedAt int64           `yaml:"created_at"`
	UpdatedAt int64           `yaml:"updated_at"`
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
	if err := os.WriteFile(policyPath, data, 0644); err != nil {
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

	now := Policy{
		Name:      name,
		Allowed:   allowed,
		Denied:    denied,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	// Check if this is a new policy
	if _, exists := pm.policies[name]; !exists {
		now.CreatedAt = time.Now().Unix()
	}

	pm.policies[name] = &now
	return &now, nil
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
	policy.UpdatedAt = 0 // Will be set when saved

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
	policy.UpdatedAt = 0 // Will be set when saved

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
	policy.UpdatedAt = 0 // Will be set when saved

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
	policy.UpdatedAt = 0 // Will be set when saved

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
