# stilltent — Complete Technical Explanation

## What This Project Is

stilltent is an **autonomous AI agent system** that builds entire software projects from scratch, without human involvement. You give it a project description — a simple markdown file in the `project/` directory — and it does everything else. It scaffolds the codebase, writes the code, writes the tests, sets up CI, opens pull requests, reviews them, merges them, and then keeps going. It doesn't stop. It runs for days, making hundreds of commits and hundreds of pull requests, continuously thinking about how to make the project better.

The system is not a one-shot code generator. It is an always-on developer. It wakes up every 60 seconds, looks at the state of the repository, decides what to do next, does it, records what it learned, and goes back to sleep. Then it wakes up again and does it again. It remembers what it tried before, what worked, what failed, and what it learned. It uses that memory to make better decisions in the next iteration.

The core idea is simple: if you can describe a project in a README, the agent can build it. Not all at once — iteratively, one small pull request at a time, the way a careful developer would.

---

## Why This Exists

Most AI coding tools are interactive. You ask them a question, they give you an answer, and then the context is gone. You have to re-explain everything next time. They don't remember what they did yesterday. They don't know what they tried and failed. They don't improve over time.

stilltent is different. It runs autonomously in the background. It has persistent memory. It learns from its own history. It doesn't need you to tell it what to do — it reads the project description, looks at the repository, and figures out what needs to happen next. It makes small, focused changes. It tests everything. It opens pull requests with structured descriptions. When it's confident, it merges them automatically. When it's not confident, it leaves them open for human review.

The goal is that after five days of autonomous operation, the repository should be meaningfully better than when the agent started — more features, better test coverage, cleaner code, better documentation.

---

## The Project Description: Where It All Starts

Everything begins with a file: `project/README.md`. This is the project specification. It tells the agent what to build.

The project description is intentionally simple. Here's what the current one looks like:

```markdown
# mytool

A command-line tool for managing local development environments. Written in Go.

## Goals
- Create, start, stop, and destroy local dev environments
- Support Docker-based environments with custom configurations
- Provide a simple YAML-based configuration format
- Include comprehensive test coverage

## Non-Goals
- This is not a cloud deployment tool
- No GUI — CLI only
- No support for Kubernetes (just Docker)
```

That's it. From this description, the agent will:

1. Initialize a Go module
2. Create a project structure with `cmd/`, `internal/`, `pkg/` directories
3. Set up a Makefile for building and testing
4. Create a CLI entry point
5. Implement the YAML configuration parser
6. Build Docker integration for environment management
7. Write unit tests for every component
8. Set up GitHub Actions CI
9. Add linting, formatting, and build verification
10. Write documentation
11. And then keep improving it — adding error handling, edge case tests, refactoring, adding features

The agent reads this file during its first iteration bootstrap phase. Every subsequent iteration, it refers back to the project's direction when deciding what to work on next.

---

## High-Level Architecture

The system is composed of five services running inside Docker Compose, with LLM inference routed to OpenRouter:

```
┌──────────────────────────────────────────────────────────────┐
│                        Host Machine                          │
│                                                              │
│  ┌─────────────────────────────────────────────────────────┐ │
│  │              Docker Compose (stilltent-net)             │ │
│  │                                                         │ │
│  │  ┌───────────┐    triggers     ┌───────────────────┐   │ │
│  │  │Orchestrator├───────────────►│   OpenClaw Gateway │   │ │
│  │  │ (loop.py)  │    every 60s   │  (Agent Runtime)   │   │ │
│  │  └───────────┘                 └─────┬──────┬──────┘   │ │
│  │                                      │      │          │ │
│  │                          memory ops  │      │ LLM API  │ │
│  │                                      │      │          │ │
│  │  ┌───────────┐                 ┌─────▼──┐   │          │ │
│  │  │   TiDB    │◄────────────────┤ mnemo  │   │          │ │
│  │  │ (v8.4.0)  │   SQL queries   │ server │   │          │ │
│  │  └───────────┘                 └───┬────┘   │          │ │
│  │                                    │        │          │ │
│  │                        embeddings  │        │          │ │
│  │                                    │        │          │ │
│  │                              ┌─────▼─────┐  │          │ │
│  │                              │  embed-   │  │          │ │
│  │                              │  service  │  │          │ │
│  │                              │  (C, 256d)│  │          │ │
│  │                              └───────────┘  │          │ │
│  │                                              │          │ │
│  └──────────────────────────────────────────────┼──────────┘ │
│                                                  │            │
│                                    HTTPS to OpenRouter.ai     │
│                                    (Qwen3 Coder Next)         │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

Every component has a specific, minimal responsibility:

- **TiDB** stores data (memories, tenant metadata, upload tasks)
- **embed-service** generates embedding vectors from text (local, zero API cost)
- **mnemo-server** provides a REST API for memory operations
- **OpenClaw** runs the agent (LLM calls, tool execution, memory plugin)
- **Orchestrator** triggers the agent on a schedule, tracks cost, enforces budget

---

## The Five Services — Deep Technical Detail

### 1. TiDB (Database)

**What it is:** TiDB is a MySQL-compatible distributed database running as a single-node instance inside Docker, with native vector column types.

**Image:** `pingcap/tidb:v8.4.0` | **Port:** 4000 | **Memory:** 1.5 GB (internal max-memory 1 GB) | **CPU:** 1.0 core

**Why TiDB:** Native `VECTOR(N)` column types let the memory system store embeddings directly in the database — no separate vector store needed. The memories table has `VECTOR(256)` for embeddings and TiDB computes cosine similarity in SQL via `VEC_COSINE_DISTANCE()`.

**Schema:** Two databases created by `scripts/init-tidb.sql`:

1. **`mnemos`** (control plane): `tenants` table (multi-tenant metadata) and `upload_tasks` (file ingestion)
2. **`mnemos_tenant`** (data plane): `memories` table with columns: `id` (UUID), `content` (MEDIUMTEXT), `source`, `tags` (JSON), `metadata` (JSON), `embedding` (VECTOR(256)), `memory_type` (pinned|insight|digest), `agent_id`, `session_id`, `state` (active|paused|archived|deleted), `version`, `updated_by`, `superseded_by`

The init script includes a migration step: `ALTER TABLE memories MODIFY COLUMN embedding VECTOR(256) NULL` followed by clearing old incompatible embeddings. The Makefile's `init-db` target includes a readiness loop (30 retries × 2s) before running the SQL.

**Health check:** HTTP GET `http://127.0.0.1:10080/status` every 60s, 30s start period.

---

### 2. embed-service (Local Embedding Server)

**What it is:** A standalone HTTP server in pure C implementing an OpenAI-compatible embeddings API. Produces 256-dimensional L2-normalized float32 vectors via multi-channel feature hashing. Zero external dependencies.

**Port:** 8090 | **Memory:** 64 MB | **CPU:** 0.5 cores | **Threads:** 2 | **Filesystem:** read-only

**Why custom C instead of an API?** Eliminates embedding cost ($0.02/M tokens saved), eliminates network latency, produces deterministic output (same input = same vector), and is code-aware (unlike generic text embedding models).

**The five-channel embedding algorithm:**

| Channel | Dims | What It Captures |
|---------|------|------------------|
| 1: Trigrams | 0–63 | Character-level patterns (typos, morphology) via FNV-1a hashing with TF normalization |
| 2: Words | 64–127 | Word-level semantics with IDF approximation: `1 + log(1 + word_length)` |
| 3: Code | 128–191 | 50+ programming keywords, function calls, operators, camelCase/snake_case splitting, indentation depth, structural statistics |
| 4: Bigrams | 192–223 | Adjacent word pairs for local context coherence |
| 5: Global | 224–255 | Text length, vocabulary richness, punctuation density, code token density, uppercase ratio, digit ratio |

Channel 3 is what makes this model suited for a coding agent. It recognizes `function` as a keyword, splits `parseConfig` into `parse` + `Config`, and weights `()` as a function call indicator.

**Tokenization:** Whitespace/punctuation split + camelCase/snake_case sub-tokenization. Multi-character operators preserved. Max 4096 tokens, 256 chars each.

**API:** `POST /v1/embeddings` (OpenAI-compatible), `POST /embeddings`, `GET /health`.

**Docker:** Multi-stage Alpine build with static linking. Tiny container, millisecond startup.

---

### 3. mnemo-server (Memory API)

**What it is:** Go REST API providing persistent memory as a service.

**Language:** Go 1.23 (chi router) | **Port:** 8082 | **Memory:** 256 MB | **CPU:** 1.0 core

**Depends on:** TiDB (healthy) + embed-service (healthy)

**Endpoints:** `POST /v1alpha2/mem9s/memories` (store), `GET /v1alpha2/mem9s/memories` (search), `GET /:id` (get), `PUT /:id` (update), `DELETE /:id` (soft-delete), `POST /v1alpha1/mem9s` (provision tenant).

**Hybrid search:** Keyword matching (SQL `LIKE`) + vector similarity (embed-service → `VEC_COSINE_DISTANCE()`) merged and ranked.

**Architecture:** `handler → service → repository` strict layering. Multi-tenant isolation, rate limiting, Prometheus metrics.

**Health check:** `wget http://127.0.0.1:8082/health` every 15s, 15s start period.

---

### 4. OpenClaw Gateway (Agent Runtime)

**What it is:** Open-source agent runtime providing LLM routing, tool execution, and memory plugin integration.

**Port:** 18789 | **Memory:** 768 MB | **CPU:** 1.5 cores

**Custom Dockerfile adds:** `gh` CLI, `@mem9/openclaw` plugin, git identity (`stilltent-agent <agent@stilltent.local>`)

**Model:** `qwen/qwen3-coder-next` via OpenRouter — 262K context, 65K max output, $0.12/M input, $0.75/M output. Purpose-built for autonomous coding with CLI/IDE integration and failure recovery.

**Compaction:** Safeguard mode with 60K token ceiling — prevents context overflow during long tool-use chains.

**How it works:** Orchestrator sends HTTP POST → OpenClaw creates session → sends to LLM → LLM generates tool calls → OpenClaw executes tools (shell, files, memory, GitHub) → sends results back to LLM → loop until final response → returns to orchestrator.

**Key config (`openclaw.json`):** OpenRouter provider, token auth, mem9 plugin (pointing to `http://mnemo-server:8082`), streaming enabled, native commands auto.

**API key injection:** `sed` substitution at startup replaces placeholder with actual key. GitHub token via git `url.insteadOf`. Neither stored in files.

**Depends on:** mnemo-server (healthy) + TiDB (healthy). Health check: HTTP fetch `/healthz` every 60s.

---

### 5. Orchestrator (Loop Driver)

**What it is:** Python script (~800 lines, stdlib only) that triggers the agent and manages the operational envelope.

**Memory:** 128 MB | **CPU:** 0.25 cores

The orchestrator does NOT make decisions — the agent (via SKILL.md) makes all decisions. The orchestrator only:
1. Checks if the agent should run (no PAUSE, budget OK, circuit breaker closed)
2. Sends trigger prompt to OpenClaw
3. Waits for response (600s timeout)
4. Evaluates success/failure/idle
5. Tracks cost and writes metrics
6. Sleeps and repeats

**Circuit breaker:** Protects against budget drain when the gateway is down.
- CLOSED → OPEN after 5 consecutive gateway failures
- OPEN: Calls skipped for cooldown (starts at 5 minutes, doubles on each failed probe, caps at 1 hour)
- HALF_OPEN: One probe call allowed. Success → CLOSED. Failure → OPEN with doubled cooldown.

**Idle detection:** Biggest source of token waste is iterations where agent finds no work and returns "skipped". Detection uses layered signals:
- Structured: JSON result field = "skipped"
- Work indicators: PR numbers, `git push`, `gh pr create`, branch names → NOT idle
- Idle phrases: "no issues", "no work", "nothing to do", "all tests pass"
Backoff: 60s base, doubles each idle iteration, caps at 15 minutes. Exits immediately when work appears. Saves 30-40% of tokens during idle periods.

**Budget guard:** Projects total spend over `TOTAL_RUNTIME_HOURS` based on actual token consumption rate. If projected spend exceeds `BUDGET_LIMIT` ($50), creates PAUSE file. Also checks if current spend already exceeds limit. Needs at least 6 minutes of data before projecting.

**Cost tracking:** Per-iteration cost using Qwen3 Coder Next pricing ($0.12/M input, $0.75/M output). Writes to `metrics.json`: total spend, average cost per iteration, projected total, budget remaining.

**Metrics writer:** Background thread flushes metrics to disk every 10 iterations or 60 seconds (whichever comes first). Atomic write (tmp + rename) prevents corruption. Compact JSON (no indentation).

**Success detection:** Only explicit `"result": "success"`, `"partial"`, or `"skipped"` in the agent's JSON summary counts. Missing/unparseable summaries are failures — prevents silent failures from inflating the success rate.

**Other features:** Auto-pause after 25 consecutive failures, scheduled shutdown after 120 hours, exponential backoff on failures (60×2^N, cap 1 hour), HTTP retry with backoff (5s, 10s, 20s), interruptible sleep, graceful SIGTERM/SIGINT handling, health summaries every 50 iterations.

---

## The Agent Protocol — SKILL.md

106 lines. Deliberately compact — every line serves a purpose, fewer tokens consumed per iteration.

### The 7-Phase Iteration Protocol

**Phase 1: RECALL** — Search memory for: test results, in-progress plans, failed approaches, architectural decisions. First iteration with no memories skips to Phase 2.

**Phase 2: ASSESS** — `git checkout main && git pull`, `git log`, file structure, `gh pr list`, `gh issue list`, `gh run list`, run tests. Priority: fix tests > review PRs > continue plan > issues > test coverage > features > refactor > docs.

**Phase 3: PLAN** — Iteration number, type, summary, files, tests, confidence, risk. Gates: <0.5 = simpler task. >10 files = break down. Protected files = no auto-merge. Store plan in memory.

**Phase 4: IMPLEMENT** — Create `agent/YYYYMMDDHHMMSS-slug` branch. Incremental changes, test after each, max 3 fix attempts, conventional commits. 8-minute budget.

**Phase 5: VALIDATE** — Full test suite + lint + build. All pass or abandon branch.

**Phase 6: SUBMIT** — Push, `gh pr create` with structured body. Merge rules: ≥0.7 + tests + no protected = auto-merge. 0.5-0.7 = merge + log. <0.5 = leave open. Protected = `[HUMAN-REVIEW]`.

**Phase 6b: REVIEW EXTERNAL PRs** — Checkout, test, review diff. Approve/merge, request changes, comment/close, or skip.

**Phase 7: LEARN** — Store: iteration log, repo state (every 5), failed approaches, architectural decisions. Consolidate every 50 iterations.

### Long-Duration Rules

1. Resume mid-task via `session_state` memory
2. ONE thing per iteration, confidence ≥ 0.6
3. Digest every 10 iterations, consolidate every 25
4. Read targeted ranges, not whole files
5. Pin persistent failures (3+)
6. When idle: edge-case tests, error messages, refactor, CI hardening

### Memory Format

Compact key-value, not prose. File paths/line refs, not raw code. Tag consistently. 5 well-tagged > 50 unstructured.

---

## The Agent Identity — AGENTS.md

72 lines. Core principles:
1. Every change through a PR
2. Tests non-negotiable
3. Memory is continuity
4. Smaller is better
5. Pause when uncertain
6. Leave breadcrumbs
7. Never stop building

**Tool usage:** Use tools, never circumvent them. If a tool is broken, fix it. Test suite doesn't catch regressions → write better tests. Memory queries return noise → store better-structured memories.

**Hard limits:** Never delete >30%, modify secrets, push to main, modify SKILL.md/AGENTS.md, bypass tests, circumvent tools, destructive commands, install system packages.

---

## The Agent's Tools

| Category | Tools | Purpose |
|----------|-------|---------|
| Shell | git, test runners, linters, builds, `gh`, `find`/`grep`/`head`/`tail` | Commands, filesystem, GitHub |
| Files | Read/write | Source code, configuration |
| Memory | `memory_store`, `memory_search`, `memory_get`, `memory_update`, `memory_delete` | Persistent context |
| GitHub | `gh pr create/merge/list/review/diff/checkout`, `gh issue list`, `gh run list` | PR workflow, CI |

Critical principle: **tools are capabilities, not obstacles.** Use them. Fix them if broken. Never work around them.

---

## The Memory System

```
Agent → mem9 plugin → mnemo-server → embed-service (256-dim) + TiDB (SQL + vector)
```

**Types:** `pinned` (long-lived), `insight` (learnings), `digest` (summaries)
**States:** `active`, `paused`, `archived`, `deleted`
**Search:** Hybrid keyword + vector. mnemo-server calls embed-service for query vector, runs parallel SQL `LIKE` + `VEC_COSINE_DISTANCE`, merges results.
**Consolidation:** Every 50 iterations — summarize, dedupe, update state.

---

## Docker Compose — Resource Budget

Tuned for **8 GB RAM / 2 Intel vCPU / 160 GB disk droplet ($48/month)**:

| Service | Memory | CPU | Notes |
|---------|--------|-----|-------|
| TiDB | 2.5 GB | 1.5 | Internal max-memory 2 GB |
| embed-service | 128 MB | 0.5 | Read-only filesystem, 2 threads |
| mnemo-server | 512 MB | 1.0 | Depends on TiDB + embed healthy |
| OpenClaw | 1.5 GB | 1.5 | Agent runtime |
| Orchestrator | 128 MB | 0.25 | Budget tracking, idle detection |
| **Total** | **~4.8 GB** | — | ~3.2 GB for OS/Docker/buffers/cache |

Startup order: TiDB → embed-service → mnemo-server → OpenClaw → Orchestrator

All services: `restart: unless-stopped`, `on-failure` deploy policy with max 5 attempts, 10s delay, json-file logging with 10m/3 file rotation.

Recommended: 4 GB swap on the droplet (good practice with 8 GB RAM).

---

## Make Targets

| Command | Purpose |
|---------|---------|
| `make up` / `make down` | Start / stop stack |
| `make logs` / `make logs-follow` | Follow all logs / with 50-line tail |
| `make health` | Per-service health + OpenRouter API |
| `make preflight` | Pre-flight checks (Docker, .env, keys, ports) |
| `make bootstrap` | First-time setup |
| `make init-db` | Wait for TiDB + create schema |
| `make pause` / `make resume` | Agent control |
| `make stats` | Iteration statistics |
| `make cost` | Spend, projected, budget remaining |
| `make rebuild` | Force rebuild all images |
| `make build-all` | Build all images in parallel |
| `make reset-metrics` | Clear metrics + unpause |
| `make clean` | Full teardown |
| `make deploy` | DigitalOcean instructions |
| `make scan-secrets` / `make install-hooks` | Security |

---

## Configuration

### GitHub
`GITHUB_TOKEN` (PAT with repo+workflow), `TARGET_REPO` (owner/name)

### LLM
`OPENROUTER_API_KEY`, `OPENROUTER_MODEL` (default: `qwen/qwen3-coder-next`)

### Embedding
`EMBEDDING_MODEL` (`local-embed`), `EMBEDDING_PROVIDER` (`ollama`), `EMBEDDING_DIM` (`256`)

### TiDB
`TIDB_HOST` (`tidb`), `TIDB_PORT` (`4000`), `TIDB_USER` (`root`), `TIDB_PASSWORD` (empty), `TIDB_DATABASE` (`mnemos`)

### mnemo-server
`MEM9_API_PORT` (`8082`), `MEM9_API_KEY` (`stilltent-local-dev-key`)

### Orchestrator
`LOOP_INTERVAL` (`60`), `COOLDOWN_SECONDS` (`30`), `ITERATION_TIMEOUT` (`600`), `MAX_CONSECUTIVE_FAILURES` (`25`), `TOTAL_RUNTIME_HOURS` (`120`), `BUDGET_LIMIT` (`50`), `DAILY_BUDGET_LIMIT` (`15`)

---

## Cost

| Cost Type | Rate |
|-----------|------|
| Input tokens (Qwen3 Coder Next) | $0.12 per million |
| Output tokens | $0.75 per million |
| Embeddings | **$0** (local) |

Budget: $50 for 120 hours. Budget guard projects spend and auto-pauses. Idle detection saves 30-40% during quiet periods. `make cost` for real-time monitoring.

---

## Security

- **Network:** Internal Docker bridge. Only TiDB:4000 exposed to host.
- **Auth:** OpenClaw token, mnemo-server API key, GitHub PAT via git insteadOf.
- **Agent limits:** No secrets modification, no main push, no test bypass, no destructive commands.
- **Scanning:** gitleaks + pre-commit hooks.

---

## Thinking About This Differently

Everything above is machinery. The real point: **describe a project in a paragraph, walk away, come back to a working codebase.**

The agent is a junior developer who never sleeps, never gets bored, and never forgets. Not as smart as a senior developer, but infinitely patient. Try, fail, learn, try differently, eventually get it right. Hundreds of times.

---

## Three Nested Feedback Loops

**Loop 1: Iteration (minutes)** — Assess, plan, implement, validate, submit, learn. The heartbeat.

**Loop 2: Learning (hours)** — Digest every 10 iterations, consolidate every 25. Understanding deepens. Early iterations explore, later iterations target known weak spots.

**Loop 3: Self-improvement (days)** — Accumulated experience. Stop retrying failures. Focus on high-confidence areas. Fix tools that hold you back. The agent improves its own infrastructure.

Memory connects all three. Without it, every iteration starts from scratch. With it, the agent gets better over time.

---

## The "Fix the Tool" Principle

This is the most important behavioral rule. When a tool produces bad results, there are two responses:

**Bad:** Stop using the tool. Work without context. Repeat mistakes.
**Good:** Examine why. Store better memories. Write better tests. Improve the CI workflow.

Over 100 iterations:
- Iterations 1-10: Scaffold, initial tests
- Iterations 11-30: Implement features
- Iterations 31-50: Tests catch regressions. Agent fixes code AND improves test suite.
- Iterations 51-100: Memory accumulates. Agent knows which approaches work, which files are fragile, which tests are reliable. Success rate climbs because the toolchain improved.

The agent doesn't just use tools — it maintains them. Better tools → better output → more tool improvements → compounding returns.

---

## From the Repository's Perspective

The target repo receives well-formatted PRs from "stilltent-agent". Each has a clear title, structured description, test results, confidence score. The commit history looks like any well-maintained project:

```
feat: add YAML configuration parser for environment definitions
test: add unit tests for config parser edge cases
fix: handle missing file extension in config path
refactor: extract Docker client initialization into separate function
docs: add usage examples to README
```

Small, focused, tested. Branch names: `agent/YYYYMMDDHHMMSS-slug`. Conventional commits. A human reviewing the repo can understand the work without knowing it was AI-generated.

---

## What Could Go Wrong

| Problem | Mitigation |
|---------|-----------|
| Bad code | Confidence-gated merging, PR workflow, human review |
| Stuck in loop | Emergency procedures in SKILL.md, 25-failure auto-pause, circuit breaker |
| Runaway API bill | Budget guard ($50 limit), idle detection (30-40% savings), `make cost` |
| Broken tests | Never pushes to main. Abandons failing branches. Fixing broken main is highest priority. |
| Sensitive files | AGENTS.md hard limits, protected files list, pre-commit hooks |
| Gateway down | Circuit breaker (5-failure threshold → 5min cooldown → doubles to 1hr cap) |

---

## Deployment

**Local:**
```bash
cp .env.example .env && make preflight && make up && make init-db && make bootstrap
```

**DigitalOcean:** Ubuntu 24.04, 8GB RAM / 2 Intel vCPU / 160GB disk ($48/mo). No GPU required.
```bash
curl -fsSL https://get.docker.com | sh && git clone <repo> ~/stilltent && cd ~/stilltent
cp .env.example .env && nano .env && make bootstrap
```

---

## Summary

stilltent takes a project description and builds it — one PR at a time, hundreds of PRs over days, autonomously. Five services: TiDB (database), embed-service (local C embeddings, 256-dim, zero cost), mnemo-server (memory API), OpenClaw (agent runtime with Qwen3 Coder Next, 262K context, $0.12/$0.75 per M tokens), orchestrator (budget tracking, circuit breaker, idle detection).

The agent protocol is 106 lines of SKILL.md. The identity is 72 lines of AGENTS.md. The stack fits on a $48/month droplet (8 GB RAM, 2 Intel vCPU, 160 GB disk) with a $50 budget for 5 days. The agent uses its tools — memory, testing, GitHub CLI, shell — and fixes them when they're insufficient.

Everything is designed for one purpose: take a paragraph of project description and turn it into a working codebase with hundreds of tested, reviewed commits.
