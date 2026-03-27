#!/usr/bin/env bash
# teardown.sh — Universal teardown for stilltent
# Stops containers, removes volumes/images, and optionally cleans VPS artifacts.
#
# Usage: teardown.sh [--full] [--provider railway|render|heroku]
#   --full    Also remove swap, stilltent user, and Docker (VPS only)
#   --provider  Tear down PaaS services
set -euo pipefail

FULL=false
PROVIDER=""
PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

log()  { echo "[teardown] $*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --full) FULL=true; shift ;;
    --provider) PROVIDER="$2"; shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

# ── Docker teardown (works everywhere) ───────────────────────────────
teardown_docker() {
  cd "$PROJECT_DIR"

  if ! command -v docker &>/dev/null; then
    log "Docker not found — skipping container teardown"
    return
  fi

  log "Stopping all containers..."
  docker compose down --timeout 30 2>/dev/null || true

  log "Removing volumes..."
  docker compose down -v 2>/dev/null || true

  log "Removing stilltent images..."
  docker images --format '{{.Repository}}:{{.Tag}}' \
    | grep -E "stilltent|openclaw|mnemo|embed-service|orchestrator" \
    | xargs -r docker rmi -f 2>/dev/null || true

  # Remove dangling images from our builds
  docker image prune -f 2>/dev/null || true

  log "Docker teardown complete"
}

# ── VPS cleanup (optional, only with --full) ─────────────────────────
teardown_vps_full() {
  if [[ "$FULL" != "true" ]]; then
    return
  fi

  if [[ $EUID -ne 0 ]]; then
    log "SKIP: Full VPS cleanup requires root"
    return
  fi

  log "Running full VPS cleanup..."

  # Remove swap
  if swapon --show | grep -q "/swapfile"; then
    log "Removing swap..."
    swapoff /swapfile 2>/dev/null || true
    rm -f /swapfile
    sed -i '/swapfile/d' /etc/fstab 2>/dev/null || true
    log "Swap removed"
  fi

  # Remove stilltent user
  if id stilltent &>/dev/null; then
    log "Removing stilltent user..."
    userdel -r stilltent 2>/dev/null || true
    log "User removed"
  fi

  # Remove project directory
  if [[ -d /home/stilltent/stilltent ]]; then
    rm -rf /home/stilltent/stilltent
    log "Project directory removed"
  fi

  log "Full VPS cleanup complete"
}

# ── PaaS teardown ────────────────────────────────────────────────────
teardown_railway() {
  if [[ -z "${RAILWAY_TOKEN:-}" ]]; then
    log "RAILWAY_TOKEN not set — cannot tear down Railway services"
    return
  fi

  log "Tearing down Railway project..."
  if command -v railway &>/dev/null; then
    export RAILWAY_TOKEN
    railway down --yes 2>/dev/null || {
      log "WARN: railway down failed — delete project manually at https://railway.com/dashboard"
    }
  else
    log "Railway CLI not found — delete project at https://railway.com/dashboard"
  fi
}

teardown_render() {
  if [[ -z "${RENDER_API_KEY:-}" ]]; then
    log "RENDER_API_KEY not set — cannot tear down Render services"
    return
  fi

  log "Tearing down Render services..."

  # List and delete stilltent services
  local SERVICES
  SERVICES=$(curl -s -H "Authorization: Bearer ${RENDER_API_KEY}" \
    "https://api.render.com/v1/services?name=stilltent&limit=20" 2>/dev/null || echo "[]")

  echo "$SERVICES" | python3 -c "
import sys, json
services = json.load(sys.stdin)
for svc in services:
    sid = svc.get('service', {}).get('id', svc.get('id', ''))
    name = svc.get('service', {}).get('name', svc.get('name', ''))
    if sid:
        print(f'{sid} {name}')
" 2>/dev/null | while read -r sid name; do
    log "Deleting Render service: $name ($sid)"
    curl -s -X DELETE \
      -H "Authorization: Bearer ${RENDER_API_KEY}" \
      "https://api.render.com/v1/services/${sid}" > /dev/null 2>&1 || true
  done

  # Clean up generated files
  rm -f "$PROJECT_DIR/render.yaml"

  log "Render teardown complete"
}

teardown_heroku() {
  if [[ -z "${HEROKU_API_KEY:-}" ]]; then
    log "HEROKU_API_KEY not set — cannot tear down Heroku app"
    return
  fi

  local APP_NAME="${HEROKU_APP_NAME:-stilltent}"

  log "Tearing down Heroku app '$APP_NAME'..."
  if command -v heroku &>/dev/null; then
    export HEROKU_API_KEY
    heroku apps:destroy "$APP_NAME" --confirm "$APP_NAME" 2>/dev/null || {
      log "WARN: Could not destroy app — delete at https://dashboard.heroku.com"
    }
  else
    # Use API directly
    curl -s -X DELETE \
      -H "Authorization: Bearer ${HEROKU_API_KEY}" \
      -H "Accept: application/vnd.heroku+json; version=3" \
      "https://api.heroku.com/apps/${APP_NAME}" > /dev/null 2>&1 || true
  fi

  # Clean up generated files
  rm -f "$PROJECT_DIR/heroku.yml" "$PROJECT_DIR/Procfile"

  log "Heroku teardown complete"
}

# ── Auto-detect PaaS provider if not specified ───────────────────────
detect_paas_provider() {
  if [[ -n "$PROVIDER" ]]; then
    return
  fi
  # Try to detect from environment
  if [[ -n "${RAILWAY_TOKEN:-}" ]]; then PROVIDER="railway"; fi
  if [[ -n "${RENDER_API_KEY:-}" ]]; then PROVIDER="render"; fi
  if [[ -n "${HEROKU_API_KEY:-}" ]]; then PROVIDER="heroku"; fi
}

# ── Main ─────────────────────────────────────────────────────────────
log "Starting teardown..."

# Always tear down Docker
teardown_docker

# PaaS teardown if provider specified or detected
detect_paas_provider
case "${PROVIDER:-}" in
  railway) teardown_railway ;;
  render)  teardown_render  ;;
  heroku)  teardown_heroku  ;;
  "") ;; # No PaaS provider — VPS or local only
  *) log "Unknown provider: $PROVIDER" ;;
esac

# VPS full cleanup
teardown_vps_full

# Clean workspace artifacts
rm -rf "$PROJECT_DIR/workspace/repo" 2>/dev/null || true
rm -f "$PROJECT_DIR/workspace/PAUSE" 2>/dev/null || true

echo ""
log "Teardown complete."
if [[ "$FULL" == "true" ]]; then
  log "Full cleanup performed (swap, user, project removed)"
else
  log "Use --full for complete VPS cleanup (removes swap, user, Docker)"
fi
