#!/usr/bin/env python3
"""
Display orchestrator statistics from metrics.json written by loop.py.

Reads /workspace/metrics.json inside a container, or
./workspace/metrics.json when running on the host.

Usage:
    python3 orchestrator/stats.py
    make stats
"""

import json
import os
import sys
from datetime import datetime

# In-container path first, fall back to host-relative path
METRICS_FILE = os.environ.get(
    "METRICS_FILE",
    "/workspace/metrics.json"
    if os.path.exists("/workspace/metrics.json")
    else os.path.join(os.path.dirname(__file__), "..", "workspace", "metrics.json"),
)


def format_uptime(seconds: int) -> str:
    """Convert seconds into a human-readable 'Xd Xh Xm' string."""
    days, rem = divmod(seconds, 86400)
    hours, rem = divmod(rem, 3600)
    minutes = rem // 60
    parts = []
    if days:
        parts.append(f"{days}d")
    if hours or days:
        parts.append(f"{hours}h")
    parts.append(f"{minutes}m")
    return " ".join(parts)


def format_timestamp(ts: str) -> str:
    """Convert ISO timestamp to a friendlier display format."""
    if not ts:
        return "never"
    try:
        dt = datetime.fromisoformat(ts.replace("Z", "+00:00"))
        return dt.strftime("%Y-%m-%d %H:%M:%S UTC")
    except (ValueError, AttributeError):
        return ts


def _load_metrics() -> dict:
    """Load metrics.json and return the parsed dict, or exit if missing."""
    if not os.path.exists(METRICS_FILE):
        print("No metrics yet — has the orchestrator run?")
        print(f"  Expected: {os.path.abspath(METRICS_FILE)}")
        sys.exit(1)
    with open(METRICS_FILE, encoding="utf-8") as f:
        return json.load(f)


def cost_summary() -> str:
    """Return a one-line cost summary suitable for dashboards and scripts."""
    m = _load_metrics()
    total = m.get("total_spend_usd", 0.0)
    avg = m.get("avg_cost_per_iteration_usd", 0.0)
    projected = m.get("projected_total_usd", 0.0)
    remaining = m.get("budget_remaining_usd", 0.0)
    return (
        f"Spent ${total:.4f} | Avg ${avg:.4f}/iter | "
        f"Projected ${projected:.2f} | Budget ${remaining:.2f} remaining"
    )


def main():
    m = _load_metrics()

    total = m.get("total_iterations", 0)
    successes = m.get("successful_iterations", 0)
    failures = m.get("failed_iterations", 0)
    consec = m.get("current_consecutive_failures", 0)
    last_at = m.get("last_iteration_at", "")
    uptime = m.get("uptime_seconds", 0)
    status = m.get("status", "unknown")

    success_pct = f"{successes / total * 100:.1f}%" if total > 0 else "N/A"
    failure_pct = f"{failures / total * 100:.1f}%" if total > 0 else "N/A"

    print("=== stilltent stats ===")
    print(f"Status:              {status}")
    print(f"Total iterations:    {total}")
    print(f"Successful:          {successes} ({success_pct})")
    print(f"Failed:              {failures} ({failure_pct})")
    print(f"Consecutive fails:   {consec}")
    print(f"Last iteration:      {format_timestamp(last_at)}")
    print(f"Uptime:              {format_uptime(uptime)}")

    # Cost visibility
    spend = m.get("total_spend_usd", 0.0)
    avg_cost = m.get("avg_cost_per_iteration_usd", 0.0)
    projected = m.get("projected_total_usd", 0.0)
    budget_limit = m.get("budget_limit_usd", 0.0)
    budget_remaining = m.get("budget_remaining_usd", 0.0)

    print(f"\n=== cost ===")
    print(f"Total spend:         ${spend:.4f}")
    print(f"Avg cost/iteration:  ${avg_cost:.4f}")
    print(f"Projected total:     ${projected:.2f}")
    print(f"Budget remaining:    ${budget_remaining:.2f} / ${budget_limit:.2f}")
    print(f"Pricing:             $0.12/M input, $0.75/M output")

    # Learning metrics
    hyp_confirmed = m.get("hypotheses_confirmed", 0)
    hyp_refuted = m.get("hypotheses_refuted", 0)
    hyp_partial = m.get("hypotheses_partial", 0)
    hyp_inconclusive = m.get("hypotheses_inconclusive", 0)
    total_hyp = hyp_confirmed + hyp_refuted + hyp_partial + hyp_inconclusive
    improve_iters = m.get("improvement_iterations", 0)

    if total_hyp > 0 or improve_iters > 0:
        confirm_pct = f"{hyp_confirmed / total_hyp * 100:.0f}%" if total_hyp > 0 else "N/A"
        improve_pct = f"{improve_iters / total * 100:.0f}%" if total > 0 else "N/A"
        print(f"\n=== learning ===")
        print(f"Hypotheses tested:   {total_hyp}")
        print(f"Confirmed:           {hyp_confirmed} ({confirm_pct})")
        print(f"Refuted:             {hyp_refuted}")
        print(f"Partial:             {hyp_partial}")
        print(f"Inconclusive:        {hyp_inconclusive}")
        print(f"Improvement iters:   {improve_iters} ({improve_pct} of total)")


if __name__ == "__main__":
    main()
