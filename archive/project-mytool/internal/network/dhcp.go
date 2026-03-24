package network

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
)

// DHCP message types
const (
	DHCPDiscover = 1
	DHCPOffer    = 2
	DHCPRequest  = 3
	DHCPDecline  = 4
	DHCPAck      = 5
	DHCPNak      = 6
	DHCPRelease  = 7
	DHCPInform   = 8
)

// DHCP option codes
const (
	OptSubnetMask       = 1
	OptRouter           = 3
	OptDNS              = 6
	OptHostname         = 12
	OptDomainName       = 15
	OptBroadcast        = 28
	OptRequestedIP      = 50
	OptLeaseTime        = 51
	OptMessageType      = 53
	OptServerIdentifier = 54
	OptEnd              = 255
)

// DHCPLease represents an IP address lease
type DHCPLease struct {
	MAC       net.HardwareAddr
	IP        net.IP
	Hostname  string
	ExpiresAt time.Time
	VMName    string
}

// DHCPServer is an embedded DHCP server for the tent bridge network.
// It assigns IP addresses to VMs from a configured range and tracks
// leases so the sandbox manager can look up VM IPs by name.
type DHCPServer struct {
	mu sync.Mutex

	// Network configuration
	serverIP  net.IP
	subnet    *net.IPNet
	gateway   net.IP
	dns       []net.IP
	rangeStart net.IP
	rangeEnd   net.IP
	leaseDur  time.Duration

	// Active leases: MAC address string -> lease
	leases map[string]*DHCPLease

	// VM name -> MAC mapping for hostname-based lookup
	vmMACs map[string]string

	// Next IP to try allocating
	nextIP net.IP

	// Listener
	conn     *net.UDPConn
	iface    string
	stopCh   chan struct{}
	stopped  bool
}

// DHCPServerConfig holds configuration for the embedded DHCP server
type DHCPServerConfig struct {
	// ServerIP is the IP of the bridge interface (DHCP server address)
	ServerIP string
	// Subnet in CIDR notation (e.g., "172.16.0.0/24")
	Subnet string
	// RangeStart is the first IP in the DHCP pool
	RangeStart string
	// RangeEnd is the last IP in the DHCP pool
	RangeEnd string
	// Gateway IP (usually same as ServerIP)
	Gateway string
	// DNS servers to advertise
	DNS []string
	// LeaseDuration for DHCP leases
	LeaseDuration time.Duration
	// Interface to bind to
	Interface string
}

// DefaultDHCPConfig returns a default DHCP configuration for the tent bridge
func DefaultDHCPConfig() *DHCPServerConfig {
	return &DHCPServerConfig{
		ServerIP:      "172.16.0.1",
		Subnet:        "172.16.0.0/24",
		RangeStart:    "172.16.0.100",
		RangeEnd:      "172.16.0.200",
		Gateway:       "172.16.0.1",
		DNS:           []string{"8.8.8.8", "8.8.4.4"},
		LeaseDuration: 24 * time.Hour,
		Interface:     "tent0",
	}
}

// NewDHCPServer creates a new embedded DHCP server
func NewDHCPServer(cfg *DHCPServerConfig) (*DHCPServer, error) {
	if cfg == nil {
		cfg = DefaultDHCPConfig()
	}

	serverIP := net.ParseIP(cfg.ServerIP)
	if serverIP == nil {
		return nil, fmt.Errorf("invalid server IP: %s", cfg.ServerIP)
	}

	_, subnet, err := net.ParseCIDR(cfg.Subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet: %w", err)
	}

	rangeStart := net.ParseIP(cfg.RangeStart)
	if rangeStart == nil {
		return nil, fmt.Errorf("invalid range start: %s", cfg.RangeStart)
	}

	rangeEnd := net.ParseIP(cfg.RangeEnd)
	if rangeEnd == nil {
		return nil, fmt.Errorf("invalid range end: %s", cfg.RangeEnd)
	}

	gateway := net.ParseIP(cfg.Gateway)
	if gateway == nil {
		gateway = serverIP
	}

	var dns []net.IP
	for _, d := range cfg.DNS {
		ip := net.ParseIP(d)
		if ip != nil {
			dns = append(dns, ip)
		}
	}

	leaseDur := cfg.LeaseDuration
	if leaseDur == 0 {
		leaseDur = 24 * time.Hour
	}

	return &DHCPServer{
		serverIP:   serverIP.To4(),
		subnet:     subnet,
		gateway:    gateway.To4(),
		dns:        dns,
		rangeStart: rangeStart.To4(),
		rangeEnd:   rangeEnd.To4(),
		leaseDur:   leaseDur,
		leases:     make(map[string]*DHCPLease),
		vmMACs:     make(map[string]string),
		nextIP:     copyIP(rangeStart.To4()),
		iface:      cfg.Interface,
		stopCh:     make(chan struct{}),
	}, nil
}

// RegisterVM associates a VM name with a MAC address so the DHCP server
// can track which IP belongs to which VM
func (s *DHCPServer) RegisterVM(vmName string, mac net.HardwareAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vmMACs[vmName] = mac.String()
}

// UnregisterVM removes a VM's MAC association and releases its lease
func (s *DHCPServer) UnregisterVM(vmName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	macStr, ok := s.vmMACs[vmName]
	if ok {
		delete(s.leases, macStr)
		delete(s.vmMACs, vmName)
	}
}

// GetVMIP returns the IP address assigned to a VM, if any
func (s *DHCPServer) GetVMIP(vmName string) (net.IP, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	macStr, ok := s.vmMACs[vmName]
	if !ok {
		return nil, fmt.Errorf("VM %s not registered with DHCP server", vmName)
	}
	lease, ok := s.leases[macStr]
	if !ok {
		return nil, fmt.Errorf("no lease found for VM %s", vmName)
	}
	return lease.IP, nil
}

// GetLeases returns a copy of all active leases
func (s *DHCPServer) GetLeases() []*DHCPLease {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var result []*DHCPLease
	for _, lease := range s.leases {
		if lease.ExpiresAt.After(now) {
			lc := *lease
			result = append(result, &lc)
		}
	}
	return result
}

// Start begins listening for DHCP requests on UDP port 67
func (s *DHCPServer) Start() error {
	addr := &net.UDPAddr{
		IP:   net.IPv4zero,
		Port: 67,
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on UDP :67: %w", err)
	}
	s.conn = conn

	go s.serve()
	return nil
}

// Stop shuts down the DHCP server
func (s *DHCPServer) Stop() {
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

// serve is the main DHCP packet processing loop
func (s *DHCPServer) serve() {
	buf := make([]byte, 1500)
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

		if n < 240 {
			continue // Too small to be a valid DHCP packet
		}

		pkt := parseDHCPPacket(buf[:n])
		if pkt == nil {
			continue
		}

		resp := s.handlePacket(pkt)
		if resp == nil {
			continue
		}

		// Send response
		respBytes := resp.marshal()
		dstAddr := &net.UDPAddr{
			IP:   net.IPv4bcast,
			Port: 68,
		}
		if addr != nil && !addr.IP.Equal(net.IPv4zero) {
			dstAddr = addr
		}
		s.conn.WriteToUDP(respBytes, dstAddr)
	}
}

// handlePacket processes a DHCP packet and returns a response
func (s *DHCPServer) handlePacket(pkt *dhcpPacket) *dhcpPacket {
	msgType := pkt.getOption(OptMessageType)
	if len(msgType) == 0 {
		return nil
	}

	switch msgType[0] {
	case DHCPDiscover:
		return s.handleDiscover(pkt)
	case DHCPRequest:
		return s.handleRequest(pkt)
	case DHCPRelease:
		s.handleRelease(pkt)
		return nil
	default:
		return nil
	}
}

// handleDiscover responds to a DHCPDISCOVER with a DHCPOFFER
func (s *DHCPServer) handleDiscover(pkt *dhcpPacket) *dhcpPacket {
	s.mu.Lock()
	defer s.mu.Unlock()

	mac := pkt.clientMAC()
	macStr := mac.String()

	// Check for existing lease
	var offerIP net.IP
	if lease, ok := s.leases[macStr]; ok {
		offerIP = lease.IP
	} else {
		// Allocate new IP
		ip := s.allocateIP()
		if ip == nil {
			return nil // Pool exhausted
		}
		offerIP = ip
	}

	return s.buildResponse(pkt, DHCPOffer, offerIP)
}

// handleRequest responds to a DHCPREQUEST with a DHCPACK or DHCPNAK
func (s *DHCPServer) handleRequest(pkt *dhcpPacket) *dhcpPacket {
	s.mu.Lock()
	defer s.mu.Unlock()

	mac := pkt.clientMAC()
	macStr := mac.String()

	// Check requested IP
	requestedIP := net.IP(pkt.getOption(OptRequestedIP))
	if requestedIP == nil || len(requestedIP) != 4 {
		// Use ciaddr if no requested IP option
		requestedIP = pkt.ciaddr()
	}

	if requestedIP == nil || requestedIP.Equal(net.IPv4zero) {
		return s.buildResponse(pkt, DHCPNak, nil)
	}

	// Verify IP is in our range
	if !s.subnet.Contains(requestedIP) {
		return s.buildResponse(pkt, DHCPNak, nil)
	}

	// Check that the IP is not already leased to a different MAC
	for otherMAC, lease := range s.leases {
		if otherMAC != macStr && lease.IP.Equal(requestedIP) && lease.ExpiresAt.After(time.Now()) {
			return s.buildResponse(pkt, DHCPNak, nil)
		}
	}

	// Create or update lease
	hostname := string(pkt.getOption(OptHostname))
	vmName := s.vmNameForMAC(macStr)

	s.leases[macStr] = &DHCPLease{
		MAC:       mac,
		IP:        copyIP(requestedIP),
		Hostname:  hostname,
		ExpiresAt: time.Now().Add(s.leaseDur),
		VMName:    vmName,
	}

	return s.buildResponse(pkt, DHCPAck, requestedIP)
}

// handleRelease processes a DHCPRELEASE
func (s *DHCPServer) handleRelease(pkt *dhcpPacket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mac := pkt.clientMAC()
	delete(s.leases, mac.String())
}

// allocateIP finds the next available IP in the pool. Must be called with mu held.
func (s *DHCPServer) allocateIP() net.IP {
	start := ipToUint32(s.rangeStart)
	end := ipToUint32(s.rangeEnd)

	// Build set of used IPs
	used := make(map[uint32]bool)
	for _, lease := range s.leases {
		if lease.ExpiresAt.After(time.Now()) {
			used[ipToUint32(lease.IP)] = true
		}
	}

	// Scan from nextIP through end, then wrap around
	cur := ipToUint32(s.nextIP)
	for i := uint32(0); i <= end-start; i++ {
		candidate := start + ((cur - start + i) % (end - start + 1))
		if !used[candidate] {
			ip := uint32ToIP(candidate)
			s.nextIP = uint32ToIP(candidate + 1)
			if ipToUint32(s.nextIP) > end {
				s.nextIP = copyIP(s.rangeStart)
			}
			return ip
		}
	}
	return nil // Pool exhausted
}

// vmNameForMAC returns the VM name for a MAC, if registered. Must be called with mu held.
func (s *DHCPServer) vmNameForMAC(macStr string) string {
	for name, m := range s.vmMACs {
		if m == macStr {
			return name
		}
	}
	return ""
}

// buildResponse constructs a DHCP response packet
func (s *DHCPServer) buildResponse(req *dhcpPacket, msgType byte, offerIP net.IP) *dhcpPacket {
	resp := &dhcpPacket{
		data: make([]byte, 576),
	}

	// Op: BOOTREPLY
	resp.data[0] = 2
	// HType: Ethernet
	resp.data[1] = 1
	// HLen: 6
	resp.data[2] = 6
	// Hops
	resp.data[3] = 0
	// XID - copy from request
	copy(resp.data[4:8], req.data[4:8])
	// Secs
	binary.BigEndian.PutUint16(resp.data[8:10], 0)
	// Flags - copy from request
	copy(resp.data[10:12], req.data[10:12])

	// CIAddr - leave as zero (client-filled field per RFC 2131)

	// YIAddr - offered IP
	if offerIP != nil {
		copy(resp.data[16:20], offerIP.To4())
	}

	// SIAddr - server IP
	copy(resp.data[20:24], s.serverIP)

	// GIAddr - zero
	// CHAddr - copy from request
	copy(resp.data[28:44], req.data[28:44])

	// Magic cookie
	copy(resp.data[236:240], []byte{99, 130, 83, 99})

	// Options
	opts := resp.data[240:]
	idx := 0

	// Message type
	opts[idx] = OptMessageType
	idx++
	opts[idx] = 1
	idx++
	opts[idx] = msgType
	idx++

	// Server identifier
	opts[idx] = OptServerIdentifier
	idx++
	opts[idx] = 4
	idx++
	copy(opts[idx:idx+4], s.serverIP)
	idx += 4

	if offerIP != nil {
		// Lease time
		opts[idx] = OptLeaseTime
		idx++
		opts[idx] = 4
		idx++
		binary.BigEndian.PutUint32(opts[idx:idx+4], uint32(s.leaseDur.Seconds()))
		idx += 4

		// Subnet mask
		mask := s.subnet.Mask
		opts[idx] = OptSubnetMask
		idx++
		opts[idx] = 4
		idx++
		copy(opts[idx:idx+4], mask)
		idx += 4

		// Router (gateway)
		opts[idx] = OptRouter
		idx++
		opts[idx] = 4
		idx++
		copy(opts[idx:idx+4], s.gateway)
		idx += 4

		// DNS
		if len(s.dns) > 0 {
			opts[idx] = OptDNS
			idx++
			opts[idx] = byte(len(s.dns) * 4)
			idx++
			for _, dns := range s.dns {
				copy(opts[idx:idx+4], dns.To4())
				idx += 4
			}
		}
	}

	// End
	opts[idx] = OptEnd
	idx++

	resp.data = resp.data[:240+idx]
	return resp
}

// dhcpPacket wraps a raw DHCP packet
type dhcpPacket struct {
	data []byte
}

// parseDHCPPacket parses raw bytes into a DHCP packet
func parseDHCPPacket(data []byte) *dhcpPacket {
	if len(data) < 240 {
		return nil
	}
	// Verify magic cookie
	if data[236] != 99 || data[237] != 130 || data[238] != 83 || data[239] != 99 {
		return nil
	}
	pkt := &dhcpPacket{data: make([]byte, len(data))}
	copy(pkt.data, data)
	return pkt
}

// clientMAC returns the client hardware address
func (p *dhcpPacket) clientMAC() net.HardwareAddr {
	hlen := int(p.data[2])
	if hlen > 16 {
		hlen = 16
	}
	mac := make(net.HardwareAddr, hlen)
	copy(mac, p.data[28:28+hlen])
	return mac
}

// ciaddr returns the client IP address field
func (p *dhcpPacket) ciaddr() net.IP {
	return net.IP(p.data[12:16]).To4()
}

// getOption returns the value of a DHCP option
func (p *dhcpPacket) getOption(optCode byte) []byte {
	if len(p.data) <= 240 {
		return nil
	}
	opts := p.data[240:]
	for i := 0; i < len(opts); {
		if opts[i] == OptEnd {
			break
		}
		if opts[i] == 0 { // Padding
			i++
			continue
		}
		if i+1 >= len(opts) {
			break
		}
		code := opts[i]
		length := int(opts[i+1])
		i += 2
		if i+length > len(opts) {
			break
		}
		if code == optCode {
			val := make([]byte, length)
			copy(val, opts[i:i+length])
			return val
		}
		i += length
	}
	return nil
}

// marshal serializes the packet to bytes
func (p *dhcpPacket) marshal() []byte {
	return p.data
}

// Helper functions

func copyIP(ip net.IP) net.IP {
	dup := make(net.IP, len(ip))
	copy(dup, ip)
	return dup
}

func ipToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	return binary.BigEndian.Uint32(ip)
}

func uint32ToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, n)
	return ip
}
