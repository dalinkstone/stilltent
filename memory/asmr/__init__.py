"""
ASMR — Agentic Search and Memory Retrieval layer.

Sits between the agent and the memory backend (mem9 or supermemory), adding
parallel agentic reasoning for both ingestion and retrieval.

Components:
    ObserverPool  — Parallel observer agents for memory ingestion
    SearcherPool  — Parallel search agents for retrieval
    Ensemble      — Multi-variant answering ensemble for complex decisions
    MemoryRouter  — Unified interface that routes to the appropriate backend
"""

from memory.asmr.observer import ObserverPool, Observer, KNOWLEDGE_VECTORS
from memory.asmr.searcher import SearcherPool, SearchAgent, SEARCH_SPECIALIZATIONS
from memory.asmr.ensemble import Ensemble, EnsembleVariant, VARIANT_DEFINITIONS
from memory.asmr.router import MemoryRouter

__all__ = [
    "ObserverPool",
    "Observer",
    "SearcherPool",
    "SearchAgent",
    "Ensemble",
    "EnsembleVariant",
    "MemoryRouter",
    "KNOWLEDGE_VECTORS",
    "SEARCH_SPECIALIZATIONS",
    "VARIANT_DEFINITIONS",
]
