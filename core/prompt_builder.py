#!/usr/bin/env python3
"""
prompt_builder.py — Generate agent prompts from a target repo's README.md.

Reads the target repo's README.md and stilltent.yml, extracts project metadata,
detects tech stack, and renders SKILL.md, AGENTS.md, and LEARNING.md using
Jinja2 templates.

Usage:
    python core/prompt_builder.py
    python core/prompt_builder.py --config stilltent.yml --repo workspace/repo
    python core/prompt_builder.py --readme path/to/README.md
"""

import argparse
import os
import re
import subprocess
import sys
from pathlib import Path

import yaml

try:
    from jinja2 import Environment, FileSystemLoader
except ImportError:
    print("ERROR: jinja2 is required. Install with: pip install jinja2", file=sys.stderr)
    sys.exit(1)


REPO_ROOT = Path(__file__).resolve().parent.parent
TEMPLATES_DIR = REPO_ROOT / "config" / "prompts"
DEFAULT_CONFIG = REPO_ROOT / "stilltent.yml"
DEFAULT_REPO_DIR = REPO_ROOT / "workspace" / "repo"


# ─── README parsing ──────────────────────────────────────────────────────────

def parse_readme(readme_text: str) -> dict:
    """Parse a README.md into structured project metadata.

    Extracts:
        title: str — from the first # heading
        description: str — first paragraph after the title
        goals: list[str] — from ## Goals or ## Features section
        nongoals: list[str] — from ## Non-Goals section
        tech_stack_mentions: list[str] — from ## Tech Stack section
        architecture: str — from ## Architecture section
        raw: str — the full README text (fallback)
    """
    result = {
        "title": "",
        "description": "",
        "goals": [],
        "nongoals": [],
        "tech_stack_mentions": [],
        "architecture": "",
        "raw": readme_text,
    }

    if not readme_text.strip():
        return result

    lines = readme_text.split("\n")

    # Extract title from first H1
    for line in lines:
        stripped = line.strip()
        if stripped.startswith("# ") and not stripped.startswith("##"):
            result["title"] = stripped[2:].strip()
            break

    # Parse sections by H2 headers
    sections = _split_sections(readme_text)

    # Description: first paragraph after title, before any H2
    preamble = sections.get("_preamble", "")
    result["description"] = _extract_first_paragraph(preamble)

    # Goals: from ## Goals or ## Features
    for key in ("goals", "features"):
        if key in sections:
            result["goals"] = _extract_list_items(sections[key])
            break

    # Non-Goals
    for key in ("non-goals", "nongoals", "non goals"):
        if key in sections:
            result["nongoals"] = _extract_list_items(sections[key])
            break

    # Tech Stack
    for key in ("tech stack", "technology", "technologies", "stack"):
        if key in sections:
            result["tech_stack_mentions"] = _extract_list_items(sections[key])
            break

    # Architecture
    for key in ("architecture", "design"):
        if key in sections:
            result["architecture"] = sections[key].strip()
            break

    return result


def _split_sections(text: str) -> dict:
    """Split markdown into sections keyed by lowercase H2 header text.

    The content before the first H2 is stored under '_preamble'.
    """
    sections = {}
    current_key = "_preamble"
    current_lines = []

    for line in text.split("\n"):
        match = re.match(r"^##\s+(.+)$", line.strip())
        if match:
            sections[current_key] = "\n".join(current_lines)
            current_key = match.group(1).strip().lower()
            current_lines = []
        else:
            current_lines.append(line)

    sections[current_key] = "\n".join(current_lines)
    return sections


def _extract_first_paragraph(text: str) -> str:
    """Extract the first non-empty paragraph from text, skipping the H1 line."""
    paragraphs = []
    current = []

    for line in text.split("\n"):
        stripped = line.strip()
        # Skip H1 title line
        if stripped.startswith("# ") and not stripped.startswith("##"):
            continue
        if stripped == "":
            if current:
                paragraphs.append(" ".join(current))
                current = []
        else:
            current.append(stripped)

    if current:
        paragraphs.append(" ".join(current))

    return paragraphs[0] if paragraphs else ""


def _extract_list_items(text: str) -> list[str]:
    """Extract bullet-point or numbered list items from a markdown section."""
    items = []
    for line in text.split("\n"):
        stripped = line.strip()
        # Match: - item, * item, + item, 1. item, 1) item
        match = re.match(r"^[-*+]\s+(.+)$", stripped) or re.match(
            r"^\d+[.)]\s+(.+)$", stripped
        )
        if match:
            items.append(match.group(1).strip())
    return items


# ─── Tech stack detection ────────────────────────────────────────────────────

# Map of marker files to tech stack entries
TECH_STACK_MARKERS = {
    "package.json": "Node.js / JavaScript",
    "tsconfig.json": "TypeScript",
    "go.mod": "Go",
    "Cargo.toml": "Rust",
    "requirements.txt": "Python",
    "pyproject.toml": "Python",
    "setup.py": "Python",
    "Pipfile": "Python",
    "pom.xml": "Java (Maven)",
    "build.gradle": "Java (Gradle)",
    "build.gradle.kts": "Kotlin (Gradle)",
    "Gemfile": "Ruby",
    "mix.exs": "Elixir",
    "Makefile": "Make",
    "CMakeLists.txt": "C/C++ (CMake)",
    "docker-compose.yml": "Docker Compose",
    "Dockerfile": "Docker",
    ".flutter": "Flutter / Dart",
    "pubspec.yaml": "Dart",
    "composer.json": "PHP (Composer)",
    "Package.swift": "Swift",
    "project.clj": "Clojure",
    "deno.json": "Deno",
    "bun.lockb": "Bun",
}

# Map of marker files to test commands
TEST_COMMAND_MARKERS = {
    "package.json": "npm test",
    "go.mod": "go test ./...",
    "Cargo.toml": "cargo test",
    "requirements.txt": "pytest",
    "pyproject.toml": "pytest",
    "setup.py": "pytest",
    "pom.xml": "mvn test",
    "build.gradle": "gradle test",
    "build.gradle.kts": "gradle test",
    "Gemfile": "bundle exec rspec",
    "mix.exs": "mix test",
    "CMakeLists.txt": "ctest",
    "composer.json": "composer test",
    "Package.swift": "swift test",
    "deno.json": "deno test",
}


def detect_tech_stack(repo_dir: Path) -> list[str]:
    """Detect tech stack from marker files in the repo directory."""
    if not repo_dir.exists():
        return []

    detected = []
    for marker, tech in TECH_STACK_MARKERS.items():
        if (repo_dir / marker).exists():
            detected.append(tech)

    return detected


def detect_test_command(repo_dir: Path) -> str:
    """Auto-detect the test command based on repo marker files.

    Returns the first matching test command, or empty string if none detected.
    """
    if not repo_dir.exists():
        return ""

    for marker, cmd in TEST_COMMAND_MARKERS.items():
        if (repo_dir / marker).exists():
            # Refine npm test: check if package.json has a test script
            if marker == "package.json":
                try:
                    import json

                    pkg = json.loads((repo_dir / marker).read_text())
                    scripts = pkg.get("scripts", {})
                    if "test" in scripts:
                        return "npm test"
                    elif "test:unit" in scripts:
                        return "npm run test:unit"
                    # No test script defined — skip
                    continue
                except (json.JSONDecodeError, OSError):
                    continue
            return cmd

    return ""


# ─── Memory, sandbox, and tool instructions ──────────────────────────────────

MEMORY_BACKEND_INSTRUCTIONS = {
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

SANDBOX_VALIDATION_INSTRUCTIONS = {
    "daytona": "Run all tests inside the Daytona sandbox. The sandbox provides an isolated environment with the project's dependencies pre-installed.",
    "local": "Run all tests locally in the workspace directory.",
    "none": "Run all tests locally. No sandbox is configured.",
}

TOOL_INSTRUCTIONS_BY_RUNTIME = {
    "openclaw": (
        "You are running inside the **OpenClaw** agent runtime.\n\n"
        "Available tools:\n"
        "- `shell` — Execute shell commands in the workspace\n"
        "- `git` — Git operations (commit, branch, push, PR)\n"
        "- `browser` — Web browsing for documentation lookup\n"
        "- `memory` — Query and store knowledge via the mem9 plugin\n\n"
        "Use the shell tool for file operations, running tests, and build commands.\n"
        "Use the git tool for all version control operations.\n"
        "Use the memory tool at the start and end of every iteration."
    ),
    "nanoclaw": (
        "You are running inside the **NanoClaw** lightweight agent runtime.\n\n"
        "Available tools:\n"
        "- `shell` — Execute shell commands in the workspace\n"
        "- `git` — Git operations\n"
        "- `memory` — Query and store knowledge\n\n"
        "NanoClaw is a minimal runtime. Use shell commands for all file operations, "
        "builds, and tests. Keep tool calls focused and efficient."
    ),
    "nemoclaw": (
        "You are running inside the **NemoClaw** GPU-accelerated agent runtime.\n\n"
        "Available tools:\n"
        "- `shell` — Execute shell commands in the workspace\n"
        "- `git` — Git operations\n"
        "- `memory` — Query and store knowledge\n"
        "- `gpu` — GPU-accelerated compute (available for ML workloads)\n\n"
        "NemoClaw supports NVIDIA GPU workloads. Use GPU tools when the project "
        "involves ML training, inference, or heavy computation. For standard "
        "engineering tasks, use shell and git tools as normal."
    ),
}


# ─── Template rendering ─────────────────────────────────────────────────────

def build_template_context(
    config: dict,
    readme_meta: dict,
    repo_dir: Path,
) -> dict:
    """Build the full template context from config, README metadata, and repo.

    Returns a dict with all variables needed by SKILL.md.tmpl, AGENTS.md.tmpl,
    and LEARNING.md.tmpl.
    """
    memory_backend = config.get("memory", {}).get("backend", "mem9")
    sandbox_provider = config.get("sandbox", {}).get("provider", "daytona")
    agent_runtime = config.get("agent", {}).get("runtime", "openclaw")

    # Tech stack: prefer README mentions, fall back to auto-detection
    tech_stack = readme_meta.get("tech_stack_mentions", [])
    if not tech_stack:
        tech_stack = detect_tech_stack(repo_dir)

    # Test command: prefer stilltent.yml override, then auto-detect
    test_command = config.get("test", {}).get("command", "")
    if not test_command:
        test_command = detect_test_command(repo_dir)

    # Project description: prefer parsed description, fall back to raw README
    description = readme_meta.get("description", "")
    if not description:
        description = readme_meta.get("raw", "No README.md found.")

    # Project context for AGENTS.md — brief summary
    title = readme_meta.get("title", "") or "Unknown Project"
    goals = readme_meta.get("goals", [])
    if goals:
        goals_summary = " Goals: " + "; ".join(goals[:5])
    else:
        goals_summary = ""
    project_context = f"**{title}** — {description}{goals_summary}"

    return {
        # SKILL.md variables
        "PROJECT_NAME": title,
        "PROJECT_DESCRIPTION": description,
        "PROJECT_GOALS": readme_meta.get("goals", []),
        "PROJECT_NONGOALS": readme_meta.get("nongoals", []),
        "TECH_STACK": tech_stack,
        "TEST_COMMAND": test_command,
        "MEMORY_BACKEND_INSTRUCTIONS": MEMORY_BACKEND_INSTRUCTIONS.get(
            memory_backend, MEMORY_BACKEND_INSTRUCTIONS["mem9"]
        ),
        "MEMORY_STORE_INSTRUCTIONS": MEMORY_STORE_INSTRUCTIONS.get(
            memory_backend, MEMORY_STORE_INSTRUCTIONS["mem9"]
        ),
        "VALIDATION_INSTRUCTIONS": SANDBOX_VALIDATION_INSTRUCTIONS.get(
            sandbox_provider, SANDBOX_VALIDATION_INSTRUCTIONS["local"]
        ),
        "SANDBOX_INSTRUCTIONS": SANDBOX_VALIDATION_INSTRUCTIONS.get(
            sandbox_provider, SANDBOX_VALIDATION_INSTRUCTIONS["local"]
        ),
        # AGENTS.md variables
        "PROJECT_CONTEXT": project_context,
        "TOOL_INSTRUCTIONS": TOOL_INSTRUCTIONS_BY_RUNTIME.get(
            agent_runtime, TOOL_INSTRUCTIONS_BY_RUNTIME["openclaw"]
        ),
    }


def render_templates(context: dict) -> dict[str, str]:
    """Render all prompt templates with the given context.

    Returns a dict mapping filename to rendered content:
        {"SKILL.md": "...", "AGENTS.md": "...", "LEARNING.md": "..."}
    """
    env = Environment(
        loader=FileSystemLoader(str(TEMPLATES_DIR)),
        keep_trailing_newline=True,
        trim_blocks=True,
        lstrip_blocks=True,
    )

    rendered = {}
    for tmpl_name, output_name in [
        ("SKILL.md.tmpl", "SKILL.md"),
        ("AGENTS.md.tmpl", "AGENTS.md"),
        ("LEARNING.md.tmpl", "LEARNING.md"),
    ]:
        template = env.get_template(tmpl_name)
        rendered[output_name] = template.render(**context)

    return rendered


def write_rendered(rendered: dict[str, str], *output_dirs: Path) -> None:
    """Write rendered templates to one or more output directories."""
    for output_dir in output_dirs:
        output_dir.mkdir(parents=True, exist_ok=True)
        for filename, content in rendered.items():
            path = output_dir / filename
            path.write_text(content)
            print(f"  wrote {path}")


# ─── Repo cloning ───────────────────────────────────────────────────────────

def ensure_repo(config: dict, repo_dir: Path) -> Path:
    """Ensure the target repo is cloned (or pulled) into repo_dir.

    Returns the repo_dir path.
    """
    target = config.get("target", {}).get("repo", "")
    branch = config.get("target", {}).get("branch", "main")

    if not target:
        # No remote repo configured — use local repo_dir if it exists
        if repo_dir.exists():
            print(f"prompt_builder: using local repo at {repo_dir}")
            return repo_dir
        print("prompt_builder: no target.repo configured and no local repo found", file=sys.stderr)
        return repo_dir

    github_token = os.environ.get("GITHUB_TOKEN", "")
    if github_token:
        clone_url = f"https://{github_token}@github.com/{target}.git"
    else:
        clone_url = f"https://github.com/{target}.git"

    if (repo_dir / ".git").exists():
        print(f"prompt_builder: pulling latest for {target}")
        subprocess.run(
            ["git", "-C", str(repo_dir), "checkout", branch],
            capture_output=True,
        )
        subprocess.run(
            ["git", "-C", str(repo_dir), "pull", "origin", branch],
            capture_output=True,
        )
    else:
        print(f"prompt_builder: cloning {target}")
        repo_dir.mkdir(parents=True, exist_ok=True)
        subprocess.run(
            ["git", "clone", "--branch", branch, clone_url, str(repo_dir)],
            check=True,
        )

    return repo_dir


# ─── Main ────────────────────────────────────────────────────────────────────

def main():
    parser = argparse.ArgumentParser(
        description="Generate agent prompts from a target repo's README.md"
    )
    parser.add_argument(
        "--config",
        default=str(DEFAULT_CONFIG),
        help="Path to stilltent.yml (default: stilltent.yml)",
    )
    parser.add_argument(
        "--repo",
        default=str(DEFAULT_REPO_DIR),
        help="Path to target repo (default: workspace/repo)",
    )
    parser.add_argument(
        "--readme",
        default=None,
        help="Path to README.md (overrides --repo auto-detection)",
    )
    parser.add_argument(
        "--output",
        default=None,
        help="Output directory (default: workspace/repo/.stilltent + config dir)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print rendered templates to stdout instead of writing files",
    )
    args = parser.parse_args()

    # Load config
    config_path = Path(args.config)
    if not config_path.exists():
        print(f"ERROR: Config not found: {config_path}", file=sys.stderr)
        sys.exit(1)
    with open(config_path) as f:
        config = yaml.safe_load(f)

    repo_dir = Path(args.repo)

    # Ensure repo is cloned/updated
    if not args.readme:
        ensure_repo(config, repo_dir)

    # Read README.md
    if args.readme:
        readme_path = Path(args.readme)
    else:
        readme_path = repo_dir / "README.md"

    if readme_path.exists():
        readme_text = readme_path.read_text()
        print(f"prompt_builder: parsing {readme_path} ({len(readme_text)} chars)")
    else:
        readme_text = ""
        print(f"prompt_builder: no README.md found at {readme_path}, using defaults")

    # Parse README
    readme_meta = parse_readme(readme_text)
    print(f"  title: {readme_meta['title'] or '(none)'}")
    print(f"  description: {readme_meta['description'][:80] or '(none)'}...")
    print(f"  goals: {len(readme_meta['goals'])} items")
    print(f"  nongoals: {len(readme_meta['nongoals'])} items")
    print(f"  tech mentions: {len(readme_meta['tech_stack_mentions'])} items")

    # Build context and render
    context = build_template_context(config, readme_meta, repo_dir)
    print(f"  tech_stack: {context['TECH_STACK'] or '(none detected)'}")
    print(f"  test_command: {context['TEST_COMMAND'] or '(none detected)'}")

    rendered = render_templates(context)

    if args.dry_run:
        for name, content in rendered.items():
            print(f"\n{'=' * 60}")
            print(f"  {name}")
            print(f"{'=' * 60}")
            print(content)
        return

    # Write to output directories
    output_dirs = []
    if args.output:
        output_dirs.append(Path(args.output))
    else:
        # Write to workspace/repo/.stilltent/ (agent reads from here)
        output_dirs.append(repo_dir / ".stilltent")
        # Also write to the workspace root (for volume mount compatibility)
        workspace_dir = REPO_ROOT / "workspace"
        if workspace_dir.exists():
            output_dirs.append(workspace_dir)

    print(f"\nprompt_builder: writing rendered templates...")
    write_rendered(rendered, *output_dirs)
    print("prompt_builder: done.")


if __name__ == "__main__":
    main()
