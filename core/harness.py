"""
stilltent harness — Main entry point.

Reads stilltent.yml, assembles the configured stack (agent runtime,
memory backend, sandbox, orchestrator), generates prompts from the
target repo's README.md, and launches the autonomous engineering loop.
"""

import os
import sys
import yaml
from pathlib import Path

from core.prompt_builder import (
    build_template_context,
    ensure_repo,
    parse_readme,
    render_templates,
    write_rendered,
)


CONFIG_FILE = os.environ.get("STILLTENT_CONFIG", "stilltent.yml")
REPO_ROOT = Path(__file__).resolve().parent.parent


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


def generate_prompts(config: dict, repo_dir: Path = None) -> dict[str, str]:
    """Generate all agent prompts from config and the target repo's README.

    Returns the rendered templates dict: {"SKILL.md": ..., "AGENTS.md": ..., "LEARNING.md": ...}
    """
    if repo_dir is None:
        repo_dir = REPO_ROOT / "workspace" / "repo"

    # Ensure repo is available
    ensure_repo(config, repo_dir)

    # Read and parse README
    readme_path = repo_dir / "README.md"
    readme_text = readme_path.read_text() if readme_path.exists() else ""
    readme_meta = parse_readme(readme_text)

    # Build context and render
    context = build_template_context(config, readme_meta, repo_dir)
    rendered = render_templates(context)

    # Write to output directories
    output_dirs = [repo_dir / ".stilltent"]
    workspace_dir = REPO_ROOT / "workspace"
    if workspace_dir.exists():
        output_dirs.append(workspace_dir)
    write_rendered(rendered, *output_dirs)

    return rendered


def main():
    config = load_config()
    stack = assemble_stack(config)

    print(f"stilltent harness")
    print(f"  target repo:    {stack['target_repo']}")
    print(f"  agent runtime:  {stack['agent_runtime']}")
    print(f"  memory backend: {stack['memory_backend']}")
    print(f"  sandbox:        {stack['sandbox_provider']}")
    print(f"  deploy target:  {stack['deploy_target']}")

    # Generate prompts from target README
    rendered = generate_prompts(config)
    for name, content in rendered.items():
        print(f"  {name}: {len(content)} chars")

    # TODO: Wire up orchestrator loop, agent runtime, memory, and sandbox
    # This will be implemented in subsequent phases.
    print("\nHarness assembled. Orchestrator integration pending.")


if __name__ == "__main__":
    main()
