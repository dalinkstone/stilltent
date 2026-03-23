//go:build linux
// +build linux

package vm

import "time"

// discoverGuestIP on Linux is a no-op — the embedded DHCP server or
// static IP configuration handles IP assignment and the hypervisor
// backends (KVM, Firecracker) report the IP directly.
func discoverGuestIP(vmName string, timeout time.Duration) string {
	return ""
}
