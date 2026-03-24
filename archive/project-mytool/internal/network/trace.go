package network

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// TraceEvent represents a single captured network connection event.
type TraceEvent struct {
	Timestamp   time.Time `json:"timestamp" yaml:"timestamp"`
	SandboxName string    `json:"sandbox_name" yaml:"sandbox_name"`
	Direction   string    `json:"direction" yaml:"direction"` // "egress" or "ingress"
	Protocol    string    `json:"protocol" yaml:"protocol"`   // "tcp", "udp", "icmp"
	SrcIP       string    `json:"src_ip" yaml:"src_ip"`
	SrcPort     int       `json:"src_port" yaml:"src_port"`
	DstIP       string    `json:"dst_ip" yaml:"dst_ip"`
	DstPort     int       `json:"dst_port" yaml:"dst_port"`
	DstHostname string    `json:"dst_hostname,omitempty" yaml:"dst_hostname,omitempty"`
	Action      string    `json:"action" yaml:"action"` // "allowed", "blocked", "unknown"
	MatchedRule string    `json:"matched_rule,omitempty" yaml:"matched_rule,omitempty"`
	BytesSent   uint64    `json:"bytes_sent,omitempty" yaml:"bytes_sent,omitempty"`
	BytesRecv   uint64    `json:"bytes_recv,omitempty" yaml:"bytes_recv,omitempty"`
}

// TraceSession manages an active network trace for one or more sandboxes.
type TraceSession struct {
	ID          string        `json:"id" yaml:"id"`
	SandboxName string        `json:"sandbox_name" yaml:"sandbox_name"`
	StartedAt   time.Time     `json:"started_at" yaml:"started_at"`
	StoppedAt   *time.Time    `json:"stopped_at,omitempty" yaml:"stopped_at,omitempty"`
	Filter      *TraceFilter  `json:"filter,omitempty" yaml:"filter,omitempty"`
	Events      []*TraceEvent `json:"events" yaml:"events"`
	Active      bool          `json:"active" yaml:"active"`
	mu          sync.Mutex
}

// TraceFilter controls which events are captured.
type TraceFilter struct {
	Protocol  string `json:"protocol,omitempty" yaml:"protocol,omitempty"`   // empty = all protocols
	Port      int    `json:"port,omitempty" yaml:"port,omitempty"`           // 0 = all ports
	DstIP     string `json:"dst_ip,omitempty" yaml:"dst_ip,omitempty"`       // empty = all destinations
	Action    string `json:"action,omitempty" yaml:"action,omitempty"`       // empty = all, "blocked", "allowed"
	Direction string `json:"direction,omitempty" yaml:"direction,omitempty"` // empty = all, "egress", "ingress"
}

// TraceManager manages network trace sessions for sandboxes.
type TraceManager struct {
	baseDir  string
	sessions map[string]*TraceSession
	firewall *EgressFirewall
	mu       sync.Mutex
}

// NewTraceManager creates a new trace manager.
func NewTraceManager(baseDir string, firewall *EgressFirewall) (*TraceManager, error) {
	traceDir := filepath.Join(baseDir, "network-traces")
	if err := os.MkdirAll(traceDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create trace directory: %w", err)
	}

	return &TraceManager{
		baseDir:  baseDir,
		sessions: make(map[string]*TraceSession),
		firewall: firewall,
	}, nil
}

// StartTrace begins a new trace session for a sandbox.
func (tm *TraceManager) StartTrace(sandboxName string, filter *TraceFilter) (*TraceSession, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Check if there's already an active trace for this sandbox
	if existing, ok := tm.sessions[sandboxName]; ok && existing.Active {
		return nil, fmt.Errorf("trace already active for sandbox %q (session %s)", sandboxName, existing.ID)
	}

	session := &TraceSession{
		ID:          fmt.Sprintf("trace-%s-%d", sandboxName, time.Now().UnixNano()),
		SandboxName: sandboxName,
		StartedAt:   time.Now(),
		Filter:      filter,
		Events:      make([]*TraceEvent, 0),
		Active:      true,
	}

	tm.sessions[sandboxName] = session
	return session, nil
}

// StopTrace ends a trace session and saves the results.
func (tm *TraceManager) StopTrace(sandboxName string) (*TraceSession, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	session, ok := tm.sessions[sandboxName]
	if !ok {
		return nil, fmt.Errorf("no trace session for sandbox %q", sandboxName)
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	if !session.Active {
		return nil, fmt.Errorf("trace session for sandbox %q is not active", sandboxName)
	}

	now := time.Now()
	session.StoppedAt = &now
	session.Active = false

	// Save trace to disk
	if err := tm.saveTrace(session); err != nil {
		return session, fmt.Errorf("trace stopped but failed to save: %w", err)
	}

	return session, nil
}

// RecordEvent records a network event for a sandbox if tracing is active.
func (tm *TraceManager) RecordEvent(event *TraceEvent) {
	tm.mu.Lock()
	session, ok := tm.sessions[event.SandboxName]
	tm.mu.Unlock()

	if !ok || !session.Active {
		return
	}

	// Apply filter
	if session.Filter != nil && !matchesFilter(event, session.Filter) {
		return
	}

	session.mu.Lock()
	session.Events = append(session.Events, event)
	session.mu.Unlock()
}

// EvaluateConnection checks whether a connection would be allowed and records a trace event.
// Returns the action taken ("allowed" or "blocked") and the matched rule (if any).
func (tm *TraceManager) EvaluateConnection(sandboxName, protocol, dstIP string, dstPort int) (string, string) {
	action := "blocked"
	matchedRule := ""

	if tm.firewall != nil {
		ip := net.ParseIP(dstIP)
		if ip != nil && tm.firewall.IsAllowed(sandboxName, ip, dstPort) {
			action = "allowed"
			// Find which rule matched
			rules := tm.firewall.GetSandboxRules(sandboxName)
			if rules != nil {
				matchedRule = findMatchingRule(rules, ip, dstPort)
			}
		}
	}

	// Try reverse DNS for the destination
	hostname := ""
	names, err := net.LookupAddr(dstIP)
	if err == nil && len(names) > 0 {
		hostname = names[0]
	}

	event := &TraceEvent{
		Timestamp:   time.Now(),
		SandboxName: sandboxName,
		Direction:   "egress",
		Protocol:    protocol,
		DstIP:       dstIP,
		DstPort:     dstPort,
		DstHostname: hostname,
		Action:      action,
		MatchedRule: matchedRule,
	}

	tm.RecordEvent(event)
	return action, matchedRule
}

// GetActiveTrace returns the active trace session for a sandbox, if any.
func (tm *TraceManager) GetActiveTrace(sandboxName string) *TraceSession {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	session, ok := tm.sessions[sandboxName]
	if !ok || !session.Active {
		return nil
	}
	return session
}

// GetTraceEvents returns events from the active trace session.
func (tm *TraceManager) GetTraceEvents(sandboxName string) ([]*TraceEvent, error) {
	tm.mu.Lock()
	session, ok := tm.sessions[sandboxName]
	tm.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("no trace session for sandbox %q", sandboxName)
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	events := make([]*TraceEvent, len(session.Events))
	copy(events, session.Events)
	return events, nil
}

// ListTraces returns all saved trace files for a sandbox.
func (tm *TraceManager) ListTraces(sandboxName string) ([]TraceInfo, error) {
	traceDir := filepath.Join(tm.baseDir, "network-traces")
	pattern := filepath.Join(traceDir, fmt.Sprintf("trace-%s-*.yaml", sandboxName))

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to list traces: %w", err)
	}

	var traces []TraceInfo
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var session TraceSession
		if err := yaml.Unmarshal(data, &session); err != nil {
			continue
		}
		traces = append(traces, TraceInfo{
			ID:          session.ID,
			SandboxName: session.SandboxName,
			StartedAt:   session.StartedAt,
			StoppedAt:   session.StoppedAt,
			EventCount:  len(session.Events),
			FilePath:    path,
		})
	}

	return traces, nil
}

// LoadTrace loads a saved trace session from disk.
func (tm *TraceManager) LoadTrace(traceID string) (*TraceSession, error) {
	traceDir := filepath.Join(tm.baseDir, "network-traces")
	path := filepath.Join(traceDir, traceID+".yaml")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read trace file: %w", err)
	}

	var session TraceSession
	if err := yaml.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse trace file: %w", err)
	}

	return &session, nil
}

// DeleteTrace removes a saved trace file.
func (tm *TraceManager) DeleteTrace(traceID string) error {
	traceDir := filepath.Join(tm.baseDir, "network-traces")
	path := filepath.Join(traceDir, traceID+".yaml")

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete trace: %w", err)
	}
	return nil
}

// SimulateTrace evaluates a list of endpoints against the sandbox's policy
// and returns trace events showing what would be allowed/blocked. Useful for
// testing firewall rules without generating real traffic.
func (tm *TraceManager) SimulateTrace(sandboxName string, endpoints []string) ([]*TraceEvent, error) {
	var events []*TraceEvent

	for _, ep := range endpoints {
		rule, err := parseEndpoint(ep)
		if err != nil {
			return nil, fmt.Errorf("invalid endpoint %q: %w", ep, err)
		}

		port := rule.Port
		if port == 0 {
			port = 443 // default to HTTPS for simulation
		}

		proto := rule.Proto
		if proto == "" {
			proto = "tcp"
		}

		// Evaluate each resolved IP
		ips := rule.ResolvedIPs
		if rule.CIDR != nil {
			// For CIDRs, test the network address
			ips = []net.IP{rule.CIDR.IP}
		}
		if len(ips) == 0 {
			// Unresolvable hostname
			events = append(events, &TraceEvent{
				Timestamp:   time.Now(),
				SandboxName: sandboxName,
				Direction:   "egress",
				Protocol:    proto,
				DstIP:       ep,
				DstPort:     port,
				DstHostname: ep,
				Action:      "blocked",
				MatchedRule: "dns-resolution-failed",
			})
			continue
		}

		for _, ip := range ips {
			action := "blocked"
			matchedRule := ""

			if tm.firewall != nil && tm.firewall.IsAllowed(sandboxName, ip, port) {
				action = "allowed"
				rules := tm.firewall.GetSandboxRules(sandboxName)
				if rules != nil {
					matchedRule = findMatchingRule(rules, ip, port)
				}
			}

			events = append(events, &TraceEvent{
				Timestamp:   time.Now(),
				SandboxName: sandboxName,
				Direction:   "egress",
				Protocol:    proto,
				DstIP:       ip.String(),
				DstPort:     port,
				DstHostname: rule.Endpoint,
				Action:      action,
				MatchedRule: matchedRule,
			})
		}
	}

	return events, nil
}

// TraceStats computes summary statistics for a trace session.
func (s *TraceSession) TraceStats() TraceStatistics {
	s.mu.Lock()
	defer s.mu.Unlock()

	stats := TraceStatistics{
		TotalEvents: len(s.Events),
		UniqueDestinations: make(map[string]bool),
	}

	for _, ev := range s.Events {
		switch ev.Action {
		case "allowed":
			stats.AllowedCount++
		case "blocked":
			stats.BlockedCount++
		default:
			stats.UnknownCount++
		}

		switch ev.Protocol {
		case "tcp":
			stats.TCPCount++
		case "udp":
			stats.UDPCount++
		case "icmp":
			stats.ICMPCount++
		}

		dst := ev.DstIP
		if ev.DstHostname != "" {
			dst = ev.DstHostname
		}
		stats.UniqueDestinations[dst] = true

		stats.TotalBytesSent += ev.BytesSent
		stats.TotalBytesRecv += ev.BytesRecv
	}

	stats.UniqueDestCount = len(stats.UniqueDestinations)
	return stats
}

// TraceStatistics holds summary statistics for a trace session.
type TraceStatistics struct {
	TotalEvents        int             `json:"total_events"`
	AllowedCount       int             `json:"allowed_count"`
	BlockedCount       int             `json:"blocked_count"`
	UnknownCount       int             `json:"unknown_count"`
	TCPCount           int             `json:"tcp_count"`
	UDPCount           int             `json:"udp_count"`
	ICMPCount          int             `json:"icmp_count"`
	UniqueDestCount    int             `json:"unique_destinations"`
	UniqueDestinations map[string]bool `json:"-" yaml:"-"`
	TotalBytesSent     uint64          `json:"total_bytes_sent"`
	TotalBytesRecv     uint64          `json:"total_bytes_recv"`
}

// TraceInfo is a summary of a saved trace, used for listing.
type TraceInfo struct {
	ID          string     `json:"id"`
	SandboxName string     `json:"sandbox_name"`
	StartedAt   time.Time  `json:"started_at"`
	StoppedAt   *time.Time `json:"stopped_at,omitempty"`
	EventCount  int        `json:"event_count"`
	FilePath    string     `json:"-"`
}

// saveTrace persists a trace session to disk as YAML.
func (tm *TraceManager) saveTrace(session *TraceSession) error {
	traceDir := filepath.Join(tm.baseDir, "network-traces")
	if err := os.MkdirAll(traceDir, 0755); err != nil {
		return fmt.Errorf("failed to create trace directory: %w", err)
	}

	data, err := yaml.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal trace: %w", err)
	}

	path := filepath.Join(traceDir, session.ID+".yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write trace file: %w", err)
	}

	return nil
}

// matchesFilter checks whether an event matches the trace filter.
func matchesFilter(event *TraceEvent, filter *TraceFilter) bool {
	if filter.Protocol != "" && event.Protocol != filter.Protocol {
		return false
	}
	if filter.Port != 0 && event.DstPort != filter.Port {
		return false
	}
	if filter.DstIP != "" && event.DstIP != filter.DstIP {
		return false
	}
	if filter.Action != "" && event.Action != filter.Action {
		return false
	}
	if filter.Direction != "" && event.Direction != filter.Direction {
		return false
	}
	return true
}

// findMatchingRule returns a string describing which rule allowed the connection.
func findMatchingRule(rules *SandboxRules, ip net.IP, port int) string {
	for _, rule := range rules.Rules {
		if rule.Port != 0 && rule.Port != port {
			continue
		}
		if rule.CIDR != nil && rule.CIDR.Contains(ip) {
			return rule.Endpoint
		}
		for _, rip := range rule.ResolvedIPs {
			if rip.Equal(ip) {
				return rule.Endpoint
			}
		}
	}
	return ""
}
