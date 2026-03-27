"""
ASMR Router — Unified memory interface that routes to the appropriate backend.

Routes memory operations based on the configured backend in stilltent.yml:

  - memory.backend == "mem9":        direct calls to mnemo-server API
  - memory.backend == "supermemory": direct calls to Supermemory API
  - memory.backend == "asmr":        wraps mem9 with observer/searcher/ensemble layers

Exposes a unified interface:
  - store(content, tags, metadata)   -> memory ID
  - search(query, limit)             -> list of findings
  - get(id)                          -> single memory record
  - update(id, content)              -> updated memory
  - delete(id)                       -> success bool

The agent code never knows which backend is active — it calls the same API.

Uses ONLY the Python standard library (no pip dependencies).
"""

import json
import logging
import os
import urllib.error
import urllib.request
from typing import Optional

from memory.asmr.observer import ObserverPool, KNOWLEDGE_VECTORS
from memory.asmr.searcher import SearcherPool
from memory.asmr.ensemble import Ensemble

logger = logging.getLogger("asmr.router")


# =============================================================================
# Low-level backend clients
# =============================================================================


class Mem9Client:
    """Direct client for the mnemo-server REST API.

    Endpoints (from scripts/test-mem9.py):
        GET    /healthz                       — health check
        POST   /v1alpha2/mem9s/memories       — create memory (async, 202)
        GET    /v1alpha2/mem9s/memories?q=... — search memories
        GET    /v1alpha2/mem9s/memories/:id   — get single memory
        PUT    /v1alpha2/mem9s/memories/:id   — update memory
        DELETE /v1alpha2/mem9s/memories/:id   — delete memory (204)
    """

    MEMORIES_PATH = "/v1alpha2/mem9s/memories"

    def __init__(self, api_url: str, api_key: str, agent_id: str = "stilltent-agent"):
        self.api_url = api_url.rstrip("/")
        self.api_key = api_key
        self.agent_id = agent_id

    def _request(
        self,
        method: str,
        path: str,
        body: Optional[dict] = None,
        timeout: int = 15,
    ) -> tuple[Optional[int], dict, str]:
        """Make an HTTP request to mnemo-server."""
        url = self.api_url + path
        headers = {
            "X-API-Key": self.api_key,
            "X-Mnemo-Agent-Id": self.agent_id,
            "Content-Type": "application/json",
        }
        data = json.dumps(body).encode() if body else None
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            resp = urllib.request.urlopen(req, timeout=timeout)
            raw = resp.read().decode()
            parsed = json.loads(raw) if raw.strip() else {}
            return resp.status, parsed, raw
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode() if exc.fp else ""
            parsed = {}
            try:
                parsed = json.loads(raw)
            except Exception:
                pass
            return exc.code, parsed, raw
        except Exception as exc:
            logger.error("mem9 request %s %s failed: %s", method, path, exc)
            return None, {}, str(exc)

    def health(self) -> bool:
        status, _, _ = self._request("GET", "/healthz")
        return status == 200

    def store(self, content: str, tags: list[str] = None, metadata: dict = None) -> Optional[str]:
        body = {"content": content}
        if tags:
            body["tags"] = tags
        if metadata:
            body["metadata"] = metadata
        status, parsed, _ = self._request("POST", self.MEMORIES_PATH, body)
        if status in (200, 201, 202):
            return parsed.get("id", "accepted")
        logger.error("mem9 store failed: status=%s", status)
        return None

    def search(self, query: str, limit: int = 10) -> list[dict]:
        path = f"{self.MEMORIES_PATH}?q={urllib.request.quote(query)}&limit={limit}"
        status, parsed, _ = self._request("GET", path)
        if status == 200:
            return parsed.get("memories", [])
        return []

    def get(self, memory_id: str) -> Optional[dict]:
        status, parsed, _ = self._request("GET", f"{self.MEMORIES_PATH}/{memory_id}")
        if status == 200:
            return parsed
        return None

    def update(self, memory_id: str, content: str, tags: list[str] = None) -> Optional[dict]:
        body = {"content": content}
        if tags:
            body["tags"] = tags
        status, parsed, _ = self._request("PUT", f"{self.MEMORIES_PATH}/{memory_id}", body)
        if status == 200:
            return parsed
        return None

    def delete(self, memory_id: str) -> bool:
        status, _, _ = self._request("DELETE", f"{self.MEMORIES_PATH}/{memory_id}")
        return status in (200, 204)


class SupermemoryClient:
    """Client for the Supermemory API (external SaaS or self-hosted).

    Supermemory uses a standard REST API:
        POST   /api/v1/memories         — add memory
        GET    /api/v1/memories/search   — search memories
        GET    /api/v1/memories/:id      — get memory
        PUT    /api/v1/memories/:id      — update memory
        DELETE /api/v1/memories/:id      — delete memory
    """

    def __init__(self, api_url: str, api_key: str):
        self.api_url = api_url.rstrip("/")
        self.api_key = api_key

    def _request(
        self,
        method: str,
        path: str,
        body: Optional[dict] = None,
        timeout: int = 15,
    ) -> tuple[Optional[int], dict, str]:
        url = self.api_url + path
        headers = {
            "Authorization": f"Bearer {self.api_key}",
            "Content-Type": "application/json",
        }
        data = json.dumps(body).encode() if body else None
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            resp = urllib.request.urlopen(req, timeout=timeout)
            raw = resp.read().decode()
            parsed = json.loads(raw) if raw.strip() else {}
            return resp.status, parsed, raw
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode() if exc.fp else ""
            parsed = {}
            try:
                parsed = json.loads(raw)
            except Exception:
                pass
            return exc.code, parsed, raw
        except Exception as exc:
            logger.error("supermemory request %s %s failed: %s", method, path, exc)
            return None, {}, str(exc)

    def store(self, content: str, tags: list[str] = None, metadata: dict = None) -> Optional[str]:
        body = {"content": content}
        if tags:
            body["tags"] = tags
        if metadata:
            body["metadata"] = metadata
        status, parsed, _ = self._request("POST", "/api/v1/memories", body)
        if status in (200, 201):
            return parsed.get("id")
        return None

    def search(self, query: str, limit: int = 10) -> list[dict]:
        path = f"/api/v1/memories/search?q={urllib.request.quote(query)}&limit={limit}"
        status, parsed, _ = self._request("GET", path)
        if status == 200:
            return parsed.get("memories", parsed.get("results", []))
        return []

    def get(self, memory_id: str) -> Optional[dict]:
        status, parsed, _ = self._request("GET", f"/api/v1/memories/{memory_id}")
        if status == 200:
            return parsed
        return None

    def update(self, memory_id: str, content: str, tags: list[str] = None) -> Optional[dict]:
        body = {"content": content}
        if tags:
            body["tags"] = tags
        status, parsed, _ = self._request("PUT", f"/api/v1/memories/{memory_id}", body)
        if status == 200:
            return parsed
        return None

    def delete(self, memory_id: str) -> bool:
        status, _, _ = self._request("DELETE", f"/api/v1/memories/{memory_id}")
        return status in (200, 204)


# =============================================================================
# Unified Memory Router
# =============================================================================


class MemoryRouter:
    """Unified memory interface that routes to the configured backend.

    Usage:
        config = load_config()  # from stilltent.yml
        memory = MemoryRouter(config)

        # Store
        memory.store("Agent discovered a race condition in auth flow", tags=["error_patterns"])

        # Search
        results = memory.search("authentication issues", limit=10)

        # Ensemble decision (only for high-stakes)
        decision = memory.evaluate_decision("Should we refactor the auth module?", confidence=0.3)
    """

    def __init__(self, config: dict):
        """Initialize the router based on stilltent.yml config.

        Args:
            config: full stilltent.yml config dict
        """
        memory_cfg = config.get("memory", {})
        self.backend = memory_cfg.get("backend", "mem9")
        self._config = config

        # Initialize the appropriate backend
        if self.backend == "mem9":
            self._client = Mem9Client(
                api_url=os.environ.get("MEM9_API_URL", "http://mnemo-server:8082"),
                api_key=os.environ.get("MEM9_API_KEY", "stilltent-local-dev-key"),
            )
            self._observer_pool = None
            self._searcher_pool = None
            self._ensemble = None

        elif self.backend == "supermemory":
            self._client = SupermemoryClient(
                api_url=os.environ.get(
                    "SUPERMEMORY_API_URL", "https://api.supermemory.ai"
                ),
                api_key=memory_cfg.get("supermemory_api_key", "")
                or os.environ.get("SUPERMEMORY_API_KEY", ""),
            )
            self._observer_pool = None
            self._searcher_pool = None
            self._ensemble = None

        elif self.backend == "asmr":
            # ASMR wraps mem9 with parallel observer/searcher/ensemble layers
            self._client = Mem9Client(
                api_url=os.environ.get("MEM9_API_URL", "http://mnemo-server:8082"),
                api_key=os.environ.get("MEM9_API_KEY", "stilltent-local-dev-key"),
            )
            self._observer_pool = ObserverPool(config)
            self._searcher_pool = SearcherPool(config)
            self._ensemble = Ensemble(config)
        else:
            raise ValueError(f"Unknown memory backend: {self.backend}")

        logger.info("MemoryRouter initialized: backend=%s", self.backend)

    def store(
        self,
        content: str,
        tags: list[str] = None,
        metadata: dict = None,
    ) -> Optional[str]:
        """Store content in memory.

        For ASMR backend, the content is also processed by observer agents
        to extract structured knowledge into tagged memories.

        Args:
            content: the raw content to store
            tags: optional list of tags
            metadata: optional metadata dict

        Returns:
            Memory ID if successful, None otherwise.
        """
        # Always store the raw content in the underlying backend
        memory_id = self._client.store(content, tags, metadata)

        if self.backend == "asmr" and self._observer_pool and memory_id:
            # Additionally, run observers to extract structured knowledge
            iteration_number = (metadata or {}).get("iteration_number", 0)
            try:
                observations = self._observer_pool.observe_single(
                    content, iteration_number
                )
                # Store each observation as a separate tagged memory
                for vector, findings in observations.items():
                    for finding in findings:
                        if not finding.get("content"):
                            continue
                        obs_tags = [vector] + finding.get("tags", [])
                        if tags:
                            obs_tags.extend(tags)
                        obs_metadata = {
                            "source_type": "asmr_observer",
                            "knowledge_vector": vector,
                            "confidence": finding.get("confidence", 0.5),
                            "source_iteration": finding.get("source_iteration", 0),
                            "observer_id": finding.get("observer_id", -1),
                        }
                        self._client.store(
                            finding["content"],
                            tags=list(set(obs_tags)),
                            metadata=obs_metadata,
                        )
                logger.info(
                    "ASMR observers extracted %d findings across %d vectors",
                    sum(len(f) for f in observations.values()),
                    sum(1 for f in observations.values() if f),
                )
            except Exception as exc:
                logger.error("ASMR observer processing failed: %s", exc)
                # Raw content was already stored — observer failure is non-fatal

        return memory_id

    def search(self, query: str, limit: int = 10) -> list[dict]:
        """Search memory for relevant content.

        For ASMR backend, deploys parallel search agents with different
        specializations for richer retrieval.

        Args:
            query: search query
            limit: max number of results

        Returns:
            List of memory dicts with content, tags, metadata, and relevance scores.
        """
        if self.backend == "asmr" and self._searcher_pool:
            try:
                result = self._searcher_pool.search(query, limit)
                return result.get("findings", [])
            except Exception as exc:
                logger.error("ASMR searcher failed, falling back to direct search: %s", exc)
                # Fall through to direct search

        return self._client.search(query, limit)

    def get(self, memory_id: str) -> Optional[dict]:
        """Get a single memory by ID."""
        return self._client.get(memory_id)

    def update(self, memory_id: str, content: str, tags: list[str] = None) -> Optional[dict]:
        """Update a memory's content."""
        return self._client.update(memory_id, content, tags)

    def delete(self, memory_id: str) -> bool:
        """Delete a memory by ID."""
        return self._client.delete(memory_id)

    def evaluate_decision(
        self,
        decision_context: str,
        confidence: float = 0.5,
        is_architectural: bool = False,
    ) -> Optional[dict]:
        """Route a complex decision through the ensemble (ASMR only).

        Only triggers if:
            - Backend is "asmr"
            - confidence < 0.5 OR is_architectural is True

        Args:
            decision_context: description of the decision with relevant context
            confidence: the agent's current confidence (0.0-1.0)
            is_architectural: whether this involves architectural changes

        Returns:
            Ensemble result dict if triggered, None if skipped.
        """
        if self.backend != "asmr" or not self._ensemble:
            return None

        if not self._ensemble.should_trigger(confidence, is_architectural):
            logger.info(
                "Ensemble skipped: confidence=%.2f, architectural=%s",
                confidence,
                is_architectural,
            )
            return None

        logger.info(
            "Ensemble triggered: confidence=%.2f, architectural=%s",
            confidence,
            is_architectural,
        )
        return self._ensemble.evaluate(decision_context)

    def health(self) -> dict:
        """Check health of the memory backend and ASMR layers."""
        result = {"backend": self.backend, "healthy": False}

        if hasattr(self._client, "health"):
            result["healthy"] = self._client.health()
        else:
            # Supermemory doesn't have a standard health endpoint
            result["healthy"] = True  # assume healthy

        if self.backend == "asmr":
            result["observer_count"] = len(self._observer_pool.observers) if self._observer_pool else 0
            result["searcher_count"] = len(self._searcher_pool.agents) if self._searcher_pool else 0
            result["ensemble_variants"] = len(self._ensemble.variants) if self._ensemble else 0

        return result
