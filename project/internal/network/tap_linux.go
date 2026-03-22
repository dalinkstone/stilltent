//go:build linux
// +build linux

// tap_linux.go provides native Go TAP device management using ioctl syscalls
// and NAT/masquerade setup for sandbox internet connectivity on Linux.
//
// Instead of shelling out to `ip` commands, this uses the Linux tun/tap ioctl
// interface (/dev/net/tun) to create TAP devices, and netlink-style ioctls
// to configure them. It also sets up IP forwarding and iptables MASQUERADE
// rules so sandboxes can reach allowed external endpoints.

package network

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	tunDevice    = "/dev/net/tun"
	ifNameSize   = 16
	defaultBridge = "tent0"
	bridgeSubnet  = "172.16.0.0/24"
	bridgeIP      = "172.16.0.1"
	bridgeCIDR    = "172.16.0.1/24"
)

// ifreq is the Linux ifreq structure used in network ioctl calls.
type ifreq struct {
	name  [ifNameSize]byte
	flags uint16
	_pad  [22]byte // padding to match sizeof(struct ifreq)
}

// TAPManager manages TAP device lifecycle using native Linux syscalls.
type TAPManager struct {
	mu      sync.Mutex
	devices map[string]*TAPDevice
}

// TAPDevice represents an open TAP device with its file descriptor.
type TAPDevice struct {
	Name string
	File *os.File
	fd   int
}

// NewTAPManager creates a new TAP device manager.
func NewTAPManager() *TAPManager {
	return &TAPManager{
		devices: make(map[string]*TAPDevice),
	}
}

// CreateTAP creates a new TAP device using the ioctl interface.
// The device name is "tap-<vmName>". Returns the opened TAP device.
func (tm *TAPManager) CreateTAP(vmName string) (*TAPDevice, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tapName := fmt.Sprintf("tap-%s", vmName)

	// Truncate name if too long for the kernel interface
	if len(tapName) >= ifNameSize {
		tapName = tapName[:ifNameSize-1]
	}

	// Check if already managed
	if dev, exists := tm.devices[tapName]; exists {
		return dev, nil
	}

	// Open /dev/net/tun
	f, err := os.OpenFile(tunDevice, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", tunDevice, err)
	}

	// Prepare ioctl request: IFF_TAP | IFF_NO_PI
	var req ifreq
	copy(req.name[:], tapName)
	req.flags = unix.IFF_TAP | unix.IFF_NO_PI

	// TUNSETIFF ioctl to create the TAP device
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		f.Fd(),
		unix.TUNSETIFF,
		uintptr(unsafe.Pointer(&req)),
	)
	if errno != 0 {
		f.Close()
		return nil, fmt.Errorf("TUNSETIFF failed for %s: %w", tapName, errno)
	}

	// Set device persistent so it survives fd close if needed
	_, _, errno = unix.Syscall(
		unix.SYS_IOCTL,
		f.Fd(),
		unix.TUNSETPERSIST,
		1,
	)
	if errno != 0 {
		f.Close()
		return nil, fmt.Errorf("TUNSETPERSIST failed for %s: %w", tapName, errno)
	}

	// Bring the TAP device up using a raw socket + ioctl
	if err := setInterfaceUp(tapName); err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to bring up %s: %w", tapName, err)
	}

	dev := &TAPDevice{
		Name: tapName,
		File: f,
		fd:   int(f.Fd()),
	}
	tm.devices[tapName] = dev
	return dev, nil
}

// DestroyTAP closes and removes a TAP device.
func (tm *TAPManager) DestroyTAP(vmName string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tapName := fmt.Sprintf("tap-%s", vmName)
	if len(tapName) >= ifNameSize {
		tapName = tapName[:ifNameSize-1]
	}

	dev, exists := tm.devices[tapName]
	if exists {
		// Clear persistent flag before closing
		unix.Syscall(
			unix.SYS_IOCTL,
			dev.File.Fd(),
			unix.TUNSETPERSIST,
			0,
		)
		dev.File.Close()
		delete(tm.devices, tapName)
	}

	// Also try to bring down and delete via netlink as a fallback
	setInterfaceDown(tapName)
	return nil
}

// GetTAP returns a managed TAP device by VM name.
func (tm *TAPManager) GetTAP(vmName string) (*TAPDevice, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tapName := fmt.Sprintf("tap-%s", vmName)
	dev, ok := tm.devices[tapName]
	return dev, ok
}

// ListTAPs returns all managed TAP device names.
func (tm *TAPManager) ListTAPs() []string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	names := make([]string, 0, len(tm.devices))
	for name := range tm.devices {
		names = append(names, name)
	}
	return names
}

// Close shuts down all managed TAP devices.
func (tm *TAPManager) Close() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for name, dev := range tm.devices {
		unix.Syscall(unix.SYS_IOCTL, dev.File.Fd(), unix.TUNSETPERSIST, 0)
		dev.File.Close()
		setInterfaceDown(name)
		delete(tm.devices, name)
	}
}

// setInterfaceUp brings a network interface up using ioctl on a raw socket.
func setInterfaceUp(name string) error {
	sock, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("failed to create socket: %w", err)
	}
	defer unix.Close(sock)

	var ifr [40]byte // sizeof(struct ifreq) on most platforms
	copy(ifr[:ifNameSize], name)

	// SIOCGIFFLAGS - get current flags
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(sock),
		unix.SIOCGIFFLAGS,
		uintptr(unsafe.Pointer(&ifr[0])),
	)
	if errno != 0 {
		return fmt.Errorf("SIOCGIFFLAGS failed: %w", errno)
	}

	// Set IFF_UP | IFF_RUNNING
	flags := *(*uint16)(unsafe.Pointer(&ifr[ifNameSize]))
	flags |= unix.IFF_UP | unix.IFF_RUNNING
	*(*uint16)(unsafe.Pointer(&ifr[ifNameSize])) = flags

	// SIOCSIFFLAGS - set flags
	_, _, errno = unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(sock),
		unix.SIOCSIFFLAGS,
		uintptr(unsafe.Pointer(&ifr[0])),
	)
	if errno != 0 {
		return fmt.Errorf("SIOCSIFFLAGS failed: %w", errno)
	}

	return nil
}

// setInterfaceDown brings a network interface down.
func setInterfaceDown(name string) {
	sock, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return
	}
	defer unix.Close(sock)

	var ifr [40]byte
	copy(ifr[:ifNameSize], name)

	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(sock),
		unix.SIOCGIFFLAGS,
		uintptr(unsafe.Pointer(&ifr[0])),
	)
	if errno != 0 {
		return
	}

	flags := *(*uint16)(unsafe.Pointer(&ifr[ifNameSize]))
	flags &^= unix.IFF_UP
	*(*uint16)(unsafe.Pointer(&ifr[ifNameSize])) = flags

	unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(sock),
		unix.SIOCSIFFLAGS,
		uintptr(unsafe.Pointer(&ifr[0])),
	)
}

// AddToBridge adds a TAP device to a bridge interface.
func AddToBridge(bridgeName, tapName string) error {
	// Get the TAP interface index
	iface, err := net.InterfaceByName(tapName)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", tapName, err)
	}

	sock, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("failed to create socket: %w", err)
	}
	defer unix.Close(sock)

	// SIOCBRADDIF - add interface to bridge
	var ifr [40]byte
	copy(ifr[:ifNameSize], bridgeName)
	*(*int32)(unsafe.Pointer(&ifr[ifNameSize])) = int32(iface.Index)

	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(sock),
		unix.SIOCBRADDIF,
		uintptr(unsafe.Pointer(&ifr[0])),
	)
	if errno != 0 {
		return fmt.Errorf("SIOCBRADDIF failed: %w", errno)
	}

	return nil
}

// NATManager handles IP forwarding and masquerade rules for sandbox networking.
type NATManager struct {
	mu         sync.Mutex
	initialized bool
	outIface    string // detected outbound interface
}

// NewNATManager creates a new NAT manager.
func NewNATManager() *NATManager {
	return &NATManager{}
}

// Setup enables IP forwarding and configures NAT/masquerade for the bridge subnet.
// This allows sandboxes to reach the internet (subject to egress firewall rules).
func (nm *NATManager) Setup() error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if nm.initialized {
		return nil
	}

	// Enable IP forwarding
	if err := enableIPForwarding(); err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %w", err)
	}

	// Detect the default outbound interface
	outIface, err := detectDefaultInterface()
	if err != nil {
		return fmt.Errorf("failed to detect default interface: %w", err)
	}
	nm.outIface = outIface

	// Set up MASQUERADE rule for the bridge subnet
	if err := nm.addMasqueradeRule(); err != nil {
		return fmt.Errorf("failed to add masquerade rule: %w", err)
	}

	// Allow forwarding from bridge to outbound interface
	if err := nm.addForwardingRules(); err != nil {
		return fmt.Errorf("failed to add forwarding rules: %w", err)
	}

	nm.initialized = true
	return nil
}

// Teardown removes NAT rules. IP forwarding is left enabled as other
// services may depend on it.
func (nm *NATManager) Teardown() error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if !nm.initialized {
		return nil
	}

	// Remove masquerade rule
	exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING",
		"-s", bridgeSubnet, "!", "-o", defaultBridge,
		"-j", "MASQUERADE").Run()

	// Remove forwarding rules
	exec.Command("iptables", "-D", "FORWARD",
		"-i", defaultBridge, "-o", nm.outIface,
		"-j", "ACCEPT").Run()
	exec.Command("iptables", "-D", "FORWARD",
		"-i", nm.outIface, "-o", defaultBridge,
		"-m", "state", "--state", "RELATED,ESTABLISHED",
		"-j", "ACCEPT").Run()

	nm.initialized = false
	return nil
}

// enableIPForwarding writes to /proc/sys/net/ipv4/ip_forward.
func enableIPForwarding() error {
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644)
}

// IsIPForwardingEnabled checks if IPv4 forwarding is active.
func IsIPForwardingEnabled() (bool, error) {
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(data)) == "1", nil
}

// detectDefaultInterface finds the interface used for the default route.
func detectDefaultInterface() (string, error) {
	// Read /proc/net/route to find the default route
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", fmt.Errorf("failed to read /proc/net/route: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] { // Skip header
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// Default route has destination 00000000
		if fields[1] == "00000000" {
			return fields[0], nil
		}
	}

	// Fallback: try common interface names
	for _, name := range []string{"eth0", "ens33", "enp0s3", "wlan0"} {
		if _, err := net.InterfaceByName(name); err == nil {
			return name, nil
		}
	}

	return "", fmt.Errorf("no default route found")
}

// addMasqueradeRule adds the iptables NAT masquerade rule.
func (nm *NATManager) addMasqueradeRule() error {
	// Check if rule already exists
	check := exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING",
		"-s", bridgeSubnet, "!", "-o", defaultBridge,
		"-j", "MASQUERADE")
	if check.Run() == nil {
		return nil // Rule already exists
	}

	// Add the rule
	cmd := exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING",
		"-s", bridgeSubnet, "!", "-o", defaultBridge,
		"-j", "MASQUERADE")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("iptables masquerade failed: %s: %w", string(output), err)
	}
	return nil
}

// addForwardingRules adds iptables FORWARD rules for bridge<->outbound traffic.
func (nm *NATManager) addForwardingRules() error {
	// Allow outbound from bridge
	check := exec.Command("iptables", "-C", "FORWARD",
		"-i", defaultBridge, "-o", nm.outIface,
		"-j", "ACCEPT")
	if check.Run() != nil {
		cmd := exec.Command("iptables", "-A", "FORWARD",
			"-i", defaultBridge, "-o", nm.outIface,
			"-j", "ACCEPT")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("iptables forward out failed: %s: %w", string(output), err)
		}
	}

	// Allow return traffic
	check = exec.Command("iptables", "-C", "FORWARD",
		"-i", nm.outIface, "-o", defaultBridge,
		"-m", "state", "--state", "RELATED,ESTABLISHED",
		"-j", "ACCEPT")
	if check.Run() != nil {
		cmd := exec.Command("iptables", "-A", "FORWARD",
			"-i", nm.outIface, "-o", defaultBridge,
			"-m", "state", "--state", "RELATED,ESTABLISHED",
			"-j", "ACCEPT")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("iptables forward in failed: %s: %w", string(output), err)
		}
	}

	return nil
}

// DetectedInterface returns the outbound interface name detected during setup.
func (nm *NATManager) DetectedInterface() string {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	return nm.outIface
}

// IsInitialized reports whether NAT has been set up.
func (nm *NATManager) IsInitialized() bool {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	return nm.initialized
}
