#!/usr/bin/env python3
"""
Claude Code adapter — exposes the same HTTP interface as OpenClaw gateway
(POST /v1/chat/completions, GET /healthz) but uses the Anthropic API with
tool_use to drive an autonomous coding agent.

The adapter translates between the OpenAI-compatible chat completions format
(what the orchestrator sends) and the Anthropic Messages API with tools.

Tool calls are executed locally: shell commands run in the workspace, files
are read/written directly, git operations use the CLI, and memory operations
hit the mem9 API.

Usage:
    python adapter.py
    LISTEN_PORT=18789 python adapter.py

Environment:
    ANTHROPIC_API_KEY    Anthropic API key (required)
    LISTEN_PORT          Port to listen on (default: 18789)
    WORKSPACE_DIR        Workspace directory (default: /workspace)
    GITHUB_TOKEN         GitHub token for git operations
    TARGET_REPO          Target repo in owner/repo format
    AGENT_MEMORY_URL     Memory API URL (default: http://mnemo-server:8082)
    MEM9_API_KEY         Memory API key
    ANTHROPIC_MODEL      Model to use (default: claude-sonnet-4-20250514)
    MAX_TOOL_ROUNDS      Max tool-use rounds per request (default: 50)
"""

import json
import logging
import os
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path

# =============================================================================
# Configuration
# =============================================================================

LISTEN_PORT = int(os.environ.get("LISTEN_PORT", "18789"))
WORKSPACE_DIR = Path(os.environ.get("WORKSPACE_DIR", "/workspace"))
ANTHROPIC_API_KEY = os.environ.get("ANTHROPIC_API_KEY", "")
ANTHROPIC_MODEL = os.environ.get("ANTHROPIC_MODEL", "claude-sonnet-4-20250514")
GITHUB_TOKEN = os.environ.get("GITHUB_TOKEN", "")
TARGET_REPO = os.environ.get("TARGET_REPO", "")
AGENT_MEMORY_URL = os.environ.get("AGENT_MEMORY_URL", "http://mnemo-server:8082")
MEM9_API_KEY = os.environ.get("MEM9_API_KEY", "stilltent-local-dev-key")
MAX_TOOL_ROUNDS = int(os.environ.get("MAX_TOOL_ROUNDS", "50"))

TOOLS_FILE = Path(__file__).parent / "tools.json"

# =============================================================================
# Logging
# =============================================================================

logging.basicConfig(
    level=logging.INFO,
    format="[%(asctime)s] [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%dT%H:%M:%SZ",
    stream=sys.stdout,
)
logger = logging.getLogger("claude-code-adapter")

# =============================================================================
# Tool definitions — loaded from tools.json
# =============================================================================

_tools = []


def load_tools():
    """Load tool definitions from tools.json."""
    global _tools
    if TOOLS_FILE.exists():
        with open(TOOLS_FILE) as f:
            _tools = json.load(f)
        logger.info("Loaded %d tools from %s", len(_tools), TOOLS_FILE)
    else:
        logger.warning("tools.json not found at %s — no tools available", TOOLS_FILE)


# =============================================================================
# Tool execution
# =============================================================================


def execute_tool(name: str, input_data: dict) -> str:
    """Execute a tool call and return the result as a string."""
    try:
        if name == "shell":
            return _tool_shell(input_data)
        elif name == "file_read":
            return _tool_file_read(input_data)
        elif name == "file_write":
            return _tool_file_write(input_data)
        elif name == "git":
            return _tool_git(input_data)
        elif name == "memory_store":
            return _tool_memory_store(input_data)
        elif name == "memory_search":
            return _tool_memory_search(input_data)
        else:
            return json.dumps({"error": f"Unknown tool: {name}"})
    except Exception as exc:
        logger.error("Tool %s failed: %s", name, exc)
        return json.dumps({"error": str(exc)})


def _tool_shell(input_data: dict) -> str:
    """Execute a shell command in the workspace."""
    command = input_data.get("command", "")
    timeout = min(input_data.get("timeout", 120), 300)
    cwd = str(WORKSPACE_DIR / "repo")

    try:
        result = subprocess.run(
            ["sh", "-c", command],
            capture_output=True,
            text=True,
            timeout=timeout,
            cwd=cwd,
            env={
                **os.environ,
                "HOME": os.environ.get("HOME", "/root"),
                "PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin"),
            },
        )
        output = result.stdout
        if result.stderr:
            output += "\n--- stderr ---\n" + result.stderr
        # Truncate very long outputs
        if len(output) > 50000:
            output = output[:25000] + "\n\n[... truncated ...]\n\n" + output[-25000:]
        return json.dumps({
            "exit_code": result.returncode,
            "output": output,
        })
    except subprocess.TimeoutExpired:
        return json.dumps({"error": f"Command timed out after {timeout}s"})


def _tool_file_read(input_data: dict) -> str:
    """Read a file from the workspace."""
    path = input_data.get("path", "")
    if not path:
        return json.dumps({"error": "path is required"})

    filepath = WORKSPACE_DIR / "repo" / path
    try:
        content = filepath.read_text(encoding="utf-8", errors="replace")
        if len(content) > 100000:
            content = content[:50000] + "\n\n[... truncated ...]\n\n" + content[-50000:]
        return json.dumps({"path": path, "content": content})
    except FileNotFoundError:
        return json.dumps({"error": f"File not found: {path}"})
    except IsADirectoryError:
        entries = sorted(p.name for p in filepath.iterdir())
        return json.dumps({"path": path, "type": "directory", "entries": entries[:200]})


def _tool_file_write(input_data: dict) -> str:
    """Write content to a file in the workspace."""
    path = input_data.get("path", "")
    content = input_data.get("content", "")
    if not path:
        return json.dumps({"error": "path is required"})

    filepath = WORKSPACE_DIR / "repo" / path
    filepath.parent.mkdir(parents=True, exist_ok=True)
    filepath.write_text(content, encoding="utf-8")
    return json.dumps({"path": path, "bytes_written": len(content.encode("utf-8"))})


def _tool_git(input_data: dict) -> str:
    """Execute a git command in the workspace repo."""
    subcommand = input_data.get("subcommand", "")
    args = input_data.get("args", [])
    if not subcommand:
        return json.dumps({"error": "subcommand is required"})

    cmd = ["git", subcommand] + args
    cwd = str(WORKSPACE_DIR / "repo")

    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=60,
            cwd=cwd,
        )
        output = result.stdout
        if result.stderr:
            output += "\n" + result.stderr
        return json.dumps({"exit_code": result.returncode, "output": output.strip()})
    except subprocess.TimeoutExpired:
        return json.dumps({"error": "Git command timed out"})


def _tool_memory_store(input_data: dict) -> str:
    """Store a memory via the mem9 API."""
    content = input_data.get("content", "")
    tags = input_data.get("tags", [])
    if not content:
        return json.dumps({"error": "content is required"})

    payload = json.dumps({
        "content": content,
        "tags": tags,
        "source": "claude-code-agent",
    }).encode("utf-8")

    url = f"{AGENT_MEMORY_URL}/v1alpha2/mem9s/memories"
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {MEM9_API_KEY}",
        "X-Mnemo-Agent-Id": "claude-code-agent",
    }

    try:
        req = urllib.request.Request(url, data=payload, headers=headers, method="POST")
        resp = urllib.request.urlopen(req, timeout=10)
        body = resp.read().decode("utf-8")
        return body
    except Exception as exc:
        return json.dumps({"error": f"Memory store failed: {exc}"})


def _tool_memory_search(input_data: dict) -> str:
    """Search memories via the mem9 API."""
    query = input_data.get("query", "")
    limit = input_data.get("limit", 5)
    if not query:
        return json.dumps({"error": "query is required"})

    payload = json.dumps({
        "query": query,
        "limit": limit,
    }).encode("utf-8")

    url = f"{AGENT_MEMORY_URL}/v1alpha2/mem9s/memories/search"
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {MEM9_API_KEY}",
        "X-Mnemo-Agent-Id": "claude-code-agent",
    }

    try:
        req = urllib.request.Request(url, data=payload, headers=headers, method="POST")
        resp = urllib.request.urlopen(req, timeout=10)
        body = resp.read().decode("utf-8")
        return body
    except Exception as exc:
        return json.dumps({"error": f"Memory search failed: {exc}"})


# =============================================================================
# Anthropic API client (stdlib-only, no SDK dependency)
# =============================================================================


def call_anthropic(messages: list, system: str = "") -> dict:
    """Call the Anthropic Messages API with tools."""
    payload = {
        "model": ANTHROPIC_MODEL,
        "max_tokens": 16384,
        "messages": messages,
        "tools": _tools,
    }
    if system:
        payload["system"] = system

    data = json.dumps(payload).encode("utf-8")
    headers = {
        "Content-Type": "application/json",
        "x-api-key": ANTHROPIC_API_KEY,
        "anthropic-version": "2023-06-01",
    }

    req = urllib.request.Request(
        "https://api.anthropic.com/v1/messages",
        data=data,
        headers=headers,
        method="POST",
    )
    resp = urllib.request.urlopen(req, timeout=300)
    body = resp.read().decode("utf-8")
    return json.loads(body)


# =============================================================================
# Agentic loop — multi-turn tool use until the model stops
# =============================================================================


def run_agent(prompt: str) -> str:
    """Run the agentic loop: send prompt, execute tools, repeat until done.

    Returns the final text response from the model.
    """
    system = (
        f"You are an autonomous coding agent working on the repository "
        f"'{TARGET_REPO}'. Your workspace is at /workspace/repo. "
        f"You have tools for shell commands, file I/O, git operations, "
        f"and persistent memory. Execute the task described in the user "
        f"prompt. When done, provide a concise summary of what you did "
        f"and the result."
    )

    messages = [{"role": "user", "content": prompt}]

    for round_num in range(1, MAX_TOOL_ROUNDS + 1):
        logger.info("Agent round %d/%d", round_num, MAX_TOOL_ROUNDS)

        try:
            response = call_anthropic(messages, system=system)
        except Exception as exc:
            logger.error("Anthropic API error: %s", exc)
            return f"Error calling Anthropic API: {exc}"

        stop_reason = response.get("stop_reason", "end_turn")
        content_blocks = response.get("content", [])

        # Collect text and tool_use blocks
        text_parts = []
        tool_uses = []
        for block in content_blocks:
            if block.get("type") == "text":
                text_parts.append(block["text"])
            elif block.get("type") == "tool_use":
                tool_uses.append(block)

        # If no tool calls, we're done
        if stop_reason != "tool_use" or not tool_uses:
            return "\n".join(text_parts) if text_parts else "Agent completed with no output."

        # Append assistant message to conversation
        messages.append({"role": "assistant", "content": content_blocks})

        # Execute all tool calls and build tool_result message
        tool_results = []
        for tool_use in tool_uses:
            tool_name = tool_use["name"]
            tool_input = tool_use["input"]
            tool_id = tool_use["id"]

            logger.info("  Tool call: %s(%s)", tool_name, json.dumps(tool_input)[:200])
            result = execute_tool(tool_name, tool_input)
            logger.info("  Tool result: %s", result[:200])

            tool_results.append({
                "type": "tool_result",
                "tool_use_id": tool_id,
                "content": result,
            })

        messages.append({"role": "user", "content": tool_results})

    return "Agent reached maximum tool rounds without completing."


# =============================================================================
# HTTP server — OpenAI-compatible chat completions interface
# =============================================================================


class AgentHandler(BaseHTTPRequestHandler):
    """HTTP handler that mimics the OpenClaw gateway interface."""

    def log_message(self, fmt, *args):
        logger.info(fmt, *args)

    def do_GET(self):
        if self.path == "/healthz":
            self._respond(200, {"status": "ok", "runtime": "claude-code"})
        else:
            self._respond(404, {"error": "not found"})

    def do_POST(self):
        if self.path == "/v1/chat/completions":
            self._handle_chat_completions()
        else:
            self._respond(404, {"error": "not found"})

    def _handle_chat_completions(self):
        """Handle an OpenAI-compatible chat completions request.

        Extracts the user prompt, runs the agentic loop, and returns a
        response in the same format the orchestrator expects.
        """
        try:
            content_length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(content_length).decode("utf-8")
            request = json.loads(body)
        except (json.JSONDecodeError, ValueError) as exc:
            self._respond(400, {"error": f"Invalid request: {exc}"})
            return

        # Extract the user prompt from the OpenAI-format messages
        messages = request.get("messages", [])
        prompt = ""
        for msg in messages:
            if msg.get("role") == "user":
                prompt = msg.get("content", "")
                break

        if not prompt:
            self._respond(400, {"error": "No user message found in request"})
            return

        logger.info("Received prompt (%d chars), starting agent loop", len(prompt))

        try:
            result_text = run_agent(prompt)
        except Exception as exc:
            logger.error("Agent loop failed: %s", exc)
            result_text = f"Agent error: {exc}"

        # Return in OpenAI chat completion format (what the orchestrator parses)
        response = {
            "id": f"claude-code-{int(time.time())}",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": ANTHROPIC_MODEL,
            "choices": [
                {
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": result_text,
                    },
                    "finish_reason": "stop",
                }
            ],
            "usage": {
                "prompt_tokens": 0,
                "completion_tokens": 0,
                "total_tokens": 0,
            },
        }

        self._respond(200, response)

    def _respond(self, status: int, body: dict):
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(body).encode("utf-8"))


# =============================================================================
# Server startup
# =============================================================================


class ThreadedHTTPServer(HTTPServer):
    """HTTPServer that handles each request in a new thread."""
    daemon_threads = True

    def process_request(self, request, client_address):
        thread = threading.Thread(
            target=self.process_request_thread,
            args=(request, client_address),
        )
        thread.daemon = True
        thread.start()

    def process_request_thread(self, request, client_address):
        try:
            self.finish_request(request, client_address)
        except Exception:
            self.handle_error(request, client_address)
        finally:
            self.shutdown_request(request)


def main():
    if not ANTHROPIC_API_KEY:
        logger.error("ANTHROPIC_API_KEY is required")
        sys.exit(1)

    load_tools()

    # Ensure workspace exists
    (WORKSPACE_DIR / "repo").mkdir(parents=True, exist_ok=True)

    server = ThreadedHTTPServer(("0.0.0.0", LISTEN_PORT), AgentHandler)
    logger.info("Claude Code adapter listening on :%d", LISTEN_PORT)
    logger.info("  ANTHROPIC_MODEL = %s", ANTHROPIC_MODEL)
    logger.info("  WORKSPACE_DIR   = %s", WORKSPACE_DIR)
    logger.info("  TARGET_REPO     = %s", TARGET_REPO)
    logger.info("  MEMORY_URL      = %s", AGENT_MEMORY_URL)
    logger.info("  MAX_TOOL_ROUNDS = %d", MAX_TOOL_ROUNDS)

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        logger.info("Shutting down")
        server.shutdown()


if __name__ == "__main__":
    main()
