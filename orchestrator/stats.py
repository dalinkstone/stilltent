#!/usr/bin/env python3
"""
Display orchestrator statistics from the stats file written by run.sh.

Usage:
    python3 orchestrator/stats.py
    make stats
"""

import json
import os
import sys

STATS_FILE = os.environ.get(
    "ORCHESTRATOR_STATS_FILE",
    os.path.join(os.path.dirname(__file__), "..", "workspace", ".orchestrator-stats.json"),
)


def main():
    if not os.path.exists(STATS_FILE):
        print("No stats file found. The orchestrator has not run yet.")
        print(f"  Expected: {os.path.abspath(STATS_FILE)}")
        sys.exit(1)

    with open(STATS_FILE) as f:
        stats = json.load(f)

    total = stats.get("total_iterations", 0)
    successes = stats.get("total_successes", 0)
    failures = stats.get("total_failures", 0)
    consec = stats.get("consecutive_failures", 0)
    last_run = stats.get("last_run", "never")
    repo = stats.get("target_repo", "unknown")

    rate = f"{(successes / total * 100):.1f}%" if total > 0 else "N/A"

    print("=" * 50)
    print("  stilltent orchestrator stats")
    print("=" * 50)
    print(f"  Target repo:            {repo}")
    print(f"  Total iterations:       {total}")
    print(f"  Successes:              {successes}")
    print(f"  Failures:               {failures}")
    print(f"  Success rate:           {rate}")
    print(f"  Consecutive failures:   {consec}")
    print(f"  Last run:               {last_run}")
    print("=" * 50)


if __name__ == "__main__":
    main()
