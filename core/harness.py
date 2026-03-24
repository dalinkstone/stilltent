"""
stilltent harness — Main entry point.

Reads stilltent.yml, assembles the configured stack (agent runtime,
memory backend, sandbox, orchestrator), and launches the autonomous
engineering loop.
"""

import os
import sys
import yaml
from pathlib import Path


CONFIG_FILE = os.environ.get("STILLTENT_CONFIG", "stilltent.yml")


def load_config(path: str = CONFIG_FILE) -> dict:
    """Load and return the stilltent.yml configuration."""
    config_path = Path(path)
    if not config_path.exists():
        print(f"ERROR: Config file not found: {config_path}", file=sys.stderr)
        sys.exit(1)
    with open(config_path) as f:
        return yaml.safe_load(f)


def assemble_stack(config: dict) -> dict:
    """Resolve runtime, memory, sandbox, and deploy from config."""
    return {
        "agent_runtime": config.get("agent", {}).get("runtime", "openclaw"),
        "memory_backend": config.get("memory", {}).get("backend", "mem9"),
        "sandbox_provider": config.get("sandbox", {}).get("provider", "daytona"),
        "deploy_target": config.get("deploy", {}).get("target", "digitalocean"),
        "target_repo": config.get("target", {}).get("repo", ""),
    }


def main():
    config = load_config()
    stack = assemble_stack(config)

    print(f"stilltent harness")
    print(f"  target repo:    {stack['target_repo']}")
    print(f"  agent runtime:  {stack['agent_runtime']}")
    print(f"  memory backend: {stack['memory_backend']}")
    print(f"  sandbox:        {stack['sandbox_provider']}")
    print(f"  deploy target:  {stack['deploy_target']}")

    # TODO: Wire up orchestrator loop, agent runtime, memory, and sandbox
    # This will be implemented in subsequent phases.
    print("\nHarness assembled. Orchestrator integration pending.")


if __name__ == "__main__":
    main()
