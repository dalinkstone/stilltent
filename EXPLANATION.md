# stilltent — Project Explanation

## What Is This?

stilltent is an **autonomous AI agent system** that continuously develops, manages, and improves a GitHub repository — without human intervention. It uses locally-hosted language models, persistent memory, and a structured iteration protocol to act as an always-on software developer.

The system clones a target GitHub repository, examines its state, plans work, implements changes, runs tests, opens pull requests, and (when confident) merges them — all in a loop that runs on a configurable interval.

---

## High-Level Architecture

The system is composed of four core services, orchestrated via Docker Compose on an internal bridge network (`stilltent-net`):

```
┌──────────────────────────────────────────────────────────────┐
│                        Host Machine                          │
│                                                              │
│   ┌──────────┐                                               │
│   │  Ollama   │  ← Runs natively on the host (not in Docker) │
│   │  (LLM)   │    Provides chat completion & text embeddings │
│   └────┬─────┘                                               │
│        │ http://host.docker.internal:11434                   │
│        │                                                     │
│  ┌─────┼───────────────────────────────────────────────────┐ │
│  │     │           Docker Compose Stack                    │ │
│  │     │                                                   │ │
│  │  ┌──▼─────────────┐    ┌──────────────┐                │ │
│  │  │  OpenClaw       │◄───│ Orchestrator │                │ │
│  │  │  Gateway        │    │  (loop.py)   │                │ │
│  │  │  (Agent Runtime)│    └──────────────┘                │ │
│  │  └──┬─────────────┘                                    │ │
│  │     │                                                   │ │
│  │  ┌──▼─────────────┐    ┌──────────────┐                │ │
│  │  │  mnemo-server   │◄───│    TiDB      │                │ │
│  │  │  (Memory API)   │    │  (Database)  │                │ │
│  │  └────────────────┘    └──────────────┘                │ │
│  └─────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
```

### The Four Services

1. **TiDB** — A MySQL-compatible distributed database (v8.4.0) that stores all persistent data. It supports native vector types, enabling hybrid keyword + semantic search over the agent's memories. Data is stored in a named Docker volume (`tidb-data`) so it survives container restarts.

2. **mnemo-server (mem9)** — A Go REST API that provides persistent memory as a service. It stores, searches, and manages "memories" — structured records the agent creates as it works. Memories have types (pinned insights, session logs, digests), states (active, archived, deleted), and vector embeddings for semantic search. The server supports multi-tenant isolation, rate limiting, and Prometheus metrics.

3. **OpenClaw Gateway** — The agent runtime. This is where the AI "lives." OpenClaw provides an OpenAI-compatible chat completions endpoint, manages sessions, executes tools (shell commands, file I/O, GitHub CLI, memory operations), and routes messages to the LLM provider (Ollama). It has a plugin system, and mem9 is registered as the memory plugin.

4. **Orchestrator** — A lightweight Python script (~410 lines, stdlib only) that drives the agent loop. It does not make decisions — it just triggers the agent on a schedule, monitors for failures, collects metrics, and enforces time budgets. Think of it as a cron job with health monitoring.

### Host-Side Dependency

- **Ollama** runs natively on the host machine (not inside Docker). It serves the chat model (`qwen3:32b-nothink`, a 32-billion parameter model) and the embedding model (`nomic-embed-text`, 768-dimensional vectors). Containers reach it via `host.docker.internal:11434`.

---

## How the Agent Loop Works

The orchestrator runs an infinite loop:

```
while not shutdown:
    1. Check if PAUSE file exists → if yes, sleep and retry
    2. Check consecutive failure count → if too many, auto-pause
    3. Build a trigger prompt and send it to OpenClaw
    4. Wait for the agent's response (with timeout)
    5. Evaluate success/failure from the response
    6. Write metrics to workspace/metrics.json
    7. Sleep for the configured interval
    8. Repeat
```

Each time the agent is triggered, it follows a **7-phase iteration protocol** defined in `workspace/SKILL.md`:

### Phase 1: RECALL
Search persistent memory for context — recent test results, in-progress work, failed approaches to avoid, and architectural decisions.

### Phase 2: ASSESS
Examine the target repository's current state: git log, file structure, open PRs, open issues, CI status, and test results. Determine the highest-priority action.

### Phase 3: PLAN
Write a structured plan with an action type, files to modify, expected outcome, confidence score (0.0–1.0), and risk assessment. Low-confidence plans are rejected. Large changes are broken into smaller iterations.

### Phase 4: IMPLEMENT
Create a branch (`agent/YYYYMMDDHHMMSS-<slug>`), make incremental changes, run tests after each change, and commit. If tests fail, attempt up to 3 fixes before reverting. Time budget: 8 minutes.

### Phase 5: VALIDATE
Run the full test suite, linter, and build. If anything fails and can't be fixed within 2 minutes, abandon the branch.

### Phase 6: SUBMIT
Push the branch and open a pull request via `gh pr create`. Auto-merge if tests pass and confidence is high enough. Leave PRs open for human review if confidence is low or protected files were modified. Also reviews external PRs from contributors.

### Phase 7: LEARN
Record what happened in memory — iteration logs, repository state snapshots, failed approaches, and architectural decisions. Every 50 iterations, consolidate memories to prevent unbounded growth.

### Priority System

The agent follows a strict priority order when choosing what to work on:

1. Fix failing tests (blocking everything else)
2. Review external PRs (time-sensitive)
3. Continue in-progress plans
4. Address open issues
5. Improve test coverage
6. Implement new features
7. Refactor for clarity
8. Improve documentation

---

## The Memory System (mnemo-server / mem9)

The memory system is what makes this agent meaningfully different from a one-shot AI coding assistant. Memories persist across iterations and across process restarts.

### How Memory Works

- **Storage**: Memories are stored in TiDB with both text content and vector embeddings.
- **Search**: Hybrid search combines keyword matching (full-text index) with vector similarity (cosine distance) to find relevant memories.
- **Embeddings**: Generated by Ollama's `nomic-embed-text` model (768 dimensions). The mnemo-server calls Ollama's embedding endpoint when storing new memories.
- **Types**: Memories can be `pinned` (important, long-lived), `insight` (extracted learnings), or `digest` (summaries).
- **States**: Memories can be `active`, `paused`, `archived`, or `deleted`.
- **Versioning**: Memories track version numbers and `updated_by` fields. Last-write-wins conflict resolution.

### Multi-Tenant Design

The mnemo-server supports multiple tenants (agents or projects), each with their own isolated database. A control-plane database (`mnemos`) stores tenant metadata, while each tenant gets a separate data-plane database. API keys authenticate and route requests to the correct tenant.

### Agent Plugins

The memory system has plugins for three different AI coding tools:

- **OpenClaw Plugin** — TypeScript plugin that registers memory tools (store, search, get, update, delete) directly into OpenClaw's tool system.
- **Claude Code Plugin** — Bash hooks and skill files that integrate mem9 with Claude Code via curl-based REST calls.
- **OpenCode Plugin** — TypeScript plugin with system.transform hooks for automatic memory injection.

---

## Configuration

### Environment Variables

All configuration flows through a `.env` file (copied from `.env.example`). Key variables:

| Variable | Purpose |
|----------|---------|
| `TARGET_REPO` | The GitHub repository to manage (format: `owner/repo`) |
| `GITHUB_TOKEN` | Fine-grained PAT for GitHub API access |
| `OLLAMA_MODEL` | Chat model name (default: `qwen3:32b-nothink`) |
| `OLLAMA_EMBED_MODEL` | Embedding model name (default: `nomic-embed-text`) |
| `LOOP_INTERVAL` | Seconds between agent iterations (default: 60) |
| `ITERATION_TIMEOUT` | Max seconds per iteration (default: 600) |
| `MAX_CONSECUTIVE_FAILURES` | Auto-pause threshold (default: 10) |

### Docker Compose

The `docker-compose.yml` defines all four services with:
- Health checks (TCP for TiDB, HTTP `/healthz` for mnemo-server and OpenClaw)
- Dependency ordering (TiDB starts first, then mnemo-server, then OpenClaw, then orchestrator)
- Localhost-only port bindings (nothing exposed to the network)
- Named volumes for database persistence
- `restart: unless-stopped` for all services

### OpenClaw Configuration

OpenClaw is configured via `config/openclaw/openclaw.json`, which defines:
- Model providers (pointing to Ollama on the host)
- Plugin slots (mem9 as the memory provider)
- Gateway settings (port, auth mode)

### Agent Behavior

The agent's behavior is defined entirely by `workspace/SKILL.md` — a plain-text protocol document that the orchestrator instructs the agent to read and follow each iteration. This makes the agent's behavior transparent and editable without code changes.

---

## Workspace Layout

```
workspace/
├── SKILL.md          # The agent's operating protocol (read every iteration)
├── AGENTS.md         # Development guidelines for the target project
├── repo/             # Git clone of the target repository
├── metrics.json      # Orchestrator metrics (iterations, success rate, uptime)
├── PAUSE             # If this file exists, the agent stops looping
└── orchestrator.log  # Log file from the orchestrator
```

The `PAUSE` file is the human override mechanism. Creating it (`make pause` or `touch workspace/PAUSE`) stops the agent. Removing it (`make resume`) resumes operation. The orchestrator also creates this file automatically after too many consecutive failures.

---

## Build and Operations

### Make Targets

| Command | What It Does |
|---------|-------------|
| `make up` | Start the full Docker Compose stack |
| `make down` | Stop all services |
| `make logs` | Follow logs from all services |
| `make health` | Run health checks against all services |
| `make bootstrap` | First-time setup: clone repo, initialize database, start agent |
| `make pause` | Create PAUSE file to stop the agent |
| `make resume` | Remove PAUSE file to restart the agent |
| `make stats` | Show iteration count and success rate |
| `make init-db` | Initialize TiDB schema (run once after first startup) |
| `make clean` | Full teardown: stop containers, remove volumes, delete cloned repo |
| `make scan-secrets` | Scan for leaked secrets using gitleaks |
| `make install-hooks` | Install git pre-commit hook for secret detection |

### Setup Flow

1. Copy `.env.example` to `.env` and fill in values
2. Start Ollama on the host with the required models
3. `make up` — starts TiDB, mnemo-server, OpenClaw, and orchestrator
4. `make init-db` — creates database schema
5. `make health` — verifies all services are running
6. `make bootstrap` — clones the target repo and kicks off the first iteration

### Scripts

The `scripts/` directory contains operational scripts:
- `bootstrap.sh` — First-time initialization
- `health-check.sh` — Service health verification
- `init-tidb.sql` — Database schema creation
- `clone-target-repo.sh` — Repository cloning
- `test-mem9.py` / `test-openclaw.py` — Smoke tests
- `pre-commit-secrets.sh` — Secret scanning git hook
- `validate-workspace.sh` — Workspace file validation
- `preflight.sh` — Full pre-flight check suite
- `teardown.sh` — Cleanup

---

## Security Measures

- **Localhost-only ports**: All service ports are bound to `127.0.0.1`, preventing network exposure.
- **Token authentication**: The OpenClaw gateway requires a bearer token for API access.
- **API key authentication**: The mnemo-server uses API keys to authenticate and route tenant requests.
- **Secret scanning**: gitleaks integration with custom rules (`.gitleaks.toml`) scans both the working tree and git history.
- **Pre-commit hooks**: A git hook runs secret pattern matching before every commit.
- **Git credential isolation**: GitHub tokens are injected via environment variables and configured through git's `insteadOf` URL rewriting, not stored in files.

---

## Benchmarking

The `mnemo-server/benchmark/` directory contains a benchmarking suite for evaluating memory retrieval quality:

- **LoCoMo** — A framework for testing long-context memory retrieval
- **MR-NIAH** — Multi-retrieval "needle in a haystack" tests
- **Scripts** — `benchmark.sh`, `drive-session.py`, and `report.py` for running and analyzing benchmarks

---

## Key Design Decisions

1. **Locally-hosted models**: Uses Ollama instead of cloud APIs. This means no API costs, no rate limits, and full control over the model. The tradeoff is requiring a machine powerful enough to run a 32B parameter model.

2. **Memory as a service**: The memory system is a separate, API-driven service — not embedded in the agent. This allows multiple agents to share memories, enables independent scaling, and keeps the agent stateless.

3. **Protocol-driven behavior**: The agent's behavior is defined by a plain-text SKILL.md file, not hardcoded logic. Changing the agent's behavior means editing a markdown file, not rewriting code.

4. **Dumb orchestrator, smart agent**: The orchestrator is intentionally minimal — it just triggers the agent and tracks metrics. All intelligence lives in the agent (via SKILL.md and the LLM). This separation makes each component easier to reason about and debug.

5. **Auto-pause on failure**: The system has built-in circuit breakers. After a configurable number of consecutive failures, the orchestrator creates a PAUSE file and stops, preventing runaway failures.

6. **Confidence-gated merging**: The agent rates its own confidence for each change. High-confidence changes are auto-merged. Low-confidence changes are left as open PRs for human review.

---

## Summary

stilltent is a self-contained system for autonomous software development. It combines a locally-hosted LLM (Ollama), persistent vector-backed memory (mnemo-server + TiDB), an agent runtime (OpenClaw), and a loop driver (orchestrator) into a system that can continuously maintain and improve a GitHub repository. The agent follows a structured protocol, learns from its own history, and knows when to stop and ask for help.
