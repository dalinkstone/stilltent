#!/bin/bash
# dev-loop.sh — Continuous Claude Code development loop
#
# Runs Claude Code non-interactively in a loop. Each iteration:
#   1. Pulls latest from main
#   2. Checks for agent-fix issues
#   3. Runs Claude Code to implement the next feature
#   4. Pushes results
#   5. Immediately starts the next iteration (no delay)
#
# Usage:
#   ./scripts/dev-loop.sh          # run forever
#   ./scripts/dev-loop.sh --once   # single iteration (for testing)
#
# Stop: Ctrl+C or kill the process
# Pause: touch ~/stilltent/PAUSE (loop checks each iteration)
# Resume: rm ~/stilltent/PAUSE

set -uo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
PROJECT_DIR="$REPO_DIR/project"
LOG_DIR="$REPO_DIR/scripts/loop-logs"
PAUSE_FILE="$REPO_DIR/PAUSE"
AUTH_FAIL_FILE="$REPO_DIR/.auth-failed"
ONCE=false

mkdir -p "$LOG_DIR"

if [[ "${1:-}" == "--once" ]]; then
    ONCE=true
fi

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# ── The prompt that drives each iteration ────────────────────────────

read -r -d '' PROMPT << 'PROMPTEOF' || true
You are building "tent" — a microVM sandbox runtime for AI workloads. The project is in the project/ directory.

CRITICAL RULES:
- ONLY produce feat: commits. No docs, tests, refactors, or summaries.
- ALL code must compile. Run: cd project && go build -o tent ./cmd/tent
- Do NOT create markdown files, iteration logs, or architecture docs.
- Do NOT modify files outside project/ (no workspace/, scripts/, config/).

DO THIS NOW:
1. First, check for agent-fix issues that need fixing:
   gh issue list --repo dalinkstone/stilltent --label agent-fix --state open --limit 5
   If any exist, read the issue body (gh issue view <N> --repo dalinkstone/stilltent), fix it, and close it when done.

2. If no issues, look at what needs building:
   - Read project/README.md for the full spec
   - Check what directories exist: ls project/internal/
   - The spec requires: hypervisor/, sandbox/, virtio/, boot/, image/, network/ (with policy.go and vmnet_darwin.go), compose/, storage/, config/, state/
   - Find what's missing or incomplete and implement it

3. Write the code. Build it to verify: cd project && go build -o tent ./cmd/tent

4. If it compiles, commit and push:
   git add project/
   git commit -m "feat: <what you built>"
   git push origin main

5. If you fixed an agent-fix issue, close it:
   gh issue comment <N> --repo dalinkstone/stilltent --body "Fixed in commit $(git rev-parse --short HEAD). <summary>"
   gh issue close <N> --repo dalinkstone/stilltent

FOCUS: Make tent actually work on macOS. The owner needs to run:
  tent create mybox --from ubuntu:22.04 && tent start mybox
Every feature you build should get closer to that goal.
PROMPTEOF

# ── Auth check ───────────────────────────────────────────────────────

check_auth() {
    # Quick auth check — run a trivial prompt
    if claude -p "respond with OK" --max-turns 1 &>/dev/null 2>&1; then
        rm -f "$AUTH_FAIL_FILE"
        return 0
    else
        return 1
    fi
}

wait_for_auth() {
    echo -e "${RED}=== Authentication failed ===${NC}"
    echo ""
    echo "Claude Code is not authenticated or the session expired."
    echo ""
    echo "To fix this, open a NEW terminal and SSH into the VPS:"
    echo "  ssh root@$(curl -s ifconfig.me 2>/dev/null || echo '<VPS_IP>')"
    echo ""
    echo "Then run:"
    echo "  claude login"
    echo ""
    echo "Follow the URL it gives you, authenticate in your browser,"
    echo "then come back here. The loop will resume automatically."
    echo ""

    touch "$AUTH_FAIL_FILE"

    # Poll until auth works again
    while true; do
        sleep 30
        echo -n "Checking auth... "
        if check_auth; then
            echo -e "${GREEN}OK! Resuming loop.${NC}"
            rm -f "$AUTH_FAIL_FILE"
            return 0
        else
            echo "still failing. Waiting 30s..."
        fi
    done
}

# ── Main loop ────────────────────────────────────────────────────────

ITERATION=0
TOTAL_FEATS=0

echo -e "${CYAN}=== tent dev-loop ===${NC}"
echo "Project: $PROJECT_DIR"
echo "Logs:    $LOG_DIR"
echo "Pause:   touch $PAUSE_FILE"
echo "Stop:    Ctrl+C"
echo ""

while true; do
    ITERATION=$((ITERATION + 1))
    TIMESTAMP=$(date +%Y%m%d-%H%M%S)
    LOGFILE="$LOG_DIR/iteration-${ITERATION}-${TIMESTAMP}.log"

    # ── Check for pause file ─────────────────────────────────────
    if [[ -f "$PAUSE_FILE" ]]; then
        echo -e "${YELLOW}Paused (remove $PAUSE_FILE to resume)${NC}"
        while [[ -f "$PAUSE_FILE" ]]; do
            sleep 10
        done
        echo -e "${GREEN}Resumed.${NC}"
    fi

    echo -e "${CYAN}=== Iteration $ITERATION starting at $(date) ===${NC}"

    # ── Check auth ───────────────────────────────────────────────
    if ! check_auth; then
        wait_for_auth
    fi

    # ── Pull latest ──────────────────────────────────────────────
    cd "$REPO_DIR"
    git pull origin main --ff-only 2>&1 || true

    # ── Record state before ──────────────────────────────────────
    COMMIT_BEFORE=$(git rev-parse HEAD 2>/dev/null || echo "none")

    # ── Run Claude Code ──────────────────────────────────────────
    echo "Running Claude Code..."
    START_TIME=$(date +%s)

    # Run claude with the prompt, capture output
    # --allowedTools restricts to safe tools
    # Timeout after 30 minutes to prevent runaway sessions
    cd "$REPO_DIR"
    timeout 1800 claude -p "$PROMPT" \
        --allowedTools "Bash,Read,Write,Edit,Glob,Grep" \
        2>&1 | tee "$LOGFILE" || true

    END_TIME=$(date +%s)
    DURATION=$(( END_TIME - START_TIME ))

    # ── Check what happened ──────────────────────────────────────
    COMMIT_AFTER=$(git rev-parse HEAD 2>/dev/null || echo "none")

    if [[ "$COMMIT_BEFORE" != "$COMMIT_AFTER" ]]; then
        # New commits were made
        NEW_COMMITS=$(git log --oneline "$COMMIT_BEFORE".."$COMMIT_AFTER" 2>/dev/null | head -5)
        FEAT_COUNT=$(echo "$NEW_COMMITS" | grep -c "^[a-f0-9]* feat:" || true)
        TOTAL_FEATS=$((TOTAL_FEATS + FEAT_COUNT))

        echo -e "${GREEN}Shipped:${NC}"
        echo "$NEW_COMMITS" | while read -r line; do echo "  $line"; done
        echo ""
    else
        echo -e "${YELLOW}No commits this iteration.${NC}"

        # Check if auth failed during the run
        if grep -qi "unauthorized\|authentication\|login required\|session expired" "$LOGFILE" 2>/dev/null; then
            echo -e "${RED}Auth may have expired during this iteration.${NC}"
            wait_for_auth
        fi
    fi

    echo -e "${CYAN}Duration: ${DURATION}s | Total feats shipped: $TOTAL_FEATS | Log: $LOGFILE${NC}"
    echo ""

    if $ONCE; then
        echo "Single iteration complete (--once mode)."
        exit 0
    fi

    # No sleep — immediately start next iteration
done
