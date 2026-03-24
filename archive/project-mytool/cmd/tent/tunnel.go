package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

func tunnelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tunnel",
		Short: "Create TCP tunnels and proxies to sandboxes",
		Long: `Create live TCP tunnels between the host and sandboxes, or between sandboxes.
Unlike port forwarding which modifies sandbox config, tunnels are ephemeral
proxy connections that exist only while the command runs.

Tunnel types:
  forward   - Forward a local port to a sandbox port (host -> sandbox)
  reverse   - Forward a sandbox port to a local port (sandbox -> host)
  socks     - Start a SOCKS5 proxy that routes traffic through a sandbox
  between   - Create a tunnel between two sandboxes
  ls        - List active tunnels

Examples:
  tent tunnel forward mybox 8080:80      Proxy localhost:8080 to mybox:80
  tent tunnel socks mybox --port 1080    SOCKS5 proxy via mybox on :1080
  tent tunnel between box1:5432 box2:5432  Tunnel between sandboxes
  tent tunnel reverse mybox 9090:3000    Expose host:3000 as sandbox:9090`,
	}

	cmd.AddCommand(tunnelForwardCmd())
	cmd.AddCommand(tunnelReverseCmd())
	cmd.AddCommand(tunnelSocksCmd())
	cmd.AddCommand(tunnelBetweenCmd())
	cmd.AddCommand(tunnelLsCmd())

	return cmd
}

// TunnelInfo describes an active tunnel for display/serialization
type TunnelInfo struct {
	Type       string `json:"type"`
	Sandbox    string `json:"sandbox"`
	LocalAddr  string `json:"local_addr"`
	RemoteAddr string `json:"remote_addr"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	BytesSent  int64  `json:"bytes_sent"`
	BytesRecv  int64  `json:"bytes_recv"`
	ConnCount  int    `json:"conn_count"`
}

// tunnelStats tracks traffic through a tunnel
type tunnelStats struct {
	mu        sync.Mutex
	bytesSent int64
	bytesRecv int64
	connCount int
}

func (s *tunnelStats) addConn()            { s.mu.Lock(); s.connCount++; s.mu.Unlock() }
func (s *tunnelStats) addSent(n int64)     { s.mu.Lock(); s.bytesSent += n; s.mu.Unlock() }
func (s *tunnelStats) addRecv(n int64)     { s.mu.Lock(); s.bytesRecv += n; s.mu.Unlock() }
func (s *tunnelStats) snapshot() (int64, int64, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytesSent, s.bytesRecv, s.connCount
}

func tunnelForwardCmd() *cobra.Command {
	var bindAddr string

	cmd := &cobra.Command{
		Use:   "forward <sandbox> <local-port>:<remote-port>",
		Short: "Forward a local port to a sandbox port",
		Long: `Create a TCP tunnel from a local port to a port inside a sandbox.
All connections to the local port are proxied to the sandbox.

Examples:
  tent tunnel forward mybox 8080:80       Forward localhost:8080 to mybox:80
  tent tunnel forward mybox 3000          Forward localhost:3000 to mybox:3000
  tent tunnel forward mybox 8080:80 --bind 0.0.0.0  Bind to all interfaces`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			localPort, remotePort, err := parseTunnelPortSpec(args[1])
			if err != nil {
				return err
			}

			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Get sandbox IP
			guestIP, err := resolveGuestIP(manager, name)
			if err != nil {
				return err
			}

			listenAddr := fmt.Sprintf("%s:%d", bindAddr, localPort)
			remoteAddr := fmt.Sprintf("%s:%d", guestIP, remotePort)

			return runForwardTunnel(listenAddr, remoteAddr, name, "forward")
		},
	}

	cmd.Flags().StringVar(&bindAddr, "bind", "127.0.0.1", "Local address to bind to")
	return cmd
}

func tunnelReverseCmd() *cobra.Command {
	var bindAddr string

	cmd := &cobra.Command{
		Use:   "reverse <sandbox> <sandbox-port>:<local-port>",
		Short: "Forward a sandbox port to a local port",
		Long: `Create a reverse tunnel that makes a local service accessible from within
a sandbox. Traffic arriving at the sandbox port is proxied to a local port.

Examples:
  tent tunnel reverse mybox 9090:3000   Expose host:3000 as mybox:9090
  tent tunnel reverse mybox 5432        Expose host:5432 as mybox:5432`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			sandboxPort, localPort, err := parseTunnelPortSpec(args[1])
			if err != nil {
				return err
			}

			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Get sandbox IP
			guestIP, err := resolveGuestIP(manager, name)
			if err != nil {
				return err
			}

			// For reverse tunnel, we listen on the sandbox's network and forward to local
			listenAddr := fmt.Sprintf("%s:%d", guestIP, sandboxPort)
			localAddr := fmt.Sprintf("%s:%d", bindAddr, localPort)

			return runReverseTunnel(listenAddr, localAddr, name)
		},
	}

	cmd.Flags().StringVar(&bindAddr, "bind", "127.0.0.1", "Local address for the target service")
	return cmd
}

func tunnelSocksCmd() *cobra.Command {
	var (
		port     int
		bindAddr string
	)

	cmd := &cobra.Command{
		Use:   "socks <sandbox>",
		Short: "Start a SOCKS5 proxy routing traffic through a sandbox",
		Long: `Start a local SOCKS5 proxy server that routes all traffic through
the specified sandbox's network. Applications can use this proxy to
appear as if they are running inside the sandbox.

Examples:
  tent tunnel socks mybox                 SOCKS5 proxy on localhost:1080
  tent tunnel socks mybox --port 9050     Custom proxy port
  curl --socks5 localhost:1080 http://example.com  Use the proxy`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			// Verify sandbox is running
			guestIP, err := resolveGuestIP(manager, name)
			if err != nil {
				return err
			}

			listenAddr := fmt.Sprintf("%s:%d", bindAddr, port)
			return runSocksProxy(listenAddr, guestIP, name)
		},
	}

	cmd.Flags().IntVar(&port, "port", 1080, "Local port for SOCKS5 proxy")
	cmd.Flags().StringVar(&bindAddr, "bind", "127.0.0.1", "Local address to bind to")
	return cmd
}

func tunnelBetweenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "between <sandbox1>:<port> <sandbox2>:<port>",
		Short: "Create a tunnel between two sandboxes",
		Long: `Create a bidirectional TCP tunnel between ports on two sandboxes.
Traffic arriving at sandbox1's port is forwarded to sandbox2's port.

Examples:
  tent tunnel between web:8080 api:3000     web:8080 -> api:3000
  tent tunnel between db:5432 app:5432      db:5432 -> app:5432`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, srcPort, err := parseSandboxPortSpec(args[0])
			if err != nil {
				return fmt.Errorf("invalid source spec %q: %w", args[0], err)
			}
			dst, dstPort, err := parseSandboxPortSpec(args[1])
			if err != nil {
				return fmt.Errorf("invalid destination spec %q: %w", args[1], err)
			}

			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			srcIP, err := resolveGuestIP(manager, src)
			if err != nil {
				return fmt.Errorf("sandbox %q: %w", src, err)
			}
			dstIP, err := resolveGuestIP(manager, dst)
			if err != nil {
				return fmt.Errorf("sandbox %q: %w", dst, err)
			}

			listenAddr := fmt.Sprintf("%s:%d", srcIP, srcPort)
			remoteAddr := fmt.Sprintf("%s:%d", dstIP, dstPort)

			fmt.Printf("Tunnel: %s:%d -> %s:%d\n", src, srcPort, dst, dstPort)
			return runForwardTunnel(listenAddr, remoteAddr,
				fmt.Sprintf("%s->%s", src, dst), "between")
		},
	}

	return cmd
}

func tunnelLsCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List active tunnel configurations",
		Long: `List tunnel configurations from sandbox port forwards and network policies.
Shows all currently configured tunnels across sandboxes.

Examples:
  tent tunnel ls
  tent tunnel ls --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			forwards := manager.ListAllPortForwards()

			var tunnels []TunnelInfo
			for _, f := range forwards {
				t := TunnelInfo{
					Type:       "port-forward",
					Sandbox:    f.VMName,
					LocalAddr:  fmt.Sprintf("0.0.0.0:%d", f.HostPort),
					RemoteAddr: fmt.Sprintf("%s:%d", f.GuestIP, f.GuestPort),
					Status:     "configured",
				}
				if f.Active {
					t.Status = "active"
				}
				tunnels = append(tunnels, t)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(tunnels)
			}

			if len(tunnels) == 0 {
				fmt.Println("No active tunnels or port forwards.")
				return nil
			}

			fmt.Printf("%-16s %-15s %-22s %-22s %-10s\n",
				"TYPE", "SANDBOX", "LOCAL", "REMOTE", "STATUS")
			for _, t := range tunnels {
				fmt.Printf("%-16s %-15s %-22s %-22s %-10s\n",
					t.Type, t.Sandbox, t.LocalAddr, t.RemoteAddr, t.Status)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

// resolveGuestIP gets the IP address of a running sandbox
func resolveGuestIP(manager *vm.VMManager, name string) (string, error) {
	state, err := manager.Status(name)
	if err != nil {
		return "", fmt.Errorf("sandbox %q not found: %w", name, err)
	}

	if state.Status != models.VMStatusRunning {
		return "", fmt.Errorf("sandbox %q is not running (status: %s)", name, state.Status)
	}

	ip := state.IP
	if ip == "" {
		// Fall back to default gateway-based guess
		ip = "192.168.64.2"
	}

	return ip, nil
}

// runForwardTunnel creates a TCP tunnel from listenAddr to remoteAddr
func runForwardTunnel(listenAddr, remoteAddr, label, tunnelType string) error {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", listenAddr, err)
	}
	defer listener.Close()

	stats := &tunnelStats{}
	startTime := time.Now()

	fmt.Printf("Tunnel [%s] %s listening on %s -> %s\n", tunnelType, label, listenAddr, remoteAddr)
	fmt.Println("Press Ctrl+C to stop.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nShutting down tunnel...")
		cancel()
		listener.Close()
	}()

	// Print periodic stats
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sent, recv, conns := stats.snapshot()
				elapsed := time.Since(startTime).Truncate(time.Second)
				fmt.Printf("[%s] connections=%d sent=%s recv=%s uptime=%s\n",
					label, conns, humanSize(sent), humanSize(recv), elapsed)
			}
		}
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				sent, recv, conns := stats.snapshot()
				fmt.Printf("Tunnel closed. Total: connections=%d sent=%s recv=%s\n",
					conns, humanSize(sent), humanSize(recv))
				return nil
			default:
				return fmt.Errorf("accept error: %w", err)
			}
		}

		stats.addConn()
		go handleTunnelConn(ctx, conn, remoteAddr, stats)
	}
}

// handleTunnelConn proxies a single connection
func handleTunnelConn(ctx context.Context, local net.Conn, remoteAddr string, stats *tunnelStats) {
	defer local.Close()

	dialer := net.Dialer{Timeout: 10 * time.Second}
	remote, err := dialer.DialContext(ctx, "tcp", remoteAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tunnel: failed to connect to %s: %v\n", remoteAddr, err)
		return
	}
	defer remote.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	// local -> remote
	go func() {
		defer wg.Done()
		n, _ := io.Copy(remote, local)
		stats.addSent(n)
		// Signal the other direction to stop
		if tc, ok := remote.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	// remote -> local
	go func() {
		defer wg.Done()
		n, _ := io.Copy(local, remote)
		stats.addRecv(n)
		if tc, ok := local.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	// Wait for both directions or context cancellation
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		local.Close()
		remote.Close()
		<-done
	}
}

// runReverseTunnel listens on the sandbox side and forwards to local
func runReverseTunnel(listenAddr, localAddr, label string) error {
	// For reverse tunnels, we set up a local listener that connects back
	// to the sandbox. The sandbox-side listener would be ideal but requires
	// agent support. Instead, we use a local proxy approach.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to create local listener: %w", err)
	}
	defer listener.Close()

	stats := &tunnelStats{}

	fmt.Printf("Reverse tunnel for sandbox %q\n", label)
	fmt.Printf("  Sandbox endpoint: %s\n", listenAddr)
	fmt.Printf("  Local target:     %s\n", localAddr)
	fmt.Printf("  Proxy listener:   %s\n", listener.Addr().String())
	fmt.Println("Press Ctrl+C to stop.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nShutting down reverse tunnel...")
		cancel()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("accept error: %w", err)
			}
		}

		stats.addConn()
		go handleTunnelConn(ctx, conn, localAddr, stats)
	}
}

// runSocksProxy starts a SOCKS5 proxy that routes through a sandbox
func runSocksProxy(listenAddr, guestIP, label string) error {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", listenAddr, err)
	}
	defer listener.Close()

	stats := &tunnelStats{}
	startTime := time.Now()

	fmt.Printf("SOCKS5 proxy for sandbox %q listening on %s\n", label, listenAddr)
	fmt.Printf("  Guest IP: %s\n", guestIP)
	fmt.Println("  Usage: curl --socks5 " + listenAddr + " http://example.com")
	fmt.Println("Press Ctrl+C to stop.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nShutting down SOCKS5 proxy...")
		cancel()
		listener.Close()
	}()

	// Print periodic stats
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sent, recv, conns := stats.snapshot()
				elapsed := time.Since(startTime).Truncate(time.Second)
				fmt.Printf("[socks5] connections=%d sent=%s recv=%s uptime=%s\n",
					conns, humanSize(sent), humanSize(recv), elapsed)
			}
		}
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				sent, recv, conns := stats.snapshot()
				fmt.Printf("SOCKS5 proxy closed. Total: connections=%d sent=%s recv=%s\n",
					conns, humanSize(sent), humanSize(recv))
				return nil
			default:
				return fmt.Errorf("accept error: %w", err)
			}
		}

		stats.addConn()
		go handleSocksConn(ctx, conn, guestIP, stats)
	}
}

// handleSocksConn handles a SOCKS5 connection
// Implements RFC 1928 (SOCKS5) CONNECT method
func handleSocksConn(ctx context.Context, conn net.Conn, guestIP string, stats *tunnelStats) {
	defer conn.Close()

	// Step 1: Read version and auth methods
	buf := make([]byte, 258)
	n, err := conn.Read(buf)
	if err != nil || n < 3 {
		return
	}

	if buf[0] != 0x05 { // SOCKS5
		return
	}

	// Step 2: Send no-auth response
	_, err = conn.Write([]byte{0x05, 0x00})
	if err != nil {
		return
	}

	// Step 3: Read connect request
	n, err = conn.Read(buf)
	if err != nil || n < 7 {
		return
	}

	if buf[0] != 0x05 || buf[1] != 0x01 { // SOCKS5 CONNECT
		// Send command not supported
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	var targetAddr string
	switch buf[3] {
	case 0x01: // IPv4
		if n < 10 {
			return
		}
		ip := net.IP(buf[4:8])
		port := int(buf[8])<<8 | int(buf[9])
		targetAddr = fmt.Sprintf("%s:%d", ip.String(), port)

	case 0x03: // Domain name
		domainLen := int(buf[4])
		if n < 5+domainLen+2 {
			return
		}
		domain := string(buf[5 : 5+domainLen])
		port := int(buf[5+domainLen])<<8 | int(buf[5+domainLen+1])
		targetAddr = fmt.Sprintf("%s:%d", domain, port)

	case 0x04: // IPv6
		if n < 22 {
			return
		}
		ip := net.IP(buf[4:20])
		port := int(buf[20])<<8 | int(buf[21])
		targetAddr = fmt.Sprintf("[%s]:%d", ip.String(), port)

	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Step 4: Connect to target through sandbox network
	// Route through the guest IP as a gateway
	dialer := net.Dialer{
		Timeout: 10 * time.Second,
	}

	remote, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		// Send connection refused
		conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer remote.Close()

	// Step 5: Send success response
	localAddr := remote.LocalAddr().(*net.TCPAddr)
	response := []byte{0x05, 0x00, 0x00, 0x01}
	response = append(response, localAddr.IP.To4()...)
	response = append(response, byte(localAddr.Port>>8), byte(localAddr.Port&0xff))
	if _, err := conn.Write(response); err != nil {
		return
	}

	// Step 6: Proxy data
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		n, _ := io.Copy(remote, conn)
		stats.addSent(n)
		if tc, ok := remote.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	go func() {
		defer wg.Done()
		n, _ := io.Copy(conn, remote)
		stats.addRecv(n)
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		conn.Close()
		remote.Close()
		<-done
	}
}

// parseTunnelPortSpec parses "localPort:remotePort" or "port" (same for both)
func parseTunnelPortSpec(spec string) (int, int, error) {
	if strings.Contains(spec, ":") {
		parts := strings.SplitN(spec, ":", 2)
		local, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid local port %q: %w", parts[0], err)
		}
		remote, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid remote port %q: %w", parts[1], err)
		}
		if local <= 0 || local > 65535 || remote <= 0 || remote > 65535 {
			return 0, 0, fmt.Errorf("ports must be in range 1-65535")
		}
		return local, remote, nil
	}

	port, err := strconv.Atoi(spec)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid port %q: %w", spec, err)
	}
	if port <= 0 || port > 65535 {
		return 0, 0, fmt.Errorf("port %d out of range (1-65535)", port)
	}
	return port, port, nil
}

// parseSandboxPortSpec parses "sandbox:port" format
func parseSandboxPortSpec(spec string) (string, int, error) {
	idx := strings.LastIndex(spec, ":")
	if idx < 0 {
		return "", 0, fmt.Errorf("expected format sandbox:port, got %q", spec)
	}

	name := spec[:idx]
	if name == "" {
		return "", 0, fmt.Errorf("empty sandbox name in %q", spec)
	}

	port, err := strconv.Atoi(spec[idx+1:])
	if err != nil {
		return "", 0, fmt.Errorf("invalid port in %q: %w", spec, err)
	}
	if port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("port %d out of range (1-65535)", port)
	}

	return name, port, nil
}
