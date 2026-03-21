#!/usr/bin/env bash
# deploy-digitalocean.sh — One-shot setup script for stilltent on a fresh
# DigitalOcean droplet (Ubuntu 24.04). Installs Docker, clones the repo,
# configures .env, starts the stack, and kicks off the first iteration.
#
# Usage:
#   curl -sSL <raw-script-url> | bash
#   — or —
#   scp scripts/deploy-digitalocean.sh root@<droplet-ip>:~ && ssh root@<droplet-ip> bash deploy-digitalocean.sh
#
# Idempotent: safe to run multiple times on the same droplet.
set -euo pipefail

REPO_URL="${STILLTENT_REPO_URL:-https://github.com/dalinstone/stilltent.git}"
INSTALL_DIR="${STILLTENT_DIR:-/root/stilltent}"

echo "=== stilltent DigitalOcean Deploy ==="
echo ""

# -------------------------------------------------------------------
# 1. Update system packages
# -------------------------------------------------------------------
echo ">>> Updating system packages..."
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get upgrade -y -qq

# -------------------------------------------------------------------
# 2. Install Docker and Docker Compose (official repo, not snap)
# -------------------------------------------------------------------
if command -v docker &>/dev/null; then
    echo ">>> Docker already installed: $(docker --version)"
else
    echo ">>> Installing Docker..."
    apt-get install -y -qq ca-certificates curl gnupg

    install -m 0755 -d /etc/apt/keyrings
    if [ ! -f /etc/apt/keyrings/docker.asc ]; then
        curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
        chmod a+r /etc/apt/keyrings/docker.asc
    fi

    if [ ! -f /etc/apt/sources.list.d/docker.list ]; then
        echo \
          "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
          https://download.docker.com/linux/ubuntu \
          $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
          > /etc/apt/sources.list.d/docker.list
        apt-get update -qq
    fi

    apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    echo ">>> Docker installed: $(docker --version)"
fi

# -------------------------------------------------------------------
# 3. Install utilities
# -------------------------------------------------------------------
echo ">>> Installing git, make, curl, jq, ufw, fail2ban, mysql-client..."
apt-get install -y -qq git make curl jq ufw fail2ban default-mysql-client

# -------------------------------------------------------------------
# 4. Set up swap (4G — good practice on 8GB droplet, critical on 4GB)
# -------------------------------------------------------------------
SWAP_SIZE="${STILLTENT_SWAP_SIZE:-4G}"
echo ">>> Setting up ${SWAP_SIZE} swap file..."

if swapon --show | grep -q /swapfile; then
    echo "    (skipped — swap already active)"
else
    fallocate -l "$SWAP_SIZE" /swapfile
    chmod 600 /swapfile
    mkswap /swapfile
    swapon /swapfile

    # Persist across reboots
    if ! grep -q '/swapfile' /etc/fstab; then
        echo '/swapfile none swap sw 0 0' >> /etc/fstab
    fi

    echo "    Swap file created and activated."
fi

# -------------------------------------------------------------------
# 5. Configure UFW firewall
# -------------------------------------------------------------------
echo ">>> Configuring UFW firewall..."

if ufw status | grep -q "Status: active"; then
    echo "    (skipped — UFW already active)"
else
    ufw allow OpenSSH
    ufw --force enable
    echo "    UFW enabled — SSH allowed, all other inbound denied."
fi

# -------------------------------------------------------------------
# 6. Configure Docker log rotation (prevents disk fill during long runs)
# -------------------------------------------------------------------
echo ">>> Configuring Docker log rotation..."

DOCKER_DAEMON="/etc/docker/daemon.json"

if [ -f "$DOCKER_DAEMON" ] && python3 -c "
import json, sys
d = json.load(open('$DOCKER_DAEMON'))
sys.exit(0 if d.get('log-opts',{}).get('max-size') == '50m' else 1)
" 2>/dev/null; then
    echo "    (skipped — already configured)"
else
    mkdir -p /etc/docker
    cat > "$DOCKER_DAEMON" << 'EOF'
{
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "50m",
    "max-file": "3"
  }
}
EOF
    systemctl restart docker
    echo "    Docker log rotation set: max 50MB x 3 files per container."
fi

# -------------------------------------------------------------------
# 7. Clone the repo (skip if already present)
# -------------------------------------------------------------------
if [ -d "$INSTALL_DIR/.git" ]; then
    echo ">>> Repo already cloned at $INSTALL_DIR — pulling latest..."
    git -C "$INSTALL_DIR" pull --ff-only || echo "    (pull skipped — may have local changes)"
else
    echo ">>> Cloning stilltent repo..."
    if [ -n "${GITHUB_TOKEN:-}" ]; then
        # Use token for private repo access
        CLONE_URL="https://${GITHUB_TOKEN}@${REPO_URL#https://}"
    else
        CLONE_URL="$REPO_URL"
    fi
    git clone "$CLONE_URL" "$INSTALL_DIR"
fi

# -------------------------------------------------------------------
# 8. Configure .env
# -------------------------------------------------------------------
cd "$INSTALL_DIR"

if [ -f .env ]; then
    echo ">>> .env already exists — skipping creation."
    echo "    Edit manually if needed: nano $INSTALL_DIR/.env"
else
    echo ">>> Creating .env from .env.example..."
    cp .env.example .env

    echo ""
    echo "Three values are required to continue."
    echo ""

    # OPENROUTER_API_KEY
    read -rp "OPENROUTER_API_KEY (from https://openrouter.ai/keys): " OPENROUTER_API_KEY
    while [ -z "$OPENROUTER_API_KEY" ]; do
        echo "  This is required."
        read -rp "OPENROUTER_API_KEY: " OPENROUTER_API_KEY
    done
    sed -i "s|^OPENROUTER_API_KEY=.*|OPENROUTER_API_KEY=${OPENROUTER_API_KEY}|" .env

    # GITHUB_TOKEN
    read -rp "GITHUB_TOKEN (fine-grained PAT with repo+workflow): " GITHUB_TOKEN_INPUT
    while [ -z "$GITHUB_TOKEN_INPUT" ]; do
        echo "  This is required."
        read -rp "GITHUB_TOKEN: " GITHUB_TOKEN_INPUT
    done
    sed -i "s|^GITHUB_TOKEN=.*|GITHUB_TOKEN=${GITHUB_TOKEN_INPUT}|" .env

    # TARGET_REPO
    read -rp "TARGET_REPO (owner/repo format): " TARGET_REPO
    while [ -z "$TARGET_REPO" ]; do
        echo "  This is required."
        read -rp "TARGET_REPO: " TARGET_REPO
    done
    sed -i "s|^TARGET_REPO=.*|TARGET_REPO=${TARGET_REPO}|" .env

    echo ""
    echo ">>> .env configured. All other values use defaults."
    echo "    Edit later with: nano $INSTALL_DIR/.env"
fi

# -------------------------------------------------------------------
# 9. Start the stack
# -------------------------------------------------------------------
echo ""
echo ">>> Starting stilltent stack..."
make up

# -------------------------------------------------------------------
# 10. Wait for services to boot
# -------------------------------------------------------------------
echo ">>> Waiting 30 seconds for services to start..."
sleep 30

# -------------------------------------------------------------------
# 11. Initialize the database
# -------------------------------------------------------------------
echo ">>> Initializing database..."
make init-db

# -------------------------------------------------------------------
# 12. Health check
# -------------------------------------------------------------------
echo ">>> Running health checks..."
make health

# -------------------------------------------------------------------
# 13. Bootstrap — clone target repo and start first iteration
# -------------------------------------------------------------------
echo ">>> Bootstrapping — cloning target repo and starting first iteration..."
# Ensure workspace dir is writable by container users (node uid 1000, orchestrator)
chmod 777 "${INSTALL_DIR}/workspace" 2>/dev/null || true
mkdir -p "${INSTALL_DIR}/workspace/repo"
chmod 777 "${INSTALL_DIR}/workspace/repo"
# Ensure config dir is writable by node (uid 1000) inside the container
chown -R 1000:1000 "${INSTALL_DIR}/config/openclaw" 2>/dev/null || chmod -R a+rw "${INSTALL_DIR}/config/openclaw" || true
# Remove stale openclaw extension (superseded by mem9 plugin)
rm -rf "${INSTALL_DIR}/config/openclaw/extensions/openclaw" 2>/dev/null || true
make bootstrap

# -------------------------------------------------------------------
# 14. Summary
# -------------------------------------------------------------------
DROPLET_IP=$(curl -sf http://169.254.169.254/metadata/v1/interfaces/public/0/ipv4/address 2>/dev/null || hostname -I | awk '{print $1}')
RUNTIME_HOURS=$(grep -oP '^TOTAL_RUNTIME_HOURS=\K.*' .env 2>/dev/null || echo "120")

echo ""
echo "==========================================="
echo "  stilltent is now running"
echo "==========================================="
echo ""
echo "  Estimated runtime: ${RUNTIME_HOURS} hours"
echo ""
echo "  Monitor:  ssh root@${DROPLET_IP} then cd stilltent && make logs"
echo "  Pause:    make pause"
echo "  Resume:   make resume"
echo "  Stats:    make stats"
echo ""
echo "==========================================="
