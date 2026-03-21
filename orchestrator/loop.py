#!/usr/bin/env python3
"""
stilltent orchestrator — autonomous agent loop driver

Sends trigger prompts to the OpenClaw gateway on a configurable interval,
monitors for hangs (via timeout), tracks success/failure metrics, and
auto-pauses after too many consecutive failures.

Designed for multi-day unattended runs (3-5 days). Features:
  - Exponential backoff on consecutive failures (caps at 1 hour)
  - Scheduled shutdown after TOTAL_RUNTIME_HOURS
  - Periodic health summaries every 50 iterations
  - Cooldown between iterations to avoid API hammering
  - Retry wrapper on HTTP calls (3 retries on connection/5xx errors)
  - Dual logging to stdout and workspace/orchestrator.log

The orchestrator does NOT make decisions — the agent (via SKILL.md) makes
all decisions. This script only:

  1. Checks if the agent should run (no PAUSE file, not too many failures)
  2. Sends a trigger prompt to the OpenClaw gateway
  3. Waits for the response (with a timeout)
  4. Logs the result and writes metrics
  5. Sleeps for the configured cooldown
  6. Repeats

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
    TOTAL_RUNTIME_HOURS        Graceful shutdown after this many hours (default: 120)
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
TOTAL_RUNTIME_HOURS = float(os.environ.get("TOTAL_RUNTIME_HOURS", "120"))
WORKSPACE_DIR = Path(os.environ.get("WORKSPACE_DIR", "/workspace"))
LOG_FILE = Path(os.environ.get("LOG_FILE", str(WORKSPACE_DIR / "orchestrator.log")))
OPENCLAW_GATEWAY_TOKEN = os.environ.get("OPENCLAW_GATEWAY_TOKEN", "")
TARGET_REPO = os.environ.get("TARGET_REPO", "")
DAILY_BUDGET_LIMIT = float(os.environ.get("DAILY_BUDGET_LIMIT", "5.0"))

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
    "current_consecutive_failures": 0,
    "success_rate": 0.0,
    "last_iteration_at": None,
    "uptime_seconds": 0,
    "status": "starting",
    "total_prompt_tokens": 0,
    "total_completion_tokens": 0,
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
            _metrics["total_prompt_tokens"] = saved.get("total_prompt_tokens", 0)
            _metrics["total_completion_tokens"] = saved.get("total_completion_tokens", 0)
            log(f"Resumed metrics: {_metrics['total_iterations']} prior iterations")
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
    prompt_tokens = usage.get("prompt_tokens", 0)
    completion_tokens = usage.get("completion_tokens", 0)
    _metrics["total_prompt_tokens"] += prompt_tokens
    _metrics["total_completion_tokens"] += completion_tokens
    return prompt_tokens, completion_tokens


def _estimate_spend() -> tuple[str, float]:
    """Rough spend estimate based on accumulated token counts.

    Returns (formatted_string, raw_float) so callers can display or compare.

    Uses approximate OpenRouter pricing for typical models:
      - Prompt:     $3.00 / 1M tokens
      - Completion: $15.00 / 1M tokens
    These are rough estimates — actual prices vary by model.
    """
    prompt_cost = (_metrics["total_prompt_tokens"] / 1_000_000) * 3.00
    completion_cost = (_metrics["total_completion_tokens"] / 1_000_000) * 15.00
    total = prompt_cost + completion_cost
    return f"${total:.2f}", total


def _check_budget_guard() -> bool:
    """Check if estimated daily spend exceeds the budget limit.

    Extrapolates current spend to a 24h rate based on elapsed wall-clock time.
    Returns True if the budget guard has triggered (caller should stop).
    """
    elapsed_hours = (time.monotonic() - _start_time) / 3600
    if elapsed_hours < 0.01:
        return False  # too early to estimate

    _, total_spend = _estimate_spend()
    daily_rate = (total_spend / elapsed_hours) * 24.0

    if daily_rate > DAILY_BUDGET_LIMIT:
        log(
            f"BUDGET GUARD: estimated daily spend ${daily_rate:.2f} "
            f"exceeds limit ${DAILY_BUDGET_LIMIT:.2f}. "
            f"Total spend so far: ${total_spend:.2f} over {elapsed_hours:.1f}h. "
            f"Creating PAUSE file.",
            logging.ERROR,
        )
        pause_file = WORKSPACE_DIR / "PAUSE"
        try:
            pause_file.parent.mkdir(parents=True, exist_ok=True)
            pause_file.write_text(
                f"Budget guard: estimated daily spend ${daily_rate:.2f} "
                f"exceeds limit ${DAILY_BUDGET_LIMIT:.2f}\n",
                encoding="utf-8",
            )
        except OSError as exc:
            log(f"Could not create PAUSE file: {exc}", logging.ERROR)
        return True
    return False


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
    daily_rate = (spend_total / elapsed_hours * 24.0) if elapsed_hours > 0.01 else 0.0

    log("=" * 60)
    log(f"HEALTH SUMMARY (every 50 iterations)")
    log(f"  Total iterations:    {iteration}")
    log(f"  Successful:          {_metrics['successful_iterations']}")
    log(f"  Failed:              {_metrics['failed_iterations']}")
    log(f"  Success rate:        {success_rate:.1f}%")
    log(f"  Wall-clock hours:    {elapsed_hours:.1f}h")
    log(f"  Total tokens used:   {total_tokens:,}")
    log(f"  Estimated spend:     {spend_str} (daily rate: ${daily_rate:.2f})")
    log(f"  Budget limit:        ${DAILY_BUDGET_LIMIT:.2f}/day")
    log(f"  Circuit breaker:     {_circuit_breaker.state}")
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
    """
    text = extract_text_from_response(response)

    # Try to find the structured JSON summary in the response
    try:
        json_match = re.search(r'\{[^{}]*"result"[^{}]*\}', text)
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
    log(
        "No JSON summary with 'result' field found in agent response "
        "— counting as failure to prevent silent failures",
        logging.WARNING,
    )
    return False


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
    log(f"  OPENCLAW_URL              = {OPENCLAW_URL}")
    log(f"  LOOP_INTERVAL             = {LOOP_INTERVAL}s")
    log(f"  COOLDOWN_SECONDS          = {COOLDOWN_SECONDS}s")
    log(f"  EFFECTIVE_COOLDOWN        = {effective_cooldown}s")
    log(f"  ITERATION_TIMEOUT         = {ITERATION_TIMEOUT}s")
    log(f"  MAX_CONSECUTIVE_FAILURES  = {MAX_CONSECUTIVE_FAILURES}")
    log(f"  TOTAL_RUNTIME_HOURS       = {TOTAL_RUNTIME_HOURS}h")
    log(f"  HTTP_RETRY_COUNT          = {HTTP_RETRY_COUNT}")
    log(f"  HTTP_RETRY_BASE_DELAY     = {HTTP_RETRY_BASE_DELAY}s (backoff: 5, 10, 20)")
    log(f"  DAILY_BUDGET_LIMIT        = ${DAILY_BUDGET_LIMIT:.2f}")
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

            try:
                response = send_to_openclaw(prompt, timeout=ITERATION_TIMEOUT)

                # Track token usage from response
                _extract_token_usage(response)

                if response_indicates_success(response):
                    consecutive_failures = 0
                    _metrics["successful_iterations"] += 1
                    _circuit_breaker.record_success()
                    log(f"Iteration {iteration} completed successfully")
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

            # -- 6. Cooldown ---------------------------------------------------
            if _shutdown_requested:
                break
            log(f"Cooling down {effective_cooldown}s before next iteration")
            _interruptible_sleep(effective_cooldown)

    finally:
        # ---- Clean shutdown --------------------------------------------------
        log("Shutting down gracefully")
        _metrics["status"] = "stopped"
        _metrics["uptime_seconds"] = int(time.monotonic() - _start_time)
        _metrics_writer.mark_dirty()
        _metrics_writer.stop()
        log("Goodbye")


if __name__ == "__main__":
    main()
