#!/usr/bin/env bash
# deploy-paas.sh — Deploy stilltent to Railway, Render, or Heroku
# Usage: deploy-paas.sh [--provider railway|render|heroku]
#
# Auto-detects provider from environment variables if --provider not given:
#   RAILWAY_TOKEN  → Railway
#   RENDER_API_KEY → Render
#   HEROKU_API_KEY → Heroku
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
PROVIDER=""

log()  { echo "[deploy-paas] $*"; }
die()  { echo "[deploy-paas] ERROR: $*" >&2; exit 1; }

# ── Parse args ───────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --provider) PROVIDER="$2"; shift 2 ;;
    *) die "Unknown argument: $1" ;;
  esac
done

# ── Auto-detect provider ────────────────────────────────────────────
if [[ -z "$PROVIDER" ]]; then
  if [[ -n "${RAILWAY_TOKEN:-}" ]]; then
    PROVIDER="railway"
  elif [[ -n "${RENDER_API_KEY:-}" ]]; then
    PROVIDER="render"
  elif [[ -n "${HEROKU_API_KEY:-}" ]]; then
    PROVIDER="heroku"
  else
    die "No provider detected. Set RAILWAY_TOKEN, RENDER_API_KEY, or HEROKU_API_KEY, or use --provider"
  fi
fi

log "Deploying to: $PROVIDER"

# ══════════════════════════════════════════════════════════════════════
# Railway
# ══════════════════════════════════════════════════════════════════════
deploy_railway() {
  [[ -n "${RAILWAY_TOKEN:-}" ]] || die "RAILWAY_TOKEN is required"

  # Install Railway CLI if needed
  if ! command -v railway &>/dev/null; then
    log "Installing Railway CLI..."
    npm install -g @railway/cli 2>/dev/null \
      || curl -fsSL https://railway.com/install.sh | sh
  fi

  export RAILWAY_TOKEN

  cd "$PROJECT_DIR"

  # Generate railway.toml
  log "Generating railway.toml..."
  cat > railway.toml <<'TOML'
[build]
builder = "DOCKERFILE"

[deploy]
healthcheckPath = "/healthz"
healthcheckTimeout = 300
restartPolicyType = "ON_FAILURE"
restartPolicyMaxRetries = 5

# TiDB service
[[services]]
name = "tidb"
source = { image = "pingcap/tidb:v8.4.0" }
internalPort = 4000
variables = {}

[services.deploy]
healthcheckPath = "/status"
healthcheckTimeout = 180

# Embed service
[[services]]
name = "embed-service"
source = { dockerfile = "memory/mem9/embed-service/Dockerfile" }
internalPort = 8090

[services.deploy]
healthcheckPath = "/health"

# Mnemo server (mem9)
[[services]]
name = "mnemo-server"
source = { dockerfile = "memory/mem9/mnemo-server/server/Dockerfile" }
internalPort = 8082

[services.deploy]
healthcheckPath = "/health"

# OpenClaw gateway
[[services]]
name = "openclaw-gateway"
source = { dockerfile = "dockerfiles/openclaw.Dockerfile" }
internalPort = 18789

[services.deploy]
healthcheckPath = "/healthz"

# Orchestrator
[[services]]
name = "orchestrator"
source = { dockerfile = "core/orchestrator/Dockerfile" }
TOML

  # Link to project or create one
  if ! railway status &>/dev/null; then
    log "Creating Railway project..."
    railway init --name stilltent
  fi

  # Deploy
  log "Deploying to Railway..."
  railway up --detach

  # Wait for deployment and get URL
  log "Waiting for deployment..."
  sleep 10
  local DEPLOY_URL
  DEPLOY_URL=$(railway status --json 2>/dev/null \
    | python3 -c "import sys, json; d=json.load(sys.stdin); print(d.get('deploymentDomain', 'unknown'))" \
    2>/dev/null || echo "check Railway dashboard")

  # Health check
  log "Running health check..."
  sleep 30
  if curl -sf "https://${DEPLOY_URL}/healthz" &>/dev/null; then
    log "Health check: PASSED"
  else
    log "Health check: gateway not yet reachable (may still be starting)"
  fi

  echo ""
  echo "╔══════════════════════════════════════════════════╗"
  echo "║   Railway Deployment Complete                    ║"
  echo "╠══════════════════════════════════════════════════╣"
  echo "║  URL: https://$DEPLOY_URL"
  echo "║  Dashboard: https://railway.com/dashboard"
  echo "╚══════════════════════════════════════════════════╝"

  # Clean up generated file
  rm -f railway.toml
}

# ══════════════════════════════════════════════════════════════════════
# Render
# ══════════════════════════════════════════════════════════════════════
deploy_render() {
  [[ -n "${RENDER_API_KEY:-}" ]] || die "RENDER_API_KEY is required"

  cd "$PROJECT_DIR"

  # Generate render.yaml (Render Blueprint)
  log "Generating render.yaml..."
  cat > render.yaml <<YAML
# Render Blueprint — stilltent
# Push this file to trigger Render's Blueprint deploy
services:
  - type: private
    name: tidb
    runtime: image
    image:
      url: pingcap/tidb:v8.4.0
    envVars:
      - key: PORT
        value: "4000"
    healthCheckPath: /status

  - type: private
    name: embed-service
    runtime: docker
    dockerfilePath: memory/mem9/embed-service/Dockerfile
    envVars:
      - key: EMBED_PORT
        value: "8090"
      - key: EMBED_DIMS
        value: "256"
    healthCheckPath: /health

  - type: private
    name: mnemo-server
    runtime: docker
    dockerfilePath: memory/mem9/mnemo-server/server/Dockerfile
    envVars:
      - key: MNEMO_DSN
        fromService:
          name: tidb
          type: private
          property: host
      - key: EMBED_BASE_URL
        fromService:
          name: embed-service
          type: private
          property: hostport
      - key: MEM9_API_KEY
        fromGroup: stilltent-secrets
    healthCheckPath: /health

  - type: web
    name: openclaw-gateway
    runtime: docker
    dockerfilePath: dockerfiles/openclaw.Dockerfile
    envVars:
      - key: OPENROUTER_API_KEY
        fromGroup: stilltent-secrets
      - key: OPENCLAW_GATEWAY_TOKEN
        fromGroup: stilltent-secrets
    healthCheckPath: /healthz

  - type: worker
    name: orchestrator
    runtime: docker
    dockerfilePath: core/orchestrator/Dockerfile
    envVars:
      - key: AGENT_URL
        fromService:
          name: openclaw-gateway
          type: web
          property: hostport
      - key: LOOP_INTERVAL
        value: "60"

envVarGroups:
  - name: stilltent-secrets
    envVars:
      - key: OPENROUTER_API_KEY
        sync: false
      - key: OPENCLAW_GATEWAY_TOKEN
        sync: false
      - key: MEM9_API_KEY
        sync: false
      - key: GITHUB_TOKEN
        sync: false
YAML

  log "render.yaml generated."
  log ""
  log "To deploy on Render:"
  log "  1. Push render.yaml to your repo"
  log "  2. Go to https://dashboard.render.com → New Blueprint Instance"
  log "  3. Connect your repo and Render will pick up render.yaml"
  log "  4. Set secrets in the 'stilltent-secrets' env group"
  log ""

  # If Render CLI is available, trigger deploy
  if command -v render &>/dev/null; then
    log "Render CLI detected — triggering deploy..."
    render blueprint launch --yes 2>/dev/null || {
      log "Blueprint launch requires manual trigger via dashboard"
    }
  fi

  # Verify via API
  log "Checking services via Render API..."
  local SERVICES
  SERVICES=$(curl -s -H "Authorization: Bearer ${RENDER_API_KEY}" \
    "https://api.render.com/v1/services?name=stilltent&limit=10" 2>/dev/null || echo "[]")

  local COUNT
  COUNT=$(echo "$SERVICES" | python3 -c "import sys, json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")

  echo ""
  echo "╔══════════════════════════════════════════════════╗"
  echo "║   Render Deployment                              ║"
  echo "╠══════════════════════════════════════════════════╣"
  echo "║  Blueprint:  render.yaml generated"
  echo "║  Services:   $COUNT found via API"
  echo "║  Dashboard:  https://dashboard.render.com"
  echo "╚══════════════════════════════════════════════════╝"
}

# ══════════════════════════════════════════════════════════════════════
# Heroku
# ══════════════════════════════════════════════════════════════════════
deploy_heroku() {
  [[ -n "${HEROKU_API_KEY:-}" ]] || die "HEROKU_API_KEY is required"

  # Install Heroku CLI if needed
  if ! command -v heroku &>/dev/null; then
    log "Installing Heroku CLI..."
    curl -fsSL https://cli-assets.heroku.com/install.sh | sh
  fi

  export HEROKU_API_KEY

  cd "$PROJECT_DIR"

  local APP_NAME="${HEROKU_APP_NAME:-stilltent}"

  # Create app if it doesn't exist
  if ! heroku apps:info "$APP_NAME" &>/dev/null; then
    log "Creating Heroku app '$APP_NAME'..."
    heroku create "$APP_NAME" --stack container
  fi

  # Generate heroku.yml
  log "Generating heroku.yml..."
  cat > heroku.yml <<YAML
# Heroku container deployment — stilltent
build:
  docker:
    gateway:
      dockerfile: dockerfiles/openclaw.Dockerfile
    orchestrator:
      dockerfile: core/orchestrator/Dockerfile
    embed:
      dockerfile: memory/mem9/embed-service/Dockerfile
    mnemo:
      dockerfile: memory/mem9/mnemo-server/server/Dockerfile

run:
  gateway:
    command:
      - node
      - dist/index.js
  orchestrator:
    command:
      - python3
      - main.py
  embed:
    command:
      - ./embed-service
  mnemo:
    command:
      - ./mnemo-server
YAML

  # Generate Procfile for process types
  log "Generating Procfile..."
  cat > Procfile <<'PROC'
web: node dist/index.js
worker: python3 core/orchestrator/main.py
PROC

  # Heroku uses Postgres instead of TiDB — set adapter env var
  log "Configuring Heroku addons..."

  # Add Heroku Postgres if not present
  if ! heroku addons:info heroku-postgresql --app "$APP_NAME" &>/dev/null; then
    heroku addons:create heroku-postgresql:essential-0 --app "$APP_NAME" 2>/dev/null || {
      log "WARN: Could not add Postgres addon (may need billing)"
    }
  fi

  # Set memory backend to use Postgres adapter
  heroku config:set \
    MEMORY_BACKEND=postgres \
    HEROKU_MODE=true \
    --app "$APP_NAME" 2>/dev/null || true

  # Set environment variables from .env
  if [[ -f .env ]]; then
    log "Pushing env vars from .env..."
    while IFS='=' read -r key value; do
      # Skip comments and empty lines
      [[ "$key" =~ ^#.*$ || -z "$key" ]] && continue
      # Skip TiDB vars (using Postgres on Heroku)
      [[ "$key" =~ ^TIDB_ ]] && continue
      heroku config:set "${key}=${value}" --app "$APP_NAME" 2>/dev/null || true
    done < .env
  fi

  # Set stack to container
  heroku stack:set container --app "$APP_NAME" 2>/dev/null || true

  # Deploy
  log "Deploying to Heroku..."
  git push heroku main 2>/dev/null || {
    # Add heroku remote if needed
    heroku git:remote --app "$APP_NAME" 2>/dev/null || true
    git push heroku main 2>/dev/null || {
      log "WARN: git push failed — ensure heroku remote is configured"
    }
  }

  # Scale dynos
  log "Scaling dynos..."
  heroku ps:scale web=1 worker=1 --app "$APP_NAME" 2>/dev/null || true

  # Health check
  sleep 15
  local APP_URL
  APP_URL=$(heroku apps:info "$APP_NAME" --json 2>/dev/null \
    | python3 -c "import sys, json; print(json.load(sys.stdin).get('app', {}).get('web_url', 'unknown'))" \
    2>/dev/null || echo "https://${APP_NAME}.herokuapp.com")

  if curl -sf "${APP_URL}healthz" &>/dev/null; then
    log "Health check: PASSED"
  else
    log "Health check: not yet reachable (may still be starting)"
  fi

  echo ""
  echo "╔══════════════════════════════════════════════════╗"
  echo "║   Heroku Deployment Complete                     ║"
  echo "╠══════════════════════════════════════════════════╣"
  echo "║  App:        $APP_NAME"
  echo "║  URL:        $APP_URL"
  echo "║  Memory:     Heroku Postgres (adapter mode)"
  echo "║  Dashboard:  https://dashboard.heroku.com/apps/$APP_NAME"
  echo "╚══════════════════════════════════════════════════╝"
  echo ""
  echo "NOTE: Heroku uses Postgres instead of TiDB for memory storage."
  echo "Vector search uses pgvector extension. Set MEMORY_BACKEND=postgres"
  echo "in your Heroku config to activate the Postgres adapter."

  # Clean up generated files
  rm -f heroku.yml Procfile
}

# ── Dispatch ─────────────────────────────────────────────────────────
case "$PROVIDER" in
  railway) deploy_railway ;;
  render)  deploy_render  ;;
  heroku)  deploy_heroku  ;;
  *) die "Unknown provider: $PROVIDER (use railway, render, or heroku)" ;;
esac
