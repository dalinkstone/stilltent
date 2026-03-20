#!/usr/bin/env bash
# =============================================================================
# repokeeper orchestrator
#
# Clones/updates TARGET_REPO into /workspace/repo, configures git/gh auth,
# and optionally loops to trigger the OpenClaw agent on each iteration.
#
# Usage:
#   ./orchestrator/run.sh              # one-shot: sync repo and exit
#   ./orchestrator/run.sh --loop       # loop mode: sync + trigger agent repeatedly
#
# Environment variables:
#   TARGET_REPO             (required) GitHub repo in owner/repo format
#   GITHUB_TOKEN            (required) Fine-grained PAT with repo+workflow scope
#   LOOP_INTERVAL           (optional) Seconds between iterations (default: 60)
#   ITERATION_TIMEOUT       (optional) Max seconds per iteration (default: 600)
#   MAX_CONSECUTIVE_FAILURES (optional) Failures before pausing (default: 10)
#   OPENCLAW_URL            (optional) Gateway base URL (default: http://localhost:18789)
#   OPENCLAW_GATEWAY_TOKEN  (optional) Bearer token for the gateway
#   WORKSPACE_DIR           (optional) Override workspace root (default: /workspace)
# =============================================================================
set -euo pipefail

: "${TARGET_REPO:?TARGET_REPO is required (owner/repo)}"
: "${GITHUB_TOKEN:?GITHUB_TOKEN is required}"

WORKSPACE="${WORKSPACE_DIR:-/workspace}"
REPO_DIR="${WORKSPACE}/repo"
PAUSE_FILE="${WORKSPACE}/PAUSE"
LOOP_INTERVAL="${LOOP_INTERVAL:-60}"
ITERATION_TIMEOUT="${ITERATION_TIMEOUT:-600}"
MAX_CONSECUTIVE_FAILURES="${MAX_CONSECUTIVE_FAILURES:-10}"
OPENCLAW_URL="${OPENCLAW_URL:-http://localhost:18789}"
OPENCLAW_GATEWAY_TOKEN="${OPENCLAW_GATEWAY_TOKEN:-}"
STATS_FILE="${WORKSPACE}/.orchestrator-stats.json"

consecutive_failures=0
total_iterations=0
total_successes=0

log() { echo "[orchestrator] $(date -u +%Y-%m-%dT%H:%M:%SZ) $*"; }

# ── Sync: clone or update the target repo ───────────────────────────
sync_repo() {
    if [ -d "${REPO_DIR}/.git" ]; then
        log "Pulling latest for ${TARGET_REPO}..."
        git -C "${REPO_DIR}" fetch --prune
        git -C "${REPO_DIR}" pull --ff-only || {
            log "pull --ff-only failed (working tree may be dirty), continuing"
        }
    else
        log "Cloning ${TARGET_REPO} into ${REPO_DIR}..."
        mkdir -p "${WORKSPACE}"
        git clone "https://x-access-token:${GITHUB_TOKEN}@github.com/${TARGET_REPO}.git" "${REPO_DIR}"
    fi
}

# ── Configure git identity and gh CLI ────────────────────────────────
setup_auth() {
    git -C "${REPO_DIR}" config user.name "repokeeper[bot]"
    git -C "${REPO_DIR}" config user.email "repokeeper[bot]@users.noreply.github.com"
    echo "${GITHUB_TOKEN}" | gh auth login --with-token 2>/dev/null || true
    gh auth setup-git 2>/dev/null || true
}

# ── Trigger the OpenClaw agent via the gateway chat completions API ──
trigger_agent() {
    local message="Run a maintenance cycle on ${TARGET_REPO}. Sync the repo, triage new issues, review open PRs, check CI status, and log findings to your daily memory file."
    local auth_header=""
    if [ -n "${OPENCLAW_GATEWAY_TOKEN}" ]; then
        auth_header="-H \"Authorization: Bearer ${OPENCLAW_GATEWAY_TOKEN}\""
    fi

    log "Triggering agent..."
    local status
    status=$(curl -s -o /dev/null -w "%{http_code}" \
        -X POST "${OPENCLAW_URL}/v1/chat/completions" \
        -H "Content-Type: application/json" \
        ${auth_header:+-H "Authorization: Bearer ${OPENCLAW_GATEWAY_TOKEN}"} \
        --max-time "${ITERATION_TIMEOUT}" \
        -d "{
            \"model\": \"ollama-local/qwen3:32b\",
            \"messages\": [{\"role\": \"user\", \"content\": $(printf '%s' "$message" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')}]
        }" 2>/dev/null) || status="000"

    if [ "$status" -ge 200 ] && [ "$status" -lt 300 ]; then
        log "Agent responded (HTTP ${status})"
        return 0
    else
        log "Agent request failed (HTTP ${status})"
        return 1
    fi
}

# ── Write stats to a JSON file for stats.py to read ─────────────────
write_stats() {
    cat > "${STATS_FILE}" << EOF
{
    "total_iterations": ${total_iterations},
    "total_successes": ${total_successes},
    "total_failures": $((total_iterations - total_successes)),
    "consecutive_failures": ${consecutive_failures},
    "last_run": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
    "target_repo": "${TARGET_REPO}"
}
EOF
}

# ── Main ─────────────────────────────────────────────────────────────
log "TARGET_REPO=${TARGET_REPO}"
log "REPO_DIR=${REPO_DIR}"

# Always sync and set up auth on first run
sync_repo
setup_auth
log "Workspace ready: ${REPO_DIR}"

# If not in loop mode, exit after setup
if [ "${1:-}" != "--loop" ]; then
    log "One-shot mode complete. Use --loop to run continuously."
    exit 0
fi

# ── Loop mode ────────────────────────────────────────────────────────
log "Entering loop mode (interval=${LOOP_INTERVAL}s, timeout=${ITERATION_TIMEOUT}s)"

while true; do
    # Check for pause file
    if [ -f "${PAUSE_FILE}" ]; then
        log "PAUSED (${PAUSE_FILE} exists). Sleeping ${LOOP_INTERVAL}s..."
        sleep "${LOOP_INTERVAL}"
        continue
    fi

    total_iterations=$((total_iterations + 1))
    log "=== Iteration ${total_iterations} ==="

    # Sync repo before each cycle
    sync_repo

    # Trigger the agent
    if trigger_agent; then
        consecutive_failures=0
        total_successes=$((total_successes + 1))
    else
        consecutive_failures=$((consecutive_failures + 1))
        log "Consecutive failures: ${consecutive_failures}/${MAX_CONSECUTIVE_FAILURES}"

        if [ "${consecutive_failures}" -ge "${MAX_CONSECUTIVE_FAILURES}" ]; then
            log "Too many consecutive failures. Creating pause file."
            touch "${PAUSE_FILE}"
        fi
    fi

    write_stats
    log "Sleeping ${LOOP_INTERVAL}s..."
    sleep "${LOOP_INTERVAL}"
done
