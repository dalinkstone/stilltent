#!/usr/bin/env bash
# monitor.sh — Status dashboard for stilltent running on a VPS.
# Prints system health, agent metrics, estimated cost, and recent activity.
#
# Usage:
#   bash scripts/monitor.sh
#   make monitor
set -euo pipefail

WORKSPACE="${WORKSPACE_DIR:-workspace}"
METRICS_FILE="${WORKSPACE}/metrics.json"
ORCH_LOG="${WORKSPACE}/orchestrator.log"
REPO_DIR="${WORKSPACE}/repo"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

section() {
    echo ""
    echo "==========================================="
    echo "  $1"
    echo "==========================================="
}

# Read a key from metrics.json (requires jq)
metric() {
    local key="$1"
    local default="${2:-}"
    if [ -f "$METRICS_FILE" ] && command -v jq &>/dev/null; then
        jq -r ".$key // \"$default\"" "$METRICS_FILE" 2>/dev/null || echo "$default"
    else
        echo "$default"
    fi
}

# ---------------------------------------------------------------------------
# 1. SYSTEM STATUS
# ---------------------------------------------------------------------------

section "SYSTEM STATUS"

echo ""
echo "  Uptime:       $(uptime -p 2>/dev/null || uptime | sed 's/.*up /up /' | sed 's/,.*load.*//')"
echo ""
echo "  Disk usage:"
df -h / 2>/dev/null | awk 'NR==2 {printf "    Total: %s  Used: %s  Avail: %s  Use%%: %s\n", $2, $3, $4, $5}'
echo ""
echo "  Memory:"
if command -v free &>/dev/null; then
    free -h | awk '/^Mem:/ {printf "    Total: %s  Used: %s  Free: %s\n", $2, $3, $4}'
else
    # macOS fallback
    vm_stat 2>/dev/null | awk '
        /Pages free/     { free=$NF }
        /Pages active/   { active=$NF }
        /Pages inactive/ { inactive=$NF }
        /Pages wired/    { wired=$NF }
        END {
            gsub(/\./,"",free); gsub(/\./,"",active)
            gsub(/\./,"",inactive); gsub(/\./,"",wired)
            total = (free+active+inactive+wired) * 4096 / 1073741824
            used = (active+wired) * 4096 / 1073741824
            printf "    Total: %.1fG  Used: %.1fG  Free: %.1fG\n", total, used, total-used
        }
    '
fi
echo ""
echo "  Containers:"
docker compose ps 2>/dev/null | sed 's/^/    /' || echo "    (docker compose not available)"

# ---------------------------------------------------------------------------
# 2. AGENT STATUS
# ---------------------------------------------------------------------------

section "AGENT STATUS"

if [ -f "$METRICS_FILE" ]; then
    total=$(metric total_iterations 0)
    successes=$(metric successful_iterations 0)
    failures=$(metric failed_iterations 0)
    consec=$(metric current_consecutive_failures 0)
    rate=$(metric success_rate 0)
    last_at=$(metric last_iteration_at "never")
    uptime_secs=$(metric uptime_seconds 0)
    status=$(metric status "unknown")

    # Convert uptime seconds to human-readable
    days=$((uptime_secs / 86400))
    hours=$(( (uptime_secs % 86400) / 3600 ))
    mins=$(( (uptime_secs % 3600) / 60 ))
    uptime_str=""
    [ "$days" -gt 0 ] && uptime_str="${days}d "
    [ "$hours" -gt 0 ] || [ "$days" -gt 0 ] && uptime_str="${uptime_str}${hours}h "
    uptime_str="${uptime_str}${mins}m"

    rate_pct=$(echo "$rate" | awk '{printf "%.1f", $1 * 100}')

    echo ""
    echo "  Status:              $status"
    echo "  Total iterations:    $total"
    echo "  Successful:          $successes ($rate_pct%)"
    echo "  Failed:              $failures"
    echo "  Consecutive fails:   $consec"
    echo "  Last iteration:      $last_at"
    echo "  Hours running:       $uptime_str"
else
    echo ""
    echo "  No metrics found at $METRICS_FILE"
    echo "  Has the orchestrator run yet?"
fi

# ---------------------------------------------------------------------------
# 3. ESTIMATED COST
# ---------------------------------------------------------------------------

section "ESTIMATED COST"

if [ -f "$METRICS_FILE" ] && command -v jq &>/dev/null; then
    prompt_tokens=$(metric total_prompt_tokens 0)
    completion_tokens=$(metric total_completion_tokens 0)

    if [ "$prompt_tokens" != "0" ] || [ "$completion_tokens" != "0" ]; then
        # Rates: $3.00/1M prompt, $15.00/1M completion (matches orchestrator estimates)
        cost=$(echo "$prompt_tokens $completion_tokens" | awk '{
            prompt_cost = ($1 / 1000000) * 3.00
            comp_cost   = ($2 / 1000000) * 15.00
            total       = prompt_cost + comp_cost
            printf "%.4f", total
        }')
        total_tokens=$((prompt_tokens + completion_tokens))

        echo ""
        echo "  Input tokens:      $(printf "%'d" "$prompt_tokens")"
        echo "  Output tokens:     $(printf "%'d" "$completion_tokens")"
        echo "  Total tokens:      $(printf "%'d" "$total_tokens")"
        echo "  Estimated spend:   \$$cost"
        echo ""
        echo "  Rates: \$3.00/1M input, \$15.00/1M output (approx)"
    else
        echo ""
        echo "  No token usage recorded yet."
        echo "  Cost tracking starts after the first successful API call."
    fi
else
    echo ""
    if ! command -v jq &>/dev/null; then
        echo "  jq not installed — install with: apt install jq"
    else
        echo "  No metrics file found."
    fi
fi

# ---------------------------------------------------------------------------
# 4. RECENT ACTIVITY
# ---------------------------------------------------------------------------

section "RECENT ACTIVITY"

echo ""
echo "  --- Last 20 lines of orchestrator log ---"
echo ""
if [ -f "$ORCH_LOG" ]; then
    tail -20 "$ORCH_LOG" | sed 's/^/  /'
else
    echo "  No orchestrator log found at $ORCH_LOG"
fi

echo ""
echo "  --- Last 5 commits in workspace/repo ---"
echo ""
if [ -d "$REPO_DIR/.git" ]; then
    git -C "$REPO_DIR" log --oneline -5 2>/dev/null | sed 's/^/  /' || echo "  (no commits yet)"
else
    echo "  No repo cloned at $REPO_DIR"
fi

echo ""
