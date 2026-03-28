#!/usr/bin/env python3
"""
harness.py — Master orchestration script.

This is what `make bootstrap` calls. It takes the system from zero to
a running autonomous coding agent in a single invocation:

  1. Read and validate stilltent.yml
  2. Generate the Docker Compose file
  3. Build all containers
  4. Start the stack
  5. Wait for all health checks to pass
  6. Initialize the database (if using mem9)
  7. Clone the target repo into workspace/
  8. Run the prompt builder to generate SKILL.md, AGENTS.md, LEARNING.md
  9. If using Daytona: create a sandbox workspace
 10. Seed initial memory with the project description
 11. Run the first iteration
 12. Print a summary of what was set up and how to monitor

Usage:
    python core/harness.py
    python core/harness.py --config stilltent.yml
"""

import json
import os
import subprocess
import sys
import time
import urllib.request
import urllib.error
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT))

from core.validate import load_and_validate
from core.compose import load_config as load_compose_config, select_fragments, assemble, generate_header, AGENT_SERVICE_NAMES
from core.prompt_builder import (
    build_template_context,
    ensure_repo,
    parse_readme,
    render_templates,
    write_rendered,
)

CONFIG_FILE = os.environ.get("STILLTENT_CONFIG", str(REPO_ROOT / "stilltent.yml"))


def banner(text: str):
    print(f"\n{'='*50}")
    print(f"  {text}")
    print(f"{'='*50}")


def step(n: int, total: int, msg: str):
    print(f"\n[{n}/{total}] {msg}")


def run(cmd: list[str] | str, check: bool = True, capture: bool = False, **kwargs) -> subprocess.CompletedProcess:
    """Run a shell command with logging."""
    if isinstance(cmd, str):
        kwargs.setdefault("shell", True)
    result = subprocess.run(cmd, capture_output=capture, text=True, **kwargs)
    if check and result.returncode != 0:
        stderr = result.stderr if capture else ""
        print(f"  Command failed (exit {result.returncode}): {cmd}", file=sys.stderr)
        if stderr:
            print(f"  {stderr[:500]}", file=sys.stderr)
    return result


def wait_for_url(url: str, label: str, timeout: int = 180, interval: int = 2) -> bool:
    """Poll a URL until it returns 2xx or timeout."""
    print(f"  Waiting for {label}...", end="", flush=True)
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            req = urllib.request.Request(url, method="GET")
            resp = urllib.request.urlopen(req, timeout=5)
            if 200 <= resp.status < 300:
                print(f" ready ({resp.status})")
                return True
        except Exception:
            pass
        time.sleep(interval)
        print(".", end="", flush=True)
    print(f" TIMEOUT after {timeout}s")
    return False


def http_post_json(url: str, data: dict, headers: dict = None) -> dict | None:
    """POST JSON to a URL, return parsed response or None on error."""
    body = json.dumps(data).encode("utf-8")
    req = urllib.request.Request(url, data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        resp = urllib.request.urlopen(req, timeout=600)
        return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        print(f"  HTTP {e.code}: {e.read().decode('utf-8', errors='replace')[:300]}")
        return None
    except Exception as e:
        print(f"  Request failed: {e}")
        return None


# ─── Steps ───────────────────────────────────────────────────────────────────

def step_validate(config_path: str) -> dict:
    """Step 1: Read and validate stilltent.yml."""
    step(1, 12, "Validating stilltent.yml...")
    config, issues = load_and_validate(config_path)

    errors = [i for i in issues if i["level"] == "error"]
    warnings = [i for i in issues if i["level"] == "warning"]

    for issue in issues:
        prefix = "ERROR" if issue["level"] == "error" else "WARN"
        print(f"  {prefix}: {issue['message']}")

    if errors:
        print(f"\n  Validation failed: {len(errors)} error(s)")
        sys.exit(1)

    if warnings:
        print(f"  {len(warnings)} warning(s) — proceeding")

    runtime = config.get("agent", {}).get("runtime", "openclaw")
    memory = config.get("memory", {}).get("backend", "mem9")
    sandbox = config.get("sandbox", {}).get("provider", "none")
    target = config.get("target", {}).get("repo", "")

    print(f"  runtime={runtime}  memory={memory}  sandbox={sandbox}")
    if target:
        print(f"  target={target}")
    return config


def step_generate_compose(config: dict) -> str:
    """Step 2: Generate docker-compose.yml."""
    step(2, 12, "Generating docker-compose.yml...")

    config_path = CONFIG_FILE
    fragments = select_fragments(config)
    agent_runtime = config.get("agent", {}).get("runtime", "openclaw")

    print(f"  Merging {len(fragments)} fragments: {', '.join(fragments)}")
    composed = assemble(fragments, agent_runtime, config)

    header = generate_header(fragments, config_path)
    output_yaml = yaml.dump(composed, default_flow_style=False, sort_keys=False)
    result = header + "\n" + output_yaml

    output_path = REPO_ROOT / "docker-compose.yml"
    output_path.write_text(result)
    print(f"  Wrote {output_path}")
    return str(output_path)


def step_build(config: dict):
    """Step 3: Build all containers."""
    step(3, 12, "Building containers...")
    run("docker compose build --parallel", cwd=str(REPO_ROOT))


def step_start_stack(config: dict):
    """Step 4: Start the stack."""
    step(4, 12, "Starting stack...")
    run("docker compose up -d", cwd=str(REPO_ROOT))
    # Stop orchestrator — we'll run a manual first iteration
    run("docker compose stop orchestrator", cwd=str(REPO_ROOT), check=False)


def step_health_checks(config: dict) -> bool:
    """Step 5: Wait for all health checks to pass."""
    step(5, 12, "Waiting for services to be healthy...")

    memory = config.get("memory", {}).get("backend", "mem9")
    all_ok = True

    # TiDB
    if not wait_for_url("http://127.0.0.1:10080/status", "TiDB", timeout=180):
        all_ok = False

    # Memory backend services
    if memory in ("mem9", "asmr"):
        if not wait_for_url("http://127.0.0.1:8090/health", "embed-service", timeout=60):
            all_ok = False
        mem9_port = os.environ.get("MEM9_API_PORT", "8082")
        if not wait_for_url(f"http://127.0.0.1:{mem9_port}/health", "mnemo-server", timeout=90):
            all_ok = False

    # Agent runtime
    runtime = config.get("agent", {}).get("runtime", "openclaw")
    port_map = {"openclaw": "18789", "nanoclaw": "18790", "nemoclaw": "18791", "claude-code": "18792"}
    agent_port = os.environ.get("OPENCLAW_PORT", port_map.get(runtime, "18789"))
    if not wait_for_url(f"http://127.0.0.1:{agent_port}/healthz", f"{runtime} agent", timeout=120):
        # Non-fatal: some runtimes don't have /healthz
        print(f"  {runtime} health endpoint not responding (may be normal)")

    if not all_ok:
        print("  WARNING: Some services failed health checks. Check 'docker compose logs'.")

    return all_ok


def step_init_db(config: dict):
    """Step 6: Initialize the database."""
    step(6, 12, "Initializing database...")

    memory = config.get("memory", {}).get("backend", "mem9")
    if memory not in ("mem9", "asmr"):
        print(f"  Skipping — not using mem9/asmr (backend={memory})")
        return

    mysql_bin = os.environ.get("MYSQL_BIN", "mysql")
    # Try common paths
    for candidate in [mysql_bin, "mysql", "/opt/homebrew/opt/mysql-client@8.4/bin/mysql"]:
        result = subprocess.run([candidate, "--version"], capture_output=True, text=True)
        if result.returncode == 0:
            mysql_bin = candidate
            break
    else:
        print("  WARNING: mysql client not found — skipping DB init")
        print("  Install with: brew install mysql-client@8.4")
        return

    # Check if DB already exists
    check = subprocess.run(
        [mysql_bin, "-h", "127.0.0.1", "-P", "4000", "-u", "root", "-e", "USE mnemos"],
        capture_output=True, text=True,
    )
    if check.returncode == 0:
        print("  Database 'mnemos' already exists — skipping init")
        return

    init_sql = REPO_ROOT / "scripts" / "init-tidb.sql"
    if not init_sql.exists():
        print(f"  WARNING: {init_sql} not found — skipping DB init")
        return

    result = subprocess.run(
        f"{mysql_bin} -h 127.0.0.1 -P 4000 -u root < {init_sql}",
        shell=True, capture_output=True, text=True,
    )
    if result.returncode == 0:
        print("  Database initialized")
    else:
        print(f"  WARNING: DB init failed: {result.stderr[:200]}")


def step_clone_repo(config: dict) -> Path:
    """Step 7: Clone the target repo into workspace/."""
    step(7, 12, "Cloning target repository...")

    repo_dir = REPO_ROOT / "workspace" / "repo"
    target = config.get("target", {}).get("repo", "")

    if not target:
        if repo_dir.exists() and (repo_dir / ".git").exists():
            print(f"  No target.repo set, using existing repo at {repo_dir}")
        else:
            print("  No target.repo configured — workspace/repo will be empty")
            repo_dir.mkdir(parents=True, exist_ok=True)
        return repo_dir

    ensure_repo(config, repo_dir)

    if (repo_dir / ".git").exists():
        result = subprocess.run(
            ["git", "-C", str(repo_dir), "rev-parse", "--short", "HEAD"],
            capture_output=True, text=True,
        )
        sha = result.stdout.strip() if result.returncode == 0 else "unknown"
        print(f"  Repository ready at {repo_dir} ({sha})")
    else:
        print(f"  WARNING: Clone may have failed — no .git in {repo_dir}")

    return repo_dir


def step_generate_prompts(config: dict, repo_dir: Path) -> dict:
    """Step 8: Generate SKILL.md, AGENTS.md, LEARNING.md."""
    step(8, 12, "Generating agent prompts...")

    readme_path = repo_dir / "README.md"
    readme_text = readme_path.read_text() if readme_path.exists() else ""

    if not readme_text:
        print("  WARNING: No README.md found — using defaults")

    readme_meta = parse_readme(readme_text)
    context = build_template_context(config, readme_meta, repo_dir)
    rendered = render_templates(context)

    output_dirs = []
    stilltent_dir = repo_dir / ".stilltent"
    output_dirs.append(stilltent_dir)
    workspace_dir = REPO_ROOT / "workspace"
    if workspace_dir.exists():
        output_dirs.append(workspace_dir)

    write_rendered(rendered, *output_dirs)

    for name, content in rendered.items():
        print(f"  {name}: {len(content)} chars")

    return rendered


def step_setup_sandbox(config: dict):
    """Step 9: If using Daytona, create a sandbox workspace."""
    step(9, 12, "Setting up sandbox...")

    provider = config.get("sandbox", {}).get("provider", "none")
    if provider != "daytona":
        print(f"  Skipping — sandbox.provider={provider}")
        return

    try:
        from sandbox.daytona.client import DaytonaClient
    except ImportError:
        print("  WARNING: Daytona client not available")
        return

    target = config.get("target", {}).get("repo", "")
    branch = config.get("target", {}).get("branch", "main")

    if not target:
        print("  Skipping — no target.repo configured")
        return

    api_key = (
        config.get("sandbox", {}).get("daytona_api_key")
        or os.environ.get("DAYTONA_API_KEY", "")
    )
    base_url = (
        config.get("sandbox", {}).get("daytona_base_url")
        or os.environ.get("DAYTONA_BASE_URL", "https://app.daytona.io")
    )

    client = DaytonaClient(api_key=api_key, base_url=base_url)
    result = client.create_workspace(target, branch)
    print(f"  Daytona workspace: {result.get('workspace_id', 'unknown')} ({result.get('status', 'unknown')})")


def step_seed_memory(config: dict):
    """Step 10: Seed initial memory with the project description."""
    step(10, 12, "Seeding initial memory...")

    memory = config.get("memory", {}).get("backend", "mem9")
    target = config.get("target", {}).get("repo", "") or "local project"

    seed_text = (
        f"stilltent initialized. Target repository: {target}. "
        f"This is the first iteration. No prior history exists. "
        f"Start by reading the repository README and following SKILL.md Phase 2 (Assess)."
    )

    if memory in ("mem9", "asmr"):
        port = os.environ.get("MEM9_API_PORT", "8082")
        api_key = os.environ.get("MEM9_API_KEY", "stilltent-local-dev-key")
        url = f"http://localhost:{port}/v1alpha2/mem9s/memories"

        result = http_post_json(
            url,
            data={
                "content": seed_text,
                "tags": ["bootstrap"],
                "metadata": {"source": "bootstrap", "target_repo": target},
            },
            headers={
                "X-API-Key": api_key,
                "X-Mnemo-Agent-Id": "stilltent-agent",
            },
        )
        if result is not None:
            print("  Seed memory created")
        else:
            print("  WARNING: Could not seed memory — continuing anyway")

    elif memory == "supermemory":
        print("  Supermemory seeding: not yet implemented — seed manually")
    else:
        print(f"  Skipping — memory backend '{memory}'")


def step_first_iteration(config: dict):
    """Step 11: Run the first iteration."""
    step(11, 12, "Running first iteration...")

    runtime = config.get("agent", {}).get("runtime", "openclaw")
    port_map = {"openclaw": "18789", "nanoclaw": "18790", "nemoclaw": "18791", "claude-code": "18792"}
    agent_port = os.environ.get("OPENCLAW_PORT", port_map.get(runtime, "18789"))
    token = os.environ.get("OPENCLAW_GATEWAY_TOKEN", "")
    timeout = int(os.environ.get("ITERATION_TIMEOUT", "600"))

    prompt = (
        "Read and follow /workspace/SKILL.md. This is iteration 1 (bootstrap). "
        "Execute the complete iteration protocol (Phase 1 through Phase 7). "
        'When finished, respond with a JSON summary: '
        '{"iteration": 1, "action_type": "bootstrap", "summary": "<description>", '
        '"result": "<success|failure>", "pr_number": null, "merged": null, '
        '"confidence": 0.0, "error": null}'
    )

    url = f"http://localhost:{agent_port}/v1/chat/completions"
    headers = {"Authorization": f"Bearer {token}"} if token else {}

    agent_svc = AGENT_SERVICE_NAMES.get(runtime, "openclaw-gateway")
    data = {
        "model": f"{agent_svc}:main" if runtime != "claude-code" else "claude-code",
        "messages": [{"role": "user", "content": prompt}],
    }

    print(f"  Sending bootstrap prompt to {runtime} at {url}...")
    print(f"  Timeout: {timeout}s")

    result = http_post_json(url, data=data, headers=headers)
    if result:
        # Try to extract the response
        try:
            content = result.get("choices", [{}])[0].get("message", {}).get("content", "")
            if content:
                print(f"\n  Agent response ({len(content)} chars):")
                # Print first 500 chars
                for line in content[:500].split("\n"):
                    print(f"    {line}")
                if len(content) > 500:
                    print(f"    ... ({len(content) - 500} more chars)")
        except (KeyError, IndexError):
            print(f"  Raw response: {json.dumps(result)[:500]}")
    else:
        print("  WARNING: First iteration did not return a response")
        print("  This is OK — the agent may need manual triggering")

    # Initialize metrics
    metrics_path = REPO_ROOT / "workspace" / "metrics.json"
    if not metrics_path.exists() or metrics_path.stat().st_size < 3:
        metrics_path.write_text(json.dumps({
            "total_iterations": 1 if result else 0,
            "successful_iterations": 1 if result else 0,
            "failed_iterations": 0,
            "total_spend_usd": 0.0,
        }, indent=2))


def step_summary(config: dict):
    """Step 12: Print setup summary."""
    step(12, 12, "Bootstrap complete!")

    runtime = config.get("agent", {}).get("runtime", "openclaw")
    memory = config.get("memory", {}).get("backend", "mem9")
    sandbox = config.get("sandbox", {}).get("provider", "none")
    target = config.get("target", {}).get("repo", "") or "local"
    budget = config.get("orchestrator", {}).get("budget_limit", 50)
    hours = config.get("orchestrator", {}).get("total_runtime_hours", 120)

    banner("stilltent is ready")
    print(f"""
  Target:     {target}
  Runtime:    {runtime}
  Memory:     {memory}
  Sandbox:    {sandbox}
  Budget:     ${budget} over {hours} hours

  What happens next:
    1. Review the first iteration output above
    2. Check for new branches/PRs: gh pr list
    3. If satisfied, start autonomous mode:

       make start

  Monitor with:
    make logs       # follow all logs
    make stats      # iteration count, success rate
    make cost       # spend vs budget
    make health     # service health
    make pause      # emergency stop

  The agent will iterate continuously, building your project
  from the README.md — writing code, running tests, opening PRs,
  and learning from each cycle.
""")


# ─── Main ────────────────────────────────────────────────────────────────────

def main():
    import argparse

    parser = argparse.ArgumentParser(description="stilltent bootstrap harness")
    parser.add_argument("--config", default=CONFIG_FILE, help="Path to stilltent.yml")
    parser.add_argument("--skip-build", action="store_true", help="Skip container build")
    parser.add_argument("--skip-iteration", action="store_true", help="Skip first iteration")
    args = parser.parse_args()

    banner("stilltent bootstrap")

    config = step_validate(args.config)
    step_generate_compose(config)

    if not args.skip_build:
        step_build(config)
    else:
        step(3, 12, "Skipping build (--skip-build)")

    step_start_stack(config)
    step_health_checks(config)
    step_init_db(config)
    repo_dir = step_clone_repo(config)
    step_generate_prompts(config, repo_dir)
    step_setup_sandbox(config)
    step_seed_memory(config)

    if not args.skip_iteration:
        step_first_iteration(config)
    else:
        step(11, 12, "Skipping first iteration (--skip-iteration)")

    step_summary(config)


if __name__ == "__main__":
    main()
