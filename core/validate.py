#!/usr/bin/env python3
"""
validate.py — Validate stilltent.yml against expected schema.

Checks required fields, valid enum values, and cross-validates
configuration combinations (e.g., Daytona requires API key).

Usage:
    python core/validate.py
    python core/validate.py --config stilltent.yml
"""

import os
import sys
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parent.parent

VALID_RUNTIMES = {"openclaw", "nanoclaw", "nemoclaw", "claude-code"}
VALID_MEMORY_BACKENDS = {"mem9", "supermemory", "asmr"}
VALID_SANDBOX_PROVIDERS = {"daytona", "local", "none"}
VALID_DEPLOY_TARGETS = {"digitalocean", "vultr", "railway", "render", "heroku", "local"}

# Required top-level sections
REQUIRED_SECTIONS = ["target", "agent", "memory", "sandbox", "orchestrator", "deploy"]


def validate(config: dict) -> list[dict]:
    """Validate stilltent.yml config.

    Returns a list of issues, each a dict with:
        level: "error" | "warning"
        message: str
    """
    issues = []

    def error(msg: str):
        issues.append({"level": "error", "message": msg})

    def warn(msg: str):
        issues.append({"level": "warning", "message": msg})

    if not isinstance(config, dict):
        error("Config file is empty or not a valid YAML mapping")
        return issues

    # Check required sections
    for section in REQUIRED_SECTIONS:
        if section not in config:
            error(f"Missing required section: '{section}'")

    # --- target ---
    target = config.get("target", {}) or {}
    if not target.get("repo"):
        warn("target.repo is empty — set it to owner/repo before running bootstrap")

    # --- agent ---
    agent = config.get("agent", {}) or {}
    runtime = agent.get("runtime", "")
    if not runtime:
        error("agent.runtime is required")
    elif runtime not in VALID_RUNTIMES:
        error(
            f"agent.runtime '{runtime}' is not valid. "
            f"Must be one of: {', '.join(sorted(VALID_RUNTIMES))}"
        )

    if not agent.get("model"):
        warn("agent.model is empty — the LLM model to use")

    # --- memory ---
    memory = config.get("memory", {}) or {}
    backend = memory.get("backend", "")
    if not backend:
        error("memory.backend is required")
    elif backend not in VALID_MEMORY_BACKENDS:
        error(
            f"memory.backend '{backend}' is not valid. "
            f"Must be one of: {', '.join(sorted(VALID_MEMORY_BACKENDS))}"
        )

    # --- sandbox ---
    sandbox = config.get("sandbox", {}) or {}
    provider = sandbox.get("provider", "")
    if not provider:
        error("sandbox.provider is required")
    elif provider not in VALID_SANDBOX_PROVIDERS:
        error(
            f"sandbox.provider '{provider}' is not valid. "
            f"Must be one of: {', '.join(sorted(VALID_SANDBOX_PROVIDERS))}"
        )

    # --- deploy ---
    deploy = config.get("deploy", {}) or {}
    deploy_target = deploy.get("target", "")
    if not deploy_target:
        error("deploy.target is required")
    elif deploy_target not in VALID_DEPLOY_TARGETS:
        error(
            f"deploy.target '{deploy_target}' is not valid. "
            f"Must be one of: {', '.join(sorted(VALID_DEPLOY_TARGETS))}"
        )

    # --- orchestrator ---
    orch = config.get("orchestrator", {}) or {}
    if orch.get("budget_limit") is not None:
        try:
            budget = float(orch["budget_limit"])
            if budget <= 0:
                error("orchestrator.budget_limit must be positive")
        except (ValueError, TypeError):
            error("orchestrator.budget_limit must be a number")

    if orch.get("loop_interval") is not None:
        try:
            interval = int(orch["loop_interval"])
            if interval < 10:
                warn("orchestrator.loop_interval < 10s may cause rate limiting")
        except (ValueError, TypeError):
            error("orchestrator.loop_interval must be an integer")

    # --- Cross-validation ---

    # NemoClaw hardware warning
    if runtime == "nemoclaw":
        warn(
            "agent.runtime 'nemoclaw' requires NVIDIA GPU hardware. "
            "Ensure your deployment target has GPU support."
        )

    # Daytona requires API key
    if provider == "daytona":
        daytona_key = (
            sandbox.get("daytona_api_key")
            or os.environ.get("DAYTONA_API_KEY", "")
        )
        if not daytona_key:
            error(
                "sandbox.provider is 'daytona' but DAYTONA_API_KEY is not set. "
                "Set it in .env or sandbox.daytona_api_key in stilltent.yml."
            )

    # Supermemory requires API key
    if backend == "supermemory":
        sm_key = (
            memory.get("supermemory_api_key")
            or os.environ.get("SUPERMEMORY_API_KEY", "")
        )
        if not sm_key:
            error(
                "memory.backend is 'supermemory' but SUPERMEMORY_API_KEY is not set. "
                "Set it in .env or memory.supermemory_api_key in stilltent.yml."
            )

    # Claude Code cross-validation
    claude_cfg = config.get("claude_code", {}) or {}
    if claude_cfg.get("enabled") or runtime == "claude-code":
        anthropic_key = (
            claude_cfg.get("api_key")
            or os.environ.get("ANTHROPIC_API_KEY", "")
        )
        if not anthropic_key:
            error(
                "Claude Code is enabled but ANTHROPIC_API_KEY is not set. "
                "Set it in .env or claude_code.api_key in stilltent.yml."
            )

    return issues


def load_and_validate(config_path: str = None) -> tuple[dict, list[dict]]:
    """Load stilltent.yml and validate it. Returns (config, issues)."""
    if config_path is None:
        config_path = str(REPO_ROOT / "stilltent.yml")

    path = Path(config_path)
    if not path.exists():
        return {}, [{"level": "error", "message": f"Config file not found: {path}"}]

    with open(path) as f:
        config = yaml.safe_load(f) or {}

    issues = validate(config)
    return config, issues


def main():
    import argparse

    parser = argparse.ArgumentParser(description="Validate stilltent.yml")
    parser.add_argument(
        "--config",
        default=str(REPO_ROOT / "stilltent.yml"),
        help="Path to stilltent.yml",
    )
    parser.add_argument(
        "--strict",
        action="store_true",
        help="Treat warnings as errors",
    )
    args = parser.parse_args()

    config, issues = load_and_validate(args.config)

    errors = [i for i in issues if i["level"] == "error"]
    warnings = [i for i in issues if i["level"] == "warning"]

    if not issues:
        print("stilltent.yml: valid")
        return

    for issue in issues:
        prefix = "ERROR" if issue["level"] == "error" else "WARN"
        print(f"  {prefix}: {issue['message']}")

    if errors:
        print(f"\nValidation failed: {len(errors)} error(s), {len(warnings)} warning(s)")
        sys.exit(1)
    elif args.strict and warnings:
        print(f"\nValidation failed (strict mode): {len(warnings)} warning(s)")
        sys.exit(1)
    else:
        print(f"\nstilltent.yml: valid ({len(warnings)} warning(s))")


if __name__ == "__main__":
    main()
