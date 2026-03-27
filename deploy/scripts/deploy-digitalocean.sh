#!/usr/bin/env bash
# deploy-digitalocean.sh — Deploy stilltent to a DigitalOcean droplet
# Usage: ssh root@DROPLET_IP 'bash -s' < deploy/scripts/deploy-digitalocean.sh
#   or:  scp this script to the droplet and run it there.
#
# Expects: GITHUB_TOKEN, TARGET_REPO in environment or .env
# Optional: DIGITALOCEAN_TOKEN (enables DO monitoring + API firewall)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="/home/stilltent/stilltent"
BRANCH="${DEPLOY_BRANCH:-main}"
REPO_URL="${DEPLOY_REPO_URL:-https://github.com/dalinstone/stilltent.git}"

log() { echo "[deploy-do] $*"; }

# ── 1. Run common VPS hardening ─────────────────────────────────────
log "Running VPS hardening..."
if [[ -f "$SCRIPT_DIR/harden-vps.sh" ]]; then
  source "$SCRIPT_DIR/harden-vps.sh"
  main
else
  # When running remotely before repo is cloned, inline the essentials
  log "harden-vps.sh not found locally — fetching from repo..."
  curl -fsSL "https://raw.githubusercontent.com/dalinstone/stilltent/${BRANCH}/deploy/scripts/harden-vps.sh" \
    -o /tmp/harden-vps.sh
  source /tmp/harden-vps.sh
  main
  rm -f /tmp/harden-vps.sh
fi

# ── 2. DigitalOcean-specific: monitoring agent ──────────────────────
install_do_monitoring() {
  if [[ -f /opt/digitalocean/bin/do-agent ]]; then
    log "SKIP: DO monitoring agent already installed"
    return
  fi

  log "Installing DigitalOcean monitoring agent..."
  curl -sSL https://repos.insights.digitalocean.com/install.sh | bash || {
    log "WARN: Could not install DO monitoring agent (non-fatal)"
  }
}

# ── 3. DigitalOcean-specific: API firewall ──────────────────────────
configure_do_firewall() {
  if [[ -z "${DIGITALOCEAN_TOKEN:-}" ]]; then
    log "SKIP: DIGITALOCEAN_TOKEN not set, skipping API firewall config"
    return
  fi

  log "Configuring DigitalOcean Cloud Firewall via API..."

  # Get droplet ID from metadata
  local DROPLET_ID
  DROPLET_ID=$(curl -s http://169.254.169.254/metadata/v1/id 2>/dev/null || echo "")
  if [[ -z "$DROPLET_ID" ]]; then
    log "WARN: Could not get droplet ID from metadata (not on DO?), skipping firewall"
    return
  fi

  # Check if firewall already exists
  local EXISTING
  EXISTING=$(curl -s -X GET \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${DIGITALOCEAN_TOKEN}" \
    "https://api.digitalocean.com/v2/firewalls" \
    | grep -o '"name":"stilltent-fw"' || echo "")

  if [[ -n "$EXISTING" ]]; then
    log "SKIP: stilltent-fw firewall already exists"
    return
  fi

  # Create firewall: allow SSH inbound, allow all outbound
  curl -s -X POST \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${DIGITALOCEAN_TOKEN}" \
    -d "{
      \"name\": \"stilltent-fw\",
      \"inbound_rules\": [
        {
          \"protocol\": \"tcp\",
          \"ports\": \"22\",
          \"sources\": {\"addresses\": [\"0.0.0.0/0\", \"::/0\"]}
        }
      ],
      \"outbound_rules\": [
        {
          \"protocol\": \"tcp\",
          \"ports\": \"all\",
          \"destinations\": {\"addresses\": [\"0.0.0.0/0\", \"::/0\"]}
        },
        {
          \"protocol\": \"udp\",
          \"ports\": \"all\",
          \"destinations\": {\"addresses\": [\"0.0.0.0/0\", \"::/0\"]}
        }
      ],
      \"droplet_ids\": [${DROPLET_ID}]
    }" \
    "https://api.digitalocean.com/v2/firewalls" > /dev/null

  log "Cloud Firewall 'stilltent-fw' created and attached to droplet $DROPLET_ID"
}

# ── 4. Clone repo & deploy ──────────────────────────────────────────
deploy_stack() {
  log "Deploying stilltent stack..."

  # Clone or update repo
  if [[ -d "$REPO_DIR/.git" ]]; then
    log "Repo exists — pulling latest..."
    cd "$REPO_DIR"
    sudo -u stilltent git pull origin "$BRANCH"
  else
    log "Cloning repo..."
    sudo -u stilltent git clone --branch "$BRANCH" "$REPO_URL" "$REPO_DIR"
    cd "$REPO_DIR"
  fi

  # Move .env if provided via scp or environment
  if [[ -f /root/.env.stilltent ]]; then
    log "Moving .env from /root/.env.stilltent..."
    cp /root/.env.stilltent "$REPO_DIR/.env"
    chown stilltent:stilltent "$REPO_DIR/.env"
    chmod 600 "$REPO_DIR/.env"
  elif [[ ! -f "$REPO_DIR/.env" ]]; then
    log "WARNING: No .env file found!"
    log "  Copy your .env to the droplet first:"
    log "  scp .env root@DROPLET_IP:/root/.env.stilltent"
    log "  Then re-run this script."
    exit 1
  fi

  # Run bootstrap as stilltent user
  log "Running bootstrap..."
  cd "$REPO_DIR"
  sudo -u stilltent make bootstrap

  log "Stack deployed successfully."
}

# ── 5. Print deployment summary ─────────────────────────────────────
print_deploy_summary() {
  local IP
  IP=$(curl -s http://169.254.169.254/metadata/v1/interfaces/public/0/ipv4/address 2>/dev/null \
    || hostname -I | awk '{print $1}')

  echo ""
  echo "╔══════════════════════════════════════════════════╗"
  echo "║   DigitalOcean Deployment Complete               ║"
  echo "╠══════════════════════════════════════════════════╣"
  echo "║  IP:         $IP"
  echo "║  User:       stilltent"
  echo "║  Repo:       $REPO_DIR"
  echo "║  Stack:      $(cd "$REPO_DIR" && docker compose ps --format '{{.Name}}' 2>/dev/null | wc -l | tr -d ' ') services"
  echo "╚══════════════════════════════════════════════════╝"
  echo ""
  echo "Management commands (as stilltent user):"
  echo "  cd $REPO_DIR"
  echo "  make health        # Check service health"
  echo "  make logs          # Follow logs"
  echo "  make start         # Start orchestrator (autonomous mode)"
  echo "  make pause         # Pause the agent"
  echo "  make cost          # Check spend"
  echo ""
  echo "SSH access:"
  echo "  ssh stilltent@$IP"
}

# ── Main ─────────────────────────────────────────────────────────────
install_do_monitoring
configure_do_firewall
deploy_stack
print_deploy_summary
