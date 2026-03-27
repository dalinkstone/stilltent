#!/usr/bin/env bash
# deploy-vultr.sh — Deploy stilltent to a Vultr VPS
# Usage: ssh root@VULTR_IP 'bash -s' < deploy/scripts/deploy-vultr.sh
#   or:  scp this script to the VPS and run it there.
#
# Expects: GITHUB_TOKEN, TARGET_REPO in environment or .env
# Optional: VULTR_API_KEY (enables Vultr CLI + firewall group)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="/home/stilltent/stilltent"
BRANCH="${DEPLOY_BRANCH:-main}"
REPO_URL="${DEPLOY_REPO_URL:-https://github.com/dalinstone/stilltent.git}"

log() { echo "[deploy-vultr] $*"; }

# ── 1. Run common VPS hardening ─────────────────────────────────────
log "Running VPS hardening..."
if [[ -f "$SCRIPT_DIR/harden-vps.sh" ]]; then
  source "$SCRIPT_DIR/harden-vps.sh"
  main
else
  log "harden-vps.sh not found locally — fetching from repo..."
  curl -fsSL "https://raw.githubusercontent.com/dalinstone/stilltent/${BRANCH}/deploy/scripts/harden-vps.sh" \
    -o /tmp/harden-vps.sh
  source /tmp/harden-vps.sh
  main
  rm -f /tmp/harden-vps.sh
fi

# ── 2. Vultr-specific: install Vultr CLI ────────────────────────────
install_vultr_cli() {
  if [[ -z "${VULTR_API_KEY:-}" ]]; then
    log "SKIP: VULTR_API_KEY not set, skipping Vultr CLI"
    return
  fi

  if command -v vultr-cli &>/dev/null; then
    log "SKIP: vultr-cli already installed"
  else
    log "Installing Vultr CLI..."
    local ARCH
    ARCH=$(dpkg --print-architecture)
    local VER="3.4.0"
    curl -fsSL "https://github.com/vultr/vultr-cli/releases/download/v${VER}/vultr-cli_${VER}_linux_${ARCH}.tar.gz" \
      | tar xz -C /usr/local/bin vultr-cli
    chmod +x /usr/local/bin/vultr-cli
    log "vultr-cli v${VER} installed"
  fi

  export VULTR_API_KEY
}

# ── 3. Vultr-specific: configure firewall group ─────────────────────
configure_vultr_firewall() {
  if [[ -z "${VULTR_API_KEY:-}" ]]; then
    log "SKIP: VULTR_API_KEY not set, skipping API firewall"
    return
  fi

  log "Configuring Vultr firewall group..."

  # Check if firewall group already exists
  local FW_GROUP_ID
  FW_GROUP_ID=$(vultr-cli firewall group list -o json 2>/dev/null \
    | python3 -c "
import sys, json
data = json.load(sys.stdin)
groups = data.get('firewall_groups', [])
for g in groups:
    if g.get('description') == 'stilltent':
        print(g['id'])
        sys.exit(0)
" 2>/dev/null || echo "")

  if [[ -n "$FW_GROUP_ID" ]]; then
    log "SKIP: Firewall group 'stilltent' already exists ($FW_GROUP_ID)"
    return
  fi

  # Create firewall group
  FW_GROUP_ID=$(vultr-cli firewall group create --description "stilltent" -o json 2>/dev/null \
    | python3 -c "import sys, json; print(json.load(sys.stdin).get('firewall_group', {}).get('id', ''))" \
    2>/dev/null || echo "")

  if [[ -z "$FW_GROUP_ID" ]]; then
    log "WARN: Could not create firewall group (non-fatal)"
    return
  fi

  # Add SSH rule
  vultr-cli firewall rule create \
    --id "$FW_GROUP_ID" \
    --protocol tcp \
    --port 22 \
    --subnet "0.0.0.0" \
    --size 0 \
    --ip-type v4 2>/dev/null || true

  # Get instance ID from metadata
  local INSTANCE_ID
  INSTANCE_ID=$(curl -s http://169.254.169.254/v1/instance-v2-id 2>/dev/null || echo "")

  if [[ -n "$INSTANCE_ID" ]]; then
    # Attach firewall group to instance (via API, vultr-cli doesn't support this directly)
    curl -s -X PUT \
      -H "Authorization: Bearer ${VULTR_API_KEY}" \
      -H "Content-Type: application/json" \
      -d "{\"firewall_group_id\": \"$FW_GROUP_ID\"}" \
      "https://api.vultr.com/v2/instances/${INSTANCE_ID}" > /dev/null || true
    log "Firewall group 'stilltent' created and attached to instance"
  else
    log "Firewall group 'stilltent' created (attach manually if not on Vultr metadata)"
  fi
}

# ── 4. Clone repo & deploy ──────────────────────────────────────────
deploy_stack() {
  log "Deploying stilltent stack..."

  if [[ -d "$REPO_DIR/.git" ]]; then
    log "Repo exists — pulling latest..."
    cd "$REPO_DIR"
    sudo -u stilltent git pull origin "$BRANCH"
  else
    log "Cloning repo..."
    sudo -u stilltent git clone --branch "$BRANCH" "$REPO_URL" "$REPO_DIR"
    cd "$REPO_DIR"
  fi

  # Move .env if provided
  if [[ -f /root/.env.stilltent ]]; then
    log "Moving .env from /root/.env.stilltent..."
    cp /root/.env.stilltent "$REPO_DIR/.env"
    chown stilltent:stilltent "$REPO_DIR/.env"
    chmod 600 "$REPO_DIR/.env"
  elif [[ ! -f "$REPO_DIR/.env" ]]; then
    log "WARNING: No .env file found!"
    log "  scp .env root@VULTR_IP:/root/.env.stilltent"
    log "  Then re-run this script."
    exit 1
  fi

  log "Running bootstrap..."
  cd "$REPO_DIR"
  sudo -u stilltent make bootstrap

  log "Stack deployed successfully."
}

# ── 5. Print summary ────────────────────────────────────────────────
print_deploy_summary() {
  local IP
  IP=$(curl -s http://169.254.169.254/v1/internal-ip 2>/dev/null \
    || hostname -I | awk '{print $1}')

  echo ""
  echo "╔══════════════════════════════════════════════════╗"
  echo "║   Vultr Deployment Complete                      ║"
  echo "╠══════════════════════════════════════════════════╣"
  echo "║  IP:         $IP"
  echo "║  User:       stilltent"
  echo "║  Repo:       $REPO_DIR"
  echo "║  Stack:      $(cd "$REPO_DIR" && docker compose ps --format '{{.Name}}' 2>/dev/null | wc -l | tr -d ' ') services"
  echo "╚══════════════════════════════════════════════════╝"
  echo ""
  echo "Management:"
  echo "  ssh stilltent@$IP"
  echo "  cd $REPO_DIR && make health"
}

# ── Main ─────────────────────────────────────────────────────────────
install_vultr_cli
configure_vultr_firewall
deploy_stack
print_deploy_summary
