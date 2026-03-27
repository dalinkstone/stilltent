#!/usr/bin/env bash
# harden-vps.sh — Universal VPS hardening for Ubuntu 22.04/24.04
# Installs Docker, configures firewall, fail2ban, swap, log rotation,
# creates a non-root user, and hardens SSH.
# Idempotent — safe to run multiple times.
set -euo pipefail

STILLTENT_USER="stilltent"
SWAP_SIZE="4G"
SWAP_FILE="/swapfile"

# ── Helpers ──────────────────────────────────────────────────────────
log()  { echo "[harden] $*"; }
skip() { echo "[harden] SKIP: $*"; }

require_root() {
  if [[ $EUID -ne 0 ]]; then
    echo "ERROR: This script must be run as root." >&2
    exit 1
  fi
}

# ── 1. Install Docker & Docker Compose v2 ───────────────────────────
install_docker() {
  if command -v docker &>/dev/null; then
    skip "Docker already installed ($(docker --version))"
  else
    log "Installing Docker..."
    apt-get update -qq
    apt-get install -y -qq ca-certificates curl gnupg lsb-release
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
      | gpg --dearmor -o /etc/apt/keyrings/docker.gpg 2>/dev/null || true
    chmod a+r /etc/apt/keyrings/docker.gpg
    echo \
      "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
      https://download.docker.com/linux/ubuntu \
      $(lsb_release -cs) stable" \
      | tee /etc/apt/sources.list.d/docker.list > /dev/null
    apt-get update -qq
    apt-get install -y -qq docker-ce docker-ce-cli containerd.io \
      docker-buildx-plugin docker-compose-plugin
    systemctl enable docker
    systemctl start docker
    log "Docker installed: $(docker --version)"
  fi

  # Verify Compose v2
  if docker compose version &>/dev/null; then
    log "Docker Compose v2: $(docker compose version --short)"
  else
    log "ERROR: Docker Compose v2 not available" >&2
    exit 1
  fi
}

# ── 2. Configure UFW ────────────────────────────────────────────────
configure_ufw() {
  if ! command -v ufw &>/dev/null; then
    log "Installing UFW..."
    apt-get install -y -qq ufw
  fi

  log "Configuring UFW..."
  ufw default deny incoming 2>/dev/null || true
  ufw default allow outgoing 2>/dev/null || true
  ufw allow 22/tcp comment "SSH" 2>/dev/null || true
  # Docker manages its own iptables rules for published ports.
  # We only expose SSH through UFW; service ports are accessed via SSH tunnel
  # or reverse proxy.
  ufw --force enable
  log "UFW active: SSH (22/tcp) allowed, all other incoming denied"
}

# ── 3. Install & configure fail2ban ─────────────────────────────────
configure_fail2ban() {
  if ! command -v fail2ban-client &>/dev/null; then
    log "Installing fail2ban..."
    apt-get install -y -qq fail2ban
  fi

  local JAIL_LOCAL="/etc/fail2ban/jail.local"
  if [[ -f "$JAIL_LOCAL" ]] && grep -q "stilltent" "$JAIL_LOCAL" 2>/dev/null; then
    skip "fail2ban already configured"
  else
    log "Configuring fail2ban for SSH..."
    cat > "$JAIL_LOCAL" <<'JAIL'
# stilltent fail2ban config
[sshd]
enabled  = true
port     = ssh
filter   = sshd
logpath  = /var/log/auth.log
maxretry = 5
bantime  = 3600
findtime = 600
JAIL
    systemctl enable fail2ban
    systemctl restart fail2ban
    log "fail2ban configured: 5 retries, 1h ban"
  fi
}

# ── 4. Create swap ──────────────────────────────────────────────────
configure_swap() {
  if swapon --show | grep -q "$SWAP_FILE"; then
    skip "Swap already active at $SWAP_FILE"
    return
  fi

  if [[ -f "$SWAP_FILE" ]]; then
    skip "Swap file exists but not active — activating"
    chmod 600 "$SWAP_FILE"
    mkswap "$SWAP_FILE" > /dev/null
    swapon "$SWAP_FILE"
  else
    log "Creating ${SWAP_SIZE} swap file..."
    fallocate -l "$SWAP_SIZE" "$SWAP_FILE"
    chmod 600 "$SWAP_FILE"
    mkswap "$SWAP_FILE" > /dev/null
    swapon "$SWAP_FILE"
  fi

  # Persist in fstab
  if ! grep -q "$SWAP_FILE" /etc/fstab; then
    echo "$SWAP_FILE none swap sw 0 0" >> /etc/fstab
  fi

  # Tune swappiness for server workload
  sysctl -w vm.swappiness=10 > /dev/null
  if ! grep -q "vm.swappiness" /etc/sysctl.d/99-stilltent.conf 2>/dev/null; then
    echo "vm.swappiness=10" >> /etc/sysctl.d/99-stilltent.conf
  fi

  log "Swap configured: ${SWAP_SIZE} at $SWAP_FILE"
}

# ── 5. Docker log rotation ──────────────────────────────────────────
configure_docker_logging() {
  local DAEMON_JSON="/etc/docker/daemon.json"

  if [[ -f "$DAEMON_JSON" ]] && grep -q "json-file" "$DAEMON_JSON" 2>/dev/null; then
    skip "Docker log rotation already configured"
    return
  fi

  log "Configuring Docker log rotation..."
  mkdir -p /etc/docker
  cat > "$DAEMON_JSON" <<'DJSON'
{
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "10m",
    "max-file": "3"
  }
}
DJSON
  systemctl restart docker
  log "Docker logging: json-file, max 10m x 3 files"
}

# ── 6. Create non-root user ─────────────────────────────────────────
create_user() {
  if id "$STILLTENT_USER" &>/dev/null; then
    skip "User '$STILLTENT_USER' already exists"
  else
    log "Creating user '$STILLTENT_USER'..."
    useradd -m -s /bin/bash "$STILLTENT_USER"
    log "User '$STILLTENT_USER' created"
  fi

  # Add to docker group
  if groups "$STILLTENT_USER" | grep -q docker; then
    skip "User '$STILLTENT_USER' already in docker group"
  else
    usermod -aG docker "$STILLTENT_USER"
    log "User '$STILLTENT_USER' added to docker group"
  fi

  # Copy SSH authorized_keys from root if the user doesn't have any
  local USER_SSH_DIR="/home/$STILLTENT_USER/.ssh"
  if [[ ! -f "$USER_SSH_DIR/authorized_keys" ]] && [[ -f /root/.ssh/authorized_keys ]]; then
    mkdir -p "$USER_SSH_DIR"
    cp /root/.ssh/authorized_keys "$USER_SSH_DIR/authorized_keys"
    chown -R "$STILLTENT_USER:$STILLTENT_USER" "$USER_SSH_DIR"
    chmod 700 "$USER_SSH_DIR"
    chmod 600 "$USER_SSH_DIR/authorized_keys"
    log "Copied root SSH keys to $STILLTENT_USER"
  fi
}

# ── 7. Harden SSH ───────────────────────────────────────────────────
harden_ssh() {
  local SSHD_CONFIG="/etc/ssh/sshd_config"

  if grep -q "^PermitRootLogin prohibit-password" "$SSHD_CONFIG" 2>/dev/null; then
    skip "SSH already hardened"
    return
  fi

  log "Hardening SSH..."

  # Disable root password login (keep key-based)
  sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin prohibit-password/' "$SSHD_CONFIG"

  # Disable password authentication entirely
  sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' "$SSHD_CONFIG"

  systemctl restart sshd || systemctl restart ssh || true
  log "SSH hardened: root key-only, password auth disabled"
}

# ── 8. Kernel parameters for Docker networking ──────────────────────
configure_kernel() {
  local SYSCTL_FILE="/etc/sysctl.d/99-stilltent.conf"

  if grep -q "net.ipv4.ip_forward" "$SYSCTL_FILE" 2>/dev/null; then
    skip "Kernel parameters already configured"
    return
  fi

  log "Setting kernel parameters for Docker networking..."
  cat >> "$SYSCTL_FILE" <<'KERN'
net.ipv4.ip_forward=1
net.bridge.bridge-nf-call-iptables=1
net.bridge.bridge-nf-call-ip6tables=1
KERN
  # Load br_netfilter module (required for bridge-nf-call params)
  modprobe br_netfilter 2>/dev/null || true
  if ! grep -q "br_netfilter" /etc/modules-load.d/docker.conf 2>/dev/null; then
    echo "br_netfilter" >> /etc/modules-load.d/docker.conf
  fi
  sysctl --system > /dev/null 2>&1
  log "Kernel: ip_forward=1, bridge-nf-call-iptables=1"
}

# ── Summary ──────────────────────────────────────────────────────────
print_summary() {
  echo ""
  echo "╔══════════════════════════════════════════════════╗"
  echo "║        VPS Hardening Complete                    ║"
  echo "╠══════════════════════════════════════════════════╣"
  echo "║  Docker:     $(docker --version | cut -d' ' -f3 | tr -d ',')"
  echo "║  Compose:    $(docker compose version --short)"
  echo "║  UFW:        active (SSH only)"
  echo "║  fail2ban:   active (SSH jail)"
  echo "║  Swap:       $(swapon --show --noheadings | awk '{print $3}' || echo "${SWAP_SIZE}")"
  echo "║  Logging:    json-file (10m x 3)"
  echo "║  User:       $STILLTENT_USER (docker group)"
  echo "║  SSH:        key-only, no root password"
  echo "║  Kernel:     ip_forward, bridge-nf-call"
  echo "╚══════════════════════════════════════════════════╝"
  echo ""
  echo "Next: deploy your application as '$STILLTENT_USER'"
  echo "  su - $STILLTENT_USER"
}

# ── Main ─────────────────────────────────────────────────────────────
main() {
  require_root
  log "Starting VPS hardening..."
  export DEBIAN_FRONTEND=noninteractive

  install_docker
  configure_ufw
  configure_fail2ban
  configure_swap
  configure_docker_logging
  create_user
  harden_ssh
  configure_kernel
  print_summary
}

# Allow sourcing without executing
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
