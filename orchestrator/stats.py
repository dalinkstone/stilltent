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


def main():
    if not os.path.exists(METRICS_FILE):
        print("No metrics yet — has the orchestrator run?")
        print(f"  Expected: {os.path.abspath(METRICS_FILE)}")
        sys.exit(1)

    with open(METRICS_FILE, encoding="utf-8") as f:
        m = json.load(f)

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


if __name__ == "__main__":
    main()
