#!/usr/bin/env python3
"""
Claude Code oversight sidecar — reviews the primary agent's work periodically.

Runs as a long-lived process alongside the primary agent (openclaw/nanoclaw/
nemoclaw). Every REVIEW_EVERY_N orchestrator iterations (detected by polling
workspace/metrics.json), it:

  1. Reads the latest workspace state (git log, diff, metrics)
  2. Sends a review prompt to the Anthropic API
  3. Stores observations and feedback in memory (mem9)

This is the "Claude Code as oversight" pattern — a more capable model
spot-checking a cheaper model's autonomous work.

Usage:
    python oversight.py

Environment:
    ANTHROPIC_API_KEY     Anthropic API key (required)
    ANTHROPIC_MODEL       Model for reviews (default: claude-sonnet-4-20250514)
    WORKSPACE_DIR         Workspace directory (default: /workspace)
    AGENT_MEMORY_URL      Memory API URL (default: http://mnemo-server:8082)
    MEM9_API_KEY          Memory API key
    TARGET_REPO           GitHub repo in owner/repo format
    REVIEW_EVERY_N        Review every N iterations (default: 5)
    OVERSIGHT_BUDGET      Max USD per review session (default: 5.0)
    POLL_INTERVAL         Seconds between metrics checks (default: 30)
"""

import json
import logging
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

# =============================================================================
# Configuration
# =============================================================================

ANTHROPIC_API_KEY = os.environ.get("ANTHROPIC_API_KEY", "")
ANTHROPIC_MODEL = os.environ.get("ANTHROPIC_MODEL", "claude-sonnet-4-20250514")
WORKSPACE_DIR = Path(os.environ.get("WORKSPACE_DIR", "/workspace"))
AGENT_MEMORY_URL = os.environ.get("AGENT_MEMORY_URL", "http://mnemo-server:8082")
MEM9_API_KEY = os.environ.get("MEM9_API_KEY", "stilltent-local-dev-key")
TARGET_REPO = os.environ.get("TARGET_REPO", "")
REVIEW_EVERY_N = int(os.environ.get("REVIEW_EVERY_N", "5"))
OVERSIGHT_BUDGET = float(os.environ.get("OVERSIGHT_BUDGET", "5.0"))
POLL_INTERVAL = int(os.environ.get("POLL_INTERVAL", "30"))

METRICS_FILE = WORKSPACE_DIR / "metrics.json"
REPO_DIR = WORKSPACE_DIR / "repo"

# =============================================================================
# Logging
# =============================================================================

logging.basicConfig(
    level=logging.INFO,
    format="[%(asctime)s] [oversight] %(message)s",
    datefmt="%Y-%m-%dT%H:%M:%SZ",
    stream=sys.stdout,
)
logger = logging.getLogger("oversight")

# =============================================================================
# Helpers
# =============================================================================


def read_metrics() -> dict:
    """Read the current metrics.json from workspace."""
    try:
        if METRICS_FILE.exists():
            return json.loads(METRICS_FILE.read_text())
    except (json.JSONDecodeError, OSError):
        pass
    return {}


def get_iteration_count(metrics: dict) -> int:
    """Extract the current iteration count from metrics."""
    return metrics.get("iterations", {}).get("total", 0)


def git_recent_log(n: int = 20) -> str:
    """Get the last N git commits from the repo."""
    try:
        result = subprocess.run(
            ["git", "log", f"--oneline", f"-{n}", "--no-decorate"],
            capture_output=True, text=True, timeout=10,
            cwd=str(REPO_DIR),
        )
        return result.stdout.strip() if result.returncode == 0 else "(no git log)"
    except Exception:
        return "(git log unavailable)"


def git_diff_stat() -> str:
    """Get a summary of uncommitted changes."""
    try:
        result = subprocess.run(
            ["git", "diff", "--stat", "HEAD~5..HEAD"],
            capture_output=True, text=True, timeout=10,
            cwd=str(REPO_DIR),
        )
        return result.stdout.strip() if result.returncode == 0 else "(no diff)"
    except Exception:
        return "(diff unavailable)"


def git_recent_diff() -> str:
    """Get the actual diff of the last few commits (truncated)."""
    try:
        result = subprocess.run(
            ["git", "diff", "HEAD~3..HEAD"],
            capture_output=True, text=True, timeout=15,
            cwd=str(REPO_DIR),
        )
        diff = result.stdout.strip() if result.returncode == 0 else ""
        if len(diff) > 30000:
            diff = diff[:15000] + "\n\n[... truncated ...]\n\n" + diff[-15000:]
        return diff or "(no recent diff)"
    except Exception:
        return "(diff unavailable)"


def store_memory(content: str, tags: list[str]) -> bool:
    """Store an observation in mem9 memory."""
    payload = json.dumps({
        "content": content,
        "tags": tags,
        "source": "claude-code-oversight",
    }).encode("utf-8")

    url = f"{AGENT_MEMORY_URL}/v1alpha2/mem9s/memories"
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {MEM9_API_KEY}",
        "X-Mnemo-Agent-Id": "claude-code-oversight",
    }

    try:
        req = urllib.request.Request(url, data=payload, headers=headers, method="POST")
        urllib.request.urlopen(req, timeout=10)
        return True
    except Exception as exc:
        logger.error("Failed to store memory: %s", exc)
        return False


def call_anthropic(prompt: str) -> str:
    """Call the Anthropic Messages API for a review."""
    payload = json.dumps({
        "model": ANTHROPIC_MODEL,
        "max_tokens": 4096,
        "messages": [{"role": "user", "content": prompt}],
    }).encode("utf-8")

    headers = {
        "Content-Type": "application/json",
        "x-api-key": ANTHROPIC_API_KEY,
        "anthropic-version": "2023-06-01",
    }

    try:
        req = urllib.request.Request(
            "https://api.anthropic.com/v1/messages",
            data=payload, headers=headers, method="POST",
        )
        resp = urllib.request.urlopen(req, timeout=120)
        body = json.loads(resp.read().decode("utf-8"))
        text_blocks = [b["text"] for b in body.get("content", []) if b.get("type") == "text"]
        return "\n".join(text_blocks)
    except Exception as exc:
        logger.error("Anthropic API error: %s", exc)
        return ""


# =============================================================================
# Review logic
# =============================================================================


def run_review(iteration: int) -> None:
    """Run a single oversight review of the primary agent's work."""
    logger.info("Starting review at iteration %d", iteration)

    git_log = git_recent_log()
    diff_stat = git_diff_stat()
    recent_diff = git_recent_diff()
    metrics = read_metrics()

    prompt = f"""You are a senior engineering reviewer overseeing an autonomous coding agent
working on the repository '{TARGET_REPO}'.

The agent has completed {iteration} iterations so far. Review its recent work
and provide actionable feedback.

## Recent Git Log
```
{git_log}
```

## Diff Stats (last 5 commits)
```
{diff_stat}
```

## Recent Changes (last 3 commits)
```diff
{recent_diff}
```

## Metrics
```json
{json.dumps(metrics, indent=2)[:3000]}
```

## Your Review

Analyze the agent's work and provide:

1. **Quality Assessment**: Are the changes well-structured? Any code smells,
   anti-patterns, or bugs?
2. **Progress Assessment**: Is the agent making meaningful progress toward
   the project goals, or is it spinning/repeating?
3. **Risk Assessment**: Any security issues, breaking changes, or risky
   patterns?
4. **Recommendations**: Specific, actionable feedback the agent should
   follow in its next iterations.

Be concise and direct. Focus on issues that matter. If the work looks good,
say so briefly and note what's working well.
"""

    review = call_anthropic(prompt)
    if not review:
        logger.warning("Review returned empty — skipping memory store")
        return

    logger.info("Review complete (%d chars), storing in memory", len(review))

    # Store the review as a memory tagged for the primary agent to find
    memory_content = (
        f"[OVERSIGHT REVIEW — iteration {iteration}]\n\n"
        f"{review}\n\n"
        f"---\n"
        f"Reviewed by: claude-code-oversight\n"
        f"Iteration: {iteration}\n"
        f"Timestamp: {time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}"
    )

    store_memory(memory_content, ["oversight", "review", "feedback"])
    logger.info("Review stored in memory")


# =============================================================================
# Main loop
# =============================================================================


def main():
    if not ANTHROPIC_API_KEY:
        logger.error("ANTHROPIC_API_KEY is required")
        sys.exit(1)

    logger.info("Claude Code oversight sidecar starting")
    logger.info("  TARGET_REPO     = %s", TARGET_REPO)
    logger.info("  REVIEW_EVERY_N  = %d iterations", REVIEW_EVERY_N)
    logger.info("  OVERSIGHT_BUDGET= $%.2f per review", OVERSIGHT_BUDGET)
    logger.info("  POLL_INTERVAL   = %ds", POLL_INTERVAL)

    last_reviewed_iteration = 0

    while True:
        metrics = read_metrics()
        current_iteration = get_iteration_count(metrics)

        # Check if we've crossed a review threshold
        next_review_at = last_reviewed_iteration + REVIEW_EVERY_N
        if current_iteration >= next_review_at and current_iteration > 0:
            try:
                run_review(current_iteration)
                last_reviewed_iteration = current_iteration
            except Exception as exc:
                logger.error("Review failed: %s", exc)

        time.sleep(POLL_INTERVAL)


if __name__ == "__main__":
    main()
