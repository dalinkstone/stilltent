"""
ASMR Observer — Parallel Observer Agents for memory ingestion.

When the agent completes an iteration, instead of storing a single memory blob,
we fan out to N parallel observer agents. Each observer reads the iteration's
raw output and extracts structured knowledge across six knowledge vectors:

  1. Architectural Decisions — design patterns, tech choices, structural changes
  2. Test Intelligence — what tests exist, coverage, known failures, flaky tests
  3. Code Patterns — recurring patterns, anti-patterns, style conventions
  4. Temporal State — what changed when, dependency on sequence, before/after
  5. Error Patterns — failure modes, root causes, fix strategies that worked
  6. Project Understanding — README vs reality, gaps, progress

Each observer processes different iteration ranges concurrently (round-robin
assignment). Observers are LLM calls via the configured provider (OpenRouter,
Anthropic, OpenAI, Ollama) using a fast/cheap model.

Uses ONLY the Python standard library (no pip dependencies).
"""

import json
import logging
import os
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Optional

logger = logging.getLogger("asmr.observer")

# The six knowledge vectors that observers extract
KNOWLEDGE_VECTORS = [
    "architectural_decisions",
    "test_intelligence",
    "code_patterns",
    "temporal_state",
    "error_patterns",
    "project_understanding",
]

VECTOR_DESCRIPTIONS = {
    "architectural_decisions": (
        "Design patterns chosen or rejected, technology choices, structural changes "
        "to the codebase, dependency decisions, API design choices."
    ),
    "test_intelligence": (
        "What tests exist and what they cover, known test failures, flaky tests, "
        "test infrastructure changes, coverage gaps identified."
    ),
    "code_patterns": (
        "Recurring code patterns discovered, anti-patterns found and fixed, "
        "style conventions, idioms used, abstraction patterns."
    ),
    "temporal_state": (
        "What changed in what order, dependencies between changes, what came "
        "before and after, sequence-sensitive context, migration steps."
    ),
    "error_patterns": (
        "Failure modes encountered, root causes identified, fix strategies that "
        "worked or failed, error messages and their meanings, debugging approaches."
    ),
    "project_understanding": (
        "What the README/spec asks for vs what currently exists, feature gaps, "
        "progress toward goals, blockers, technical debt identified."
    ),
}

# System prompt for observer agents
OBSERVER_SYSTEM_PROMPT = """\
You are a knowledge extraction agent. Your job is to read the output of a \
software engineering iteration and extract structured knowledge.

You MUST respond with valid JSON only — no markdown, no explanation, no preamble.

Extract findings for these knowledge vectors:
{vectors}

For each vector, return an array of findings. Each finding has:
- "content": a concise statement of the knowledge (1-3 sentences)
- "confidence": float 0.0-1.0 (how certain you are this is accurate)
- "tags": array of short keyword tags for searchability

If a vector has no relevant findings, return an empty array for it.

Response format:
{{
  "architectural_decisions": [{{ "content": "...", "confidence": 0.9, "tags": ["..."] }}],
  "test_intelligence": [...],
  "code_patterns": [...],
  "temporal_state": [...],
  "error_patterns": [...],
  "project_understanding": [...]
}}
"""


def _build_vector_descriptions() -> str:
    """Build the vector description block for the system prompt."""
    lines = []
    for i, (vec, desc) in enumerate(VECTOR_DESCRIPTIONS.items(), 1):
        lines.append(f"{i}. {vec}: {desc}")
    return "\n".join(lines)


def _llm_request(
    provider_url: str,
    api_key: str,
    model: str,
    system_prompt: str,
    user_prompt: str,
    timeout: int = 120,
) -> dict:
    """Make a single LLM completion request via OpenAI-compatible API.

    All supported providers (OpenRouter, OpenAI, Anthropic via proxy, Ollama)
    expose an OpenAI-compatible /v1/chat/completions endpoint.
    """
    url = provider_url.rstrip("/") + "/v1/chat/completions"
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {api_key}",
    }
    payload = {
        "model": model,
        "messages": [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": user_prompt},
        ],
        "temperature": 0.3,
        "max_tokens": 4096,
        "response_format": {"type": "json_object"},
    }
    data = json.dumps(payload).encode()
    req = urllib.request.Request(url, data=data, headers=headers, method="POST")
    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
        body = json.loads(resp.read().decode())
        content = body["choices"][0]["message"]["content"]
        return json.loads(content)
    except (urllib.error.HTTPError, urllib.error.URLError) as exc:
        logger.error("LLM request failed: %s", exc)
        return {}
    except (json.JSONDecodeError, KeyError, IndexError) as exc:
        logger.error("Failed to parse LLM response: %s", exc)
        return {}


class Observer:
    """A single observer agent that extracts knowledge from iteration output."""

    def __init__(
        self,
        observer_id: int,
        provider_url: str,
        api_key: str,
        model: str,
        timeout: int = 120,
    ):
        self.observer_id = observer_id
        self.provider_url = provider_url
        self.api_key = api_key
        self.model = model
        self.timeout = timeout

    def observe(self, iteration_output: str, iteration_number: int) -> dict:
        """Process an iteration's raw output and extract structured knowledge.

        Returns a dict keyed by knowledge vector with arrays of findings.
        """
        system_prompt = OBSERVER_SYSTEM_PROMPT.format(
            vectors=_build_vector_descriptions()
        )
        user_prompt = (
            f"## Iteration {iteration_number} Output\n\n"
            f"{iteration_output}\n\n"
            f"Extract all relevant knowledge from this iteration output. "
            f"Be thorough — capture decisions, patterns, errors, and context "
            f"that would help a future agent working on this project."
        )

        logger.info(
            "Observer %d processing iteration %d", self.observer_id, iteration_number
        )
        result = _llm_request(
            self.provider_url,
            self.api_key,
            self.model,
            system_prompt,
            user_prompt,
            self.timeout,
        )

        if not result:
            logger.warning(
                "Observer %d got empty result for iteration %d",
                self.observer_id,
                iteration_number,
            )
            return {vec: [] for vec in KNOWLEDGE_VECTORS}

        # Normalize: ensure all six vectors are present
        normalized = {}
        for vec in KNOWLEDGE_VECTORS:
            findings = result.get(vec, [])
            if not isinstance(findings, list):
                findings = []
            # Tag each finding with source metadata
            for finding in findings:
                if isinstance(finding, dict):
                    finding["source_iteration"] = iteration_number
                    finding["observer_id"] = self.observer_id
            normalized[vec] = findings

        return normalized


class ObserverPool:
    """Manages a pool of parallel observer agents.

    Each observer processes different iteration ranges concurrently using
    round-robin assignment: observer 0 takes iterations 0,N,2N,...;
    observer 1 takes iterations 1,N+1,2N+1,...; etc.
    """

    def __init__(self, config: dict):
        """Initialize the observer pool from stilltent config.

        Expected config keys (from stilltent.yml):
            memory.asmr_observer_count: number of parallel observers (default 3)
            agent.provider: LLM provider name
            agent.model: model identifier

        Provider URL is resolved from provider name:
            openrouter -> https://openrouter.ai/api
            openai     -> https://api.openai.com
            anthropic  -> https://api.anthropic.com
            ollama     -> http://localhost:11434
        """
        memory_cfg = config.get("memory", {})
        agent_cfg = config.get("agent", {})

        self.count = memory_cfg.get("asmr_observer_count", 3)
        self.provider_url = _resolve_provider_url(agent_cfg.get("provider", "openrouter"))
        self.api_key = _resolve_api_key(agent_cfg.get("provider", "openrouter"))
        # Use a fast/cheap model for observers — configurable, falls back to agent model
        self.model = memory_cfg.get(
            "asmr_observer_model", agent_cfg.get("model", "qwen/qwen3-coder-next")
        )
        self.timeout = memory_cfg.get("asmr_observer_timeout", 120)

        self.observers = [
            Observer(i, self.provider_url, self.api_key, self.model, self.timeout)
            for i in range(self.count)
        ]
        logger.info(
            "ObserverPool initialized: %d observers, model=%s, provider=%s",
            self.count,
            self.model,
            agent_cfg.get("provider", "openrouter"),
        )

    def observe_iterations(
        self,
        iterations: list[dict],
        max_workers: Optional[int] = None,
    ) -> dict:
        """Process multiple iterations in parallel using round-robin assignment.

        Args:
            iterations: list of dicts with keys "output" (str) and "number" (int)
            max_workers: thread pool size (defaults to observer count)

        Returns:
            Merged dict keyed by knowledge vector with all findings from all
            observers across all iterations, deduplicated.
        """
        if not iterations:
            return {vec: [] for vec in KNOWLEDGE_VECTORS}

        workers = max_workers or self.count

        # Round-robin assignment: observer i gets iterations i, i+N, i+2N, ...
        assignments = []
        for idx, iteration in enumerate(iterations):
            observer = self.observers[idx % self.count]
            assignments.append((observer, iteration))

        merged = {vec: [] for vec in KNOWLEDGE_VECTORS}

        with ThreadPoolExecutor(max_workers=workers) as pool:
            futures = {
                pool.submit(
                    obs.observe, it["output"], it["number"]
                ): (obs.observer_id, it["number"])
                for obs, it in assignments
            }
            for future in as_completed(futures):
                obs_id, it_num = futures[future]
                try:
                    result = future.result()
                    for vec in KNOWLEDGE_VECTORS:
                        merged[vec].extend(result.get(vec, []))
                except Exception as exc:
                    logger.error(
                        "Observer %d failed on iteration %d: %s", obs_id, it_num, exc
                    )

        # Deduplicate findings by content hash
        for vec in KNOWLEDGE_VECTORS:
            seen = set()
            unique = []
            for finding in merged[vec]:
                if not isinstance(finding, dict):
                    continue
                key = finding.get("content", "")
                if key and key not in seen:
                    seen.add(key)
                    unique.append(finding)
            merged[vec] = unique

        total = sum(len(v) for v in merged.values())
        logger.info(
            "ObserverPool processed %d iterations, extracted %d findings",
            len(iterations),
            total,
        )
        return merged

    def observe_single(self, output: str, iteration_number: int) -> dict:
        """Convenience: process a single iteration through all observers in parallel.

        All observers process the same iteration, then results are merged.
        This gives diversity of extraction from the same content.
        """
        iterations_for_all = [
            {"output": output, "number": iteration_number}
        ] * self.count

        # Each observer gets the same content but may extract different things
        merged = {vec: [] for vec in KNOWLEDGE_VECTORS}

        with ThreadPoolExecutor(max_workers=self.count) as pool:
            futures = {
                pool.submit(obs.observe, output, iteration_number): obs.observer_id
                for obs in self.observers
            }
            for future in as_completed(futures):
                obs_id = futures[future]
                try:
                    result = future.result()
                    for vec in KNOWLEDGE_VECTORS:
                        merged[vec].extend(result.get(vec, []))
                except Exception as exc:
                    logger.error("Observer %d failed: %s", obs_id, exc)

        # Deduplicate
        for vec in KNOWLEDGE_VECTORS:
            seen = set()
            unique = []
            for finding in merged[vec]:
                if not isinstance(finding, dict):
                    continue
                key = finding.get("content", "")
                if key and key not in seen:
                    seen.add(key)
                    unique.append(finding)
            merged[vec] = unique

        return merged


# =============================================================================
# Provider resolution helpers
# =============================================================================

PROVIDER_URLS = {
    "openrouter": "https://openrouter.ai/api",
    "openai": "https://api.openai.com",
    "anthropic": "https://api.anthropic.com",
    "ollama": "http://localhost:11434",
}

PROVIDER_KEY_ENV = {
    "openrouter": "OPENROUTER_API_KEY",
    "openai": "OPENAI_API_KEY",
    "anthropic": "ANTHROPIC_API_KEY",
    "ollama": "",  # Ollama doesn't need a key
}


def _resolve_provider_url(provider: str) -> str:
    """Resolve provider name to base API URL."""
    env_override = os.environ.get("ASMR_PROVIDER_URL", "")
    if env_override:
        return env_override
    return PROVIDER_URLS.get(provider, PROVIDER_URLS["openrouter"])


def _resolve_api_key(provider: str) -> str:
    """Resolve API key from environment based on provider."""
    env_override = os.environ.get("ASMR_API_KEY", "")
    if env_override:
        return env_override
    key_env = PROVIDER_KEY_ENV.get(provider, "")
    if key_env:
        return os.environ.get(key_env, "")
    return ""
