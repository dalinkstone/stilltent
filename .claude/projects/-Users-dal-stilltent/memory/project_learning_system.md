---
name: Self-learning system implementation
description: Added autoresearch-inspired self-learning methodology to stilltent - LEARNING.md, enhanced SKILL.md/AGENTS.md/SOUL.md, orchestrator learning metrics
type: project
---

Implemented Karpathy autoresearch-inspired self-learning system on 2026-03-21.

**Why:** The agent was running 24/7 but not truly learning from its iterations. It needed hypothesis-driven development, quality tracking, improvement queues, self-reflection, and creative escalation to behave like a real engineer rather than a script.

**How to apply:** The learning system is embedded across multiple files:
- `workspace/LEARNING.md` — Core learning methodology document (hypothesis loop, quality metrics, improvement queue, self-reflection, creative escalation, knowledge consolidation)
- `workspace/SKILL.md` — Enhanced with hypothesis in Phase 3, measurement in Phase 7, improvement queue scheduling (every 5th/10th/25th/50th iteration)
- `workspace/AGENTS.md` — Added principles 8-11 (learn, revisit, never regress, reflect)
- `config/openclaw/workspace/SOUL.md` — Added learning philosophy, self-improvement cycle
- `orchestrator/loop.py` — Learning-aware trigger prompts (iteration-specific hints), `hypothesis_result` tracking in metrics, learning metrics in health summaries
- `orchestrator/stats.py` — Displays learning metrics (hypotheses confirmed/refuted, improvement iterations)

All 31 orchestrator tests pass after changes.
