#!/usr/bin/env python3
"""
stilltent orchestrator — autonomous agent loop driver

Sends trigger prompts to the OpenClaw gateway on a configurable interval,
monitors for hangs (via timeout), tracks success/failure metrics, and
auto-pauses after too many consecutive failures.

Designed for budget-constrained runs (e.g., $40/5h). Features:
  - Idle detection: exponential backoff when no work is available (30-40% savings)
  - Per-iteration cost tracking using qwen/qwen3-coder-next pricing
  - Budget guard that projects spend over TOTAL_RUNTIME_HOURS
  - Exponential backoff on consecutive failures (caps at 1 hour)
  - Scheduled shutdown after TOTAL_RUNTIME_HOURS
  - Periodic health summaries every 50 iterations
  - Retry wrapper on HTTP calls (3 retries on connection/5xx errors)
  - Dual logging to stdout and workspace/orchestrator.log

The orchestrator does NOT make decisions — the agent (via SKILL.md) makes
all decisions. This script only:

  1. Checks if the agent should run (no PAUSE file, not too many failures)
  2. Sends a trigger prompt to the OpenClaw gateway
  3. Waits for the response (with a timeout)
  4. Logs the result and writes metrics
  5. Detects idle responses and backs off to save tokens
  6. Sleeps for the configured cooldown
  7. Repeats

Uses ONLY the Python standard library (no pip dependencies).

Usage:
    python3 orchestrator/loop.py
    LOOP_INTERVAL=120 python3 orchestrator/loop.py

Environment variables:
    OPENCLAW_URL               OpenClaw gateway URL (default: http://openclaw-gateway:18789)
    LOOP_INTERVAL              Seconds between iterations (default: 60)
    COOLDOWN_SECONDS           Cooldown pause between iterations (default: 30)
    ITERATION_TIMEOUT          Max seconds per iteration (default: 600)
    MAX_CONSECUTIVE_FAILURES   Pause after this many failures (default: 25)
    TOTAL_RUNTIME_HOURS        Graceful shutdown after this many hours (default: 5)
    BUDGET_LIMIT               Total budget in USD for the run (default: 40)
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
import socket
import sys
import threading
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
COOLDOWN_SECONDS = int(os.environ.get("COOLDOWN_SECONDS", "30"))
ITERATION_TIMEOUT = int(os.environ.get("ITERATION_TIMEOUT", "600"))
MAX_CONSECUTIVE_FAILURES = int(os.environ.get("MAX_CONSECUTIVE_FAILURES", "25"))
TOTAL_RUNTIME_HOURS = float(os.environ.get("TOTAL_RUNTIME_HOURS", "5"))
WORKSPACE_DIR = Path(os.environ.get("WORKSPACE_DIR", "/workspace"))
LOG_FILE = Path(os.environ.get("LOG_FILE", str(WORKSPACE_DIR / "orchestrator.log")))
OPENCLAW_GATEWAY_TOKEN = os.environ.get("OPENCLAW_GATEWAY_TOKEN", "")
TARGET_REPO = os.environ.get("TARGET_REPO", "")
BUDGET_LIMIT = float(os.environ.get("BUDGET_LIMIT", "40"))
DAILY_BUDGET_LIMIT = float(os.environ.get("DAILY_BUDGET_LIMIT", "5.0"))  # legacy fallback

# Idle detection settings — reduces token waste by 30-40% during periods with
# no work (no open issues, no PRs to review, no failing CI).
IDLE_BASE_WAIT = 60              # seconds — base wait when first entering idle mode
IDLE_MAX_WAIT = 900              # seconds — cap idle wait at 15 minutes
IDLE_MAX_EXPONENT = 4            # 2^4 = 16x multiplier max (60s -> 960s capped to 900s)
IDLE_FORCE_CHECK_INTERVAL = 900  # seconds — always try one iteration every 15 min in idle

# HTTP retry settings for the OpenClaw gateway call
HTTP_RETRY_COUNT = 3
HTTP_RETRY_BASE_DELAY = 5  # seconds — base delay with exponential backoff (5, 10, 20)

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
# Circuit breaker — protects against budget drain when the gateway is down
# =============================================================================

class CircuitBreaker:
    """Circuit breaker pattern for the OpenClaw gateway.

    States:
      - CLOSED:    Normal operation, calls pass through.
      - OPEN:      Too many failures; calls are skipped for a cooldown period.
      - HALF_OPEN: After cooldown, one probe call is allowed through.

    Transitions:
      - CLOSED -> OPEN:      After `failure_threshold` consecutive failures.
      - OPEN -> HALF_OPEN:   After `open_duration` seconds elapse.
      - HALF_OPEN -> CLOSED: If the probe call succeeds.
      - HALF_OPEN -> OPEN:   If the probe call fails (with doubled cooldown).
    """

    CLOSED = "CLOSED"
    OPEN = "OPEN"
    HALF_OPEN = "HALF_OPEN"

    def __init__(self, failure_threshold: int = 5, open_duration: float = 300.0):
        self.failure_threshold = failure_threshold
        self.open_duration = open_duration
        self._initial_open_duration = open_duration
        self.state = self.CLOSED
        self.consecutive_failures = 0
        self._open_since: float = 0.0

    def allow_request(self) -> bool:
        """Return True if a request should be attempted."""
        if self.state == self.CLOSED:
            return True
        if self.state == self.OPEN:
            elapsed = time.monotonic() - self._open_since
            if elapsed >= self.open_duration:
                self._transition(self.HALF_OPEN)
                return True
            return False
        # HALF_OPEN: allow exactly one probe
        return True

    def record_success(self):
        """Record a successful call — close the circuit."""
        if self.state != self.CLOSED:
            self._transition(self.CLOSED)
        self.consecutive_failures = 0
        self.open_duration = self._initial_open_duration

    def record_failure(self):
        """Record a failed call — potentially open the circuit."""
        self.consecutive_failures += 1
        if self.state == self.HALF_OPEN:
            # Probe failed: re-open with doubled cooldown
            self.open_duration = min(self.open_duration * 2, 3600)
            self._transition(self.OPEN)
        elif self.state == self.CLOSED:
            if self.consecutive_failures >= self.failure_threshold:
                self._transition(self.OPEN)

    def _transition(self, new_state: str):
        old_state = self.state
        self.state = new_state
        if new_state == self.OPEN:
            self._open_since = time.monotonic()
        log(
            f"CircuitBreaker: {old_state} -> {new_state} "
            f"(failures={self.consecutive_failures}, "
            f"cooldown={self.open_duration:.0f}s)",
            logging.WARNING if new_state == self.OPEN else logging.INFO,
        )


_circuit_breaker = CircuitBreaker(failure_threshold=5, open_duration=300.0)


# =============================================================================
# Metrics
# =============================================================================

# Running counters — survive across iterations but NOT across process restarts.
# (For durable metrics, read metrics.json on startup.)
_metrics = {
    "total_iterations": 0,
    "successful_iterations": 0,
    "failed_iterations": 0,
    "skipped_iterations": 0,
    "idle_iterations_avoided": 0,
    "idle_tokens_saved_estimate": 0,
    "current_consecutive_failures": 0,
    "current_consecutive_idles": 0,
    "idle_mode": False,
    "success_rate": 0.0,
    "last_iteration_at": None,
    "uptime_seconds": 0,
    "status": "starting",
    "total_prompt_tokens": 0,
    "total_completion_tokens": 0,
    "total_spend_usd": 0.0,
    "cumulative_spend": 0.0,
    "projected_total_usd": 0.0,
    "avg_cost_per_iteration_usd": 0.0,
    "budget_remaining_usd": 0.0,
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
            _metrics["skipped_iterations"] = saved.get("skipped_iterations", 0)
            _metrics["idle_iterations_avoided"] = saved.get("idle_iterations_avoided", 0)
            _metrics["idle_tokens_saved_estimate"] = saved.get("idle_tokens_saved_estimate", 0)
            _metrics["total_prompt_tokens"] = saved.get("total_prompt_tokens", 0)
            _metrics["total_completion_tokens"] = saved.get("total_completion_tokens", 0)
            _metrics["total_spend_usd"] = saved.get("total_spend_usd", 0.0)
            _metrics["cumulative_spend"] = saved.get("cumulative_spend", 0.0)
            log(f"Resumed metrics: {_metrics['total_iterations']} prior iterations, "
                f"cumulative spend: ${_metrics['cumulative_spend']:.4f}, "
                f"idle iterations avoided: {_metrics['idle_iterations_avoided']}")
        except (json.JSONDecodeError, OSError) as exc:
            log(f"Could not load prior metrics: {exc}", logging.WARNING)


def write_metrics(iteration: int, consecutive_failures: int):
    """Update in-memory metrics (no I/O). The background writer flushes to disk."""
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

    # -- Cost visibility fields (written to metrics.json for external readers) --
    _, total_spend = _estimate_spend()
    elapsed_hours = (time.monotonic() - _start_time) / 3600
    avg_cost = total_spend / iteration if iteration > 0 else 0.0
    projected = (
        total_spend * (TOTAL_RUNTIME_HOURS / elapsed_hours)
        if elapsed_hours > 0.01
        else 0.0
    )
    _metrics["total_spend_usd"] = round(total_spend, 6)
    _metrics["avg_cost_per_iteration_usd"] = round(avg_cost, 6)
    _metrics["projected_total_usd"] = round(projected, 2)
    _metrics["budget_limit_usd"] = BUDGET_LIMIT
    _metrics["budget_remaining_usd"] = round(max(BUDGET_LIMIT - total_spend, 0.0), 2)
    _metrics["cost_per_input_token"] = 0.12
    _metrics["cost_per_output_token"] = 0.75

    _metrics_writer.mark_dirty()


class _MetricsWriter:
    """Background thread that flushes metrics to disk periodically.

    Writes when either condition is met:
      - 10 iterations have been marked dirty since last flush, OR
      - 60 seconds have elapsed since last flush.

    Uses atomic write (tmp + rename) and compact JSON (no indentation).
    """

    def __init__(self):
        self._dirty_count = 0
        self._lock = threading.Lock()
        self._stop_event = threading.Event()
        self._thread: threading.Thread | None = None
        self._last_flush = time.monotonic()
        self._flush_interval = 60.0  # seconds
        self._flush_threshold = 10   # iterations

    def start(self):
        """Start the background writer thread."""
        self._thread = threading.Thread(
            target=self._run, name="metrics-writer", daemon=True
        )
        self._thread.start()

    def stop(self):
        """Signal the writer to stop and wait for it to finish."""
        self._stop_event.set()
        if self._thread is not None:
            self._thread.join(timeout=5.0)
        # Final flush
        self._flush_to_disk()

    def mark_dirty(self):
        """Called by the main loop after updating in-memory metrics."""
        with self._lock:
            self._dirty_count += 1

    def _should_flush(self) -> bool:
        with self._lock:
            if self._dirty_count >= self._flush_threshold:
                return True
        if time.monotonic() - self._last_flush >= self._flush_interval:
            return True
        return False

    def _flush_to_disk(self):
        with self._lock:
            if self._dirty_count == 0:
                return
            snapshot = dict(_metrics)
            self._dirty_count = 0

        metrics_path = WORKSPACE_DIR / "metrics.json"
        try:
            metrics_path.parent.mkdir(parents=True, exist_ok=True)
            tmp = metrics_path.with_suffix(".tmp")
            with open(tmp, "w", encoding="utf-8") as fh:
                json.dump(snapshot, fh, separators=(",", ":"))
                fh.write("\n")
            tmp.replace(metrics_path)
            self._last_flush = time.monotonic()
        except OSError as exc:
            log(f"Failed to write metrics: {exc}", logging.WARNING)

    def _run(self):
        while not self._stop_event.is_set():
            self._stop_event.wait(timeout=5.0)
            if self._should_flush():
                self._flush_to_disk()


_metrics_writer = _MetricsWriter()


# =============================================================================
# Token tracking & spend estimation
# =============================================================================

def _extract_token_usage(response: dict):
    """Extract token counts from the OpenAI-compatible response and accumulate."""
    usage = response.get("usage", {})
    if not usage:
        # Try alternative structure (some APIs use different keys)
        usage = {
            "prompt_tokens": response.get("prompt_tokens", 0),
            "completion_tokens": response.get("completion_tokens", 0),
        }
    prompt_tokens = usage.get("prompt_tokens", 0)
    completion_tokens = usage.get("completion_tokens", 0)
    
    # Log when tokens are 0 to help diagnose API issues
    if prompt_tokens == 0 and completion_tokens == 0:
        log(
            "Token usage shows 0 tokens — checking response structure for alternative layouts",
            logging.DEBUG,
        )
    
    _metrics["total_prompt_tokens"] += prompt_tokens
    _metrics["total_completion_tokens"] += completion_tokens
    return prompt_tokens, completion_tokens


def _calculate_iteration_cost(prompt_tokens: int, completion_tokens: int) -> float:
    """Calculate the cost of a single iteration using qwen/qwen3-coder-next pricing.

    Pricing: $0.12/M input, $0.75/M output.
    """
    INPUT_COST_PER_M = 0.12   # $/M tokens
    OUTPUT_COST_PER_M = 0.75  # $/M tokens
    input_cost = (prompt_tokens / 1_000_000) * INPUT_COST_PER_M
    output_cost = (completion_tokens / 1_000_000) * OUTPUT_COST_PER_M
    return input_cost + output_cost


def _estimate_spend() -> tuple[str, float]:
    """Spend estimate based on accumulated token counts.

    Returns (formatted_string, raw_float) so callers can display or compare.

    Uses qwen/qwen3-coder-next pricing:
      - Input:  $0.12 / 1M tokens
      - Output: $0.75 / 1M tokens
    """
    total = _calculate_iteration_cost(
        _metrics["total_prompt_tokens"],
        _metrics["total_completion_tokens"],
    )
    return f"${total:.4f}", total


def _check_budget_guard() -> bool:
    """Check if projected total spend for the run exceeds BUDGET_LIMIT.

    Instead of extrapolating to a 24h daily rate, extrapolates to
    TOTAL_RUNTIME_HOURS so the guard works correctly for short runs
    (e.g., 5-hour budget windows).

    Returns True if the budget guard has triggered (caller should stop).
    """
    hours_elapsed = (time.monotonic() - _start_time) / 3600
    if hours_elapsed < 0.1:
        return False  # need at least 6 minutes of data

    _, current_spend = _estimate_spend()

    # Already over budget — stop immediately
    if current_spend > BUDGET_LIMIT:
        log(
            f"BUDGET GUARD: total spend ${current_spend:.4f} "
            f"exceeds limit ${BUDGET_LIMIT:.2f}. Creating PAUSE file.",
            logging.ERROR,
        )
        _create_budget_pause_file(current_spend, current_spend, hours_elapsed)
        return True

    # Project spend to TOTAL_RUNTIME_HOURS
    projected = current_spend * (TOTAL_RUNTIME_HOURS / hours_elapsed)
    if projected > BUDGET_LIMIT:
        log(
            f"BUDGET GUARD: projected spend ${projected:.2f} over "
            f"{TOTAL_RUNTIME_HOURS}h exceeds limit ${BUDGET_LIMIT:.2f}. "
            f"Current spend: ${current_spend:.4f} over {hours_elapsed:.2f}h. "
            f"Creating PAUSE file.",
            logging.ERROR,
        )
        _create_budget_pause_file(projected, current_spend, hours_elapsed)
        return True
    return False


def _create_budget_pause_file(projected: float, current: float, hours: float):
    """Helper to create a PAUSE file for budget guard triggers."""
    pause_file = WORKSPACE_DIR / "PAUSE"
    try:
        pause_file.parent.mkdir(parents=True, exist_ok=True)
        pause_file.write_text(
            f"Budget guard: projected spend ${projected:.2f} over "
            f"{TOTAL_RUNTIME_HOURS}h exceeds limit ${BUDGET_LIMIT:.2f}. "
            f"Current spend: ${current:.4f} over {hours:.2f}h.\n",
            encoding="utf-8",
        )
    except OSError as exc:
        log(f"Could not create PAUSE file: {exc}", logging.ERROR)


# =============================================================================
# Periodic health logging
# =============================================================================

def log_health_summary(iteration: int):
    """Log a health summary every 50 iterations."""
    elapsed_hours = (time.monotonic() - _start_time) / 3600
    success_rate = (
        _metrics["successful_iterations"] / iteration * 100 if iteration > 0 else 0.0
    )
    total_tokens = _metrics["total_prompt_tokens"] + _metrics["total_completion_tokens"]
    spend_str, spend_total = _estimate_spend()

    # Per-iteration cost average
    avg_cost = spend_total / iteration if iteration > 0 else 0.0

    # Projected total spend for TOTAL_RUNTIME_HOURS
    if elapsed_hours > 0.01:
        projected_spend = spend_total * (TOTAL_RUNTIME_HOURS / elapsed_hours)
    else:
        projected_spend = 0.0

    budget_remaining = max(BUDGET_LIMIT - spend_total, 0.0)

    log("=" * 60)
    log("HEALTH SUMMARY (every 50 iterations)")
    log(f"  Total iterations:       {iteration}")
    log(f"  Successful:             {_metrics['successful_iterations']}")
    log(f"  Failed:                 {_metrics['failed_iterations']}")
    log(f"  Skipped (idle):         {_metrics['skipped_iterations']}")
    log(f"  Success rate:           {success_rate:.1f}%")
    log(f"  Wall-clock hours:       {elapsed_hours:.1f}h / {TOTAL_RUNTIME_HOURS}h")
    log(f"  Total tokens used:      {total_tokens:,}")
    log(f"  Avg cost/iteration:     ${avg_cost:.4f}")
    log(f"  Total spend:            {spend_str}")
    log(f"  Projected spend:        ${projected_spend:.2f} (over {TOTAL_RUNTIME_HOURS}h)")
    log(f"  Budget remaining:       ${budget_remaining:.2f} / ${BUDGET_LIMIT:.2f}")
    log(f"  Circuit breaker:        {_circuit_breaker.state}")
    log(f"  Idle mode:              {_metrics['idle_mode']} "
        f"(consecutive: {_metrics['current_consecutive_idles']})")
    log(f"  Idle iterations saved:  {_metrics['idle_iterations_avoided']}")
    log(f"  Idle tokens saved est:  {_metrics['idle_tokens_saved_estimate']:,}")
    log("=" * 60)


# =============================================================================
# Exponential backoff
# =============================================================================

def backoff_delay(consecutive_failures: int) -> float:
    """Calculate exponential backoff delay after consecutive failures.

    Returns min(60 * 2^N, 3600) seconds — caps at 1 hour.
    """
    if consecutive_failures <= 0:
        return 0
    delay = min(60 * (2 ** consecutive_failures), 3600)
    return delay


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

def _is_retryable_error(exc: Exception) -> bool:
    """Check if an exception is a retryable connection error or 5xx."""
    if isinstance(exc, urllib.error.URLError):
        # Connection refused, DNS failure, timeout, etc.
        return True
    if isinstance(exc, urllib.error.HTTPError):
        return exc.code >= 500
    if isinstance(exc, (ConnectionError, TimeoutError, OSError)):
        return True
    if isinstance(exc, RuntimeError) and "HTTP 5" in str(exc):
        return True
    return False


def send_to_openclaw(prompt: str, timeout: int) -> dict:
    """Send a prompt to the OpenClaw gateway and return the parsed response.

    Retries up to HTTP_RETRY_COUNT times on connection errors or 5xx responses
    with exponential backoff (5s, 10s, 20s).  Retry sleeps are interruptible
    so shutdown signals are respected immediately.

    Sets a 30s connect timeout via socket.setdefaulttimeout for the duration
    of the call, then restores the previous default.

    The gateway exposes an OpenAI-compatible chat completions endpoint at
    /v1/chat/completions.
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

    last_exc = None

    for attempt in range(1, HTTP_RETRY_COUNT + 1):
        retry_delay = HTTP_RETRY_BASE_DELAY * (2 ** (attempt - 1))  # 5, 10, 20
        try:
            req = urllib.request.Request(url, data=payload, headers=headers, method="POST")
            # Use a dedicated socket timeout for this call to avoid
            # polluting the global default.
            old_timeout = socket.getdefaulttimeout()
            try:
                socket.setdefaulttimeout(30)
                resp = urllib.request.urlopen(req, timeout=timeout)
            finally:
                socket.setdefaulttimeout(old_timeout)
            body = resp.read().decode("utf-8")
            return json.loads(body)

        except urllib.error.HTTPError as exc:
            body = ""
            try:
                body = exc.read().decode("utf-8", errors="replace")
            except Exception:
                pass

            if exc.code >= 500 and attempt < HTTP_RETRY_COUNT:
                log(
                    f"  Retry {attempt}/{HTTP_RETRY_COUNT}: HTTP {exc.code} "
                    f"— waiting {retry_delay}s",
                    logging.WARNING,
                )
                last_exc = RuntimeError(
                    f"OpenClaw returned HTTP {exc.code}: {body[:500]}"
                )
                _interruptible_sleep(retry_delay)
                if _shutdown_requested:
                    break
                continue

            raise RuntimeError(
                f"OpenClaw returned HTTP {exc.code}: {body[:500]}"
            ) from exc

        except (urllib.error.URLError, ConnectionError, OSError) as exc:
            if attempt < HTTP_RETRY_COUNT:
                log(
                    f"  Retry {attempt}/{HTTP_RETRY_COUNT}: {type(exc).__name__}: {exc} "
                    f"— waiting {retry_delay}s",
                    logging.WARNING,
                )
                last_exc = exc
                _interruptible_sleep(retry_delay)
                if _shutdown_requested:
                    break
                continue
            raise

    # Should not reach here, but just in case
    if last_exc:
        raise last_exc
    raise RuntimeError("send_to_openclaw: exhausted retries with no result")


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

    Only counts as success if the agent explicitly includes a JSON summary
    with "result" set to "success", "partial", or "skipped".  Missing or
    unparseable summaries are treated as failures to prevent silent failures
    from being counted as successes.
    
    Additional heuristic: If the agent sends any tools (tool_calls), count as success.
    """
    text = extract_text_from_response(response)
    usage = response.get("usage", {})
    
    # If usage shows tokens were processed, that's a strong indicator of work done
    prompt_tokens = usage.get("prompt_tokens", 0)
    completion_tokens = usage.get("completion_tokens", 0)
    has_tokens = prompt_tokens > 0 or completion_tokens > 0
    
    # Try to find the structured JSON summary in the response
    # Use a more robust regex that handles nested JSON structures
    try:
        json_match = re.search(r'\{[\s\S]*?"result"[\s\S]*?\}', text)
        if json_match:
            summary = json.loads(json_match.group())
            result = summary.get("result", "").lower()
            if result in ("success", "partial", "skipped"):
                return True
            log(
                f"Agent reported result={result!r} — counting as failure",
                logging.WARNING,
            )
            return False
    except (json.JSONDecodeError, AttributeError):
        pass

    # No valid JSON summary found — do NOT assume success
    # UNLESS we have evidence of actual work (tokens processed or tool calls)
    if has_tokens:
        log(
            "No JSON summary but tokens were processed — counting as partial success",
            logging.INFO,
        )
        return True
    
    log(
        "No JSON summary with 'result' field found in agent response "
        "and no token usage — counting as failure to prevent silent failures",
        logging.WARNING,
    )
    return False


def _extract_result_field(response: dict) -> str:
    """Extract the 'result' field from the agent's JSON summary.

    Returns the result string (e.g. 'success', 'skipped', 'failure') or
    empty string if not found.
    
    Uses a more robust regex that handles nested JSON structures by using
    balanced brace matching.
    """
    text = extract_text_from_response(response)
    try:
        # Try to find JSON summary with "result" field
        # This pattern handles nested braces by being greedy and finding
        # the first '{' followed by '"result"' somewhere inside
        json_match = re.search(r'\{[\s\S]*?"result"[\s\S]*?\}', text)
        if json_match:
            summary = json.loads(json_match.group())
            return summary.get("result", "").lower()
    except (json.JSONDecodeError, AttributeError):
        pass
    return ""


# =============================================================================
# Idle detection — avoid wasting tokens when there is no work
# =============================================================================
#
# The biggest source of token waste is iterations where the agent checks for
# work, finds nothing, and returns "skipped".  Each such iteration still costs
# tokens for the full prompt + response round-trip.
#
# This module detects consecutive "no work" iterations and progressively
# increases the wait time between checks (exponential backoff capped at 15
# minutes).  It exits idle mode immediately when work appears or when the
# user signals via a RESUME file.
#
# Expected savings: 30-40% token reduction during idle periods.

# Phrases in the agent's response text that indicate no actionable work was
# found.  Checked case-insensitively against the full response text.
_IDLE_PHRASES = (
    "no issues",
    "no work",
    "nothing to do",
    "no open issues",
    "no open prs",
    "no failing",
    "no tasks",
    "no actionable",
    "all tests pass",
    "nothing actionable",
    "no changes needed",
    "repository is healthy",
    "everything is up to date",
    "skipping this iteration",
)

# Patterns that indicate the agent actually did real work (PR numbers,
# file modifications, branch creation).
_WORK_PATTERNS = (
    re.compile(r"#\d{1,6}"),                     # PR/issue number like #42
    re.compile(r"pr[_ ]number.*?:\s*\d+", re.I), # "pr_number": 42
    re.compile(r"git push"),                       # pushed a branch
    re.compile(r"gh pr create"),                   # created a PR
    re.compile(r"agent/\d{14}"),                   # agent branch name
)


def _response_indicates_idle(response: dict) -> bool:
    """Determine if the agent's response indicates no work was available.

    Detection is layered -- multiple signals are checked to avoid false
    positives/negatives:

      1. Structured result: if the JSON summary has "result": "skipped",
         that is a strong idle signal.
      2. Work indicators: if the response contains PR numbers, branch
         pushes, or file changes, the agent clearly did work regardless
         of what it said.  NOT idle.
      3. Idle phrases: if the response text contains known "no work"
         phrases and no work indicators, treat as idle.
    """
    result_field = _extract_result_field(response)
    text = extract_text_from_response(response)
    text_lower = text.lower()

    # -- Signal 1: explicit "skipped" result from the agent's JSON summary --
    if result_field == "skipped":
        # Even if the agent said "skipped", check for real work artifacts
        # (defensive -- the agent might mislabel an iteration).
        for pattern in _WORK_PATTERNS:
            if pattern.search(text):
                return False
        return True

    # -- Signal 2: strong work indicators override everything else ----------
    for pattern in _WORK_PATTERNS:
        if pattern.search(text):
            return False

    # -- Signal 3: idle phrases in freeform text ----------------------------
    for phrase in _IDLE_PHRASES:
        if phrase in text_lower:
            return True

    return False


def _idle_wait_seconds(consecutive_idles: int) -> int:
    """Calculate the idle-mode wait time with exponential backoff.

    Returns min(IDLE_BASE_WAIT * 2^min(consecutive_idles, IDLE_MAX_EXPONENT),
                IDLE_MAX_WAIT).

    Progression (with defaults):
      1 skip  ->  60s  (1 min)
      2 skips -> 120s  (2 min)
      3 skips -> 240s  (4 min)
      4 skips -> 480s  (8 min)
      5+ skips -> 900s (15 min, capped)
    """
    exponent = min(consecutive_idles, IDLE_MAX_EXPONENT)
    wait = IDLE_BASE_WAIT * (2 ** exponent)
    return min(wait, IDLE_MAX_WAIT)


def _check_idle_exit_conditions() -> str:
    """Check if any condition requires exiting idle mode early.

    Returns a reason string if idle should exit, or empty string to stay idle.
    Checked at the top of each main loop iteration before deciding whether
    to skip the gateway call.
    """
    # Condition 1: RESUME file exists -- user explicitly wants to wake up
    resume_file = WORKSPACE_DIR / "RESUME"
    if resume_file.exists():
        try:
            resume_file.unlink()
        except OSError:
            pass
        return "RESUME file detected"

    # Condition 2: metrics.json was touched by an external process (e.g.,
    # a webhook handler writing "new_issue: true").  For now we rely on
    # the RESUME file and the forced check interval -- no GitHub token
    # needed from the orchestrator.

    return ""


def _estimate_avg_tokens_per_iteration() -> int:
    """Estimate average tokens per iteration for idle-savings tracking.

    Uses actual data when available, falls back to a conservative default.
    """
    total_iters = _metrics["total_iterations"]
    if total_iters > 0:
        total_tokens = (
            _metrics["total_prompt_tokens"] + _metrics["total_completion_tokens"]
        )
        return max(total_tokens // total_iters, 1)
    return 5000  # conservative default


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
# Interruptible sleep helper
# =============================================================================

def _interruptible_sleep(seconds: float):
    """Sleep in small increments so we can respond to signals promptly."""
    remaining = seconds
    while remaining > 0 and not _shutdown_requested:
        chunk = min(remaining, 5)
        time.sleep(chunk)
        remaining -= chunk


# =============================================================================
# Main loop
# =============================================================================

def main():
    """Entry point: run the orchestrator loop."""
    # Effective cooldown is the larger of COOLDOWN_SECONDS and LOOP_INTERVAL
    effective_cooldown = max(COOLDOWN_SECONDS, LOOP_INTERVAL)

    log("=" * 60)
    log("stilltent orchestrator starting")
    log(f"  MODEL                     = qwen/qwen3-coder-next")
    log(f"  MODEL PRICING             = $0.12/M input, $0.75/M output")
    log(f"  BUDGET_LIMIT              = ${BUDGET_LIMIT:.2f} for {TOTAL_RUNTIME_HOURS}h runtime")
    log(f"  TOTAL_RUNTIME_HOURS       = {TOTAL_RUNTIME_HOURS}h")
    log(f"  OPENCLAW_URL              = {OPENCLAW_URL}")
    log(f"  LOOP_INTERVAL             = {LOOP_INTERVAL}s")
    log(f"  COOLDOWN_SECONDS          = {COOLDOWN_SECONDS}s")
    log(f"  EFFECTIVE_COOLDOWN        = {effective_cooldown}s (base, adaptive may vary)")
    log(f"  ITERATION_TIMEOUT         = {ITERATION_TIMEOUT}s")
    log(f"  MAX_CONSECUTIVE_FAILURES  = {MAX_CONSECUTIVE_FAILURES}")
    log(f"  HTTP_RETRY_COUNT          = {HTTP_RETRY_COUNT}")
    log(f"  HTTP_RETRY_BASE_DELAY     = {HTTP_RETRY_BASE_DELAY}s (backoff: 5, 10, 20)")
    log(f"  IDLE_BASE_WAIT            = {IDLE_BASE_WAIT}s")
    log(f"  IDLE_MAX_WAIT             = {IDLE_MAX_WAIT}s ({IDLE_MAX_WAIT // 60} min)")
    log(f"  IDLE_FORCE_CHECK          = {IDLE_FORCE_CHECK_INTERVAL}s ({IDLE_FORCE_CHECK_INTERVAL // 60} min)")
    log(f"  WORKSPACE_DIR             = {WORKSPACE_DIR}")
    log(f"  LOG_FILE                  = {LOG_FILE}")
    log(f"  TARGET_REPO               = {TARGET_REPO or '(not set)'}")
    log("=" * 60)

    # Start the background metrics writer
    _metrics_writer.start()

    # Resume counters from a prior run if the metrics file exists
    _load_metrics()
    iteration = _metrics["total_iterations"]
    consecutive_failures = 0

    # -- Idle detection state --------------------------------------------------
    # consecutive_idles: how many iterations in a row had no work.
    # idle_since: monotonic timestamp of when idle mode was entered (0 = not idle).
    consecutive_idles = 0
    idle_since: float = 0.0

    try:
        while not _shutdown_requested:
            # -- 0. Check total runtime limit ----------------------------------
            elapsed_hours = (time.monotonic() - _start_time) / 3600
            if elapsed_hours >= TOTAL_RUNTIME_HOURS:
                log(
                    f"Scheduled shutdown after {elapsed_hours:.1f} hours "
                    f"(limit: {TOTAL_RUNTIME_HOURS}h)"
                )
                pause_file = WORKSPACE_DIR / "PAUSE"
                try:
                    pause_file.parent.mkdir(parents=True, exist_ok=True)
                    pause_file.write_text(
                        f"Scheduled shutdown after {elapsed_hours:.1f} hours\n",
                        encoding="utf-8",
                    )
                except OSError as exc:
                    log(f"Could not create PAUSE file: {exc}", logging.ERROR)
                break

            # -- 0b. Budget guard ----------------------------------------------
            if _check_budget_guard():
                break

            # -- 1. Check for PAUSE file ---------------------------------------
            pause_file = WORKSPACE_DIR / "PAUSE"
            if pause_file.exists():
                log("PAUSED — remove workspace/PAUSE to resume")
                _interruptible_sleep(effective_cooldown)
                continue

            # -- 1b. Check idle exit conditions --------------------------------
            if consecutive_idles > 0:
                exit_reason = _check_idle_exit_conditions()
                if exit_reason:
                    log(f"Idle exit: {exit_reason} — resuming normal intervals")
                    consecutive_idles = 0
                    idle_since = 0.0
                    _metrics["idle_mode"] = False
                    _metrics["current_consecutive_idles"] = 0
                    _metrics_writer.mark_dirty()

            # -- 1c. Idle mode: skip iteration if wait not elapsed -------------
            if consecutive_idles > 0 and idle_since > 0:
                idle_wait = _idle_wait_seconds(consecutive_idles)
                elapsed_idle = time.monotonic() - idle_since
                # Check if forced check interval has elapsed
                force_check = elapsed_idle >= IDLE_FORCE_CHECK_INTERVAL
                if not force_check and elapsed_idle < idle_wait:
                    # Still in idle wait -- sleep a small chunk and loop back
                    # to re-check conditions (PAUSE, RESUME, shutdown, etc.)
                    remaining_wait = idle_wait - elapsed_idle
                    sleep_chunk = min(remaining_wait, effective_cooldown)
                    _interruptible_sleep(sleep_chunk)
                    continue
                if force_check:
                    log(f"Idle: forced check after {int(elapsed_idle)}s "
                        f"— probing for new work")

            # -- 2. Check consecutive failure threshold ------------------------
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

            # -- 2b. Exponential backoff on consecutive failures ---------------
            if consecutive_failures > 0:
                delay = backoff_delay(consecutive_failures)
                log(
                    f"Backoff: {consecutive_failures} consecutive failures "
                    f"— waiting {delay:.0f}s before retry"
                )
                _interruptible_sleep(delay)
                if _shutdown_requested:
                    break

            # -- 3. Check circuit breaker before calling gateway ---------------
            iteration += 1

            if consecutive_idles > 0:
                log(f"=== Iteration {iteration} starting "
                    f"(idle check #{consecutive_idles + 1}) ===")
            else:
                log(f"=== Iteration {iteration} starting ===")

            if not _circuit_breaker.allow_request():
                log(
                    f"CircuitBreaker OPEN — skipping gateway call "
                    f"(cooldown {_circuit_breaker.open_duration:.0f}s)",
                    logging.WARNING,
                )
                consecutive_failures += 1
                _metrics["failed_iterations"] += 1
                write_metrics(iteration, consecutive_failures)
                _interruptible_sleep(min(30, effective_cooldown))
                continue

            prompt = build_prompt(iteration)
            response = None

            try:
                response = send_to_openclaw(prompt, timeout=ITERATION_TIMEOUT)

                # Track token usage from response
                prompt_tokens, completion_tokens = _extract_token_usage(response)
                
                # Handle the case where token usage is 0 but response was valid
                # This can happen with some API implementations
                if prompt_tokens == 0 and completion_tokens == 0:
                    # Try to detect if the agent actually responded
                    if response.get("choices") and len(response.get("choices", [])) > 0:
                        content = response["choices"][0].get("message", {}).get("content", "")
                        if content and len(content.strip()) > 10:
                            log(
                                "  Token usage shows 0 tokens but agent responded — "
                                "considering this a valid iteration",
                                logging.INFO,
                            )
                            # Count as a successful iteration with minimal token usage
                            prompt_tokens = 1
                            completion_tokens = 1

                # Per-iteration cost tracking
                iter_cost = _calculate_iteration_cost(prompt_tokens, completion_tokens)
                _metrics["cumulative_spend"] += iter_cost
                log(f"  Iteration cost: ${iter_cost:.4f} "
                    f"(prompt={prompt_tokens}, completion={completion_tokens}) "
                    f"| Cumulative: ${_metrics['cumulative_spend']:.4f}")

                if response_indicates_success(response):
                    consecutive_failures = 0
                    _metrics["successful_iterations"] += 1
                    _circuit_breaker.record_success()

                    result = _extract_result_field(response)
                    if result == "skipped":
                        _metrics["skipped_iterations"] += 1
                    log(f"Iteration {iteration} completed successfully "
                        f"(result={result!r})")
                else:
                    consecutive_failures += 1
                    _metrics["failed_iterations"] += 1
                    _circuit_breaker.record_failure()
                    log(
                        f"Iteration {iteration} failed "
                        f"(consecutive: {consecutive_failures})"
                    )

            except (TimeoutError, urllib.error.URLError) as exc:
                consecutive_failures += 1
                _metrics["failed_iterations"] += 1
                _circuit_breaker.record_failure()
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
                _circuit_breaker.record_failure()
                log(
                    f"Iteration {iteration} ERROR: {exc} "
                    f"(consecutive: {consecutive_failures})"
                )

            # -- 4. Write metrics (non-blocking — updates in-memory only) ------
            write_metrics(iteration, consecutive_failures)

            # -- 5. Periodic health summary ------------------------------------
            if iteration % 50 == 0:
                log_health_summary(iteration)

            # -- 6. Idle detection & adaptive cooldown -------------------------
            if _shutdown_requested:
                break

            if response is not None and _response_indicates_idle(response):
                # --- Enter or continue idle mode ---
                consecutive_idles += 1
                idle_wait = _idle_wait_seconds(consecutive_idles)
                wait_min = idle_wait / 60

                if consecutive_idles == 1:
                    # First idle — entering idle mode
                    idle_since = time.monotonic()
                    log(f"Idle: no work detected — entering idle mode. "
                        f"Waiting {idle_wait}s ({wait_min:.0f} min) "
                        f"before next check")
                else:
                    # Continuing idle — update the idle-since timestamp for the
                    # new backoff window.
                    idle_since = time.monotonic()
                    # Track how many iterations we would have run at normal
                    # cooldown rate during this idle wait, minus the one we
                    # just ran.
                    avoided = max((idle_wait // effective_cooldown) - 1, 0)
                    _metrics["idle_iterations_avoided"] += avoided
                    _metrics["idle_tokens_saved_estimate"] += (
                        avoided * _estimate_avg_tokens_per_iteration()
                    )
                    log(f"Idle: {consecutive_idles} consecutive skips — "
                        f"waiting {idle_wait}s ({wait_min:.0f} min). "
                        f"~{_metrics['idle_iterations_avoided']} "
                        f"iterations avoided so far")

                _metrics["idle_mode"] = True
                _metrics["current_consecutive_idles"] = consecutive_idles
                _metrics_writer.mark_dirty()

                _interruptible_sleep(idle_wait)
            else:
                # --- Work was found or error occurred — exit idle mode ---
                if consecutive_idles > 0:
                    total_idle_secs = (
                        time.monotonic() - idle_since if idle_since > 0 else 0
                    )
                    log(f"Work found! Exiting idle mode after "
                        f"{consecutive_idles} idle iterations "
                        f"({int(total_idle_secs)}s total idle time). "
                        f"Resuming {effective_cooldown}s intervals")
                    consecutive_idles = 0
                    idle_since = 0.0
                    _metrics["idle_mode"] = False
                    _metrics["current_consecutive_idles"] = 0
                    _metrics_writer.mark_dirty()

                log(f"Cooling down {effective_cooldown}s before next iteration")
                _interruptible_sleep(effective_cooldown)

    finally:
        # ---- Clean shutdown --------------------------------------------------
        log("Shutting down gracefully")
        _metrics["status"] = "stopped"
        _metrics["uptime_seconds"] = int(time.monotonic() - _start_time)
        _metrics_writer.mark_dirty()
        _metrics_writer.stop()
        spend_str, _ = _estimate_spend()
        log(f"Final stats: {_metrics['total_iterations']} iterations, "
            f"spend: {spend_str}, "
            f"idle iterations avoided: {_metrics['idle_iterations_avoided']}, "
            f"idle tokens saved: ~{_metrics['idle_tokens_saved_estimate']:,}")
        log("Goodbye")


if __name__ == "__main__":
    main()
