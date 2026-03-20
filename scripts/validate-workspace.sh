#!/usr/bin/env bash
# validate-workspace.sh — Verify the stilltent workspace is correctly set up.
# Exit codes: 0 = all checks passed, 1 = one or more checks failed.

set -euo pipefail

# ---------------------------------------------------------------------------
# Resolve workspace directory
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
WORKSPACE_DIR="$REPO_ROOT/workspace"

# Counters
PASS=0
FAIL=0
WARN=0

pass() { ((PASS++)); printf "  \033[32mPASS\033[0m  %s\n" "$1"; }
fail() { ((FAIL++)); printf "  \033[31mFAIL\033[0m  %s\n" "$1"; }
warn() { ((WARN++)); printf "  \033[33mWARN\033[0m  %s\n" "$1"; }

echo "=== stilltent Workspace Validation ==="
echo "Workspace: $WORKSPACE_DIR"
echo ""

# ---------------------------------------------------------------------------
# 1. Check workspace directory exists and is writable
# ---------------------------------------------------------------------------
if [ -d "$WORKSPACE_DIR" ]; then
    pass "Workspace directory exists"
else
    fail "Workspace directory does not exist: $WORKSPACE_DIR"
    echo ""
    echo "Cannot continue — workspace directory is missing."
    exit 1
fi

if [ -w "$WORKSPACE_DIR" ]; then
    pass "Workspace directory is writable"
else
    fail "Workspace directory is NOT writable"
fi

# ---------------------------------------------------------------------------
# 2. SKILL.md exists
# ---------------------------------------------------------------------------
SKILL_FILE="$WORKSPACE_DIR/SKILL.md"
if [ -f "$SKILL_FILE" ]; then
    pass "SKILL.md exists"
else
    fail "SKILL.md not found at $SKILL_FILE"
fi

# ---------------------------------------------------------------------------
# 3. AGENTS.md exists
# ---------------------------------------------------------------------------
AGENTS_FILE="$WORKSPACE_DIR/AGENTS.md"
if [ -f "$AGENTS_FILE" ]; then
    pass "AGENTS.md exists"
else
    fail "AGENTS.md not found at $AGENTS_FILE"
fi

# ---------------------------------------------------------------------------
# 4. SKILL.md contains expected phase headers (Phase 1 through Phase 7)
# ---------------------------------------------------------------------------
if [ -f "$SKILL_FILE" ]; then
    MISSING_PHASES=()
    for phase_num in 1 2 3 4 5 6 7; do
        # Match "Phase <N>" in any header level, case-insensitive
        if ! grep -qi "phase ${phase_num}" "$SKILL_FILE"; then
            MISSING_PHASES+=("$phase_num")
        fi
    done

    if [ ${#MISSING_PHASES[@]} -eq 0 ]; then
        pass "SKILL.md contains all phase headers (Phase 1–7)"
    else
        fail "SKILL.md is missing phase(s): ${MISSING_PHASES[*]}"
    fi
fi

# ---------------------------------------------------------------------------
# 5. AGENTS.md contains "Hard Limits" section
# ---------------------------------------------------------------------------
if [ -f "$AGENTS_FILE" ]; then
    if grep -qi "hard limits" "$AGENTS_FILE"; then
        pass "AGENTS.md contains 'Hard Limits' section"
    else
        fail "AGENTS.md does not contain a 'Hard Limits' section"
    fi
fi

# ---------------------------------------------------------------------------
# 6. Check for stale PAUSE file
# ---------------------------------------------------------------------------
PAUSE_FILE="$WORKSPACE_DIR/PAUSE"
if [ -f "$PAUSE_FILE" ]; then
    PAUSE_AGE=""
    if stat -f %m "$PAUSE_FILE" >/dev/null 2>&1; then
        # macOS stat
        PAUSE_EPOCH=$(stat -f %m "$PAUSE_FILE")
        NOW_EPOCH=$(date +%s)
        AGE_SECS=$((NOW_EPOCH - PAUSE_EPOCH))
        PAUSE_AGE=" (age: $((AGE_SECS / 60))m $((AGE_SECS % 60))s)"
    elif stat -c %Y "$PAUSE_FILE" >/dev/null 2>&1; then
        # GNU/Linux stat
        PAUSE_EPOCH=$(stat -c %Y "$PAUSE_FILE")
        NOW_EPOCH=$(date +%s)
        AGE_SECS=$((NOW_EPOCH - PAUSE_EPOCH))
        PAUSE_AGE=" (age: $((AGE_SECS / 60))m $((AGE_SECS % 60))s)"
    fi
    warn "PAUSE file exists${PAUSE_AGE} — agent is paused. Remove with: make resume"
else
    pass "No stale PAUSE file"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "=== Results ==="
printf "  Passed: %d  |  Failed: %d  |  Warnings: %d\n" "$PASS" "$FAIL" "$WARN"
echo ""

if [ "$FAIL" -gt 0 ]; then
    printf "\033[31mWorkspace validation FAILED.\033[0m\n"
    exit 1
else
    printf "\033[32mWorkspace validation PASSED.\033[0m\n"
    exit 0
fi
