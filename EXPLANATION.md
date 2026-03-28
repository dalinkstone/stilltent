# stilltent — Complete Technical Explanation

## What This Project Is

stilltent is an **autonomous AI agent system** that builds entire software projects from scratch, without human involvement. You give it a project description — a `README.md` in any git repository — and it does everything else. It scaffolds the codebase, writes the code, writes the tests, sets up CI, opens pull requests, reviews them, merges them, and then keeps going. It doesn't stop. It runs for days, making hundreds of commits and hundreds of pull requests, continuously thinking about how to make the project better.

The system is not a one-shot code generator. It is an always-on developer. It wakes up every 60 seconds, looks at the state of the repository, decides what to do next, does it, records what it learned, and goes back to sleep. Then it wakes up again and does it again. It remembers what it tried before, what worked, what failed, and what it learned. It uses that memory to make better decisions in the next iteration.

The core idea is simple: if you can describe a project in a README, the agent can build it. Not all at once — iteratively, one small pull request at a time, the way a careful developer would.

---

## Why This Exists

Most AI coding tools are interactive. You ask them a question, they give you an answer, and then the context is gone. You have to re-explain everything next time. They don't remember what they did yesterday. They don't know what they tried and failed. They don't improve over time.

stilltent is different. It runs autonomously in the background. It has persistent memory. It learns from its own history. It doesn't need you to tell it what to do — it reads the project description, looks at the repository, and figures out what needs to happen next. It makes small, focused changes. It tests everything. It opens pull requests with structured descriptions. When it's confident, it merges them automatically. When it's not confident, it leaves them open for human review.

The goal is that after five days of autonomous operation, the repository should be meaningfully better than when the agent started — more features, better test coverage, cleaner code, better documentation.

---

## The Project Description: Where It All Starts

Everything begins with a `README.md` in the target repository. You set `target.repo` in `stilltent.yml` to point at a GitHub repo (e.g., `your-username/your-project`), and stilltent clones it, reads the README, and starts building.

The project description is intentionally simple. Here's an example:

```markdown
# myapi

A REST API for managing team task boards. Written in Python with FastAPI.

## Goals
- CRUD operations for boards, columns, and tasks
- User authentication with JWT tokens
- WebSocket support for real-time updates
- PostgreSQL storage with async queries

## Non-Goals
- This is not a frontend application
- No mobile app support
- No AI/ML features
```

From this description, the agent will:

1. Scaffold a FastAPI project structure
2. Set up models, routes, and database schemas
3. Implement authentication and JWT handling
4. Build CRUD endpoints for each resource
5. Add WebSocket support
6. Write tests for every endpoint
7. Set up CI and linting
8. And then keep improving it — adding error handling, edge case tests, refactoring, adding features

The prompt builder (`core/prompt_builder.py`) parses the README into structured metadata — title, goals, non-goals, tech stack, architecture — and renders three agent prompt files: `SKILL.md` (iteration protocol), `AGENTS.md` (identity and constraints), and `LEARNING.md` (self-improvement methodology). These are generated from Jinja2 templates in `config/prompts/`, injected with project-specific context.

---

## High-Level Architecture

The system is fully modular. Every axis — agent runtime, memory backend, sandbox provider, deploy target — is a pluggable component configured in `stilltent.yml`. The Makefile is the single entry point; `make bootstrap` takes you from zero to a running agent.

```
                    stilltent.yml + .env
                           │
                    ┌──────▼──────┐
                    │  harness.py │  (bootstrap orchestration)
                    └──────┬──────┘
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
        ┌──────────┐ ┌──────────┐ ┌──────────┐
        │validate.py│ │compose.py│ │ prompt   │
        │(schema)   │ │(fragments│ │ builder  │
        └──────────┘ │  merge)  │ └──────────┘
                     └────┬─────┘
                          ▼
                 docker-compose.yml
                          │
         ┌────────────────┼────────────────┐
         ▼                ▼                ▼
   ┌───────────┐   ┌───────────┐   ┌───────────┐
   │Orchestrator│──►│   Agent   │──►│  Memory   │
   │ (loop.py)  │   │ Runtime   │   │ Backend   │
   └───────────┘   └───────────┘   └───────────┘
         │
         ▼
   ┌───────────┐
   │  Sandbox  │
   │ Provider  │
   └───────────┘
```

### Pluggable Components

| Axis | Options | Configured In |
|------|---------|---------------|
| Agent Runtime | `openclaw`, `nanoclaw`, `nemoclaw`, `claude-code` | `agent.runtime` |
| Memory Backend | `mem9` (self-hosted), `supermemory` (SaaS), `asmr` (parallel agents) | `memory.backend` |
| Sandbox Provider | `daytona` (cloud), `local`, `none` | `sandbox.provider` |
| Deploy Target | `digitalocean`, `vultr`, `railway`, `render`, `heroku`, `local` | `deploy.target` |

The compose generator (`core/compose.py`) reads `stilltent.yml` and merges the appropriate Docker Compose fragments from `deploy/docker-compose/`:

```
base.yml                    (always: network, TiDB, volumes)
+ memory-mem9.yml           (or memory-supermemory.yml)
+ agent-openclaw.yml        (or agent-nanoclaw/nemoclaw/claude-code.yml)
+ orchestrator.yml          (always)
+ oversight-claude-code.yml (optional: when claude_code.enabled + non-claude runtime)
```

After merging, compose.py rewires `depends_on` and environment variables so the orchestrator points at the correct agent service and agents point at the correct memory service. This means each fragment can be written independently — the wiring is automated.

---

## The Core Services — Deep Technical Detail

The default configuration (openclaw + mem9) produces a five-service stack. Other configurations vary in which agent and memory services are present, but the orchestrator and base infrastructure are always the same.

### 1. TiDB (Database)

**What it is:** TiDB is a MySQL-compatible distributed database running as a single-node instance inside Docker, with native vector column types.

**Image:** `pingcap/tidb:v8.4.0` | **Port:** 4000 | **Memory:** 1.5 GB (internal max-memory 1 GB) | **CPU:** 1.0 core

**Why TiDB:** Native `VECTOR(N)` column types let the memory system store embeddings directly in the database — no separate vector store needed. The memories table has `VECTOR(256)` for embeddings and TiDB computes cosine similarity in SQL via `VEC_COSINE_DISTANCE()`.

**Schema:** Two databases created by `scripts/init-tidb.sql`:

1. **`mnemos`** (control plane): `tenants` table (multi-tenant metadata) and `upload_tasks` (file ingestion)
2. **`mnemos_tenant`** (data plane): `memories` table with columns: `id` (UUID), `content` (MEDIUMTEXT), `source`, `tags` (JSON), `metadata` (JSON), `embedding` (VECTOR(256)), `memory_type` (pinned|insight|digest), `agent_id`, `session_id`, `state` (active|paused|archived|deleted), `version`, `updated_by`, `superseded_by`

The init script includes a migration step: `ALTER TABLE memories MODIFY COLUMN embedding VECTOR(256) NULL` followed by clearing old incompatible embeddings.

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

### 4. Agent Runtimes (Pluggable)

The agent runtime is the service that executes LLM-driven tool loops. All runtimes expose the same HTTP interface on port 18789, so the orchestrator doesn't need to know which one is running.

#### OpenClaw (default)

Open-source agent runtime providing LLM routing, tool execution, and memory plugin integration.

**Port:** 18789 | **Memory:** 1.5 GB | **CPU:** 1.5 cores

**Custom Dockerfile adds:** `gh` CLI, `@mem9/openclaw` plugin, git identity (`stilltent-agent <agent@stilltent.local>`)

**Model:** `qwen/qwen3-coder-next` via OpenRouter — 262K context, 65K max output, $0.12/M input, $0.75/M output. Purpose-built for autonomous coding with CLI/IDE integration and failure recovery.

**Compaction:** Safeguard mode with 60K token ceiling — prevents context overflow during long tool-use chains.

**How it works:** Orchestrator sends HTTP POST → OpenClaw creates session → sends to LLM → LLM generates tool calls → OpenClaw executes tools (shell, files, memory, GitHub) → sends results back to LLM → loop until final response → returns to orchestrator.

**Key config (`openclaw.json`):** OpenRouter provider, token auth, mem9 plugin (pointing to the memory service), streaming enabled, native commands auto.

**API key injection:** `sed` substitution at startup replaces placeholder with actual key. GitHub token via git `url.insteadOf`. Neither stored in files.

#### NanoClaw

Lightweight agent runtime that runs Claude agents in isolated Docker containers. Same HTTP interface as OpenClaw. Uses the Anthropic API directly (requires `ANTHROPIC_API_KEY`). Minimal footprint — suitable for simpler projects or when cost control is paramount.

#### NemoClaw

NVIDIA OpenShell-based runtime with GPU-accelerated execution. Wraps OpenClaw with sandboxing policies for secure tool execution. **Requires NVIDIA GPU hardware** — the validator warns about this when selected. Best for projects involving ML training or inference workloads.

#### Claude Code

Anthropic API adapter that exposes the same HTTP interface using the Claude Messages API with `tool_use`. Supports shell, file I/O, git, and memory tools. Can also run as an **oversight sidecar** — when `claude_code.enabled` is true and the primary runtime is something else, Claude Code reviews the primary agent's work every N iterations.

---

### 5. Memory Backends (Pluggable)

#### mem9 (default, self-hosted)

The full mem9 stack: embed-service (C, 256-dim local embeddings) + mnemo-server (Go REST API) + TiDB (MySQL-compatible with native VECTOR columns). Zero external API cost for embeddings. Hybrid keyword + vector search.

```
Agent → mem9 plugin → mnemo-server → embed-service (256-dim) + TiDB (SQL + vector)
```

**Types:** `pinned` (long-lived), `insight` (learnings), `digest` (summaries)
**States:** `active`, `paused`, `archived`, `deleted`
**Search:** Hybrid keyword + vector. mnemo-server calls embed-service for query vector, runs parallel SQL `LIKE` + `VEC_COSINE_DISTANCE`, merges results.
**Consolidation:** Every 50 iterations — summarize, dedupe, update state.

#### Supermemory (SaaS)

A lightweight proxy container (`supermemory-proxy`) that exposes the same REST interface as mnemo-server but forwards requests to the Supermemory SaaS API. Agent code works identically — only the backend changes. Requires `SUPERMEMORY_API_KEY`.

#### ASMR (Parallel Agents)

Advanced multi-agent memory system inspired by the Supermemory ASMR architecture. Uses mem9 as its storage layer but adds parallel observer, searcher, and ensemble agents:

- **Observers** extract 6-vector knowledge from each iteration: architectural decisions, test intelligence, code patterns, temporal state, error patterns, project understanding
- **Searchers** perform multi-perspective queries: direct facts, related context, temporal reconstruction
- **Ensemble** synthesizes results from multiple agents with confidence scoring

Configurable via `memory.asmr_observer_count`, `memory.asmr_searcher_count`, `memory.asmr_ensemble_variants`.

---

### 6. Sandbox Providers (Pluggable)

| Provider | Description |
|----------|-------------|
| `daytona` | Isolated cloud sandboxes via the Daytona SDK. Each iteration runs in a fresh workspace with the project's dependencies pre-installed. Requires `DAYTONA_API_KEY`. |
| `local` | Run directly on the host in `workspace/repo`. No isolation. Default for development. |
| `none` | Disable sandboxing entirely. Tests run in the agent container. |

---

### 7. Orchestrator (Loop Driver)

**What it is:** Python script (~800 lines, stdlib only) that triggers the agent and manages the operational envelope.

**Memory:** 128 MB | **CPU:** 0.25 cores

The orchestrator does NOT make decisions — the agent (via SKILL.md) makes all decisions. The orchestrator only:
1. Checks if the agent should run (no PAUSE, budget OK, circuit breaker closed)
2. Sends trigger prompt to the agent runtime
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

**Cost tracking:** Per-iteration cost using model pricing. Writes to `metrics.json`: total spend, average cost per iteration, projected total, budget remaining.

**Metrics writer:** Background thread flushes metrics to disk every 10 iterations or 60 seconds (whichever comes first). Atomic write (tmp + rename) prevents corruption. Compact JSON (no indentation).

**Success detection:** Only explicit `"result": "success"`, `"partial"`, or `"skipped"` in the agent's JSON summary counts. Missing/unparseable summaries are failures — prevents silent failures from inflating the success rate.

**Other features:** Auto-pause after 25 consecutive failures, scheduled shutdown after 120 hours, exponential backoff on failures (60×2^N, cap 1 hour), HTTP retry with backoff (5s, 10s, 20s), interruptible sleep, graceful SIGTERM/SIGINT handling, health summaries every 50 iterations.

---

## The Bootstrap Pipeline

`make bootstrap` calls `core/harness.py`, which orchestrates a 12-step first-time setup:

| Step | What It Does |
|------|-------------|
| 1 | **Validate** `stilltent.yml` — required fields, valid enums, cross-validation (Daytona needs API key, NemoClaw needs GPU, etc.) |
| 2 | **Generate** `docker-compose.yml` from composable fragments |
| 3 | **Build** all Docker images in parallel |
| 4 | **Start** the stack (all services except orchestrator) |
| 5 | **Wait** for health checks on TiDB, embed-service, mnemo-server, and the agent runtime |
| 6 | **Initialize** the database schema (if using mem9/asmr) |
| 7 | **Clone** the target repository into `workspace/repo` |
| 8 | **Generate** SKILL.md, AGENTS.md, LEARNING.md from the target repo's README |
| 9 | **Set up** Daytona sandbox (if configured) |
| 10 | **Seed** initial memory with the project description |
| 11 | **Run** the first iteration — agent reads SKILL.md and executes the full 7-phase protocol |
| 12 | **Print** summary and monitoring instructions |

After bootstrap, `make start` launches the orchestrator in autonomous mode.

---

## The Compose Fragment System

Docker Compose files are assembled from modular fragments in `deploy/docker-compose/`:

```
deploy/docker-compose/
├── base.yml                  # Network, TiDB, volumes (always included)
├── memory-mem9.yml           # embed-service + mnemo-server
├── memory-supermemory.yml    # supermemory-proxy
├── agent-openclaw.yml        # OpenClaw gateway
├── agent-nanoclaw.yml        # NanoClaw runtime
├── agent-nemoclaw.yml        # NemoClaw (NVIDIA)
├── agent-claude-code.yml     # Claude Code adapter
├── orchestrator.yml          # Loop driver (always included)
└── oversight-claude-code.yml # Optional oversight sidecar
```

`core/compose.py` reads `stilltent.yml`, selects the appropriate fragments, deep-merges them (Docker Compose merge semantics: services merge by name, dicts merge recursively, scalars override), then rewires:

1. **Agent `depends_on`**: Points the agent service at the correct memory service (e.g., `mnemo-server` for mem9, `supermemory-proxy` for supermemory)
2. **Orchestrator `depends_on`**: Points at the correct agent service
3. **`AGENT_URL` / `OPENCLAW_URL`**: Set to `http://<agent-service>:18789`
4. **`AGENT_MEMORY_URL`**: Set to `http://<memory-service>:8082`

This means you can write a new agent fragment (e.g., for a custom runtime) by following the same interface contract — expose port 18789, accept `/v1/chat/completions`, provide `/healthz` — and the system will wire it in automatically.

---

## The Config Validator

`core/validate.py` checks `stilltent.yml` before any work begins:

**Required fields:** All top-level sections must be present (`target`, `agent`, `memory`, `sandbox`, `orchestrator`, `deploy`).

**Enum validation:** `agent.runtime` must be one of `openclaw|nanoclaw|nemoclaw|claude-code`. Same for memory backend, sandbox provider, and deploy target.

**Cross-validation:**
- `sandbox.provider == "daytona"` → `DAYTONA_API_KEY` must be set (in .env or stilltent.yml)
- `memory.backend == "supermemory"` → `SUPERMEMORY_API_KEY` must be set
- `agent.runtime == "nemoclaw"` → warning about GPU hardware requirements
- `claude_code.enabled` or `agent.runtime == "claude-code"` → `ANTHROPIC_API_KEY` must be set
- `orchestrator.budget_limit <= 0` → error
- `orchestrator.loop_interval < 10` → warning about rate limiting

---

## The Agent Protocol — SKILL.md

Deliberately compact — every line serves a purpose, fewer tokens consumed per iteration.

### The 7-Phase Iteration Protocol

**Phase 1: RECALL** — Search memory for: test results, in-progress plans, failed approaches, architectural decisions. First iteration with no memories skips to Phase 2.

**Phase 2: ASSESS** — `git checkout main && git pull`, `git log`, file structure, `gh pr list`, `gh issue list`, `gh run list`, run tests. Priority: fix tests > review PRs > continue plan > issues > test coverage > features > refactor > docs.

**Phase 3: PLAN** — Iteration number, type, summary, files, tests, confidence, risk. Gates: <0.5 = simpler task. >10 files = break down. Protected files = no auto-merge. Store plan in memory.

**Phase 4: IMPLEMENT** — Create `agent/YYYYMMDDHHMMSS-slug` branch. Incremental changes, test after each, max 3 fix attempts, conventional commits. 8-minute budget.

**Phase 5: VALIDATE** — Full test suite + lint + build. All pass or abandon branch.

**Phase 6: SUBMIT** — Push, `gh pr create` with structured body. Merge rules: ≥0.7 + tests + no protected = auto-merge. 0.5-0.7 = merge + log. <0.5 = leave open. Protected = `[HUMAN-REVIEW]`.

**Phase 6b: REVIEW EXTERNAL PRs** — Checkout, test, review diff. Approve/merge, request changes, comment/close, or skip.

**Phase 7: LEARN** — Store: iteration log, repo state (every 5), failed approaches, architectural decisions. **Measure the outcome against your Phase 3 hypothesis** — was it confirmed, refuted, partial, or inconclusive? Update quality metrics. Add items to the improvement queue. Consolidate every 25 iterations, deep review every 50.

### Long-Duration Rules

1. Resume mid-task via `session_state` memory
2. ONE thing per iteration, confidence ≥ 0.6
3. Digest every 10 iterations, consolidate every 25
4. Read targeted ranges, not whole files
5. Pin persistent failures (3+)
6. When idle: work the **improvement queue** first, then edge-case tests, error messages, refactor, CI hardening
7. **Every 5th iteration:** Work one improvement queue item instead of new features
8. **Every 10th iteration:** Self-reflection — evaluate hypotheses, success rate, process
9. **Every 25th iteration:** Knowledge consolidation — synthesize insights, review queue
10. **Every 50th iteration:** Deep review — re-read spec, compare to current state, set priorities

### Memory Format

Compact key-value, not prose. File paths/line refs, not raw code. Tag consistently. 5 well-tagged > 50 unstructured.

---

## The Agent Identity — AGENTS.md

Core principles:
1. Every change through a PR
2. Tests non-negotiable
3. Memory is continuity
4. Smaller is better
5. Pause when uncertain
6. Leave breadcrumbs
7. Never stop building
8. **Learn from every iteration** — every change is a hypothesis tested
9. **Revisit and improve** — at least 20% of iterations should improve past work
10. **Never regress** — track quality metrics, enforce the quality ratchet
11. **Reflect on your process** — self-evaluate every 10 iterations

**Tool usage:** Use tools, never circumvent them. If a tool is broken, fix it. Test suite doesn't catch regressions → write better tests. Memory queries return noise → store better-structured memories.

**Hard limits:** Never delete >30%, modify secrets, push to main, modify SKILL.md/AGENTS.md/LEARNING.md, bypass tests, circumvent tools, destructive commands, install system packages.

---

## The Self-Learning Methodology — LEARNING.md

This is the core addition inspired by Karpathy's autoresearch pattern. It defines HOW the agent learns across iterations:

### The Learning Loop

```
HYPOTHESIZE → IMPLEMENT → MEASURE → EVALUATE → LEARN → REPEAT
```

Every iteration is one pass through this loop. The agent forms a hypothesis before coding (Phase 3), measures the result after (Phase 7), and stores what it learned. Over hundreds of iterations, the agent becomes genuinely better at building this specific project.

### Key Concepts

- **Hypothesis-driven development:** No change without a testable prediction. "Adding input validation will prevent 3 known crash paths" (good) vs. "Make the code better" (rejected).
- **Quality metrics tracking:** Test counts, build health, coverage estimates, code health score. Updated every 5 iterations.
- **Quality ratchet:** Metrics must never regress without justification. If tests drop from 47 to 45, the agent must explain why and plan a fix.
- **Improvement queue:** A running list of things to revisit. After every PR, the agent asks "What could be better?" — and adds items. Every 5th iteration, works one item instead of new features.
- **Self-reflection:** Every 10 iterations, the agent evaluates: Am I solving the right problems? Am I repeating mistakes? Is my success rate improving?
- **Creative escalation:** When stuck for 3+ iterations: reframe → decompose → invert → research → pivot. Never brute-force.
- **Knowledge consolidation:** Every 25 iterations, synthesize insights. Every 50, deep review against the spec.

### The Engineer's Mindset

The core philosophy: a script executes the same logic every time; an engineer adapts. The agent maintains memory, metrics, a queue of improvements, and the ability to reflect on its own process. Iteration 100 should be dramatically better than iteration 1.

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

## Docker Compose — Resource Budget

Tuned for **8 GB RAM / 2 Intel vCPU / 160 GB disk droplet ($48/month)** with the default openclaw + mem9 configuration:

| Service | Memory | CPU | Notes |
|---------|--------|-----|-------|
| TiDB | 2.5 GB | 1.5 | Internal max-memory 2 GB |
| embed-service | 128 MB | 0.5 | Read-only filesystem, 2 threads |
| mnemo-server | 512 MB | 1.0 | Depends on TiDB + embed healthy |
| Agent Runtime | 1.5 GB | 1.5 | OpenClaw, NanoClaw, NemoClaw, or Claude Code |
| Orchestrator | 128 MB | 0.25 | Budget tracking, idle detection |
| **Total** | **~4.8 GB** | — | ~3.2 GB for OS/Docker/buffers/cache |

Startup order: TiDB → embed-service → mnemo-server → Agent → Orchestrator

All services: `restart: unless-stopped`, `on-failure` deploy policy with max 5 attempts, 10s delay, json-file logging with 10m/3 file rotation.

Recommended: 4 GB swap on the droplet (good practice with 8 GB RAM).

---

## Make Targets

The Makefile is the single entry point for all operations.

### Core Workflow

| Command | Purpose |
|---------|---------|
| `make generate` | Generate `docker-compose.yml` from `stilltent.yml` |
| `make build` | Generate compose + build all images |
| `make up` | Generate compose + start stack |
| `make down` | Stop stack |
| `make bootstrap` | Full first-time setup (preflight → validate → build → start → init → clone → prompts → seed → first iteration) |
| `make clean` | Full teardown including volumes |

### Agent Control

| Command | Purpose |
|---------|---------|
| `make pause` / `make resume` | Pause / resume the orchestrator loop |
| `make test-run` | Single test iteration |
| `make start` | Start autonomous mode |

### Monitoring

| Command | Purpose |
|---------|---------|
| `make logs` | Follow all logs |
| `make health` | Service health + LLM API + Daytona API |
| `make stats` | Iteration count, success rate, spend |
| `make cost` | Current spend vs budget, projected total |

### Deployment

| Command | Purpose |
|---------|---------|
| `make deploy` | Deploy based on `deploy.target` in stilltent.yml |
| `make teardown` | Tear down deployment |

### Utilities

| Command | Purpose |
|---------|---------|
| `make preflight` | Check prerequisites (Docker, .env, API keys, ports, conditional Daytona/Supermemory keys) |
| `make validate-config` | Validate `stilltent.yml` schema and cross-references |
| `make rebuild` | Force rebuild all images (no cache) |
| `make reset-metrics` | Clear metrics + unpause |
| `make scan-secrets` | Run gitleaks secret scanner |

---

## Configuration

All configuration lives in two files:

### stilltent.yml (Stack Configuration)

```yaml
target:
  repo: "owner/repo"          # GitHub repo with your README.md
  branch: "main"

agent:
  runtime: "openclaw"          # openclaw | nanoclaw | nemoclaw | claude-code
  model: "qwen/qwen3-coder-next"
  provider: "openrouter"

memory:
  backend: "mem9"              # mem9 | supermemory | asmr

sandbox:
  provider: "local"            # daytona | local | none

orchestrator:
  loop_interval: 60
  budget_limit: 50
  total_runtime_hours: 120

deploy:
  target: "local"              # digitalocean | vultr | railway | render | heroku | local
```

### .env (Secrets)

`GITHUB_TOKEN` (PAT with repo+workflow), `TARGET_REPO` (owner/name), `OPENROUTER_API_KEY`, and runtime-specific keys. See `.env.example` for the full list.

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

- **Network:** Internal Docker bridge. Only necessary ports exposed to host.
- **Auth:** Agent runtime token, memory API key, GitHub PAT via git insteadOf.
- **Agent limits:** No secrets modification, no main push, no test bypass, no destructive commands.
- **Scanning:** gitleaks + pre-commit hooks.
- **Validation:** `make preflight` and `make validate-config` catch misconfigurations before any work begins.

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
| Bad config | `make validate-config` catches issues before bootstrap. `make preflight` checks prerequisites. |

---

## Deployment

**From zero (3 commands):**
```bash
cp .env.example .env          # add your API keys
vim stilltent.yml              # point at your repo
make bootstrap                 # walk away
```

**DigitalOcean:** Ubuntu 24.04, 8GB RAM / 2 Intel vCPU / 160GB disk ($48/mo). No GPU required.
```bash
curl -fsSL https://get.docker.com | sh && git clone <repo> ~/stilltent && cd ~/stilltent
cp .env.example .env && nano .env && vim stilltent.yml && make bootstrap
```

**PaaS (Railway, Render, Heroku):** `make deploy` reads `deploy.target` and dispatches to the appropriate deployment script.

---

## Summary

stilltent takes a project description and builds it — one PR at a time, hundreds of PRs over days, autonomously.

The system is fully modular: four pluggable axes (agent runtime, memory backend, sandbox provider, deploy target) configured in a single YAML file. The default stack is five services: TiDB (database), embed-service (local C embeddings, 256-dim, zero cost), mnemo-server (memory API), OpenClaw (agent runtime), orchestrator (budget tracking, circuit breaker, idle detection). Swap OpenClaw for NanoClaw, NemoClaw, or Claude Code. Swap mem9 for Supermemory or ASMR. Add Daytona sandboxing. Deploy to DigitalOcean, Vultr, or any PaaS.

The bootstrap pipeline validates configuration, builds containers, starts the stack, clones the target repo, generates agent prompts from the README, seeds memory, and runs the first iteration — all in one command.

Everything is designed for one purpose: take a paragraph of project description and turn it into a working codebase with hundreds of tested, reviewed commits.
