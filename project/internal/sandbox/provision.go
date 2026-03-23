// Package vm provides rootfs provisioning for container-based VM images.
// OCI container images lack the pieces needed for VM boot: an init process
// and DHCP networking. This file injects a lightweight init script and
// networking config directly into the virtiofs rootfs directory so the guest
// can boot, get an IP, and run the tent-agent for host communication.
package vm

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// provisionRootfs prepares a container rootfs directory for VM boot.
// It writes a tent-init script as /sbin/init, ensures basic networking,
// copies the tent-agent binary into the guest, and optionally injects SSH
// keys when sshPubKeyPath is non-empty.
// The rootfsDir is the extracted OCI layer directory (e.g. <image>_rootfs).
func provisionRootfs(rootfsDir string, sshPubKeyPath string) error {
	if _, err := os.Stat(rootfsDir); err != nil {
		return fmt.Errorf("rootfs directory not found: %w", err)
	}

	// Read the SSH public key for injection (only when SSH is enabled)
	var sshPubKey string
	if sshPubKeyPath != "" {
		data, err := os.ReadFile(sshPubKeyPath)
		if err != nil {
			return fmt.Errorf("failed to read SSH public key: %w", err)
		}
		sshPubKey = strings.TrimSpace(string(data))
	}

	// Write the init script — this is PID 1 inside the guest
	if err := writeInitScript(rootfsDir, sshPubKey != ""); err != nil {
		return fmt.Errorf("failed to write init script: %w", err)
	}

	// Inject SSH authorized keys for root (only when SSH is enabled)
	if sshPubKey != "" {
		if err := injectSSHKeys(rootfsDir, sshPubKey); err != nil {
			return fmt.Errorf("failed to inject SSH keys: %w", err)
		}
	}

	// Ensure essential directories exist
	dirs := []string{
		"proc", "sys", "dev", "dev/pts", "run", "tmp", "var/run",
		"usr/local/bin",
	}
	if sshPubKey != "" {
		dirs = append(dirs, "etc/ssh", "root/.ssh")
	}
	for _, dir := range dirs {
		os.MkdirAll(filepath.Join(rootfsDir, dir), 0755)
	}

	// Write DHCP networking config
	if err := writeNetworkConfig(rootfsDir); err != nil {
		return fmt.Errorf("failed to write network config: %w", err)
	}

	// Write sshd config only when SSH is enabled
	if sshPubKey != "" {
		if err := writeSSHDConfig(rootfsDir); err != nil {
			return fmt.Errorf("failed to write sshd config: %w", err)
		}
	}

	// Copy the tent-agent binary into the guest rootfs
	if err := copyTentAgent(rootfsDir); err != nil {
		// Non-fatal — agent binary might not be available during development
		log.Printf("warning: failed to copy tent-agent into rootfs: %v", err)
	}

	return nil
}

// writeInitScript creates /sbin/tent-init — a shell script that acts as PID 1.
// It mounts virtual filesystems, brings up networking, starts the tent-agent
// for vsock-based host communication, and optionally starts sshd.
// This replaces the need for systemd or cloud-init.
func writeInitScript(rootfsDir string, enableSSH bool) error {
	initScript := `#!/bin/sh
# tent-init: lightweight init for container-based VMs
# This script runs as PID 1 inside the guest.

set -e

# Mount virtual filesystems
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sys /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mkdir -p /dev/pts /dev/shm
mount -t devpts devpts /dev/pts 2>/dev/null || true
mount -t tmpfs tmpfs /dev/shm 2>/dev/null || true
mount -t tmpfs tmpfs /run 2>/dev/null || true
mount -t tmpfs tmpfs /tmp 2>/dev/null || true

# Set hostname
hostname tent-sandbox 2>/dev/null || true

# Bring up loopback
ip link set lo up 2>/dev/null || true

# Start tent-agent early — it uses vsock (no network required)
if [ -x /usr/local/bin/tent-agent ]; then
    /usr/local/bin/tent-agent &
fi

# Bring up primary network interface and request DHCP lease
# Try common interface names: eth0, enp0s1, ens1
for iface in eth0 enp0s1 ens1 enp0s2 ens2; do
    if [ -d "/sys/class/net/$iface" ]; then
        ip link set "$iface" up 2>/dev/null || true
        # Use dhclient, udhcpc, or manual DHCP
        if command -v dhclient >/dev/null 2>&1; then
            dhclient -v "$iface" 2>/dev/null &
        elif command -v udhcpc >/dev/null 2>&1; then
            udhcpc -i "$iface" -b 2>/dev/null &
        fi
        break
    fi
done

# Wait for IP assignment (up to 5s, polling every 100ms)
for i in $(seq 1 50); do
    ip addr show 2>/dev/null | grep -q 'inet ' && break
    sleep 0.1
done
`

	// Conditionally add SSH setup
	if enableSSH {
		initScript += `
# --- SSH support (enabled via --ssh flag) ---

# Install openssh-server if apt is available and sshd is missing
if [ ! -x /usr/sbin/sshd ] && command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq 2>/dev/null
    apt-get install -y -qq openssh-server 2>/dev/null
fi

# Generate SSH host keys if missing
if [ -x /usr/bin/ssh-keygen ]; then
    for type in rsa ecdsa ed25519; do
        keyfile="/etc/ssh/ssh_host_${type}_key"
        if [ ! -f "$keyfile" ]; then
            ssh-keygen -t "$type" -f "$keyfile" -N "" -q 2>/dev/null || true
        fi
    done
fi

# Ensure sshd runtime directory exists
mkdir -p /run/sshd

# Start sshd if available
if [ -x /usr/sbin/sshd ]; then
    /usr/sbin/sshd -D &
fi
`
	}

	initScript += `
# Write a marker so the host knows provisioning is done
echo "tent-ready" > /run/tent-ready

# Keep PID 1 alive — reap zombies
while true; do
    wait -n 2>/dev/null || sleep 1
done
`

	// Write the init script
	initPath := filepath.Join(rootfsDir, "sbin", "tent-init")
	if err := os.MkdirAll(filepath.Dir(initPath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(initPath, []byte(initScript), 0755); err != nil {
		return err
	}

	// Create /sbin/init symlink pointing to tent-init if no init exists
	sbinInit := filepath.Join(rootfsDir, "sbin", "init")
	if _, err := os.Lstat(sbinInit); os.IsNotExist(err) {
		os.Symlink("tent-init", sbinInit)
	}

	return nil
}

// injectSSHKeys writes the public key to /root/.ssh/authorized_keys
func injectSSHKeys(rootfsDir string, sshPubKey string) error {
	sshDir := filepath.Join(rootfsDir, "root", ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return err
	}

	authKeysPath := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(authKeysPath, []byte(sshPubKey+"\n"), 0600); err != nil {
		return err
	}

	return nil
}

// writeNetworkConfig writes /etc/network/interfaces for DHCP
func writeNetworkConfig(rootfsDir string) error {
	networkDir := filepath.Join(rootfsDir, "etc", "network")
	os.MkdirAll(networkDir, 0755)

	interfaces := `auto lo
iface lo inet loopback

auto eth0
iface eth0 inet dhcp
`
	return os.WriteFile(filepath.Join(networkDir, "interfaces"), []byte(interfaces), 0644)
}

// writeSSHDConfig writes a minimal sshd_config that allows root login with keys.
// Only called when SSH is explicitly enabled.
func writeSSHDConfig(rootfsDir string) error {
	sshdConfig := `# tent sshd configuration
Port 22
PermitRootLogin prohibit-password
PubkeyAuthentication yes
AuthorizedKeysFile .ssh/authorized_keys
PasswordAuthentication no
ChallengeResponseAuthentication no
UsePAM no
Subsystem sftp /usr/lib/openssh/sftp-server
`

	sshDir := filepath.Join(rootfsDir, "etc", "ssh")
	os.MkdirAll(sshDir, 0755)
	return os.WriteFile(filepath.Join(sshDir, "sshd_config"), []byte(sshdConfig), 0644)
}

// copyTentAgent copies the tent-agent binary into the guest rootfs at
// /usr/local/bin/tent-agent. It searches for the binary in well-known
// locations (next to the tent binary, in $GOPATH/bin, etc.).
func copyTentAgent(rootfsDir string) error {
	agentBin := findTentAgentBinary()
	if agentBin == "" {
		return fmt.Errorf("tent-agent binary not found in any search path")
	}

	dstPath := filepath.Join(rootfsDir, "usr", "local", "bin", "tent-agent")
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Read and copy the binary
	data, err := os.ReadFile(agentBin)
	if err != nil {
		return fmt.Errorf("failed to read tent-agent binary: %w", err)
	}

	if err := os.WriteFile(dstPath, data, 0755); err != nil {
		return fmt.Errorf("failed to write tent-agent to rootfs: %w", err)
	}

	return nil
}

// findTentAgentBinary searches for the tent-agent binary in common locations.
func findTentAgentBinary() string {
	// Candidate locations to search
	candidates := []string{}

	// 1. Next to the running tent binary
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, "tent-agent"))
	}

	// 2. In GOPATH/bin (for development)
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		candidates = append(candidates, filepath.Join(gopath, "bin", "tent-agent"))
	}

	// 3. Standard paths
	candidates = append(candidates,
		"/usr/local/bin/tent-agent",
		"/usr/bin/tent-agent",
	)

	// 4. Platform-specific architecture build path
	candidates = append(candidates,
		fmt.Sprintf("tent-agent-%s-%s", runtime.GOOS, runtime.GOARCH),
	)

	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}

	return ""
}
