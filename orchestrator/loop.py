#!/usr/bin/env python3
"""
stilltent orchestrator — autonomous agent loop driver

Sends trigger prompts to the OpenClaw gateway on a configurable interval,
monitors for hangs (via timeout), tracks success/failure metrics, and
auto-pauses after too many consecutive failures.

The orchestrator does NOT make decisions — the agent (via SKILL.md) makes
all decisions. This script only:

  1. Checks if the agent should run (no PAUSE file, not too many failures)
  2. Sends a trigger prompt to the OpenClaw gateway
  3. Waits for the response (with a timeout)
  4. Logs the result and writes metrics
  5. Sleeps for the configured interval
  6. Repeats

Uses ONLY the Python standard library (no pip dependencies).

Usage:
    python3 orchestrator/loop.py
    LOOP_INTERVAL=120 python3 orchestrator/loop.py

Environment variables:
    OPENCLAW_URL               OpenClaw gateway URL (default: http://openclaw-gateway:18789)
    LOOP_INTERVAL              Seconds between iterations (default: 60)
    ITERATION_TIMEOUT          Max seconds per iteration (default: 600)
    MAX_CONSECUTIVE_FAILURES   Pause after this many failures (default: 10)
    WORKSPACE_DIR              Path to workspace (default: /workspace)
    LOG_FILE                   Path to log file (default: /workspace/orchestrator.log)
    OPENCLAW_GATEWAY_TOKEN     Bearer token for the gateway (optional)
    TARGET_REPO                GitHub repo in owner/repo format (optional, for prompt context)
"""

import json
import logging
import os
import re
import signal
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

# =============================================================================
# Configuration — all from environment variables with sensible defaults
# =============================================================================

# The OpenClaw gateway URL. When running in Docker Compose, containers reach
# each other by service name. When running locally, override to
# http://localhost:18789 or whatever port you've mapped.
OPENCLAW_URL = os.environ.get("OPENCLAW_URL", "http://openclaw-gateway:18789")

LOOP_INTERVAL = int(os.environ.get("LOOP_INTERVAL", "60"))
ITERATION_TIMEOUT = int(os.environ.get("ITERATION_TIMEOUT", "600"))
MAX_CONSECUTIVE_FAILURES = int(os.environ.get("MAX_CONSECUTIVE_FAILURES", "10"))
WORKSPACE_DIR = Path(os.environ.get("WORKSPACE_DIR", "/workspace"))
LOG_FILE = Path(os.environ.get("LOG_FILE", str(WORKSPACE_DIR / "orchestrator.log")))
OPENCLAW_GATEWAY_TOKEN = os.environ.get("OPENCLAW_GATEWAY_TOKEN", "")
TARGET_REPO = os.environ.get("TARGET_REPO", "")

# =============================================================================
# Logging — dual output to stdout (for `docker logs`) and a log file
# =============================================================================

logger = logging.getLogger("orchestrator")
logger.setLevel(logging.INFO)

_formatter = logging.Formatter(
    fmt="[%(asctime)s] [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%dT%H:%M:%SZ",
)
_formatter.converter = time.gmtime  # UTC timestamps

_stdout_handler = logging.StreamHandler(sys.stdout)
_stdout_handler.setFormatter(_formatter)
logger.addHandler(_stdout_handler)

# File handler is added lazily once the workspace directory exists.
_file_handler = None


def _ensure_file_handler():
    """Attach the file log handler once the workspace directory is available."""
    global _file_handler
    if _file_handler is not None:
        return
    try:
        LOG_FILE.parent.mkdir(parents=True, exist_ok=True)
        _file_handler = logging.FileHandler(str(LOG_FILE), encoding="utf-8")
        _file_handler.setFormatter(_formatter)
        logger.addHandler(_file_handler)
    except OSError as exc:
        logger.warning("Could not open log file %s: %s", LOG_FILE, exc)


def log(msg: str, level: int = logging.INFO):
    """Log a message to both stdout and the log file."""
    _ensure_file_handler()
    logger.log(level, msg)


# =============================================================================
# Metrics
# =============================================================================

# Running counters — survive across iterations but NOT across process restarts.
# (For durable metrics, read metrics.json on startup.)
_metrics = {
    "total_iterations": 0,
    "successful_iterations": 0,
    "failed_iterations": 0,
    "current_consecutive_failures": 0,
    "success_rate": 0.0,
    "last_iteration_at": None,
    "uptime_seconds": 0,
    "status": "starting",
}
_start_time = time.monotonic()


def _load_metrics():
    """Load existing metrics from disk so counters survive restarts."""
    metrics_path = WORKSPACE_DIR / "metrics.json"
    if metrics_path.exists():
        try:
            with open(metrics_path, "r", encoding="utf-8") as fh:
                saved = json.load(fh)
            _metrics["total_iterations"] = saved.get("total_iterations", 0)
            _metrics["successful_iterations"] = saved.get("successful_iterations", 0)
            _metrics["failed_iterations"] = saved.get("failed_iterations", 0)
            log(f"Resumed metrics: {_metrics['total_iterations']} prior iterations")
        except (json.JSONDecodeError, OSError) as exc:
            log(f"Could not load prior metrics: {exc}", logging.WARNING)


def write_metrics(iteration: int, consecutive_failures: int):
    """Write the metrics JSON file after each iteration."""
    _metrics["total_iterations"] = iteration
    _metrics["current_consecutive_failures"] = consecutive_failures
    _metrics["success_rate"] = (
        round(_metrics["successful_iterations"] / iteration, 3) if iteration > 0 else 0.0
    )
    _metrics["last_iteration_at"] = datetime.now(timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%SZ"
    )
    _metrics["uptime_seconds"] = int(time.monotonic() - _start_time)
    _metrics["status"] = "running"

    metrics_path = WORKSPACE_DIR / "metrics.json"
    try:
        metrics_path.parent.mkdir(parents=True, exist_ok=True)
        tmp = metrics_path.with_suffix(".tmp")
        with open(tmp, "w", encoding="utf-8") as fh:
            json.dump(_metrics, fh, indent=2)
            fh.write("\n")
        tmp.replace(metrics_path)
    except OSError as exc:
        log(f"Failed to write metrics: {exc}", logging.WARNING)


# =============================================================================
# Prompt builder
# =============================================================================

def build_prompt(iteration: int) -> str:
    """Build the trigger prompt sent to the OpenClaw agent each iteration."""
    return (
        f"Read and follow /workspace/SKILL.md. "
        f"This is iteration {iteration}. "
        f"Execute the complete iteration protocol (Phase 1 through Phase 7). "
        f"When finished, respond with a JSON summary:\n"
        "{\n"
        '  "iteration": <number>,\n'
        '  "action_type": "<fix|review|feature|test|refactor|docs|bootstrap>",\n'
        '  "summary": "<1-2 sentence description>",\n'
        '  "result": "<success|failure|partial|skipped>",\n'
        '  "pr_number": <number or null>,\n'
        '  "merged": <true|false|null>,\n'
        '  "confidence": <0.0 to 1.0>,\n'
        '  "error": "<error message or null>"\n'
        "}"
    )


# =============================================================================
# OpenClaw gateway communication
# =============================================================================

def send_to_openclaw(prompt: str, timeout: int) -> dict:
    """Send a prompt to the OpenClaw gateway and return the parsed response.

    The gateway exposes an OpenAI-compatible chat completions endpoint at
    /v1/chat/completions. Adjust the URL, model name, or payload structure
    if your OpenClaw version uses a different API shape.

    Things you may need to change:
      - OPENCLAW_URL: container hostname vs localhost
      - The model field: must match a model configured in OpenClaw
      - Auth: set OPENCLAW_GATEWAY_TOKEN if the gateway requires a bearer token
      - Session management: OpenClaw may support session IDs for context
    """
    url = f"{OPENCLAW_URL}/v1/chat/completions"

    payload = json.dumps({
        "model": "openclaw:main",
        "messages": [
            {"role": "user", "content": prompt},
        ],
    }).encode("utf-8")

    headers = {"Content-Type": "application/json"}
    if OPENCLAW_GATEWAY_TOKEN:
        headers["Authorization"] = f"Bearer {OPENCLAW_GATEWAY_TOKEN}"

    req = urllib.request.Request(url, data=payload, headers=headers, method="POST")

    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
        body = resp.read().decode("utf-8")
        return json.loads(body)
    except urllib.error.HTTPError as exc:
        body = ""
        try:
            body = exc.read().decode("utf-8", errors="replace")
        except Exception:
            pass
        raise RuntimeError(
            f"OpenClaw returned HTTP {exc.code}: {body[:500]}"
        ) from exc


def extract_text_from_response(response: dict) -> str:
    """Pull the assistant's text out of an OpenAI-compatible chat response.

    Expected shape:
        { "choices": [{ "message": { "content": "..." } }] }
    """
    try:
        return response["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError):
        # Fallback: stringify the whole thing so regex can still search it
        return json.dumps(response)


def response_indicates_success(response: dict) -> bool:
    """Check if the agent's response indicates a successful iteration.

    Looks for the JSON summary block the agent is instructed to produce,
    then falls back to keyword heuristics.
    """
    text = extract_text_from_response(response)

    # Try to find the structured JSON summary in the response
    try:
        json_match = re.search(r'\{[^{}]*"result"[^{}]*\}', text)
        if json_match:
            summary = json.loads(json_match.group())
            result = summary.get("result", "").lower()
            return result in ("success", "partial", "skipped")
    except (json.JSONDecodeError, AttributeError):
        pass

    # Fallback: check for error keywords
    lower = text.lower()
    if any(kw in lower for kw in ["error", "failed", "exception", "traceback"]):
        return False

    # If we can't determine, assume success (agent ran without crashing)
    return True


# =============================================================================
# Signal handling — graceful shutdown for `docker compose down`
# =============================================================================

_shutdown_requested = False


def _handle_signal(signum, _frame):
    """Handle SIGTERM/SIGINT: set shutdown flag so the loop exits cleanly."""
    global _shutdown_requested
    sig_name = signal.Signals(signum).name
    log(f"Received {sig_name} — shutting down after current iteration")
    _shutdown_requested = True


signal.signal(signal.SIGTERM, _handle_signal)
signal.signal(signal.SIGINT, _handle_signal)


# =============================================================================
# Main loop
# =============================================================================

def main():
    """Entry point: run the orchestrator loop."""
    log("=" * 60)
    log("stilltent orchestrator starting")
    log(f"  OPENCLAW_URL              = {OPENCLAW_URL}")
    log(f"  LOOP_INTERVAL             = {LOOP_INTERVAL}s")
    log(f"  ITERATION_TIMEOUT         = {ITERATION_TIMEOUT}s")
    log(f"  MAX_CONSECUTIVE_FAILURES  = {MAX_CONSECUTIVE_FAILURES}")
    log(f"  WORKSPACE_DIR             = {WORKSPACE_DIR}")
    log(f"  LOG_FILE                  = {LOG_FILE}")
    log(f"  TARGET_REPO               = {TARGET_REPO or '(not set)'}")
    log("=" * 60)

    # Resume counters from a prior run if the metrics file exists
    _load_metrics()
    iteration = _metrics["total_iterations"]
    consecutive_failures = 0

    while not _shutdown_requested:
        # -- 1. Check for PAUSE file ------------------------------------------
        pause_file = WORKSPACE_DIR / "PAUSE"
        if pause_file.exists():
            log("PAUSED — remove workspace/PAUSE to resume")
            time.sleep(LOOP_INTERVAL)
            continue

        # -- 2. Check consecutive failure threshold ---------------------------
        if consecutive_failures >= MAX_CONSECUTIVE_FAILURES:
            log(
                f"EMERGENCY PAUSE — {consecutive_failures} consecutive failures. "
                "Creating PAUSE file."
            )
            try:
                pause_file.parent.mkdir(parents=True, exist_ok=True)
                pause_file.touch()
            except OSError as exc:
                log(f"Could not create PAUSE file: {exc}", logging.ERROR)
            continue

        # -- 3. Send trigger prompt to OpenClaw --------------------------------
        iteration += 1
        log(f"=== Iteration {iteration} starting ===")

        prompt = build_prompt(iteration)

        try:
            response = send_to_openclaw(prompt, timeout=ITERATION_TIMEOUT)

            if response_indicates_success(response):
                consecutive_failures = 0
                _metrics["successful_iterations"] += 1
                log(f"Iteration {iteration} completed successfully")
            else:
                consecutive_failures += 1
                _metrics["failed_iterations"] += 1
                log(
                    f"Iteration {iteration} failed "
                    f"(consecutive: {consecutive_failures})"
                )

        except (TimeoutError, urllib.error.URLError) as exc:
            consecutive_failures += 1
            _metrics["failed_iterations"] += 1
            if "timed out" in str(exc).lower():
                log(
                    f"Iteration {iteration} TIMED OUT after {ITERATION_TIMEOUT}s "
                    f"(consecutive: {consecutive_failures})"
                )
            else:
                log(
                    f"Iteration {iteration} NETWORK ERROR: {exc} "
                    f"(consecutive: {consecutive_failures})"
                )

        except Exception as exc:
            consecutive_failures += 1
            _metrics["failed_iterations"] += 1
            log(
                f"Iteration {iteration} ERROR: {exc} "
                f"(consecutive: {consecutive_failures})"
            )

        # -- 4. Write metrics --------------------------------------------------
        write_metrics(iteration, consecutive_failures)

        # -- 5. Sleep ----------------------------------------------------------
        if _shutdown_requested:
            break
        log(f"Sleeping {LOOP_INTERVAL}s before next iteration")
        # Sleep in small increments so we can respond to signals promptly
        sleep_remaining = LOOP_INTERVAL
        while sleep_remaining > 0 and not _shutdown_requested:
            chunk = min(sleep_remaining, 5)
            time.sleep(chunk)
            sleep_remaining -= chunk

    # ---- Clean shutdown ------------------------------------------------------
    log("Shutting down gracefully")
    _metrics["status"] = "stopped"
    _metrics["uptime_seconds"] = int(time.monotonic() - _start_time)
    metrics_path = WORKSPACE_DIR / "metrics.json"
    try:
        with open(metrics_path, "w", encoding="utf-8") as fh:
            json.dump(_metrics, fh, indent=2)
            fh.write("\n")
    except OSError:
        pass
    log("Goodbye")


if __name__ == "__main__":
    main()
