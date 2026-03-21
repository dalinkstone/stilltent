#!/usr/bin/env bash
# harden-vps.sh — Security hardening, swap provisioning, and performance
# tuning for a fresh DigitalOcean droplet running stilltent.
#
# Run as root AFTER deploy-digitalocean.sh has completed:
#   bash scripts/harden-vps.sh
#
# Idempotent: safe to run multiple times.
set -euo pipefail

# ── Globals ──────────────────────────────────────────────────────────
USERNAME="stilltent"
SWAP_SIZE="2G"
WORKSPACE_DIR="${STILLTENT_DIR:-/root/stilltent}/workspace"
CHANGES=()                       # accumulates a summary of what was done

log()  { echo ">>> $*"; }
skip() { echo "    (skipped — already configured)"; }
note() { CHANGES+=("$1"); }

# Must be root
if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: This script must be run as root."
    exit 1
fi

echo "==========================================="
echo "  stilltent VPS hardening"
echo "==========================================="
echo ""

# =====================================================================
# 1. Create non-root user with sudo
# =====================================================================
log "Creating user '$USERNAME' with sudo privileges..."

if id "$USERNAME" &>/dev/null; then
    skip
else
    adduser --disabled-password --gecos "stilltent service" "$USERNAME"
    usermod -aG sudo "$USERNAME"
    # Allow passwordless sudo for automation
    echo "$USERNAME ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/$USERNAME"
    chmod 440 "/etc/sudoers.d/$USERNAME"
    # Copy root's authorized_keys so operator can SSH in as this user
    if [ -f /root/.ssh/authorized_keys ]; then
        mkdir -p "/home/$USERNAME/.ssh"
        cp /root/.ssh/authorized_keys "/home/$USERNAME/.ssh/authorized_keys"
        chown -R "$USERNAME:$USERNAME" "/home/$USERNAME/.ssh"
        chmod 700 "/home/$USERNAME/.ssh"
        chmod 600 "/home/$USERNAME/.ssh/authorized_keys"
    fi
    # Add to docker group so the user can manage containers
    usermod -aG docker "$USERNAME" 2>/dev/null || true
    note "Created user '$USERNAME' with sudo + docker access"
fi

# =====================================================================
# 2. Disable root SSH login
# =====================================================================
log "Disabling root SSH login..."

SSHD_CFG="/etc/ssh/sshd_config"
if grep -qE '^\s*PermitRootLogin\s+no' "$SSHD_CFG"; then
    skip
else
    # Comment out any existing PermitRootLogin lines and append the hardened one
    sed -i 's/^\s*PermitRootLogin\s.*/# &/' "$SSHD_CFG"
    echo "PermitRootLogin no" >> "$SSHD_CFG"

    # Also disable password auth (key-only)
    if ! grep -qE '^\s*PasswordAuthentication\s+no' "$SSHD_CFG"; then
        sed -i 's/^\s*PasswordAuthentication\s.*/# &/' "$SSHD_CFG"
        echo "PasswordAuthentication no" >> "$SSHD_CFG"
    fi

    systemctl reload sshd || systemctl reload ssh || true
    note "Disabled root SSH login + password authentication"
fi

# =====================================================================
# 3. UFW Firewall
# =====================================================================
log "Configuring UFW firewall..."

apt-get install -y -qq ufw

if ufw status | grep -q "Status: active"; then
    skip
else
    ufw default deny incoming
    ufw default allow outgoing
    ufw allow 22/tcp comment "SSH"
    ufw --force enable
    note "Enabled UFW — allow SSH (22), deny all other inbound"
fi

# =====================================================================
# 4. Unattended upgrades for security patches
# =====================================================================
log "Setting up unattended-upgrades..."

if dpkg -l | grep -q unattended-upgrades; then
    skip
else
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
        unattended-upgrades apt-listchanges

    cat > /etc/apt/apt.conf.d/20auto-upgrades <<'APTCONF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
APTCONF

    # Enable only security updates, and auto-reboot at 4 AM if needed
    cat > /etc/apt/apt.conf.d/50unattended-upgrades <<'APTCONF'
Unattended-Upgrade::Allowed-Origins {
    "${distro_id}:${distro_codename}-security";
    "${distro_id}ESMApps:${distro_codename}-apps-security";
    "${distro_id}ESM:${distro_codename}-infra-security";
};
Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-Time "04:00";
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";
Unattended-Upgrade::Remove-Unused-Dependencies "true";
APTCONF

    note "Enabled unattended security upgrades (auto-reboot at 04:00 if needed)"
fi

# =====================================================================
# 5. fail2ban for SSH brute-force protection
# =====================================================================
log "Configuring fail2ban..."

if systemctl is-active --quiet fail2ban 2>/dev/null; then
    skip
else
    apt-get install -y -qq fail2ban

    cat > /etc/fail2ban/jail.local <<'F2B'
[sshd]
enabled  = true
port     = ssh
filter   = sshd
backend  = systemd
maxretry = 5
findtime = 600
bantime  = 3600
F2B

    systemctl enable fail2ban
    systemctl restart fail2ban
    note "Enabled fail2ban — ban after 5 failed SSH attempts (1 hr ban)"
fi

# =====================================================================
# 6. 2 GB swap file
# =====================================================================
log "Setting up ${SWAP_SIZE} swap file..."

if swapon --show | grep -q /swapfile; then
    skip
else
    fallocate -l "$SWAP_SIZE" /swapfile
    chmod 600 /swapfile
    mkswap /swapfile
    swapon /swapfile

    # Persist across reboots
    if ! grep -q '/swapfile' /etc/fstab; then
        echo '/swapfile none swap sw 0 0' >> /etc/fstab
    fi

    # Tune swappiness — prefer RAM, swap only under pressure
    sysctl vm.swappiness=10
    if ! grep -q 'vm.swappiness' /etc/sysctl.conf; then
        echo 'vm.swappiness=10' >> /etc/sysctl.conf
    fi

    # Reduce vfs_cache_pressure to keep inode/dentry caches longer
    sysctl vm.vfs_cache_pressure=50
    if ! grep -q 'vm.vfs_cache_pressure' /etc/sysctl.conf; then
        echo 'vm.vfs_cache_pressure=50' >> /etc/sysctl.conf
    fi

    note "Created ${SWAP_SIZE} swap file (swappiness=10, vfs_cache_pressure=50)"
fi

# =====================================================================
# 7. Docker log rotation
# =====================================================================
log "Configuring Docker log rotation..."

DOCKER_DAEMON="/etc/docker/daemon.json"
DESIRED_LOG_CFG='{"log-driver":"json-file","log-opts":{"max-size":"50m","max-file":"3"}}'

if [ -f "$DOCKER_DAEMON" ] && python3 -c "
import json, sys
d = json.load(open('$DOCKER_DAEMON'))
sys.exit(0 if d.get('log-opts',{}).get('max-size') == '50m' else 1)
" 2>/dev/null; then
    skip
else
    if [ -f "$DOCKER_DAEMON" ]; then
        # Merge into existing config
        python3 -c "
import json
try:
    with open('$DOCKER_DAEMON') as f:
        cfg = json.load(f)
except (json.JSONDecodeError, FileNotFoundError):
    cfg = {}
cfg['log-driver'] = 'json-file'
cfg['log-opts'] = {'max-size': '50m', 'max-file': '3'}
with open('$DOCKER_DAEMON', 'w') as f:
    json.dump(cfg, f, indent=2)
"
    else
        mkdir -p /etc/docker
        echo "$DESIRED_LOG_CFG" | python3 -m json.tool > "$DOCKER_DAEMON"
    fi

    systemctl restart docker
    note "Configured Docker log rotation: max 50 MB x 3 files per container"
fi

# =====================================================================
# 8. Daily disk-usage watchdog cron
# =====================================================================
log "Installing disk-usage watchdog cron..."

CRON_SCRIPT="/usr/local/bin/stilltent-disk-watchdog.sh"
CRON_ENTRY="0 * * * * $CRON_SCRIPT"

cat > "$CRON_SCRIPT" <<WATCHDOG
#!/usr/bin/env bash
# Auto-generated by harden-vps.sh — checks disk usage hourly.
# Creates a PAUSE file if usage exceeds 90% to stop the orchestrator
# before the disk fills completely.

WORKSPACE_DIR="${WORKSPACE_DIR}"
THRESHOLD=90

USAGE=\$(df / --output=pcent | tail -1 | tr -dc '0-9')

if [ "\$USAGE" -ge "\$THRESHOLD" ]; then
    echo "\$(date -Iseconds) DISK WARNING: \${USAGE}% used (threshold \${THRESHOLD}%) — pausing agent" \
        >> "\${WORKSPACE_DIR}/orchestrator.log"
    echo "Disk usage \${USAGE}% exceeds \${THRESHOLD}% — auto-paused by disk watchdog at \$(date -Iseconds)" \
        > "\${WORKSPACE_DIR}/PAUSE"
fi
WATCHDOG
chmod +x "$CRON_SCRIPT"

# Install cron entry (idempotent)
if crontab -l 2>/dev/null | grep -qF "$CRON_SCRIPT"; then
    skip
else
    ( crontab -l 2>/dev/null || true; echo "$CRON_ENTRY" ) | crontab -
    note "Installed hourly disk watchdog (PAUSE if usage > 90%)"
fi

# =====================================================================
# 9. Kernel / network performance tuning
# =====================================================================
log "Applying kernel and network tuning..."

SYSCTL_TUNE="/etc/sysctl.d/99-stilltent.conf"

if [ -f "$SYSCTL_TUNE" ]; then
    skip
else
    cat > "$SYSCTL_TUNE" <<'SYSCTL'
# --- stilltent VPS tuning ---

# Increase file descriptor limits for TiDB and Docker
fs.file-max = 1048576

# Network hardening — prevent common attacks
net.ipv4.tcp_syncookies = 1
net.ipv4.conf.all.rp_filter = 1
net.ipv4.conf.default.rp_filter = 1
net.ipv4.conf.all.accept_redirects = 0
net.ipv4.conf.default.accept_redirects = 0
net.ipv4.conf.all.send_redirects = 0
net.ipv4.conf.default.send_redirects = 0
net.ipv4.icmp_echo_ignore_broadcasts = 1
net.ipv4.icmp_ignore_bogus_error_responses = 1

# Disable IPv6 if not needed (reduces attack surface)
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1

# TCP keepalive — detect dead connections faster (helps TiDB connections)
net.ipv4.tcp_keepalive_time = 600
net.ipv4.tcp_keepalive_intvl = 60
net.ipv4.tcp_keepalive_probes = 5

# Increase connection tracking for Docker networking
net.netfilter.nf_conntrack_max = 131072

# Increase socket buffer sizes for better throughput
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
net.ipv4.tcp_rmem = 4096 87380 16777216
net.ipv4.tcp_wmem = 4096 65536 16777216

# Faster TCP connection reuse
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 15

# Increase local port range for outgoing connections
net.ipv4.ip_local_port_range = 10240 65535
SYSCTL

    sysctl --system > /dev/null 2>&1
    note "Applied kernel + network tuning (TCP hardening, keepalives, buffer sizes)"
fi

# =====================================================================
# 10. Raise file descriptor limits for services
# =====================================================================
log "Setting file descriptor limits..."

LIMITS_CONF="/etc/security/limits.d/99-stilltent.conf"

if [ -f "$LIMITS_CONF" ]; then
    skip
else
    cat > "$LIMITS_CONF" <<LIMITS
# stilltent — raise file descriptor limits for TiDB and Docker workloads
*               soft    nofile          65536
*               hard    nofile          1048576
$USERNAME       soft    nofile          65536
$USERNAME       hard    nofile          1048576
root            soft    nofile          65536
root            hard    nofile          1048576
LIMITS

    note "Raised file descriptor limits (65536 soft / 1M hard)"
fi

# =====================================================================
# 11. Set up I/O scheduler tuning for better disk performance
# =====================================================================
log "Tuning I/O scheduler..."

UDEV_RULE="/etc/udev/rules.d/60-stilltent-io.rules"

if [ -f "$UDEV_RULE" ]; then
    skip
else
    # DigitalOcean droplets typically use virtio (vda). mq-deadline is
    # optimal for virtualized block devices under mixed read/write loads.
    cat > "$UDEV_RULE" <<'UDEV'
# Use mq-deadline for virtio disks (optimal for VPS mixed workloads)
ACTION=="add|change", KERNEL=="vd[a-z]", ATTR{queue/scheduler}="mq-deadline"
ACTION=="add|change", KERNEL=="sd[a-z]", ATTR{queue/scheduler}="mq-deadline"
UDEV

    # Apply immediately to current disks
    for disk in /sys/block/vd? /sys/block/sd?; do
        [ -f "$disk/queue/scheduler" ] && echo mq-deadline > "$disk/queue/scheduler" 2>/dev/null || true
    done

    note "Set I/O scheduler to mq-deadline for VPS block devices"
fi

# =====================================================================
# 12. Harden shared memory and tmp
# =====================================================================
log "Hardening /dev/shm..."

if grep -q '/dev/shm' /etc/fstab && grep -q 'nosuid' /etc/fstab; then
    skip
else
    if ! grep -q '/dev/shm.*nosuid' /etc/fstab; then
        echo 'tmpfs /dev/shm tmpfs defaults,nosuid,nodev,noexec 0 0' >> /etc/fstab
        mount -o remount /dev/shm 2>/dev/null || true
    fi
    note "Hardened /dev/shm (nosuid, nodev, noexec)"
fi

# =====================================================================
# Summary
# =====================================================================
echo ""
echo "==========================================="
echo "  Hardening complete"
echo "==========================================="
echo ""

if [ ${#CHANGES[@]} -eq 0 ]; then
    echo "  No changes — everything was already configured."
else
    for change in "${CHANGES[@]}"; do
        echo "  ✓ $change"
    done
fi

echo ""
echo "  Next steps:"
echo "    • SSH in as '$USERNAME' instead of root"
echo "    • Run stilltent from /root/stilltent (or move to /home/$USERNAME)"
echo "    • Test with: sudo ufw status && sudo fail2ban-client status sshd"
echo ""
echo "==========================================="
