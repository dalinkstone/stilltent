"""
ASMR Searcher — Parallel Search Agents for retrieval.

When the agent needs to recall context, instead of a single vector search,
we deploy N parallel search agents with different specializations:

  Agent 1: Direct Facts — searches for explicit statements, concrete decisions,
           specific test results, named entities.
  Agent 2: Related Context — looks for related code patterns, implications,
           indirect connections, similar problems solved before.
  Agent 3: Temporal Reconstruction — rebuilds timelines of what happened in
           what order, dependency chains, sequence-sensitive context.

Each search agent:
  - Receives the query
  - Has access to the memory backend's search API (mnemo-server)
  - Makes multiple targeted queries based on its specialization
  - Returns ranked findings with confidence scores and source references

The orchestrator compiles findings, deduplicates, and returns a unified
context block to the agent.

Uses ONLY the Python standard library (no pip dependencies).
"""

import json
import logging
import os
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Optional

logger = logging.getLogger("asmr.searcher")

# Search agent specializations
SEARCH_SPECIALIZATIONS = {
    "direct_facts": {
        "name": "Direct Facts",
        "description": (
            "Search for explicit statements, concrete decisions, specific test "
            "results, named entities, and definitive answers. Focus on factual, "
            "unambiguous information."
        ),
        "query_strategy": (
            "Generate 2-3 precise, targeted search queries that look for exact "
            "facts, decisions, and concrete outcomes related to the user's question. "
            "Use specific terms, names, and identifiers."
        ),
    },
    "related_context": {
        "name": "Related Context",
        "description": (
            "Search for related code patterns, implications, indirect connections, "
            "similar problems solved before, and contextual information that might "
            "be relevant even if not directly mentioned."
        ),
        "query_strategy": (
            "Generate 2-3 broader search queries that look for related patterns, "
            "similar past situations, adjacent concerns, and indirect connections. "
            "Think laterally — what else might be relevant?"
        ),
    },
    "temporal_reconstruction": {
        "name": "Temporal Reconstruction",
        "description": (
            "Rebuild timelines of what happened in what order, dependency chains, "
            "sequence-sensitive context, and evolution of decisions over time."
        ),
        "query_strategy": (
            "Generate 2-3 search queries focused on temporal aspects: what changed, "
            "when, in what order, what depended on what. Look for iteration numbers, "
            "timestamps, before/after states, and migration sequences."
        ),
    },
}

SEARCH_AGENT_SYSTEM_PROMPT = """\
You are a specialized memory search agent. Your role: {role}

{description}

Given a user query, generate targeted search queries for a memory database.
The memory database supports keyword and semantic search.

You MUST respond with valid JSON only — no markdown, no explanation.

Strategy: {query_strategy}

Response format:
{{
  "queries": [
    {{ "text": "search query text", "rationale": "why this query" }}
  ]
}}
"""

RANKING_SYSTEM_PROMPT = """\
You are a relevance ranking agent. Given a user's original query and a set of \
memory search results, score each result for relevance and usefulness.

You MUST respond with valid JSON only — no markdown, no explanation.

For each result, assign:
- "relevance": float 0.0-1.0 (how relevant to the query)
- "usefulness": float 0.0-1.0 (how useful for answering/acting on the query)
- "confidence": float 0.0-1.0 (your confidence in this assessment)

Response format:
{{
  "ranked": [
    {{ "id": "result_id", "relevance": 0.9, "usefulness": 0.8, "confidence": 0.95 }}
  ]
}}
"""


def _llm_request(
    provider_url: str,
    api_key: str,
    model: str,
    system_prompt: str,
    user_prompt: str,
    timeout: int = 60,
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
        "temperature": 0.2,
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


def _mem9_search(
    api_url: str,
    api_key: str,
    query: str,
    limit: int = 10,
    timeout: int = 10,
) -> list[dict]:
    """Search mnemo-server for memories matching the query.

    Uses the same API as scripts/test-mem9.py:
        GET /v1alpha2/mem9s/memories?q=<query>&limit=<limit>
    """
    path = f"/v1alpha2/mem9s/memories?q={urllib.request.quote(query)}&limit={limit}"
    url = api_url.rstrip("/") + path
    headers = {
        "X-API-Key": api_key,
        "X-Mnemo-Agent-Id": "asmr-searcher",
        "Content-Type": "application/json",
    }
    req = urllib.request.Request(url, headers=headers, method="GET")
    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
        body = json.loads(resp.read().decode())
        return body.get("memories", [])
    except (urllib.error.HTTPError, urllib.error.URLError) as exc:
        logger.error("mem9 search failed for query '%s': %s", query, exc)
        return []
    except (json.JSONDecodeError, KeyError) as exc:
        logger.error("Failed to parse mem9 search response: %s", exc)
        return []


class SearchAgent:
    """A single specialized search agent."""

    def __init__(
        self,
        specialization: str,
        provider_url: str,
        api_key: str,
        model: str,
        mem9_url: str,
        mem9_key: str,
        timeout: int = 60,
    ):
        self.specialization = specialization
        self.spec = SEARCH_SPECIALIZATIONS[specialization]
        self.provider_url = provider_url
        self.api_key = api_key
        self.model = model
        self.mem9_url = mem9_url
        self.mem9_key = mem9_key
        self.timeout = timeout

    def search(self, query: str, limit: int = 10) -> list[dict]:
        """Execute a specialized search: generate sub-queries, search, rank.

        Returns a list of findings, each with:
            - id: memory ID
            - content: memory content
            - relevance: float 0.0-1.0
            - confidence: float 0.0-1.0
            - source: specialization name
            - query_used: the sub-query that found this result
        """
        # Step 1: Generate targeted sub-queries using LLM
        system_prompt = SEARCH_AGENT_SYSTEM_PROMPT.format(
            role=self.spec["name"],
            description=self.spec["description"],
            query_strategy=self.spec["query_strategy"],
        )
        user_prompt = f"User query: {query}\n\nGenerate targeted search queries."

        llm_result = _llm_request(
            self.provider_url,
            self.api_key,
            self.model,
            system_prompt,
            user_prompt,
            self.timeout,
        )

        sub_queries = []
        for q in llm_result.get("queries", []):
            if isinstance(q, dict) and q.get("text"):
                sub_queries.append(q["text"])

        # Fallback: if LLM didn't generate queries, use the original
        if not sub_queries:
            sub_queries = [query]

        logger.info(
            "SearchAgent[%s] generated %d sub-queries for: %s",
            self.specialization,
            len(sub_queries),
            query[:80],
        )

        # Step 2: Execute each sub-query against mem9
        all_results = []
        seen_ids = set()
        per_query_limit = max(3, limit // len(sub_queries))

        for sub_q in sub_queries:
            memories = _mem9_search(
                self.mem9_url, self.mem9_key, sub_q, limit=per_query_limit
            )
            for mem in memories:
                mid = mem.get("id", "")
                if mid and mid not in seen_ids:
                    seen_ids.add(mid)
                    all_results.append(
                        {
                            "id": mid,
                            "content": mem.get("content", ""),
                            "tags": mem.get("tags", []),
                            "metadata": mem.get("metadata", {}),
                            "source": self.specialization,
                            "query_used": sub_q,
                            "relevance": 0.5,  # default, refined by ranking
                            "confidence": 0.5,
                        }
                    )

        if not all_results:
            return []

        # Step 3: Rank results using LLM
        ranked = self._rank_results(query, all_results)
        return ranked[:limit]

    def _rank_results(self, query: str, results: list[dict]) -> list[dict]:
        """Use LLM to rank search results by relevance."""
        if not results:
            return []

        # Build a summary of results for ranking
        result_summaries = []
        for r in results:
            result_summaries.append(
                {
                    "id": r["id"],
                    "content": r["content"][:500],  # truncate for token efficiency
                    "tags": r.get("tags", []),
                }
            )

        user_prompt = (
            f"Original query: {query}\n\n"
            f"Search results to rank:\n{json.dumps(result_summaries, indent=2)}"
        )

        llm_result = _llm_request(
            self.provider_url,
            self.api_key,
            self.model,
            RANKING_SYSTEM_PROMPT,
            user_prompt,
            self.timeout,
        )

        # Apply LLM rankings to results
        rankings = {}
        for r in llm_result.get("ranked", []):
            if isinstance(r, dict) and r.get("id"):
                rankings[r["id"]] = {
                    "relevance": float(r.get("relevance", 0.5)),
                    "usefulness": float(r.get("usefulness", 0.5)),
                    "confidence": float(r.get("confidence", 0.5)),
                }

        for result in results:
            rid = result["id"]
            if rid in rankings:
                result["relevance"] = rankings[rid]["relevance"]
                result["confidence"] = rankings[rid]["confidence"]
                result["usefulness"] = rankings[rid].get("usefulness", 0.5)

        # Sort by combined score (relevance * confidence)
        results.sort(
            key=lambda r: r.get("relevance", 0) * r.get("confidence", 0),
            reverse=True,
        )
        return results


class SearcherPool:
    """Manages a pool of parallel search agents and merges their results.

    Deploys N search agents (default 3), one per specialization. If N > 3,
    additional agents are assigned specializations round-robin.
    """

    def __init__(self, config: dict):
        """Initialize from stilltent config.

        Expected config keys:
            memory.asmr_searcher_count: number of parallel searchers (default 3)
            agent.provider: LLM provider name
            agent.model: model identifier
        """
        memory_cfg = config.get("memory", {})
        agent_cfg = config.get("agent", {})

        self.count = memory_cfg.get("asmr_searcher_count", 3)
        self.provider_url = _resolve_provider_url(agent_cfg.get("provider", "openrouter"))
        self.api_key = _resolve_api_key(agent_cfg.get("provider", "openrouter"))
        self.model = memory_cfg.get(
            "asmr_searcher_model", agent_cfg.get("model", "qwen/qwen3-coder-next")
        )
        self.timeout = memory_cfg.get("asmr_searcher_timeout", 60)

        # mem9 backend config
        self.mem9_url = os.environ.get("MEM9_API_URL", "http://mnemo-server:8082")
        self.mem9_key = os.environ.get("MEM9_API_KEY", "stilltent-local-dev-key")

        specializations = list(SEARCH_SPECIALIZATIONS.keys())
        self.agents = []
        for i in range(self.count):
            spec = specializations[i % len(specializations)]
            self.agents.append(
                SearchAgent(
                    specialization=spec,
                    provider_url=self.provider_url,
                    api_key=self.api_key,
                    model=self.model,
                    mem9_url=self.mem9_url,
                    mem9_key=self.mem9_key,
                    timeout=self.timeout,
                )
            )

        logger.info(
            "SearcherPool initialized: %d agents, model=%s",
            self.count,
            self.model,
        )

    def search(self, query: str, limit: int = 20) -> dict:
        """Run all search agents in parallel and return unified results.

        Returns:
            {
                "findings": [...],       # deduplicated, ranked list of findings
                "by_source": {           # grouped by specialization
                    "direct_facts": [...],
                    "related_context": [...],
                    "temporal_reconstruction": [...]
                },
                "total": int,
                "query": str
            }
        """
        per_agent_limit = max(5, limit // self.count + 2)
        by_source = {}

        with ThreadPoolExecutor(max_workers=self.count) as pool:
            futures = {
                pool.submit(agent.search, query, per_agent_limit): agent.specialization
                for agent in self.agents
            }
            for future in as_completed(futures):
                spec = futures[future]
                try:
                    results = future.result()
                    by_source[spec] = results
                except Exception as exc:
                    logger.error("SearchAgent[%s] failed: %s", spec, exc)
                    by_source[spec] = []

        # Merge and deduplicate across all agents
        all_findings = []
        seen_ids = set()
        for spec, results in by_source.items():
            for finding in results:
                fid = finding.get("id", "")
                if fid and fid not in seen_ids:
                    seen_ids.add(fid)
                    all_findings.append(finding)
                elif fid in seen_ids:
                    # If same memory found by multiple agents, boost confidence
                    for existing in all_findings:
                        if existing.get("id") == fid:
                            existing["confidence"] = min(
                                1.0, existing.get("confidence", 0.5) + 0.1
                            )
                            if spec not in existing.get("found_by", []):
                                existing.setdefault("found_by", [existing["source"]])
                                existing["found_by"].append(spec)
                            break

        # Final sort by relevance * confidence
        all_findings.sort(
            key=lambda r: r.get("relevance", 0) * r.get("confidence", 0),
            reverse=True,
        )

        # Trim to requested limit
        all_findings = all_findings[:limit]

        logger.info(
            "SearcherPool returned %d findings for query: %s",
            len(all_findings),
            query[:80],
        )

        return {
            "findings": all_findings,
            "by_source": by_source,
            "total": len(all_findings),
            "query": query,
        }


# =============================================================================
# Provider resolution helpers (shared with observer.py)
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
