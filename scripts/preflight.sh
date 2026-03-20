#!/usr/bin/env bash
# preflight.sh — Final pre-flight check before first orchestrator run.
#
# Starts the stack (minus orchestrator), runs every health check and
# validation, then reports a go/no-go summary.
#
# Exit codes: 0 = all checks passed, 1 = one or more checks failed.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

# Export .env so child processes (python scripts, etc.) inherit the values.
if [ -f .env ]; then
    set -a
    source .env
    set +a
fi

# ── Helpers ──────────────────────────────────────────────────────────────────

STEP=0
PASS=0
FAIL=0

step() {
    ((STEP++))
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    printf "  STEP %d: %s\n" "$STEP" "$1"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

check_result() {
    local name="$1"
    local exit_code="$2"
    if [ "$exit_code" -eq 0 ]; then
        ((PASS++))
        printf "\n  \033[32m✓ %s PASSED\033[0m\n" "$name"
    else
        ((FAIL++))
        printf "\n  \033[31m✗ %s FAILED\033[0m\n" "$name"
    fi
}

# ── Banner ───────────────────────────────────────────────────────────────────

echo ""
echo "============================================================"
echo "  stilltent — Pre-flight Check"
echo "  $(date '+%Y-%m-%d %H:%M:%S')"
echo "============================================================"

# ── Step 1: Start the stack ──────────────────────────────────────────────────

step "Start the stack"
echo "  Running: docker compose up -d"
docker compose up -d
echo ""
echo "  Container status:"
docker compose ps
echo ""

# Verify all four core services are running
EXPECTED_SERVICES=("tidb" "mnemo-server" "openclaw-gateway" "orchestrator")
ALL_UP=0
for svc in "${EXPECTED_SERVICES[@]}"; do
    if docker compose ps --status running --format '{{.Service}}' | grep -q "^${svc}$"; then
        printf "  %-25s \033[32mrunning\033[0m\n" "$svc"
    else
        printf "  %-25s \033[31mnot running\033[0m\n" "$svc"
        ALL_UP=1
    fi
done
check_result "All services running" "$ALL_UP"

# ── Step 2: Stop orchestrator (not needed for pre-flight) ────────────────────

step "Stop orchestrator (not needed yet)"
echo "  Running: docker compose stop orchestrator"
docker compose stop orchestrator
printf "  \033[33mOrchestrator stopped — will be started after pre-flight passes.\033[0m\n"

# ── Step 3: Health checks ───────────────────────────────────────────────────

step "Health checks"
set +e
bash scripts/health-check.sh
HEALTH_EXIT=$?
set -e
check_result "Health checks" "$HEALTH_EXIT"

# ── Step 4: Workspace validation ─────────────────────────────────────────────

step "Workspace validation"
set +e
bash scripts/validate-workspace.sh
VALIDATE_EXIT=$?
set -e
check_result "Workspace validation" "$VALIDATE_EXIT"

# ── Step 5: mem9 API smoke test ──────────────────────────────────────────────

step "mem9 API smoke test"
set +e
python3 scripts/test-mem9.py
MEM9_EXIT=$?
set -e
check_result "mem9 smoke test" "$MEM9_EXIT"

# ── Step 6: OpenClaw gateway smoke test ──────────────────────────────────────

step "OpenClaw gateway smoke test"
set +e
python3 scripts/test-openclaw.py
OPENCLAW_EXIT=$?
set -e
check_result "OpenClaw smoke test" "$OPENCLAW_EXIT"

# ── Summary ──────────────────────────────────────────────────────────────────

TOTAL=$((PASS + FAIL))

echo ""
echo "============================================================"
echo "  Pre-flight Summary"
echo "============================================================"
printf "  Passed: %d / %d\n" "$PASS" "$TOTAL"
echo ""

if [ "$FAIL" -gt 0 ]; then
    printf "  \033[31mPRE-FLIGHT FAILED — fix the issues above before continuing.\033[0m\n"
    echo ""
    echo "  Tip: re-run with  make preflight  after fixing."
    echo "============================================================"
    exit 1
else
    printf "  \033[32mPRE-FLIGHT PASSED — all systems go.\033[0m\n"
    echo ""
    echo "  Next step: start the orchestrator with"
    echo "    docker compose up -d orchestrator"
    echo "============================================================"
    exit 0
fi
