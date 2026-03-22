#!/bin/bash
# dev-loop.sh — Continuous Claude Code autonomous development loop
#
# Runs Claude Code in non-interactive mode, one iteration after another,
# forever. Each iteration: pulls, builds a feature, commits, pushes.
#
# Usage:
#   ./scripts/dev-loop.sh              # run forever
#   ./scripts/dev-loop.sh --once       # single iteration (for testing)
#   ./scripts/dev-loop.sh --model opus # use specific model
#
# Control:
#   Pause:   touch PAUSE       (loop checks before each iteration)
#   Resume:  rm PAUSE
#   Stop:    Ctrl+C or kill
#
# Run in tmux so it survives SSH disconnect:
#   tmux new -s devloop './scripts/dev-loop.sh'

set -uo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
PROJECT_DIR="$REPO_DIR/project"
AGENT_PROMPT="$REPO_DIR/config/claude-agent/AGENT.md"
LOG_DIR="$REPO_DIR/scripts/loop-logs"
PAUSE_FILE="$REPO_DIR/PAUSE"
ONCE=false
MODEL=""
COOLDOWN=10  # seconds between iterations — just enough for git to settle

mkdir -p "$LOG_DIR"

# Parse args
while [[ $# -gt 0 ]]; do
    case "$1" in
        --once) ONCE=true; shift ;;
        --model) MODEL="$2"; shift 2 ;;
        *) shift ;;
    esac
done

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# ── Auth check ───────────────────────────────────────────────────────

check_auth() {
    claude -p "say OK" --max-turns 1 --no-session-persistence --model "${MODEL:-opus}" 2>/dev/null | grep -qi "OK" 2>/dev/null
}

wait_for_auth() {
    echo -e "${RED}=== Claude Code authentication failed ===${NC}"
    echo ""
    echo "The session has expired or Claude Code is not logged in."
    echo ""
    echo "To fix: open ANOTHER terminal, SSH into this machine, and run:"
    echo ""
    echo -e "  ${GREEN}claude login${NC}"
    echo ""
    echo "Follow the URL in your local browser. The loop will resume automatically."
    echo ""

    while true; do
        sleep 30
        echo -n "  Checking auth... "
        if check_auth; then
            echo -e "${GREEN}OK — resuming.${NC}"
            echo ""
            return 0
        else
            echo "still expired."
        fi
    done
}

check_gh_auth() {
    gh auth status &>/dev/null 2>&1
}

# ── Build Claude Code command ────────────────────────────────────────

build_claude_cmd() {
    local cmd="claude -p"

    # The prompt is read from the agent file and passed as the system prompt.
    # The actual -p prompt is just "go" — the agent file has all instructions.
    cmd="$cmd \"Read config/claude-agent/AGENT.md and follow its instructions exactly. This is iteration $ITERATION.\""

    # Use the agent prompt as appended system context
    cmd="$cmd --append-system-prompt-file $AGENT_PROMPT"

    # Allow all the tools Claude Code needs
    cmd="$cmd --allowedTools \"Bash,Read,Write,Edit,Glob,Grep\""

    # No session persistence — each iteration is independent
    cmd="$cmd --no-session-persistence"

    # Model override if specified
    if [[ -n "$MODEL" ]]; then
        cmd="$cmd --model $MODEL"
    fi

    echo "$cmd"
}

# ── Main loop ────────────────────────────────────────────────────────

ITERATION=0
TOTAL_FEATS=0
TOTAL_FAILURES=0
START_TIMESTAMP=$(date +%s)

echo -e "${CYAN}══════════════════════════════════════${NC}"
echo -e "${CYAN}  tent dev-loop (Claude Code)${NC}"
echo -e "${CYAN}══════════════════════════════════════${NC}"
echo "  Repo:    $REPO_DIR"
echo "  Logs:    $LOG_DIR"
echo "  Prompt:  $AGENT_PROMPT"
echo "  Model:   ${MODEL:-default}"
echo "  Pause:   touch $PAUSE_FILE"
echo "  Stop:    Ctrl+C"
echo ""

# Initial auth check
echo -n "Checking Claude Code auth... "
if check_auth; then
    echo -e "${GREEN}OK${NC}"
else
    wait_for_auth
fi

echo -n "Checking GitHub CLI auth... "
if check_gh_auth; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}FAILED${NC}"
    echo "Run: gh auth login"
    echo "Or:  echo \"\$(grep GITHUB_TOKEN .env | cut -d= -f2)\" | gh auth login --with-token"
    exit 1
fi

echo ""

while true; do
    ITERATION=$((ITERATION + 1))
    TIMESTAMP=$(date +%Y%m%d-%H%M%S)
    LOGFILE="$LOG_DIR/iteration-${ITERATION}-${TIMESTAMP}.log"

    # ── Pause check ──────────────────────────────────────────────
    if [[ -f "$PAUSE_FILE" ]]; then
        echo -e "${YELLOW}⏸  Paused. Remove $PAUSE_FILE to resume.${NC}"
        while [[ -f "$PAUSE_FILE" ]]; do
            sleep 5
        done
        echo -e "${GREEN}▶  Resumed.${NC}"
    fi

    # ── Auth check (every 20 iterations to save tokens) ──────────
    if (( ITERATION % 20 == 1 )); then
        if ! check_auth; then
            wait_for_auth
        fi
    fi

    echo -e "${CYAN}── Iteration $ITERATION ──────────────────────── $(date '+%H:%M:%S') ──${NC}"

    # ── Pull latest ──────────────────────────────────────────────
    cd "$REPO_DIR"
    git pull origin main --ff-only 2>&1 | grep -v "Already up to date" | grep -v "^$" || true

    COMMIT_BEFORE=$(git rev-parse --short HEAD 2>/dev/null)

    # ── Run Claude Code ──────────────────────────────────────────
    ITER_START=$(date +%s)

    # Build and run the command
    timeout 1800 claude -p \
        "Follow the instructions in config/claude-agent/AGENT.md exactly. This is iteration $ITERATION. Build the next feature." \
        --append-system-prompt-file "$AGENT_PROMPT" \
        --allowedTools "Bash,Read,Write,Edit,Glob,Grep" \
        --model "${MODEL:-opus}" \
        --no-session-persistence \
        2>&1 | tee "$LOGFILE" || true

    ITER_END=$(date +%s)
    ITER_DURATION=$(( ITER_END - ITER_START ))

    # ── Check results ────────────────────────────────────────────
    COMMIT_AFTER=$(git rev-parse --short HEAD 2>/dev/null)

    if [[ "$COMMIT_BEFORE" != "$COMMIT_AFTER" ]]; then
        NEW_COMMITS=$(git log --oneline "$COMMIT_BEFORE".."$COMMIT_AFTER" 2>/dev/null)
        FEAT_COUNT=$(echo "$NEW_COMMITS" | grep -c "feat:" || true)
        TOTAL_FEATS=$((TOTAL_FEATS + FEAT_COUNT))

        echo -e "${GREEN}  Shipped ($FEAT_COUNT feat):${NC}"
        echo "$NEW_COMMITS" | while read -r line; do echo "    $line"; done
    else
        echo -e "${YELLOW}  No commits this iteration.${NC}"
        TOTAL_FAILURES=$((TOTAL_FAILURES + 1))

        # Check for auth failure
        if grep -qi "unauthorized\|session expired\|login required\|Could not authenticate\|not logged in" "$LOGFILE" 2>/dev/null; then
            wait_for_auth
        fi

        # Check for rate limiting
        if grep -qi "rate limit\|rate_limit\|429\|too many requests\|overloaded\|capacity" "$LOGFILE" 2>/dev/null; then
            echo -e "${YELLOW}  Rate limited. Waiting 5 minutes before retrying...${NC}"
            sleep 300
        fi
    fi

    # ── Stats line ───────────────────────────────────────────────
    ELAPSED=$(( $(date +%s) - START_TIMESTAMP ))
    ELAPSED_MIN=$(( ELAPSED / 60 ))
    echo -e "${CYAN}  ${ITER_DURATION}s | feats: $TOTAL_FEATS | no-ops: $TOTAL_FAILURES | uptime: ${ELAPSED_MIN}m | log: $(basename $LOGFILE)${NC}"
    echo ""

    if $ONCE; then
        echo "Single iteration complete (--once)."
        exit 0
    fi

    # Brief cooldown for git operations to settle
    sleep "$COOLDOWN"
done
