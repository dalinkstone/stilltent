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


# =============================================================================
# Memory instruction templates for SKILL.md placeholder resolution
# =============================================================================

MEMORY_RECALL_INSTRUCTIONS = {
    "mem9": (
        "Search memory for relevant context. Your memory backend stores and "
        "retrieves knowledge from previous iterations.\n\n"
        "Query memory with keywords related to your planned work — look for "
        "prior decisions, known issues, and established patterns."
    ),
    "supermemory": (
        "Search memory for relevant context. Your memory backend (Supermemory) "
        "stores and retrieves knowledge from previous iterations.\n\n"
        "Query memory with keywords related to your planned work — look for "
        "prior decisions, known issues, and established patterns."
    ),
    "asmr": (
        "Search memory for relevant context. Your memory search deploys multiple "
        "parallel search agents, each with a different specialization:\n\n"
        "- **Direct Facts**: explicit statements, concrete decisions, test results\n"
        "- **Related Context**: related patterns, implications, indirect connections\n"
        "- **Temporal Reconstruction**: timelines, dependency chains, evolution of decisions\n\n"
        "This means your search results may include richer, multi-perspective context "
        "than a single query would return. Results include confidence scores — "
        "prioritize high-confidence findings but don't ignore low-confidence ones "
        "that multiple agents surfaced independently."
    ),
}

MEMORY_STORE_INSTRUCTIONS = {
    "mem9": (
        "Store insights from this iteration in memory. Include:\n"
        "- Key decisions made and why\n"
        "- Errors encountered and how they were resolved\n"
        "- Patterns discovered in the codebase\n"
        "- What was tried but didn't work"
    ),
    "supermemory": (
        "Store insights from this iteration in memory. Include:\n"
        "- Key decisions made and why\n"
        "- Errors encountered and how they were resolved\n"
        "- Patterns discovered in the codebase\n"
        "- What was tried but didn't work"
    ),
    "asmr": (
        "Store your iteration output in memory. Your memory system will "
        "automatically process it through multiple parallel observer agents that "
        "extract structured knowledge across six dimensions:\n\n"
        "1. **Architectural Decisions** — design patterns, tech choices, structural changes\n"
        "2. **Test Intelligence** — test coverage, failures, flaky tests\n"
        "3. **Code Patterns** — recurring patterns, anti-patterns, conventions\n"
        "4. **Temporal State** — what changed when, sequence dependencies\n"
        "5. **Error Patterns** — failure modes, root causes, fix strategies\n"
        "6. **Project Understanding** — spec vs reality, gaps, progress\n\n"
        "Write your iteration summary as a detailed narrative — the observers "
        "will extract and categorize the knowledge automatically. The more "
        "detail you provide, the richer the extracted knowledge."
    ),
}


def resolve_memory_instructions(config: dict) -> dict:
    """Resolve memory placeholder values based on configured backend.

    Returns a dict with keys matching the placeholder names in SKILL.md.tmpl:
        MEMORY_RECALL_INSTRUCTIONS
        MEMORY_STORE_INSTRUCTIONS
    """
    backend = config.get("memory", {}).get("backend", "mem9")
    return {
        "MEMORY_RECALL_INSTRUCTIONS": MEMORY_RECALL_INSTRUCTIONS.get(
            backend, MEMORY_RECALL_INSTRUCTIONS["mem9"]
        ),
        "MEMORY_STORE_INSTRUCTIONS": MEMORY_STORE_INSTRUCTIONS.get(
            backend, MEMORY_STORE_INSTRUCTIONS["mem9"]
        ),
    }


def render_skill_template(config: dict, template_path: str = None) -> str:
    """Render the SKILL.md.tmpl with memory placeholders resolved.

    This handles the {{MEMORY_*}} placeholders. Go-style {{.Var}} placeholders
    are left intact for the agent runtime to resolve at injection time.
    """
    if template_path is None:
        template_path = str(
            Path(__file__).parent.parent / "config" / "prompts" / "SKILL.md.tmpl"
        )

    with open(template_path) as f:
        template = f.read()

    instructions = resolve_memory_instructions(config)
    for key, value in instructions.items():
        template = template.replace("{{" + key + "}}", value)

    return template


def main():
    config = load_config()
    stack = assemble_stack(config)

    print(f"stilltent harness")
    print(f"  target repo:    {stack['target_repo']}")
    print(f"  agent runtime:  {stack['agent_runtime']}")
    print(f"  memory backend: {stack['memory_backend']}")
    print(f"  sandbox:        {stack['sandbox_provider']}")
    print(f"  deploy target:  {stack['deploy_target']}")

    # Render SKILL.md with memory instructions
    skill_md = render_skill_template(config)
    print(f"\n  SKILL.md rendered: {len(skill_md)} chars, backend={stack['memory_backend']}")

    # TODO: Wire up orchestrator loop, agent runtime, memory, and sandbox
    # This will be implemented in subsequent phases.
    print("\nHarness assembled. Orchestrator integration pending.")


if __name__ == "__main__":
    main()
