// Package network provides networking for tent microVMs.
// This file implements a lightweight DNS server for compose-group service discovery.
// Sandboxes in a compose group can reach each other by name (e.g., "agent", "shared-db").
//
// The DNS server listens on UDP port 53, resolves sandbox names to their DHCP-assigned
// IPs, and forwards unknown queries to upstream resolvers.
//
// Wire format: RFC 1035 — we implement a minimal subset (A queries and responses).
package network

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// DNS header flags
const (
	dnsFlagQR     = 1 << 15 // Response
	dnsFlagAA     = 1 << 10 // Authoritative
	dnsFlagRD     = 1 << 8  // Recursion desired
	dnsFlagRA     = 1 << 7  // Recursion available
	dnsRcodeOK    = 0
	dnsRcodeNX    = 3 // Name not found
	dnsRcodeFail  = 2 // Server failure
)

// DNS record types
const (
	dnsTypeA    uint16 = 1
	dnsTypeAAAA uint16 = 28
	dnsClassIN  uint16 = 1
)

// DNSRecord maps a name to an IP address for local resolution.
type DNSRecord struct {
	Name string
	IP   net.IP
}

// DNSServer is a lightweight DNS server for compose-group service discovery.
// It resolves sandbox names to their DHCP-assigned IPs and forwards
// unknown queries to upstream DNS servers.
type DNSServer struct {
	mu sync.RWMutex

	// Local records: lowercase name -> IP
	records map[string]net.IP

	// Domain suffix for local resolution (e.g., ".tent.local")
	domain string

	// Upstream DNS servers for forwarding
	upstreams []string

	// Listener
	conn    *net.UDPConn
	bindIP  string
	stopCh  chan struct{}
	stopped bool
}

// DNSServerConfig holds configuration for the DNS server.
type DNSServerConfig struct {
	// BindIP is the IP to listen on (typically the bridge IP)
	BindIP string
	// Domain is the local domain suffix (default: "tent.local")
	Domain string
	// Upstreams are upstream DNS servers for forwarding (default: 8.8.8.8, 8.8.4.4)
	Upstreams []string
}

// DefaultDNSConfig returns default DNS server configuration.
func DefaultDNSConfig() *DNSServerConfig {
	return &DNSServerConfig{
		BindIP:    "172.16.0.1",
		Domain:    "tent.local",
		Upstreams: []string{"8.8.8.8:53", "8.8.4.4:53"},
	}
}

// NewDNSServer creates a new DNS server for service discovery.
func NewDNSServer(cfg *DNSServerConfig) (*DNSServer, error) {
	if cfg == nil {
		cfg = DefaultDNSConfig()
	}

	domain := cfg.Domain
	if domain == "" {
		domain = "tent.local"
	}
	// Ensure domain has a leading dot for suffix matching
	if !strings.HasPrefix(domain, ".") {
		domain = "." + domain
	}

	upstreams := cfg.Upstreams
	if len(upstreams) == 0 {
		upstreams = []string{"8.8.8.8:53", "8.8.4.4:53"}
	}
	// Ensure all upstreams have port
	for i, u := range upstreams {
		if !strings.Contains(u, ":") {
			upstreams[i] = u + ":53"
		}
	}

	return &DNSServer{
		records:   make(map[string]net.IP),
		domain:    domain,
		upstreams: upstreams,
		bindIP:    cfg.BindIP,
		stopCh:    make(chan struct{}),
	}, nil
}

// Register adds or updates a DNS record for a sandbox name.
func (s *DNSServer) Register(name string, ip net.IP) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[strings.ToLower(name)] = ip.To4()
}

// Unregister removes a DNS record for a sandbox name.
func (s *DNSServer) Unregister(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, strings.ToLower(name))
}

// Resolve looks up a sandbox name and returns its IP, or nil if not found.
func (s *DNSServer) Resolve(name string) net.IP {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.records[strings.ToLower(name)]
}

// Records returns a copy of all registered DNS records.
func (s *DNSServer) Records() []DNSRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]DNSRecord, 0, len(s.records))
	for name, ip := range s.records {
		result = append(result, DNSRecord{Name: name, IP: ip})
	}
	return result
}

// Start begins listening for DNS queries on UDP port 53.
func (s *DNSServer) Start() error {
	ip := net.ParseIP(s.bindIP)
	if ip == nil {
		ip = net.IPv4zero
	}
	addr := &net.UDPAddr{IP: ip, Port: 53}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("dns: failed to listen on %s:53: %w", s.bindIP, err)
	}
	s.conn = conn

	go s.serve()
	return nil
}

// Stop shuts down the DNS server.
func (s *DNSServer) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.mu.Unlock()

	close(s.stopCh)
	if s.conn != nil {
		s.conn.Close()
	}
}

// serve is the main DNS packet processing loop.
func (s *DNSServer) serve() {
	buf := make([]byte, 512) // Standard DNS UDP max
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		s.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			select {
			case <-s.stopCh:
				return
			default:
				continue
			}
		}

		if n < 12 {
			continue // Too small for a DNS header
		}

		query := make([]byte, n)
		copy(query, buf[:n])

		resp := s.handleQuery(query)
		if resp != nil {
			s.conn.WriteToUDP(resp, addr)
		}
	}
}

// handleQuery processes a DNS query and returns a response.
func (s *DNSServer) handleQuery(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}

	// Parse header
	id := binary.BigEndian.Uint16(query[0:2])
	flags := binary.BigEndian.Uint16(query[2:4])
	qdCount := binary.BigEndian.Uint16(query[4:6])

	// Only handle standard queries (opcode 0)
	opcode := (flags >> 11) & 0xF
	if opcode != 0 {
		return nil
	}

	if qdCount == 0 {
		return nil
	}

	// Parse the first question
	name, qtype, qclass, offset := parseDNSQuestion(query, 12)
	if name == "" || offset == 0 {
		return nil
	}

	if qclass != dnsClassIN {
		return nil
	}

	// Try local resolution
	localName := s.extractLocalName(name)
	if localName != "" {
		ip := s.Resolve(localName)
		if ip != nil && qtype == dnsTypeA {
			return buildDNSResponse(id, flags, query[12:offset], name, ip)
		}
		// Name matches our domain but no record — return NXDOMAIN
		if ip == nil {
			return buildDNSNXDomain(id, flags, query[12:offset])
		}
	}

	// Also try bare name (no domain suffix) for convenience
	if localName == "" {
		ip := s.Resolve(name)
		if ip != nil && qtype == dnsTypeA {
			return buildDNSResponse(id, flags, query[12:offset], name, ip)
		}
	}

	// Forward to upstream
	return s.forward(query)
}

// extractLocalName strips the domain suffix and returns the sandbox name,
// or empty string if the name doesn't match our domain.
func (s *DNSServer) extractLocalName(name string) string {
	lower := strings.ToLower(name)
	// Remove trailing dot if present
	lower = strings.TrimSuffix(lower, ".")
	suffix := strings.ToLower(s.domain)

	if strings.HasSuffix(lower, suffix) {
		return strings.TrimSuffix(lower, suffix)
	}
	return ""
}

// forward sends the query to an upstream DNS server and returns the response.
func (s *DNSServer) forward(query []byte) []byte {
	for _, upstream := range s.upstreams {
		addr, err := net.ResolveUDPAddr("udp4", upstream)
		if err != nil {
			continue
		}

		conn, err := net.DialUDP("udp4", nil, addr)
		if err != nil {
			continue
		}

		conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = conn.Write(query)
		if err != nil {
			conn.Close()
			continue
		}

		buf := make([]byte, 512)
		n, err := conn.Read(buf)
		conn.Close()
		if err != nil {
			continue
		}

		return buf[:n]
	}

	// All upstreams failed — return SERVFAIL
	return buildDNSServFail(binary.BigEndian.Uint16(query[0:2]), query)
}

// parseDNSQuestion parses a DNS question section starting at the given offset.
// Returns the queried name, type, class, and the offset after the question.
func parseDNSQuestion(data []byte, offset int) (string, uint16, uint16, int) {
	name, newOffset := decodeDNSName(data, offset)
	if newOffset == 0 || newOffset+4 > len(data) {
		return "", 0, 0, 0
	}
	qtype := binary.BigEndian.Uint16(data[newOffset : newOffset+2])
	qclass := binary.BigEndian.Uint16(data[newOffset+2 : newOffset+4])
	return name, qtype, qclass, newOffset + 4
}

// decodeDNSName decodes a DNS name from wire format (label sequences).
// Returns the decoded name and the offset after the name.
func decodeDNSName(data []byte, offset int) (string, int) {
	var parts []string
	seen := make(map[int]bool) // Prevent pointer loops
	origOffset := offset
	jumped := false

	for offset < len(data) {
		if seen[offset] {
			return "", 0 // Loop detected
		}
		seen[offset] = true

		length := int(data[offset])
		if length == 0 {
			if !jumped {
				origOffset = offset + 1
			}
			break
		}

		// Compression pointer
		if length&0xC0 == 0xC0 {
			if offset+1 >= len(data) {
				return "", 0
			}
			ptr := int(binary.BigEndian.Uint16(data[offset:offset+2]) & 0x3FFF)
			if !jumped {
				origOffset = offset + 2
			}
			offset = ptr
			jumped = true
			continue
		}

		offset++
		if offset+length > len(data) {
			return "", 0
		}
		parts = append(parts, string(data[offset:offset+length]))
		offset += length
	}

	return strings.Join(parts, "."), origOffset
}

// encodeDNSName encodes a domain name into wire format.
func encodeDNSName(name string) []byte {
	name = strings.TrimSuffix(name, ".")
	var buf []byte
	for _, label := range strings.Split(name, ".") {
		if len(label) > 63 {
			label = label[:63]
		}
		buf = append(buf, byte(len(label)))
		buf = append(buf, []byte(label)...)
	}
	buf = append(buf, 0) // Root label
	return buf
}

// buildDNSResponse constructs a DNS response with an A record answer.
func buildDNSResponse(id uint16, queryFlags uint16, question []byte, name string, ip net.IP) []byte {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil
	}

	// Response flags: QR=1, AA=1, RD (from query), RA=1, RCODE=0
	respFlags := dnsFlagQR | dnsFlagAA | dnsFlagRA | (queryFlags & dnsFlagRD)

	// Header (12 bytes)
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint16(hdr[0:2], id)
	binary.BigEndian.PutUint16(hdr[2:4], uint16(respFlags))
	binary.BigEndian.PutUint16(hdr[4:6], 1)  // QDCOUNT
	binary.BigEndian.PutUint16(hdr[6:8], 1)  // ANCOUNT
	binary.BigEndian.PutUint16(hdr[8:10], 0) // NSCOUNT
	binary.BigEndian.PutUint16(hdr[10:12], 0) // ARCOUNT

	// Answer section: name (pointer to question), type A, class IN, TTL 60, RDLENGTH 4, RDATA
	answer := make([]byte, 0, 16)
	// Use a compression pointer to the question name at offset 12
	answer = append(answer, 0xC0, 0x0C) // Pointer to offset 12
	// Type A
	answer = append(answer, 0, byte(dnsTypeA))
	// Class IN
	answer = append(answer, 0, byte(dnsClassIN))
	// TTL: 60 seconds
	ttl := make([]byte, 4)
	binary.BigEndian.PutUint32(ttl, 60)
	answer = append(answer, ttl...)
	// RDLENGTH: 4
	answer = append(answer, 0, 4)
	// RDATA: IPv4 address
	answer = append(answer, ip4...)

	// Assemble response
	resp := make([]byte, 0, len(hdr)+len(question)+len(answer))
	resp = append(resp, hdr...)
	resp = append(resp, question...)
	resp = append(resp, answer...)

	return resp
}

// buildDNSNXDomain constructs an NXDOMAIN response.
func buildDNSNXDomain(id uint16, queryFlags uint16, question []byte) []byte {
	respFlags := dnsFlagQR | dnsFlagAA | dnsFlagRA | (queryFlags & dnsFlagRD) | dnsRcodeNX

	hdr := make([]byte, 12)
	binary.BigEndian.PutUint16(hdr[0:2], id)
	binary.BigEndian.PutUint16(hdr[2:4], uint16(respFlags))
	binary.BigEndian.PutUint16(hdr[4:6], 1) // QDCOUNT
	// All other counts 0

	resp := make([]byte, 0, len(hdr)+len(question))
	resp = append(resp, hdr...)
	resp = append(resp, question...)
	return resp
}

// buildDNSServFail constructs a SERVFAIL response.
func buildDNSServFail(id uint16, query []byte) []byte {
	if len(query) < 12 {
		return nil
	}
	resp := make([]byte, len(query))
	copy(resp, query)
	// Set QR=1, RCODE=2
	binary.BigEndian.PutUint16(resp[0:2], id)
	flags := binary.BigEndian.Uint16(query[2:4])
	flags = flags | dnsFlagQR | dnsRcodeFail
	binary.BigEndian.PutUint16(resp[2:4], flags)
	// Zero out answer/authority/additional counts
	binary.BigEndian.PutUint16(resp[6:8], 0)
	binary.BigEndian.PutUint16(resp[8:10], 0)
	binary.BigEndian.PutUint16(resp[10:12], 0)
	return resp
}
