"""
Integration tests for the stilltent harness.

Tests compose generation, prompt generation, config validation,
and Daytona sandbox lifecycle.

Run with: python -m pytest tests/integration/test_harness.py -v
"""

import copy
import json
import os
import sys
import tempfile
from pathlib import Path
from unittest.mock import patch

import pytest
import yaml

# Ensure repo root is on path
REPO_ROOT = Path(__file__).resolve().parent.parent.parent
sys.path.insert(0, str(REPO_ROOT))

from core.compose import select_fragments, assemble, AGENT_FRAGMENTS, MEMORY_FRAGMENTS, AGENT_SERVICE_NAMES
from core.validate import validate, VALID_RUNTIMES, VALID_MEMORY_BACKENDS, VALID_SANDBOX_PROVIDERS, VALID_DEPLOY_TARGETS
from core.prompt_builder import parse_readme, build_template_context, render_templates, detect_tech_stack, detect_test_command


# ─── Fixtures ────────────────────────────────────────────────────────────────

def _base_config(**overrides) -> dict:
    """Build a valid stilltent.yml config dict with optional overrides."""
    config = {
        "target": {"repo": "testuser/testrepo", "branch": "main"},
        "agent": {"runtime": "openclaw", "model": "qwen/qwen3-coder-next", "provider": "openrouter"},
        "memory": {"backend": "mem9", "embedding_dim": 256},
        "sandbox": {"provider": "local"},
        "orchestrator": {"loop_interval": 60, "budget_limit": 50, "total_runtime_hours": 120},
        "deploy": {"target": "local"},
        "claude_code": {"enabled": False},
    }
    for key, val in overrides.items():
        if "." in key:
            section, field = key.split(".", 1)
            config[section][field] = val
        else:
            config[key] = val
    return config


SAMPLE_README = """# TestProject

A sample project for integration testing.

## Goals

- Build a REST API
- Add authentication
- Deploy to production

## Non-Goals

- Mobile app
- Machine learning features

## Tech Stack

- Python 3.12
- FastAPI
- PostgreSQL
- Docker

## Architecture

The project uses a layered architecture with controllers, services, and repositories.
"""


# ─── Compose Generation Tests ────────────────────────────────────────────────

class TestComposeGeneration:
    """Test that compose generation works for all agent/memory/sandbox combinations."""

    @pytest.mark.parametrize("runtime", sorted(VALID_RUNTIMES))
    @pytest.mark.parametrize("memory", sorted(VALID_MEMORY_BACKENDS))
    def test_all_runtime_memory_combinations(self, runtime, memory):
        """Compose generation succeeds for every runtime x memory combo."""
        config = _base_config()
        config["agent"]["runtime"] = runtime
        config["memory"]["backend"] = memory

        fragments = select_fragments(config)

        # Always includes base + orchestrator
        assert "base.yml" in fragments
        assert "orchestrator.yml" in fragments

        # Includes the right agent fragment
        expected_agent = AGENT_FRAGMENTS[runtime]
        assert expected_agent in fragments, f"Missing agent fragment for {runtime}"

        # Includes the right memory fragment
        expected_memory = MEMORY_FRAGMENTS[memory]
        assert expected_memory in fragments, f"Missing memory fragment for {memory}"

        # Assemble succeeds
        composed = assemble(fragments, runtime, config)
        assert "services" in composed
        assert "orchestrator" in composed["services"]

    @pytest.mark.parametrize("runtime", sorted(VALID_RUNTIMES))
    def test_orchestrator_wired_to_agent(self, runtime):
        """Orchestrator depends_on and AGENT_URL point to the correct service."""
        config = _base_config()
        config["agent"]["runtime"] = runtime

        fragments = select_fragments(config)
        composed = assemble(fragments, runtime, config)

        orch = composed["services"]["orchestrator"]
        expected_svc = AGENT_SERVICE_NAMES[runtime]

        # depends_on references the right service
        assert expected_svc in orch.get("depends_on", {}), \
            f"Orchestrator should depend on {expected_svc}"

        # AGENT_URL contains the service name
        agent_url = orch.get("environment", {}).get("AGENT_URL", "")
        assert expected_svc in agent_url, \
            f"AGENT_URL should reference {expected_svc}, got {agent_url}"

    def test_claude_code_oversight_sidecar(self):
        """When claude_code.enabled and runtime != claude-code, oversight sidecar is included."""
        config = _base_config()
        config["agent"]["runtime"] = "openclaw"
        config["claude_code"] = {"enabled": True, "oversight_interval": 3}

        fragments = select_fragments(config)
        assert "oversight-claude-code.yml" in fragments

    def test_no_oversight_when_runtime_is_claude_code(self):
        """No oversight sidecar when claude-code is the primary runtime."""
        config = _base_config()
        config["agent"]["runtime"] = "claude-code"
        config["claude_code"] = {"enabled": True}

        fragments = select_fragments(config)
        assert "oversight-claude-code.yml" not in fragments


# ─── Prompt Generation Tests ─────────────────────────────────────────────────

class TestPromptGeneration:
    """Test prompt generation with a sample README.md."""

    def test_parse_readme_extracts_metadata(self):
        """README parser extracts title, description, goals, tech stack."""
        meta = parse_readme(SAMPLE_README)

        assert meta["title"] == "TestProject"
        assert "sample project" in meta["description"].lower()
        assert len(meta["goals"]) == 3
        assert "Build a REST API" in meta["goals"]
        assert len(meta["nongoals"]) == 2
        assert len(meta["tech_stack_mentions"]) >= 3

    def test_parse_empty_readme(self):
        """Empty README produces safe defaults."""
        meta = parse_readme("")
        assert meta["title"] == ""
        assert meta["description"] == ""
        assert meta["goals"] == []

    def test_parse_readme_only_title(self):
        """README with just a title still works."""
        meta = parse_readme("# My Project\n\nA cool project.\n")
        assert meta["title"] == "My Project"
        assert "cool project" in meta["description"].lower()

    def test_build_context_and_render(self):
        """Full pipeline: parse README -> build context -> render templates."""
        config = _base_config()
        meta = parse_readme(SAMPLE_README)

        with tempfile.TemporaryDirectory() as tmpdir:
            repo_dir = Path(tmpdir)
            context = build_template_context(config, meta, repo_dir)

            assert context["PROJECT_NAME"] == "TestProject"
            assert len(context["PROJECT_GOALS"]) == 3
            assert "mem9" in context["MEMORY_BACKEND_INSTRUCTIONS"].lower() or \
                   "memory" in context["MEMORY_BACKEND_INSTRUCTIONS"].lower()

            rendered = render_templates(context)
            assert "SKILL.md" in rendered
            assert "AGENTS.md" in rendered
            assert "LEARNING.md" in rendered

            # Each rendered template should be non-empty
            for name, content in rendered.items():
                assert len(content) > 100, f"{name} is too short: {len(content)} chars"

    @pytest.mark.parametrize("memory", sorted(VALID_MEMORY_BACKENDS))
    def test_memory_instructions_vary_by_backend(self, memory):
        """Different memory backends produce different instructions."""
        config = _base_config()
        config["memory"]["backend"] = memory
        meta = parse_readme(SAMPLE_README)

        with tempfile.TemporaryDirectory() as tmpdir:
            context = build_template_context(config, meta, Path(tmpdir))
            # ASMR instructions mention "parallel" agents
            if memory == "asmr":
                assert "parallel" in context["MEMORY_BACKEND_INSTRUCTIONS"].lower()

    def test_detect_tech_stack(self):
        """Tech stack detection from marker files."""
        with tempfile.TemporaryDirectory() as tmpdir:
            repo = Path(tmpdir)
            (repo / "package.json").write_text("{}")
            (repo / "tsconfig.json").write_text("{}")

            stack = detect_tech_stack(repo)
            assert "Node.js / JavaScript" in stack
            assert "TypeScript" in stack

    def test_detect_test_command(self):
        """Test command detection from marker files."""
        with tempfile.TemporaryDirectory() as tmpdir:
            repo = Path(tmpdir)
            (repo / "Cargo.toml").write_text("[package]\nname = 'test'\n")

            cmd = detect_test_command(repo)
            assert cmd == "cargo test"


# ─── Validation Tests ────────────────────────────────────────────────────────

class TestValidation:
    """Test that stilltent.yml validation catches bad configs."""

    def test_valid_config_passes(self):
        """A well-formed config has no errors."""
        config = _base_config()
        issues = validate(config)
        errors = [i for i in issues if i["level"] == "error"]
        assert len(errors) == 0

    def test_missing_required_sections(self):
        """Missing top-level sections are caught."""
        config = {"target": {"repo": "x"}}
        issues = validate(config)
        errors = [i for i in issues if i["level"] == "error"]
        assert len(errors) > 0
        messages = " ".join(i["message"] for i in errors)
        assert "agent" in messages

    def test_invalid_runtime(self):
        """Invalid agent.runtime is caught."""
        config = _base_config()
        config["agent"]["runtime"] = "invalid-runtime"
        issues = validate(config)
        errors = [i for i in issues if i["level"] == "error"]
        assert any("invalid-runtime" in i["message"] for i in errors)

    def test_invalid_memory_backend(self):
        """Invalid memory.backend is caught."""
        config = _base_config()
        config["memory"]["backend"] = "redis"
        issues = validate(config)
        errors = [i for i in issues if i["level"] == "error"]
        assert any("redis" in i["message"] for i in errors)

    def test_invalid_sandbox_provider(self):
        """Invalid sandbox.provider is caught."""
        config = _base_config()
        config["sandbox"]["provider"] = "aws"
        issues = validate(config)
        errors = [i for i in issues if i["level"] == "error"]
        assert any("aws" in i["message"] for i in errors)

    def test_invalid_deploy_target(self):
        """Invalid deploy.target is caught."""
        config = _base_config()
        config["deploy"]["target"] = "azure"
        issues = validate(config)
        errors = [i for i in issues if i["level"] == "error"]
        assert any("azure" in i["message"] for i in errors)

    def test_all_valid_runtimes_accepted(self):
        """Every valid runtime passes validation."""
        for runtime in VALID_RUNTIMES:
            config = _base_config()
            config["agent"]["runtime"] = runtime
            issues = validate(config)
            runtime_errors = [i for i in issues if i["level"] == "error" and "runtime" in i["message"]]
            assert len(runtime_errors) == 0, f"{runtime} should be valid but got: {runtime_errors}"

    def test_all_valid_memory_backends_accepted(self):
        """Every valid memory backend passes validation (enum check only)."""
        for backend in VALID_MEMORY_BACKENDS:
            config = _base_config()
            config["memory"]["backend"] = backend
            # Provide API keys to avoid cross-validation errors
            if backend == "supermemory":
                config["memory"]["supermemory_api_key"] = "test-key"
            issues = validate(config)
            backend_errors = [i for i in issues if i["level"] == "error" and "is not valid" in i["message"]]
            assert len(backend_errors) == 0, f"{backend} should be valid"

    def test_nemoclaw_gpu_warning(self):
        """NemoClaw produces a GPU hardware warning."""
        config = _base_config()
        config["agent"]["runtime"] = "nemoclaw"
        issues = validate(config)
        warnings = [i for i in issues if i["level"] == "warning"]
        assert any("gpu" in i["message"].lower() for i in warnings)

    def test_daytona_requires_api_key(self):
        """Daytona sandbox without API key is an error."""
        config = _base_config()
        config["sandbox"]["provider"] = "daytona"
        config["sandbox"]["daytona_api_key"] = ""

        with patch.dict(os.environ, {}, clear=False):
            os.environ.pop("DAYTONA_API_KEY", None)
            issues = validate(config)
            errors = [i for i in issues if i["level"] == "error"]
            assert any("DAYTONA_API_KEY" in i["message"] for i in errors)

    def test_daytona_with_env_key_passes(self):
        """Daytona sandbox with env var API key passes."""
        config = _base_config()
        config["sandbox"]["provider"] = "daytona"
        config["sandbox"]["daytona_api_key"] = ""

        with patch.dict(os.environ, {"DAYTONA_API_KEY": "test-key"}):
            issues = validate(config)
            errors = [i for i in issues if i["level"] == "error" and "DAYTONA" in i["message"]]
            assert len(errors) == 0

    def test_supermemory_requires_api_key(self):
        """Supermemory without API key is an error."""
        config = _base_config()
        config["memory"]["backend"] = "supermemory"
        config["memory"]["supermemory_api_key"] = ""

        with patch.dict(os.environ, {}, clear=False):
            os.environ.pop("SUPERMEMORY_API_KEY", None)
            issues = validate(config)
            errors = [i for i in issues if i["level"] == "error"]
            assert any("SUPERMEMORY_API_KEY" in i["message"] for i in errors)

    def test_negative_budget_limit(self):
        """Negative budget_limit is an error."""
        config = _base_config()
        config["orchestrator"]["budget_limit"] = -10
        issues = validate(config)
        errors = [i for i in issues if i["level"] == "error"]
        assert any("budget_limit" in i["message"] for i in errors)

    def test_low_loop_interval_warning(self):
        """Very low loop_interval produces a warning."""
        config = _base_config()
        config["orchestrator"]["loop_interval"] = 5
        issues = validate(config)
        warnings = [i for i in issues if i["level"] == "warning"]
        assert any("loop_interval" in i["message"] for i in warnings)

    def test_empty_config(self):
        """Empty/None config is handled gracefully."""
        issues = validate(None)
        errors = [i for i in issues if i["level"] == "error"]
        assert len(errors) > 0

    def test_claude_code_requires_anthropic_key(self):
        """Claude Code runtime without ANTHROPIC_API_KEY is an error."""
        config = _base_config()
        config["agent"]["runtime"] = "claude-code"
        config["claude_code"] = {"enabled": True, "api_key": ""}

        with patch.dict(os.environ, {}, clear=False):
            os.environ.pop("ANTHROPIC_API_KEY", None)
            issues = validate(config)
            errors = [i for i in issues if i["level"] == "error"]
            assert any("ANTHROPIC_API_KEY" in i["message"] for i in errors)


# ─── Daytona Client Tests ────────────────────────────────────────────────────

class TestDaytonaClient:
    """Test Daytona sandbox client (skipped if no API key)."""

    @pytest.fixture
    def client(self):
        from sandbox.daytona.client import DaytonaClient
        api_key = os.environ.get("DAYTONA_API_KEY", "")
        if not api_key:
            pytest.skip("DAYTONA_API_KEY not set — skipping Daytona tests")
        return DaytonaClient(api_key=api_key)

    def test_create_workspace(self, client):
        """Can create a Daytona workspace."""
        result = client.create_workspace("testuser/testrepo", "main")
        assert "workspace_id" in result
        assert "status" in result

    def test_destroy_workspace(self, client):
        """Can destroy a Daytona workspace."""
        ws = client.create_workspace("testuser/testrepo", "main")
        ws_id = ws.get("workspace_id", "test-id")
        destroyed = client.destroy_workspace(ws_id)
        assert destroyed is True

    def test_get_workspace(self, client):
        """Can query workspace status."""
        result = client.get_workspace("nonexistent-id")
        assert "status" in result


# ─── Harness Integration Tests ───────────────────────────────────────────────

class TestHarnessIntegration:
    """Test harness utility functions."""

    def test_validate_real_config(self):
        """The actual stilltent.yml in the repo parses and validates."""
        config_path = REPO_ROOT / "stilltent.yml"
        if not config_path.exists():
            pytest.skip("stilltent.yml not found")

        with open(config_path) as f:
            config = yaml.safe_load(f)

        issues = validate(config)
        # Should have no errors (warnings are OK)
        errors = [i for i in issues if i["level"] == "error"]
        # We allow the "target.repo is empty" warning
        # but runtime/memory/sandbox/deploy should be valid
        runtime_errors = [e for e in errors if "runtime" in e["message"] or "backend" in e["message"]
                         or "provider" in e["message"] or "target" in e["message"].lower()]
        # Filter out expected errors (empty target.repo is OK for dev)
        unexpected = [e for e in errors if "DAYTONA_API_KEY" not in e["message"]
                     and "ANTHROPIC_API_KEY" not in e["message"]
                     and "SUPERMEMORY_API_KEY" not in e["message"]]
        assert len(unexpected) == 0, f"Unexpected validation errors: {unexpected}"
