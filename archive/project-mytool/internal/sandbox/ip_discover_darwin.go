//go:build darwin
// +build darwin

package vm

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"time"
)

// discoverGuestIP polls the macOS vmnet DHCP lease file to find the IP
// assigned to a recently booted VM. This is needed because VZNATNetworkDeviceAttachment
// uses macOS's built-in vmnet DHCP and there's no synchronous API to get the
// assigned IP — we have to wait for the guest to boot and request a lease.
func discoverGuestIP(vmName string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ip := readDHCPLease(vmName); ip != "" {
			return ip
		}
		if ip := readARPForVmnet(); ip != "" {
			return ip
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Last resort: vmnet NAT typically uses 192.168.64.0/24 with first
	// guest at .2 (gateway is .1)
	return "192.168.64.2"
}

// readDHCPLease parses /var/db/dhcpd_leases for an entry matching vmName or
// returns the most recent lease in the vmnet subnet.
func readDHCPLease(vmName string) string {
	f, err := os.Open("/var/db/dhcpd_leases")
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var currentName, currentIP string
	var bestIP string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "name=") {
			currentName = strings.TrimPrefix(line, "name=")
		} else if strings.HasPrefix(line, "ip_address=") {
			currentIP = strings.TrimPrefix(line, "ip_address=")
		} else if line == "}" {
			if currentIP != "" && strings.HasPrefix(currentIP, "192.168.64.") {
				if currentName == vmName {
					return currentIP
				}
				bestIP = currentIP
			}
			currentName = ""
			currentIP = ""
		}
	}

	return bestIP
}

// readARPForVmnet parses `arp -an` output for entries in the vmnet subnet.
func readARPForVmnet() string {
	out, err := exec.Command("arp", "-an").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		// Format: ? (192.168.64.2) at aa:bb:cc:dd:ee:ff on bridge100 ...
		if strings.Contains(line, "192.168.64.") && !strings.Contains(line, "(incomplete)") {
			start := strings.Index(line, "(")
			end := strings.Index(line, ")")
			if start >= 0 && end > start {
				return line[start+1 : end]
			}
		}
	}
	return ""
}
