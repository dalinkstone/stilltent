// Package network provides cross-platform networking for microVMs
package network

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/dalinkstone/tent/pkg/models"
)

// PortForwarder manages TCP port forwarding from host to guest VMs.
// It runs entirely in userspace using pure Go, so it works on both macOS and Linux
// without requiring root privileges or platform-specific firewall rules.
type PortForwarder struct {
	mu        sync.Mutex
	rules     map[string][]*forwardRule // vmName -> rules
	listeners map[string][]net.Listener // vmName -> listeners
}

// forwardRule represents a single host->guest port forwarding rule
type forwardRule struct {
	HostPort  int
	GuestPort int
	GuestIP   string
	VMName    string
}

// ForwardStatus represents the status of a port forwarding rule
type ForwardStatus struct {
	VMName    string `json:"vm_name"`
	HostPort  int    `json:"host_port"`
	GuestPort int    `json:"guest_port"`
	GuestIP   string `json:"guest_ip"`
	Active    bool   `json:"active"`
}

// NewPortForwarder creates a new port forwarder instance
func NewPortForwarder() *PortForwarder {
	return &PortForwarder{
		rules:     make(map[string][]*forwardRule),
		listeners: make(map[string][]net.Listener),
	}
}

// SetupForwards configures port forwarding rules for a VM based on its config.
// Call ActivateForwards after the VM has an IP to start actually forwarding.
func (pf *PortForwarder) SetupForwards(vmName string, ports []models.PortForward) error {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	if len(ports) == 0 {
		return nil
	}

	// Validate no port conflicts
	for _, p := range ports {
		if p.Host <= 0 || p.Host > 65535 {
			return fmt.Errorf("invalid host port %d", p.Host)
		}
		if p.Guest <= 0 || p.Guest > 65535 {
			return fmt.Errorf("invalid guest port %d", p.Guest)
		}
		if err := pf.checkPortConflict(vmName, p.Host); err != nil {
			return err
		}
	}

	rules := make([]*forwardRule, len(ports))
	for i, p := range ports {
		rules[i] = &forwardRule{
			HostPort:  p.Host,
			GuestPort: p.Guest,
			VMName:    vmName,
		}
	}

	pf.rules[vmName] = rules
	return nil
}

// ActivateForwards starts listening on host ports and forwarding to the guest IP.
// Should be called after the VM is running and has been assigned an IP.
func (pf *PortForwarder) ActivateForwards(vmName string, guestIP string) error {
	pf.mu.Lock()
	rules, ok := pf.rules[vmName]
	if !ok || len(rules) == 0 {
		pf.mu.Unlock()
		return nil // No rules configured
	}

	// Set guest IP on all rules
	for _, r := range rules {
		r.GuestIP = guestIP
	}
	pf.mu.Unlock()

	var listeners []net.Listener
	for _, rule := range rules {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", rule.HostPort))
		if err != nil {
			// Cleanup any listeners we already started
			for _, l := range listeners {
				l.Close()
			}
			return fmt.Errorf("failed to listen on host port %d: %w", rule.HostPort, err)
		}
		listeners = append(listeners, ln)

		// Start accepting connections in background
		go pf.acceptLoop(ln, rule)
	}

	pf.mu.Lock()
	pf.listeners[vmName] = listeners
	pf.mu.Unlock()

	return nil
}

// RemoveForwards stops all port forwarding for a VM and cleans up resources
func (pf *PortForwarder) RemoveForwards(vmName string) error {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	// Close all listeners
	if listeners, ok := pf.listeners[vmName]; ok {
		for _, ln := range listeners {
			ln.Close()
		}
		delete(pf.listeners, vmName)
	}

	delete(pf.rules, vmName)
	return nil
}

// ListForwards returns the status of all forwarding rules for a VM
func (pf *PortForwarder) ListForwards(vmName string) []ForwardStatus {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	rules, ok := pf.rules[vmName]
	if !ok {
		return nil
	}

	_, hasListeners := pf.listeners[vmName]

	statuses := make([]ForwardStatus, len(rules))
	for i, r := range rules {
		statuses[i] = ForwardStatus{
			VMName:    r.VMName,
			HostPort:  r.HostPort,
			GuestPort: r.GuestPort,
			GuestIP:   r.GuestIP,
			Active:    hasListeners && r.GuestIP != "",
		}
	}
	return statuses
}

// ListAllForwards returns the status of all forwarding rules across all VMs
func (pf *PortForwarder) ListAllForwards() []ForwardStatus {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	var all []ForwardStatus
	for vmName, rules := range pf.rules {
		_, hasListeners := pf.listeners[vmName]
		for _, r := range rules {
			all = append(all, ForwardStatus{
				VMName:    r.VMName,
				HostPort:  r.HostPort,
				GuestPort: r.GuestPort,
				GuestIP:   r.GuestIP,
				Active:    hasListeners && r.GuestIP != "",
			})
		}
	}
	return all
}

// checkPortConflict checks if a host port is already in use by another VM's forwarding
func (pf *PortForwarder) checkPortConflict(vmName string, hostPort int) error {
	for name, rules := range pf.rules {
		if name == vmName {
			continue
		}
		for _, r := range rules {
			if r.HostPort == hostPort {
				return fmt.Errorf("host port %d already forwarded by VM %s", hostPort, name)
			}
		}
	}
	return nil
}

// acceptLoop accepts incoming connections and forwards them to the guest
func (pf *PortForwarder) acceptLoop(ln net.Listener, rule *forwardRule) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // Listener closed
		}
		go pf.handleConnection(conn, rule)
	}
}

// handleConnection proxies a single TCP connection from host to guest
func (pf *PortForwarder) handleConnection(hostConn net.Conn, rule *forwardRule) {
	defer hostConn.Close()

	guestAddr := net.JoinHostPort(rule.GuestIP, strconv.Itoa(rule.GuestPort))

	// Connect to guest with timeout
	guestConn, err := net.DialTimeout("tcp", guestAddr, 5*time.Second)
	if err != nil {
		return // Guest not reachable
	}
	defer guestConn.Close()

	// Bidirectional copy
	done := make(chan struct{}, 2)

	go func() {
		io.Copy(guestConn, hostConn)
		done <- struct{}{}
	}()

	go func() {
		io.Copy(hostConn, guestConn)
		done <- struct{}{}
	}()

	// Wait for either direction to finish
	<-done
}
