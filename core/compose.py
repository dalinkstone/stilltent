#!/usr/bin/env python3
"""
compose.py — Assemble docker-compose.yml from composable fragments.

Reads stilltent.yml to determine which memory backend and agent runtime
are configured, then merges the appropriate deploy/docker-compose/*.yml
fragments into a single docker-compose.yml at the repo root.

Usage:
    python core/compose.py
    python core/compose.py --config stilltent.yml --output docker-compose.yml
"""

import argparse
import copy
import sys
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parent.parent
FRAGMENTS_DIR = REPO_ROOT / "deploy" / "docker-compose"

# Fragment selection rules
MEMORY_FRAGMENTS = {
    "mem9": "memory-mem9.yml",
    "supermemory": "memory-supermemory.yml",
    "asmr": "memory-mem9.yml",  # ASMR uses mem9 as its storage layer
}

AGENT_FRAGMENTS = {
    "openclaw": "agent-openclaw.yml",
    "nanoclaw": "agent-nanoclaw.yml",
    "nemoclaw": "agent-nemoclaw.yml",
    "claude-code": "agent-claude-code.yml",
}

# Maps agent runtime to the service name used for depends_on and AGENT_URL
AGENT_SERVICE_NAMES = {
    "openclaw": "openclaw-gateway",
    "nanoclaw": "nanoclaw",
    "nemoclaw": "nemoclaw",
    "claude-code": "claude-code-agent",
}


def load_config(path: Path) -> dict:
    """Load stilltent.yml configuration."""
    if not path.exists():
        print(f"ERROR: Config file not found: {path}", file=sys.stderr)
        sys.exit(1)
    with open(path) as f:
        return yaml.safe_load(f)


def load_fragment(name: str) -> dict:
    """Load a compose fragment YAML file."""
    path = FRAGMENTS_DIR / name
    if not path.exists():
        print(f"ERROR: Fragment not found: {path}", file=sys.stderr)
        sys.exit(1)
    with open(path) as f:
        return yaml.safe_load(f)


def deep_merge(base: dict, override: dict) -> dict:
    """
    Deep-merge two dicts using Docker Compose merge semantics:
    - services: merge by service name (override wins for scalar keys)
    - networks: merge by network name
    - volumes: merge by volume name
    - Other top-level keys: override replaces base
    """
    result = copy.deepcopy(base)
    for key, value in override.items():
        if key in result and isinstance(result[key], dict) and isinstance(value, dict):
            # Recursively merge dicts (services, networks, volumes)
            result[key] = deep_merge(result[key], value)
        else:
            result[key] = copy.deepcopy(value)
    return result


def select_fragments(config: dict) -> list[str]:
    """Determine which fragments to include based on stilltent.yml."""
    fragments = ["base.yml"]

    # Memory backend
    memory_backend = config.get("memory", {}).get("backend", "mem9")
    memory_frag = MEMORY_FRAGMENTS.get(memory_backend)
    if memory_frag is None:
        print(
            f"ERROR: Unknown memory backend: {memory_backend!r}. "
            f"Valid options: {', '.join(MEMORY_FRAGMENTS)}",
            file=sys.stderr,
        )
        sys.exit(1)
    fragments.append(memory_frag)

    # Agent runtime
    agent_runtime = config.get("agent", {}).get("runtime", "openclaw")
    agent_frag = AGENT_FRAGMENTS.get(agent_runtime)
    if agent_frag is None:
        print(
            f"ERROR: Unknown agent runtime: {agent_runtime!r}. "
            f"Valid options: {', '.join(AGENT_FRAGMENTS)}",
            file=sys.stderr,
        )
        sys.exit(1)
    fragments.append(agent_frag)

    # Orchestrator is always included
    fragments.append("orchestrator.yml")

    # Claude Code oversight: when claude_code.enabled is true and the primary
    # runtime is NOT claude-code, include the oversight sidecar
    claude_cfg = config.get("claude_code", {})
    if claude_cfg.get("enabled") and agent_runtime != "claude-code":
        fragments.append("oversight-claude-code.yml")

    return fragments


def assemble(fragments: list[str], agent_runtime: str, config: dict | None = None) -> dict:
    """Load and merge all selected fragments into a single compose dict."""
    result = {}
    for name in fragments:
        fragment = load_fragment(name)
        result = deep_merge(result, fragment)

    # Wire orchestrator depends_on and AGENT_URL to the selected agent service.
    # Also set OPENCLAW_URL which is what loop.py actually reads for the
    # gateway endpoint.
    agent_svc = AGENT_SERVICE_NAMES[agent_runtime]
    orch = result.get("services", {}).get("orchestrator")
    if orch is not None:
        orch["depends_on"] = {agent_svc: {"condition": "service_healthy"}}
        agent_url = f"${{AGENT_URL:-http://{agent_svc}:18789}}"
        orch["environment"]["AGENT_URL"] = agent_url
        orch["environment"]["OPENCLAW_URL"] = (
            f"${{OPENCLAW_URL:-http://{agent_svc}:18789}}"
        )

    # Wire oversight sidecar: set the review interval from config
    config = config or {}
    claude_cfg = config.get("claude_code", {})
    oversight = result.get("services", {}).get("claude-code-oversight")
    if oversight is not None:
        review_interval = claude_cfg.get("oversight_interval", 5)
        oversight["environment"]["REVIEW_EVERY_N"] = str(review_interval)
        # Oversight depends on both the primary agent and memory
        oversight["depends_on"] = {
            agent_svc: {"condition": "service_healthy"},
            "mnemo-server": {"condition": "service_healthy"},
        }

    return result


def generate_header(fragments: list[str], config_path: str) -> str:
    """Generate a header comment for the output file."""
    frag_list = "\n".join(f"#   - {f}" for f in fragments)
    return (
        f"# =============================================================================\n"
        f"# docker-compose.yml — AUTO-GENERATED by core/compose.py\n"
        f"#\n"
        f"# DO NOT EDIT THIS FILE DIRECTLY. Instead, edit the fragments in\n"
        f"# deploy/docker-compose/ and re-run: python core/compose.py\n"
        f"#\n"
        f"# Source config: {config_path}\n"
        f"# Fragments merged:\n"
        f"{frag_list}\n"
        f"# =============================================================================\n"
    )


def main():
    parser = argparse.ArgumentParser(
        description="Assemble docker-compose.yml from composable fragments"
    )
    parser.add_argument(
        "--config",
        default=str(REPO_ROOT / "stilltent.yml"),
        help="Path to stilltent.yml (default: stilltent.yml)",
    )
    parser.add_argument(
        "--output",
        default=str(REPO_ROOT / "docker-compose.yml"),
        help="Output path (default: docker-compose.yml)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print to stdout instead of writing file",
    )
    args = parser.parse_args()

    config = load_config(Path(args.config))
    fragments = select_fragments(config)

    memory_backend = config.get("memory", {}).get("backend", "mem9")
    agent_runtime = config.get("agent", {}).get("runtime", "openclaw")
    print(f"compose: memory={memory_backend}, agent={agent_runtime}")
    print(f"compose: merging {len(fragments)} fragments: {', '.join(fragments)}")

    composed = assemble(fragments, agent_runtime, config)

    header = generate_header(fragments, args.config)
    output_yaml = yaml.dump(composed, default_flow_style=False, sort_keys=False)
    result = header + "\n" + output_yaml

    if args.dry_run:
        print(result)
    else:
        output_path = Path(args.output)
        output_path.write_text(result)
        print(f"compose: wrote {output_path}")


if __name__ == "__main__":
    main()
