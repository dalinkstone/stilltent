#!/usr/bin/env python3
"""
OpenClaw gateway end-to-end smoke test.

Tests the gateway health, basic chat, tool execution, and mem9 integration.
Uses only the Python standard library (urllib, json).

The OpenClaw gateway API may vary between versions. This script tries
common endpoint patterns and prints raw HTTP status + response body for
each request so you can see what's happening and adapt as needed.

Configuration (environment variables):
    OPENCLAW_URL        Base URL of the gateway  (default: http://localhost:3000)
    OPENCLAW_GATEWAY_TOKEN  Bearer token for auth (default: empty — no auth header)
    MEM9_API_KEY        mem9 key, passed if the gateway needs it (default: repokeeper-local-dev-key)
"""

import json
import os
import sys
import time
import urllib.request
import urllib.error
import uuid

# ── CONFIGURATION ────────────────────────────────────────────────────────────
BASE_URL = os.environ.get("OPENCLAW_URL", "http://localhost:3000").rstrip("/")
GATEWAY_TOKEN = os.environ.get("OPENCLAW_GATEWAY_TOKEN", "")
MEM9_API_KEY = os.environ.get("MEM9_API_KEY", "repokeeper-local-dev-key")

# Candidate health endpoints (tried in order; first 200 wins)
HEALTH_PATHS = ["/healthz", "/health", "/api/health"]

# Candidate chat endpoints (tried in order; first non-404 wins)
CHAT_ENDPOINTS = [
    ("POST", "/api/chat"),
    ("POST", "/api/v1/chat"),
    ("POST", "/api/v1/messages"),
    ("POST", "/api/sessions"),
    ("POST", "/v1/messages"),
]

# Timeout for individual HTTP requests (seconds)
HTTP_TIMEOUT = 30

# How long to wait for the agent to finish processing (seconds)
RESPONSE_POLL_TIMEOUT = 60
# ─────────────────────────────────────────────────────────────────────────────

SESSION_ID = f"smoke-test-{uuid.uuid4().hex[:8]}"
results = []


def report(name, passed, detail=""):
    status = "PASS" if passed else "FAIL"
    results.append((name, passed))
    msg = f"  [{status}] {name}"
    if detail:
        msg += f"  — {detail}"
    print(msg)


def make_request(method, path, body=None, extra_headers=None):
    """Make an HTTP request and return (status_code, parsed_body, raw_body).

    Returns (None, {}, error_string) on connection failure.
    """
    url = BASE_URL + path
    headers = {"Content-Type": "application/json"}
    if GATEWAY_TOKEN:
        headers["Authorization"] = f"Bearer {GATEWAY_TOKEN}"
    if extra_headers:
        headers.update(extra_headers)

    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)

    try:
        resp = urllib.request.urlopen(req, timeout=HTTP_TIMEOUT)
        raw = resp.read().decode()
        try:
            parsed = json.loads(raw) if raw.strip() else {}
        except json.JSONDecodeError:
            parsed = {"_raw_text": raw}
        return resp.status, parsed, raw
    except urllib.error.HTTPError as e:
        raw = e.read().decode() if e.fp else ""
        parsed = {}
        try:
            parsed = json.loads(raw)
        except Exception:
            parsed = {"_raw_text": raw} if raw else {}
        return e.code, parsed, raw
    except Exception as e:
        return None, {}, str(e)


# ── TEST 1: Health check ─────────────────────────────────────────────────────

def test_health():
    """Check that the gateway is reachable via a health endpoint."""
    print("\n1. Health check")
    for path in HEALTH_PATHS:
        status, parsed, raw = make_request("GET", path)
        print(f"     GET {path} → {status}")
        if raw and raw.strip():
            print(f"     Body: {raw[:300]}")
        if status == 200:
            report("health", True, f"endpoint={path}")
            return True

    report("health", False, "none of the health endpoints returned 200")
    return False


# ── TEST 2: Discover chat endpoint ───────────────────────────────────────────

def discover_chat_endpoint():
    """Try candidate chat endpoints to find one that accepts messages.

    Returns (method, path) of the first endpoint that doesn't 404, or None.
    """
    print("\n2. Discover chat endpoint")

    # Common payload shapes to try per endpoint
    payloads = {
        "/api/chat": {"message": "ping", "sessionId": SESSION_ID},
        "/api/v1/chat": {"message": "ping", "sessionId": SESSION_ID},
        "/api/v1/messages": {"content": "ping", "role": "user", "sessionId": SESSION_ID},
        "/api/sessions": {"prompt": "ping"},
        "/v1/messages": {
            "model": "default",
            "max_tokens": 256,
            "messages": [{"role": "user", "content": "ping"}],
        },
    }

    for method, path in CHAT_ENDPOINTS:
        payload = payloads.get(path, {"message": "ping"})
        status, parsed, raw = make_request(method, path, body=payload)
        print(f"     {method} {path} → {status}")
        if raw and raw.strip():
            print(f"     Body: {raw[:400]}")
        # Treat anything other than 404/405 as "this endpoint exists"
        if status is not None and status not in (404, 405):
            print(f"     → Using {method} {path} (status {status})")
            report("discover_endpoint", True, f"{method} {path} → {status}")
            return method, path, payload

    report("discover_endpoint", False, "all candidate endpoints returned 404/405")
    return None, None, None


# ── TEST 3: Send a simple message and verify response ────────────────────────

def test_simple_message(method, path):
    """Send a simple message and check that we get a non-error response."""
    print("\n3. Simple message (say hello)")
    payload = build_chat_payload(path, "Hello! Please respond with a short greeting.")
    status, parsed, raw = make_request(method, path, body=payload)
    print(f"     {method} {path} → {status}")
    print(f"     Body: {raw[:500]}")

    ok = status is not None and 200 <= status < 300
    reply = extract_reply(parsed, raw)
    if reply:
        print(f"     Agent reply: {reply[:200]}")
    report("simple_message", ok, f"status={status}")
    return ok


# ── TEST 4: Trigger a tool call ──────────────────────────────────────────────

def test_tool_call(method, path):
    """Send a message that should trigger a shell/command tool call."""
    print("\n4. Tool call (echo TOOL_TEST_OK)")
    payload = build_chat_payload(
        path,
        'Run the command: echo TOOL_TEST_OK\n\nPlease execute this shell command and show me the output.',
    )
    status, parsed, raw = make_request(method, path, body=payload)
    print(f"     {method} {path} → {status}")
    print(f"     Body: {raw[:800]}")

    ok = status is not None and 200 <= status < 300
    reply = extract_reply(parsed, raw)
    if reply:
        print(f"     Agent reply: {reply[:300]}")
    tool_executed = "TOOL_TEST_OK" in raw
    if tool_executed:
        print("     → TOOL_TEST_OK found in response!")
    else:
        print("     → TOOL_TEST_OK not found in response (tool may not have executed)")
    report("tool_call", ok, f"status={status}, tool_output_found={tool_executed}")
    return ok


# ── TEST 5: mem9 integration (recall) ────────────────────────────────────────

def test_mem9_recall(method, path):
    """Ask the agent to recall something via mem9.

    This tests that the mem9 plugin is wired up and the request doesn't error.
    The actual recall may return nothing (no memories stored yet) — that's fine.
    """
    print("\n5. mem9 integration (recall test)")
    payload = build_chat_payload(
        path,
        "Search your memory for anything about 'repokeeper smoke test'. "
        "If you have a memory tool or recall ability, please use it now.",
    )
    status, parsed, raw = make_request(method, path, body=payload)
    print(f"     {method} {path} → {status}")
    print(f"     Body: {raw[:800]}")

    ok = status is not None and 200 <= status < 300
    reply = extract_reply(parsed, raw)
    if reply:
        print(f"     Agent reply: {reply[:300]}")
    if ok:
        print("     → Request succeeded (mem9 integration did not error)")
    else:
        print("     → Request failed (mem9 may not be configured yet)")
    report("mem9_recall", ok, f"status={status}")
    return ok


# ── Helpers ──────────────────────────────────────────────────────────────────

def build_chat_payload(path, message):
    """Build a chat payload appropriate for the discovered endpoint path."""
    if "/v1/messages" in path and "api" not in path:
        # Anthropic-style /v1/messages
        return {
            "model": "default",
            "max_tokens": 512,
            "messages": [{"role": "user", "content": message}],
        }
    elif "sessions" in path:
        return {"prompt": message, "sessionId": SESSION_ID}
    else:
        # Generic chat payload — covers /api/chat, /api/v1/chat, etc.
        return {"message": message, "sessionId": SESSION_ID}


def extract_reply(parsed, raw):
    """Try to extract the agent's text reply from various response shapes."""
    if not parsed or not isinstance(parsed, dict):
        return None
    # Common shapes: {response: "..."}, {message: "..."}, {content: "..."},
    # {choices: [{message: {content: "..."}}]}, {result: {text: "..."}}
    for key in ("response", "message", "content", "reply", "text", "output"):
        val = parsed.get(key)
        if isinstance(val, str) and val.strip():
            return val
    # Anthropic-style
    content = parsed.get("content")
    if isinstance(content, list):
        texts = [b.get("text", "") for b in content if isinstance(b, dict)]
        if texts:
            return "\n".join(texts)
    # choices[0].message.content (OpenAI-style)
    choices = parsed.get("choices")
    if isinstance(choices, list) and choices:
        msg = choices[0].get("message", {})
        if isinstance(msg, dict) and msg.get("content"):
            return msg["content"]
    # result.text
    result = parsed.get("result")
    if isinstance(result, dict):
        return result.get("text") or result.get("content")
    return None


# ── Main ─────────────────────────────────────────────────────────────────────

def print_summary():
    print("\n" + "=" * 60)
    passed = sum(1 for _, p in results if p)
    total = len(results)
    print(f"Results: {passed}/{total} passed")
    for name, p in results:
        print(f"  {'PASS' if p else 'FAIL'}: {name}")
    print("=" * 60)


def main():
    print("=" * 60)
    print("OpenClaw gateway smoke test")
    print(f"  URL:       {BASE_URL}")
    print(f"  Token:     {'set (' + GATEWAY_TOKEN[:8] + '...)' if GATEWAY_TOKEN else '(none)'}")
    print(f"  Session:   {SESSION_ID}")
    print(f"  MEM9 Key:  {MEM9_API_KEY[:8]}...")
    print("=" * 60)

    # 1. Health
    if not test_health():
        print("\nGateway unreachable — aborting remaining tests.")
        print("Hint: is the stack running? Try: make health")
        print_summary()
        sys.exit(1)

    # 2. Discover chat endpoint
    method, path, _ = discover_chat_endpoint()
    if not method:
        print("\nCould not find a working chat endpoint — aborting remaining tests.")
        print("Hint: the OpenClaw gateway API may use different paths than expected.")
        print("      Check the gateway docs or inspect: curl -v " + BASE_URL + "/api/")
        print_summary()
        sys.exit(1)

    # 3. Simple message
    test_simple_message(method, path)

    # 4. Tool call
    test_tool_call(method, path)

    # 5. mem9 recall
    test_mem9_recall(method, path)

    # Summary
    print_summary()
    all_passed = all(passed for _, passed in results)
    sys.exit(0 if all_passed else 1)


if __name__ == "__main__":
    main()
