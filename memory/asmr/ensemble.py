"""
ASMR Ensemble — Multi-variant Answering Ensemble for complex decisions.

When the agent faces a complex decision (architectural choice, tricky bug fix,
feature prioritization), the ensemble routes through N specialized prompt
variants running in parallel:

  Variant 1: Conservative — prioritize stability, minimal changes, test coverage
  Variant 2: Progressive — prioritize new features, velocity, user-facing value
  Variant 3: Precision — focus on edge cases, error handling, correctness
  Variant 4: Refactor — focus on code quality, patterns, maintainability

Additional variants are configurable up to 8. Each variant independently
evaluates context and proposes an action. An aggregator LLM synthesizes
using majority voting and conflict resolution.

This is optional — only triggered for high-stakes decisions where
confidence < 0.5 or architectural changes are proposed.

Uses ONLY the Python standard library (no pip dependencies).
"""

import json
import logging
import os
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Optional

logger = logging.getLogger("asmr.ensemble")

# Built-in variant definitions (up to 8)
VARIANT_DEFINITIONS = [
    {
        "id": "conservative",
        "name": "Conservative",
        "persona": (
            "You prioritize stability, minimal changes, and comprehensive test "
            "coverage. You prefer battle-tested approaches over novel solutions. "
            "When in doubt, choose the option that reduces risk and maintains "
            "backward compatibility. Avoid large refactors unless absolutely necessary."
        ),
    },
    {
        "id": "progressive",
        "name": "Progressive",
        "persona": (
            "You prioritize new features, development velocity, and user-facing "
            "value. Ship early, iterate fast. Choose approaches that unblock the "
            "most downstream work. Technical debt is acceptable if it delivers "
            "user value sooner."
        ),
    },
    {
        "id": "precision",
        "name": "Precision",
        "persona": (
            "You focus on edge cases, error handling, and correctness above all. "
            "Every input should be validated, every error path should be handled, "
            "every assumption should be documented. Prefer explicit over implicit. "
            "A correct solution that covers all cases is better than a fast one."
        ),
    },
    {
        "id": "refactor",
        "name": "Refactor",
        "persona": (
            "You focus on code quality, design patterns, and long-term "
            "maintainability. Eliminate duplication, improve naming, extract "
            "abstractions where they simplify understanding. Clean code prevents "
            "future bugs. Invest in structure now to move faster later."
        ),
    },
    {
        "id": "pragmatic",
        "name": "Pragmatic",
        "persona": (
            "You balance all concerns — features, quality, speed, reliability. "
            "Choose the approach with the best effort-to-value ratio. Don't "
            "over-engineer, but don't cut corners on things that will bite later. "
            "Consider the team's current priorities and constraints."
        ),
    },
    {
        "id": "security",
        "name": "Security",
        "persona": (
            "You prioritize security, data integrity, and defensive programming. "
            "Assume adversarial inputs. Check for injection, authorization, and "
            "data leakage risks. Prefer fail-closed over fail-open. Security "
            "issues should block all other work."
        ),
    },
    {
        "id": "performance",
        "name": "Performance",
        "persona": (
            "You focus on runtime efficiency, resource usage, and scalability. "
            "Profile before optimizing, but recognize algorithmic complexity "
            "issues early. Choose data structures and patterns that scale. "
            "Avoid premature optimization but don't ignore obvious bottlenecks."
        ),
    },
    {
        "id": "user_experience",
        "name": "User Experience",
        "persona": (
            "You prioritize the end-user experience — clear error messages, "
            "intuitive APIs, helpful documentation, and graceful degradation. "
            "Every decision should be evaluated through the lens of 'how does "
            "this affect the person using this software?'"
        ),
    },
]

VARIANT_SYSTEM_PROMPT = """\
You are a decision advisor with the following perspective:

{persona}

You are evaluating a decision in a software engineering context. Analyze the \
situation and propose your recommended action.

You MUST respond with valid JSON only — no markdown, no explanation.

Response format:
{{
  "recommendation": "Your recommended action (1-3 sentences)",
  "reasoning": "Why you recommend this (2-4 sentences)",
  "confidence": 0.0-1.0,
  "risks": ["risk 1", "risk 2"],
  "tradeoffs": ["tradeoff 1", "tradeoff 2"],
  "vote": "proceed|defer|investigate|reject"
}}

Votes:
- proceed: Go ahead with the proposed approach
- defer: Wait for more information or a better time
- investigate: Need more research before deciding
- reject: This approach has fundamental problems
"""

AGGREGATOR_SYSTEM_PROMPT = """\
You are a decision aggregator. Multiple advisors have evaluated a decision \
from different perspectives. Your job is to synthesize their recommendations \
into a final decision using these rules:

1. Majority voting: if most advisors agree on a vote, lean toward that
2. Confidence weighting: weight recommendations by advisor confidence
3. Risk union: if ANY advisor identifies a critical risk, surface it
4. Conflict resolution: when advisors disagree, explain the tradeoff clearly

You MUST respond with valid JSON only — no markdown, no explanation.

Response format:
{{
  "decision": "The synthesized decision (2-4 sentences)",
  "vote": "proceed|defer|investigate|reject",
  "confidence": 0.0-1.0,
  "supporting_perspectives": ["variant_id", ...],
  "dissenting_perspectives": ["variant_id", ...],
  "key_risks": ["risk 1", ...],
  "key_tradeoffs": ["tradeoff 1", ...],
  "rationale": "Why this decision was reached (2-4 sentences)"
}}
"""


def _llm_request(
    provider_url: str,
    api_key: str,
    model: str,
    system_prompt: str,
    user_prompt: str,
    timeout: int = 90,
) -> dict:
    """Make a single LLM completion request via OpenAI-compatible API."""
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
        "temperature": 0.4,
        "max_tokens": 2048,
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


class EnsembleVariant:
    """A single decision-making variant with a specific perspective."""

    def __init__(
        self,
        variant_def: dict,
        provider_url: str,
        api_key: str,
        model: str,
        timeout: int = 90,
    ):
        self.variant_id = variant_def["id"]
        self.name = variant_def["name"]
        self.persona = variant_def["persona"]
        self.provider_url = provider_url
        self.api_key = api_key
        self.model = model
        self.timeout = timeout

    def evaluate(self, decision_context: str) -> dict:
        """Evaluate a decision from this variant's perspective.

        Args:
            decision_context: description of the decision to be made,
                including relevant code context and memory findings.

        Returns:
            Dict with recommendation, reasoning, confidence, risks,
            tradeoffs, and vote.
        """
        system_prompt = VARIANT_SYSTEM_PROMPT.format(persona=self.persona)

        result = _llm_request(
            self.provider_url,
            self.api_key,
            self.model,
            system_prompt,
            decision_context,
            self.timeout,
        )

        if not result:
            logger.warning("Variant '%s' returned empty result", self.variant_id)
            return {
                "variant_id": self.variant_id,
                "variant_name": self.name,
                "recommendation": "",
                "reasoning": "",
                "confidence": 0.0,
                "risks": [],
                "tradeoffs": [],
                "vote": "investigate",
            }

        result["variant_id"] = self.variant_id
        result["variant_name"] = self.name
        return result


class Ensemble:
    """Multi-variant decision ensemble with aggregation.

    Only triggered for high-stakes decisions where confidence < 0.5
    or architectural changes are proposed.
    """

    # Decisions with confidence above this threshold skip the ensemble
    CONFIDENCE_THRESHOLD = 0.5

    def __init__(self, config: dict):
        """Initialize from stilltent config.

        Expected config keys:
            memory.asmr_ensemble_variants: number of variants (default 4, max 8)
            agent.provider: LLM provider name
            agent.model: model identifier
        """
        memory_cfg = config.get("memory", {})
        agent_cfg = config.get("agent", {})

        variant_count = min(
            memory_cfg.get("asmr_ensemble_variants", 4),
            len(VARIANT_DEFINITIONS),
        )
        self.provider_url = _resolve_provider_url(agent_cfg.get("provider", "openrouter"))
        self.api_key = _resolve_api_key(agent_cfg.get("provider", "openrouter"))
        self.model = memory_cfg.get(
            "asmr_ensemble_model", agent_cfg.get("model", "qwen/qwen3-coder-next")
        )
        self.timeout = memory_cfg.get("asmr_ensemble_timeout", 90)

        self.variants = [
            EnsembleVariant(
                VARIANT_DEFINITIONS[i],
                self.provider_url,
                self.api_key,
                self.model,
                self.timeout,
            )
            for i in range(variant_count)
        ]

        logger.info(
            "Ensemble initialized: %d variants (%s), model=%s",
            variant_count,
            ", ".join(v.name for v in self.variants),
            self.model,
        )

    def should_trigger(
        self,
        confidence: float,
        is_architectural: bool = False,
    ) -> bool:
        """Determine if the ensemble should be triggered.

        Args:
            confidence: the agent's current confidence in its decision (0.0-1.0)
            is_architectural: whether the decision involves architectural changes

        Returns:
            True if the ensemble should evaluate this decision.
        """
        if is_architectural:
            return True
        return confidence < self.CONFIDENCE_THRESHOLD

    def evaluate(
        self,
        decision_context: str,
        max_workers: Optional[int] = None,
    ) -> dict:
        """Run all variants in parallel and aggregate their recommendations.

        Args:
            decision_context: description of the decision, including code
                context and memory findings.
            max_workers: thread pool size (defaults to variant count)

        Returns:
            {
                "decision": str,
                "vote": str,
                "confidence": float,
                "variants": [...],       # individual variant responses
                "supporting": [...],     # variant IDs that agree
                "dissenting": [...],     # variant IDs that disagree
                "key_risks": [...],
                "key_tradeoffs": [...],
                "rationale": str
            }
        """
        workers = max_workers or len(self.variants)
        variant_results = []

        # Step 1: Run all variants in parallel
        with ThreadPoolExecutor(max_workers=workers) as pool:
            futures = {
                pool.submit(v.evaluate, decision_context): v.variant_id
                for v in self.variants
            }
            for future in as_completed(futures):
                vid = futures[future]
                try:
                    result = future.result()
                    variant_results.append(result)
                except Exception as exc:
                    logger.error("Variant '%s' failed: %s", vid, exc)
                    variant_results.append(
                        {
                            "variant_id": vid,
                            "recommendation": "",
                            "confidence": 0.0,
                            "vote": "investigate",
                            "risks": [],
                            "tradeoffs": [],
                        }
                    )

        # Step 2: Quick majority vote (without LLM if unanimous)
        votes = [r.get("vote", "investigate") for r in variant_results if r.get("vote")]
        vote_counts = {}
        for v in votes:
            vote_counts[v] = vote_counts.get(v, 0) + 1

        if len(vote_counts) == 1:
            # Unanimous — skip aggregator LLM call
            majority_vote = votes[0]
            avg_confidence = sum(
                r.get("confidence", 0.5) for r in variant_results
            ) / max(len(variant_results), 1)

            all_risks = []
            all_tradeoffs = []
            for r in variant_results:
                all_risks.extend(r.get("risks", []))
                all_tradeoffs.extend(r.get("tradeoffs", []))

            logger.info("Ensemble unanimous: vote=%s, confidence=%.2f", majority_vote, avg_confidence)

            return {
                "decision": variant_results[0].get("recommendation", ""),
                "vote": majority_vote,
                "confidence": avg_confidence,
                "variants": variant_results,
                "supporting": [r["variant_id"] for r in variant_results],
                "dissenting": [],
                "key_risks": list(set(all_risks)),
                "key_tradeoffs": list(set(all_tradeoffs)),
                "rationale": "All variants unanimously agreed.",
            }

        # Step 3: Non-unanimous — use aggregator LLM to synthesize
        return self._aggregate(decision_context, variant_results)

    def _aggregate(
        self,
        decision_context: str,
        variant_results: list[dict],
    ) -> dict:
        """Use an aggregator LLM to synthesize variant recommendations."""
        # Build summary of all variant responses
        summaries = []
        for r in variant_results:
            summaries.append(
                {
                    "variant_id": r.get("variant_id", "unknown"),
                    "variant_name": r.get("variant_name", "unknown"),
                    "recommendation": r.get("recommendation", ""),
                    "reasoning": r.get("reasoning", ""),
                    "confidence": r.get("confidence", 0.5),
                    "vote": r.get("vote", "investigate"),
                    "risks": r.get("risks", []),
                    "tradeoffs": r.get("tradeoffs", []),
                }
            )

        user_prompt = (
            f"## Decision Context\n{decision_context}\n\n"
            f"## Advisor Recommendations\n{json.dumps(summaries, indent=2)}\n\n"
            f"Synthesize these into a final decision."
        )

        result = _llm_request(
            self.provider_url,
            self.api_key,
            self.model,
            AGGREGATOR_SYSTEM_PROMPT,
            user_prompt,
            self.timeout,
        )

        if not result:
            # Fallback: use majority vote
            votes = [r.get("vote", "investigate") for r in variant_results]
            vote_counts = {}
            for v in votes:
                vote_counts[v] = vote_counts.get(v, 0) + 1
            majority_vote = max(vote_counts, key=vote_counts.get)

            return {
                "decision": "Aggregation failed; defaulting to majority vote.",
                "vote": majority_vote,
                "confidence": 0.3,
                "variants": variant_results,
                "supporting": [],
                "dissenting": [],
                "key_risks": [],
                "key_tradeoffs": [],
                "rationale": "LLM aggregation failed, used simple majority vote.",
            }

        # Merge LLM aggregation with variant data
        result["variants"] = variant_results
        result.setdefault("supporting_perspectives", [])
        result.setdefault("dissenting_perspectives", [])
        # Normalize field names
        result.setdefault("supporting", result.pop("supporting_perspectives", []))
        result.setdefault("dissenting", result.pop("dissenting_perspectives", []))

        logger.info(
            "Ensemble aggregated: vote=%s, confidence=%.2f, %d supporting, %d dissenting",
            result.get("vote", "?"),
            result.get("confidence", 0),
            len(result.get("supporting", [])),
            len(result.get("dissenting", [])),
        )

        return result


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
    "ollama": "",
}


def _resolve_provider_url(provider: str) -> str:
    env_override = os.environ.get("ASMR_PROVIDER_URL", "")
    if env_override:
        return env_override
    return PROVIDER_URLS.get(provider, PROVIDER_URLS["openrouter"])


def _resolve_api_key(provider: str) -> str:
    env_override = os.environ.get("ASMR_API_KEY", "")
    if env_override:
        return env_override
    key_env = PROVIDER_KEY_ENV.get(provider, "")
    if key_env:
        return os.environ.get(key_env, "")
    return ""
