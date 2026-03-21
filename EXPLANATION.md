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
4. Create a CLI entry point using a Go flag/command library
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

The system is composed of five services running inside Docker Compose, with LLM inference routed to OpenRouter. Here's how they fit together:

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
│                                    (Qwen3 Coder 30B)          │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

Every component has a specific, minimal responsibility:

- **TiDB** stores data (memories, tenant metadata, upload tasks)
- **embed-service** generates embedding vectors from text (local, zero API cost)
- **mnemo-server** provides a REST API for memory operations
- **OpenClaw** runs the agent (LLM calls, tool execution, memory plugin)
- **Orchestrator** triggers the agent on a schedule and tracks metrics

The components are loosely coupled. The orchestrator doesn't know what the agent does — it just sends a prompt and waits. The agent doesn't know about the orchestrator — it just follows its protocol. The memory server doesn't know it's serving an autonomous agent — it just stores and searches memories.

---

## The Five Services — Deep Technical Detail

### 1. TiDB (Database)

**What it is:** TiDB is a MySQL-compatible distributed database. In this project, it runs as a single-node instance inside Docker. It's essentially MySQL with one important addition: native vector column types.

**Image:** `pingcap/tidb:v8.4.0`
**Container name:** `stilltent-tidb`
**Port:** 4000 (MySQL protocol)
**Storage:** Docker named volume `tidb-data` (persistent across container restarts)

**Why TiDB instead of plain MySQL:** TiDB supports `VECTOR(N)` column types natively. This means the memory system can store vector embeddings directly in the database without needing a separate vector store (like Pinecone, Weaviate, or pgvector). The memories table has a `VECTOR(256)` column for embeddings (matching the local embed-service's 256-dimensional output), and TiDB can compute cosine similarity directly in SQL queries. This simplifies the architecture significantly — one database handles both relational queries (finding memories by type, state, agent ID) and vector similarity search (finding semantically related memories).

**Schema:** Two databases are created during initialization:

1. **`mnemos`** (control plane): Contains the `tenants` table (multi-tenant metadata) and `upload_tasks` table (file ingestion tracking). This is where the system tracks which agents/projects exist and how to route their data.

2. **`mnemos_tenant`** (data plane): Contains the `memories` table. This is where the actual memory content lives. Each memory has:
   - `id` — UUID primary key
   - `content` — The text content of the memory (MEDIUMTEXT, up to 16MB)
   - `source` — Where the memory came from (e.g., "iteration_log", "architectural_decision")
   - `tags` — JSON array of categorical tags
   - `metadata` — JSON object for arbitrary key-value pairs
   - `embedding` — VECTOR(256) for semantic search (matching embed-service output)
   - `memory_type` — One of: `pinned` (important, long-lived), `insight` (extracted learnings), `digest` (summaries)
   - `agent_id` — Which agent created this memory
   - `session_id` — Which session this memory originated from
   - `state` — One of: `active`, `paused`, `archived`, `deleted`
   - `version` — Integer version counter for conflict resolution
   - `updated_by` — Who last modified this memory
   - `superseded_by` — If this memory was replaced, the ID of the replacement

**Health check:** HTTP GET to TiDB's status endpoint (`http://127.0.0.1:10080/status`) every 10 seconds. The other services wait for TiDB to be healthy before starting.

**Initialization:** The schema is created by running `scripts/init-tidb.sql` via `make init-db`. This creates both databases, all tables, all indexes, and seeds a default tenant for local development with the API key `stilltent-local-dev-key`.

---

### 2. embed-service (Local Embedding Server)

**What it is:** A standalone HTTP server written in pure C that implements an OpenAI-compatible embeddings API. It produces 256-dimensional L2-normalized float32 vectors using a multi-channel feature hashing algorithm. It has zero external dependencies beyond libc and POSIX, making it extremely fast, portable, and free to operate.

**Language:** C11 (POSIX)
**Container name:** `stilltent-embed`
**Port:** 8090
**Dependencies:** libc, libm, pthreads (all standard — no third-party libraries)
**Output:** 256-dimensional float32 vectors, L2-normalized

**Why a custom C embedding service instead of an API?** The previous architecture used OpenRouter's `text-embedding-3-small` model for embeddings, which cost $0.02 per million tokens and added network latency to every memory operation. The embed-service eliminates both problems: embeddings are generated locally with zero API cost and sub-millisecond latency. It's also deterministic — the same input always produces the same vector — which makes debugging and testing easier.

**The five-channel embedding algorithm:**

The embedding algorithm splits the 256 dimensions into five channels, each capturing a different aspect of the text:

| Channel | Dimensions | What It Captures |
|---------|-----------|------------------|
| 1: Trigrams | 0–63 | Character-level patterns via overlapping 3-character substrings. Captures linguistic texture — typos, morphology, character-level similarity. Uses FNV-1a hashing with TF normalization. |
| 2: Words | 64–127 | Word-level semantics with IDF approximation. Longer words (domain-specific terms) get higher weight than short common words (a, the, is). Weight formula: `1 + log(1 + word_length)`. |
| 3: Code | 128–191 | Code-aware features. Detects 50+ programming keywords (function, def, class, return, if), function calls (words followed by `(`), operators, brackets, camelCase/snake_case splitting, indentation depth. The last 8 dimensions encode structural statistics. |
| 4: Bigrams | 192–223 | Adjacent word pairs for local context coherence. "parsing error" and "error parsing" hash to different dimensions, preserving word order in a bag-of-bigrams model. |
| 5: Global | 224–255 | Statistical features: text length, vocabulary richness, average word length, punctuation density, code token density, uppercase ratio, digit ratio, newline count, space ratio, and character-pair texture. |

**Why five channels?** Each channel answers a different question:
- Channel 1: "Does this text look similar at the character level?" (catches typos, morphological variants)
- Channel 2: "Does this text contain similar words?" (standard semantic similarity)
- Channel 3: "Is this code, and if so, what kind?" (critical for a coding agent's memories)
- Channel 4: "Do words appear in similar sequences?" (local context, phrase matching)
- Channel 5: "What kind of text is this?" (code vs prose, short vs long, dense vs sparse)

The code-awareness (Channel 3) is what makes this embedding model particularly suited for stilltent. The agent stores memories about code — test results, architectural decisions, failed approaches involving specific functions. A generic text embedding model treats `function parseConfig()` the same as any three English words. The embed-service recognizes `function` as a keyword, `parseConfig` as camelCase (splitting into `parse` and `Config`), and `()` as a function call indicator, and weights them accordingly.

**Tokenization:** The tokenizer splits on whitespace and punctuation, then sub-tokenizes camelCase and snake_case identifiers. For example, `parseConfigFile` becomes `parse`, `Config`, `File`. Multi-character operators (`==`, `!=`, `->`, `=>`, `||`, `&&`) are preserved as single tokens. Maximum 4096 tokens per document, 256 characters per token.

**Hash function:** FNV-1a (64-bit) with different seeds for each channel. The seed isolates channels — the same word hashes to different dimensions in Channel 2 vs Channel 4. The sign bit of the hash is used as a random projection (adding or subtracting the weight), which is equivalent to a simplified version of the random projection method used in locality-sensitive hashing.

**L2 normalization:** After all five channels are computed, the entire 256-dimensional vector is L2-normalized (divided by its Euclidean length). This ensures all vectors lie on the unit hypersphere, making cosine distance equivalent to L2 distance. TiDB's `VEC_COSINE_DISTANCE()` function works directly on these vectors.

**HTTP server architecture:**
- 8-thread worker pool with a mutex+condvar work queue
- POSIX sockets with TCP_NODELAY for low latency
- 10-second socket timeouts to prevent worker starvation
- Graceful shutdown on SIGINT/SIGTERM
- CORS headers for browser compatibility
- Maximum request body: 128KB, maximum input text: 64KB

**API endpoints:**

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/v1/embeddings` | POST | OpenAI-compatible embedding generation |
| `/embeddings` | POST | Same endpoint without `/v1` prefix |
| `/health` | GET | Health check (returns `{"status": "ok"}`) |

**Docker build:** Multi-stage Alpine build. Stage 1 compiles with static linking (`-static`) for portability. Stage 2 copies only the binary into a minimal Alpine image. The resulting container is tiny (a few MB) and starts in milliseconds.

**Health check:** `wget -qO- http://127.0.0.1:8090/health` every 10 seconds.

---

### 3. mnemo-server (Memory API)

**What it is:** A Go REST API that provides persistent memory as a service. It's the brain's filing cabinet. The agent stores things it learns here, and retrieves them when it needs context about what happened before.

**Language:** Go 1.23
**Framework:** chi (HTTP router), standard library for everything else
**Container name:** `stilltent-mnemo`
**Port:** 8082 (internal only — not exposed to the host)

**Why a separate memory service?** The memory system is not embedded in the agent. It's a standalone API. This is a deliberate architectural choice. It means:
- Multiple agents can share the same memory store
- The memory service can be tested, monitored, and scaled independently
- The agent remains stateless — all state lives in the memory API
- Memory operations have a clean, versioned API contract

**Core API endpoints:**

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/v1alpha1/mem9s` | Provision a new tenant |
| POST | `/v1alpha2/mem9s/memories` | Store a new memory |
| GET | `/v1alpha2/mem9s/memories` | Search memories (keyword + vector) |
| GET | `/v1alpha2/mem9s/memories/:id` | Get a specific memory |
| PUT | `/v1alpha2/mem9s/memories/:id` | Update an existing memory |
| DELETE | `/v1alpha2/mem9s/memories/:id` | Soft-delete a memory |

**How search works — hybrid retrieval:**

When the agent searches for memories, mnemo-server does two things simultaneously:

1. **Keyword search:** Uses SQL `LIKE` and full-text matching against the `content` field. This finds memories that contain the exact words the agent is looking for.

2. **Vector similarity search:** Generates an embedding vector from the search query (via the local embed-service), then computes cosine similarity against the `embedding` column of all memories. This finds memories that are semantically related, even if they don't share the same words.

The results from both searches are merged and ranked. This hybrid approach is more effective than either method alone — keyword search catches exact matches that vector search might miss (like specific error codes or file names), and vector search catches conceptual matches that keyword search would miss (like "how does authentication work" matching a memory about "session token validation").

**Embedding generation:** When a new memory is stored, mnemo-server calls the local embed-service to generate an embedding vector. The embed-service is a custom C-based server that produces 256-dimensional L2-normalized vectors using a five-channel feature hashing algorithm. These vectors are stored directly in TiDB's `VECTOR(256)` column. This is a fully local operation — no external API calls, no cost per embedding, and deterministic output (the same input always produces the same vector).

**Multi-tenant design:** The mnemo-server supports multiple isolated tenants. Each tenant has its own API key, and the `tenants` table in the control-plane database maps API keys to database connections. In the stilltent deployment, there's a single tenant (`stilltent-local-dev-key`) that points to the local TiDB instance. But the architecture supports multiple agents or projects sharing the same mnemo-server installation with complete data isolation.

**Rate limiting:** Requests are rate-limited per IP address to prevent abuse. This is more relevant for multi-tenant deployments than for stilltent's single-agent use case, but the middleware is always active.

**Prometheus metrics:** The server exposes Prometheus-format metrics for monitoring memory operation counts, latency histograms, embedding generation times, and error rates.

**Internal architecture:**

```
HTTP Request
    │
    ▼
handler/        ← HTTP handlers (parse request, call service, format response)
    │
    ▼
service/        ← Business logic (validation, embedding generation, search ranking)
    │
    ▼
repository/     ← SQL queries (TiDB/PostgreSQL/MySQL adapters)
    │
    ▼
TiDB Database
```

The code follows strict layering: handlers never touch the database directly, services never construct HTTP responses, and repositories never call external APIs. This makes each layer independently testable.

---

### 4. OpenClaw Gateway (Agent Runtime)

**What it is:** OpenClaw is an open-source agent runtime. It provides an OpenAI-compatible chat completions API, manages conversations, executes tools (shell commands, file I/O, GitHub CLI operations), and routes LLM inference to a configured provider. Think of it as the agent's body — the infrastructure that lets the LLM interact with the world.

**Base image:** `ghcr.io/openclaw/openclaw:latest`
**Custom Dockerfile:** `dockerfiles/openclaw.Dockerfile` (adds `gh` CLI and mem9 plugin)
**Container name:** `stilltent-openclaw`
**Port:** 18789 (HTTP, bound to `0.0.0.0` inside the container for Docker port mapping)

**What the custom Dockerfile adds:**

```dockerfile
FROM ghcr.io/openclaw/openclaw:latest

# Install GitHub CLI for PR operations
USER root
RUN apt-get update && apt-get install -y --no-install-recommends gh
USER node

# Install the mem9 memory plugin
RUN openclaw plugins install @mem9/openclaw

# Configure git identity for agent commits
RUN git config --global user.email "agent@stilltent.local" \
 && git config --global user.name "stilltent-agent"
```

Three things are added to the base OpenClaw image:
1. The `gh` CLI — so the agent can create PRs, review PRs, merge PRs, list issues, and interact with GitHub
2. The `@mem9/openclaw` plugin — so the agent can store and search memories via the mem9 API
3. A git identity — so the agent's commits have a consistent author

**Configuration (`config/openclaw/openclaw.json`):**

The OpenClaw gateway is configured via a JSON file that specifies:

- **Model provider:** OpenRouter (`https://openrouter.ai/api/v1`)
- **Default model:** `qwen/qwen3-coder-next` — a purpose-built autonomous coding agent model with a 262K context window and 65K max output tokens. Cost: $0.12/M input tokens, $0.75/M output tokens. This model was chosen for its strong agentic coding capabilities, failure recovery training, and sufficient context/output window for the 7-phase iteration protocol.
- **Embedding:** Handled locally by the embed-service (no external API needed for embeddings).
- **Gateway settings:** Port 18789, token-based authentication, LAN binding mode
- **Plugin configuration:** mem9 plugin registered in the `memory` slot, pointing to `http://mnemo-server:8082` with API key `stilltent-local-dev-key`
- **Command settings:** Native commands set to `auto` (shell execution allowed), native skills set to `auto`, restart on error enabled

**How the gateway processes a request:**

1. The orchestrator sends an HTTP POST to `/v1/chat/completions` with a message like "Read and follow /workspace/SKILL.md. This is iteration 42. Execute the complete iteration protocol."
2. OpenClaw receives the message and creates (or resumes) a session
3. The message is sent to the LLM provider (OpenRouter → Qwen3 Coder 30B)
4. The LLM generates a response that may include tool calls (read file, execute shell command, search memory, etc.)
5. OpenClaw executes the tool calls and sends the results back to the LLM
6. The LLM continues generating, potentially making more tool calls
7. This loop continues until the LLM produces a final text response
8. OpenClaw returns the complete response to the orchestrator

**Tool execution is the key capability.** The LLM doesn't just generate text — it reads files, runs shell commands, creates git branches, runs tests, opens pull requests, and stores memories. OpenClaw is the bridge between the LLM's decisions and the actual filesystem, git repository, and GitHub API.

**Volume mounts:**
- `./config/openclaw/` → `/home/node/.openclaw/` — OpenClaw configuration and plugins
- `./workspace/` → `/workspace/` — The agent's working directory (contains SKILL.md, AGENTS.md, the cloned repo, metrics, and logs)

**API key injection:** The OpenRouter API key is injected at container startup. The `openclaw.json` file contains a placeholder (`__OPENROUTER_API_KEY__`), and the container's startup command uses `sed` to replace it with the actual key from the environment variable. This prevents the real API key from being stored in a file on the host.

**GitHub token injection:** The GitHub token is configured via git's `url.insteadOf` mechanism. The startup command runs:
```bash
git config --global url."https://x-access-token:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"
```
This means every `git push`, `git pull`, and `gh` command inside the container automatically authenticates with the token, without the token being stored in a file.

---

### 5. Orchestrator (Loop Driver)

**What it is:** A lightweight Python script that drives the agent loop. It is intentionally dumb. It does not make decisions about what the agent should do — it just triggers the agent, waits for a response, tracks whether the iteration succeeded or failed, and writes metrics. All intelligence lives in the agent (via SKILL.md and the LLM).

**Language:** Python 3.12, standard library only (no pip dependencies)
**File:** `orchestrator/loop.py` (594 lines)
**Container name:** `stilltent-orchestrator`
**Ports:** None (internal only)

**The main loop:**

```python
while not shutdown_requested:
    # 0. Check if total runtime limit exceeded (default: 120 hours = 5 days)
    # 1. Check if PAUSE file exists → if yes, sleep and retry
    # 2. Check consecutive failure count → if too many, create PAUSE and stop
    # 2b. If in backoff, wait (exponential: 60 * 2^N seconds, max 1 hour)
    # 3. Build trigger prompt and send to OpenClaw gateway
    # 4. Wait for response (with timeout, default 600 seconds)
    # 5. Determine success/failure from response
    # 6. Update metrics and write to workspace/metrics.json
    # 7. Log periodic health summary every 50 iterations
    # 8. Sleep for cooldown period (default: 60 seconds)
```

**Trigger prompt:** Each iteration, the orchestrator sends this prompt to the agent:

```
Read and follow /workspace/SKILL.md. This is iteration N.
Execute the complete iteration protocol (Phase 1 through Phase 7).
When finished, respond with a JSON summary:
{
  "iteration": <number>,
  "action_type": "<fix|review|feature|test|refactor|docs|bootstrap>",
  "summary": "<1-2 sentence description>",
  "result": "<success|failure|partial|skipped>",
  "pr_number": <number or null>,
  "merged": <true|false|null>,
  "confidence": <0.0 to 1.0>,
  "error": "<error message or null>"
}
```

This prompt tells the agent to read its protocol file (SKILL.md) and execute the full 7-phase iteration. The JSON response format lets the orchestrator parse the result programmatically.

**Success detection:** The orchestrator tries to parse the JSON summary from the agent's response. If it finds `"result": "success"`, `"result": "partial"`, or `"result": "skipped"`, the iteration counts as successful. If it finds `"result": "failure"` or can't find the JSON block, it falls back to keyword heuristics — looking for words like "error", "failed", "exception", or "traceback" in the response text. If no negative keywords are found, it assumes success (the agent ran without crashing).

**Exponential backoff:** After consecutive failures, the orchestrator waits before retrying. The delay is `min(60 * 2^N, 3600)` seconds, where N is the number of consecutive failures. So: 120s, 240s, 480s, 960s, 1920s, 3600s (cap). This prevents the system from hammering the LLM API when something is persistently broken.

**Auto-pause:** After 25 consecutive failures (configurable via `MAX_CONSECUTIVE_FAILURES`), the orchestrator creates a `workspace/PAUSE` file and stops the loop. This is a circuit breaker — it prevents runaway failures and API spend. A human must remove the PAUSE file to resume.

**Scheduled shutdown:** After 120 hours (5 days, configurable via `TOTAL_RUNTIME_HOURS`), the orchestrator creates a PAUSE file and exits gracefully. This prevents indefinite operation without human check-ins.

**HTTP retry logic:** The orchestrator retries failed HTTP calls to the OpenClaw gateway up to 3 times, with a 10-second delay between retries. It only retries on connection errors and 5xx server errors. 4xx client errors are not retried.

**Metrics tracking:** After every iteration, the orchestrator writes `workspace/metrics.json` with:
```json
{
  "total_iterations": 35,
  "successful_iterations": 8,
  "failed_iterations": 27,
  "current_consecutive_failures": 2,
  "success_rate": 0.229,
  "last_iteration_at": "2026-03-21T05:07:57Z",
  "uptime_seconds": 180,
  "status": "running",
  "total_prompt_tokens": 0,
  "total_completion_tokens": 0
}
```

**Spend estimation:** The orchestrator tracks token usage from LLM API responses and provides rough cost estimates based on approximate OpenRouter pricing ($3/M input tokens, $15/M completion tokens).

**Health summaries:** Every 50 iterations, the orchestrator logs a structured health summary showing total iterations, success rate, wall-clock hours, total tokens used, and estimated spend.

**Signal handling:** The orchestrator handles SIGTERM and SIGINT gracefully. When Docker Compose sends SIGTERM (during `make down`), the orchestrator finishes the current sleep cycle, writes final metrics, and exits cleanly.

**Interruptible sleep:** Sleep periods use a chunked approach (5-second increments) so the orchestrator can respond to shutdown signals promptly instead of blocking for the full cooldown period.

---

## The Agent Protocol — SKILL.md in Full Detail

The agent's behavior is not hardcoded. It is defined by a plain-text markdown file: `workspace/SKILL.md`. Every iteration, the orchestrator tells the agent to read this file and follow it. Changing the agent's behavior means editing a markdown file, not rewriting code.

This is one of the most important design decisions in the project. The protocol is:
- **Transparent** — anyone can read SKILL.md and understand exactly what the agent will do
- **Editable** — changing behavior is a text edit, not a code change
- **Versionable** — the protocol is tracked in git like any other file
- **Self-documenting** — the protocol IS the documentation

### The 7-Phase Iteration Protocol

Every iteration follows seven phases in order. The agent does not skip phases.

#### Phase 1: RECALL

The agent searches its persistent memory for context about the current state of the repository. It makes four memory queries:

1. "Latest test results and CI status" — What's the current health snapshot?
2. "Current iteration plan in progress" — Is there unfinished work from a previous iteration?
3. "Failed approach do not retry" — What has been tried and failed recently?
4. "Architectural decision rationale" — What design decisions have been made?

If this is the first iteration (no memories exist), the agent skips to Phase 2 and builds context by examining the repository directly.

This phase is critical for continuity. The agent has no state between iterations — it's a fresh LLM session every time. Memory is the only thing that connects iteration 42 to iteration 41. Without recall, the agent would repeat the same mistakes, redo finished work, and have no sense of progress.

#### Phase 2: ASSESS

The agent examines the repository's current state by running a series of commands:

```bash
git checkout main && git pull origin main    # Get latest code
git log --oneline -10                         # Recent history
find . -type f -not -path './.git/*'         # File structure
gh pr list --state open --limit 20            # Open PRs
gh issue list --state open --limit 20         # Open issues
gh run list --limit 5                         # CI status
# Run the project's test suite (adapts to language)
```

After assessment, the agent answers five questions:
1. Are there open PRs from external contributors that need review?
2. Are there failing tests that need to be fixed?
3. Is there an in-progress plan from a previous iteration?
4. What is the project's current state (scaffold, active development, mature)?
5. What is the highest-value next action?

The agent then selects work based on a strict priority order:
1. Fix failing tests (everything is blocked if tests are red)
2. Review external PRs (time-sensitive — don't leave contributors waiting)
3. Continue in-progress plans (finish what you started)
4. Address open issues (bugs and feature requests)
5. Improve test coverage (more tests = more confidence)
6. Implement new features (aligned with project direction)
7. Refactor for clarity (only when stable and well-tested)
8. Improve documentation (important but lowest urgency)

This priority system ensures the agent always works on the most valuable thing. It won't add new features while tests are failing. It won't refactor code while there are open contributor PRs waiting for review.

#### Phase 3: PLAN

The agent writes a structured plan before making any changes:

```
ITERATION PLAN
Iteration: 42
Action type: feature
Summary: Add YAML configuration parser for environment definitions
Files to modify: internal/config/parser.go, internal/config/parser_test.go
Expected outcome: Users can define environments in a .devenv.yaml file
Tests to verify: go test ./internal/config/...
Confidence: 0.8
Risk assessment: Low — new files only, no existing code modified
```

Three decision gates prevent bad changes:
- **Confidence < 0.5** → Choose a simpler task. Don't proceed with a risky change.
- **More than 10 files** → Break the work into smaller iterations. Store the breakdown plan in memory.
- **Protected files touched** → Add `[HUMAN-REVIEW]` tag. Do not auto-merge.

The plan is stored in memory before implementation begins. This means the next iteration can find it and continue the work if the current iteration runs out of time.

#### Phase 4: IMPLEMENT

The agent creates a branch and makes changes:

```bash
BRANCH_NAME="agent/$(date +%Y%m%d%H%M%S)-add-yaml-parser"
git checkout -b "$BRANCH_NAME"
```

Implementation follows an inner loop:
1. Make a change
2. Run tests
3. If tests pass → commit with a conventional commit message
4. If tests fail → attempt to fix (up to 3 attempts)
5. If still failing after 3 attempts → revert and record failure in memory
6. Repeat until done or time budget (8 minutes) exceeded

Key rules:
- Changes are incremental — the agent commits after each logical change, not all at once
- Tests run after every change — catching problems early
- Focus is maintained — no unrelated improvements in the same branch
- Conventional commit messages — `feat:`, `fix:`, `test:`, `refactor:`, `docs:`, `chore:`

#### Phase 5: VALIDATE

Before opening a PR, the agent runs the full validation suite:
- All tests must pass
- Lint must be clean
- Build must succeed

If validation fails and can't be fixed within 2 minutes, the agent abandons the branch:
```bash
git checkout main
git branch -D "$BRANCH_NAME"
```

The failure is recorded in memory with what went wrong, so future iterations can avoid the same mistake.

#### Phase 6: SUBMIT

If validation passed, the agent pushes and opens a PR:

```bash
git push origin "$BRANCH_NAME"
gh pr create --base main --head "$BRANCH_NAME" \
  --title "feat: add YAML configuration parser" \
  --body "## Summary ..."
```

PR bodies include: Summary, Changes, Test Results, Confidence Score, and Iteration Context.

The merge decision is confidence-gated:
- **Confidence >= 0.7 AND all tests pass AND no protected files** → Auto-merge
- **Confidence 0.5–0.7** → Merge but flag as lower-confidence
- **Confidence < 0.5** → Should not happen (Phase 3 gate). If it does, leave PR open.
- **Protected files modified** → Never auto-merge. Label as `[HUMAN-REVIEW]`.

This graduated merge policy balances velocity with safety. High-confidence changes (adding a test, fixing a typo, implementing a straightforward feature) merge immediately. Lower-confidence changes (refactoring complex logic, changing public APIs) go through for human review.

#### Phase 6b: REVIEW EXTERNAL PRs

If the assessment phase found open PRs from external contributors, the agent reviews them:

1. Check out the PR branch
2. Run the full test suite
3. Review the diff
4. Apply review criteria: tests pass? code quality acceptable? aligned with project direction? reasonable scope?
5. Actions: approve and merge, request changes, comment and close, or skip

#### Phase 7: LEARN

The agent records what happened in memory:

1. **Iteration log** — What it did, whether it succeeded, the PR number, test deltas, lessons learned
2. **Repository state snapshot** — File count, test count, pass rate, open PRs/issues, last commit (updated every 5 iterations)
3. **Failed approaches** — If something failed: what was tried, why it failed, what to do differently, and whether to retry
4. **Architectural decisions** — If a design choice was made: the decision, rationale, alternatives considered, affected files

### Long-Duration Operation Directives

SKILL.md includes specific directives for multi-day autonomous runs:

**Session management:** The agent checks for a `session_state` memory at the start of each iteration. If the last session ended mid-task, it resumes from where it left off.

**Pacing:** The agent is instructed to prefer small, correct changes over ambitious refactors. Target 80%+ confidence on every merge. If confidence is below 0.6, skip and try something simpler. "Momentum comes from many clean merges, not from one heroic change."

**Learning velocity:**
- Every 10 iterations: Write a digest summarizing what it's learned about the codebase
- Every 25 iterations: Consolidate digests into a single comprehensive summary
- Every 50 iterations: Full memory maintenance — clean up duplicates, remove superseded memories

**Cost awareness:** The agent is reminded that it's running on a pay-per-token API and should be concise — read specific line ranges instead of entire files, use grep instead of dumping files into context.

**Error resilience:** Transient failures (network blips, rate limits) are logged and retried next iteration. Persistent failures (bad credentials, missing endpoints) are recorded as pinned memories so future iterations route around them.

**Self-improvement priority:** When the repo has no failing tests, no open PRs, and no open issues, the agent focuses on making the codebase genuinely better: edge case tests, better error messages, refactoring confusing code, documentation, and build hardening.

---

## The Memory System — How the Agent Remembers

The memory system is what separates stilltent from a one-shot code generator. Without memory, every iteration starts from scratch. With memory, the agent has continuity — it knows what it did, what worked, what failed, and what it decided.

### Memory Architecture

```
Agent (in OpenClaw)
    │
    │ memory_search("test results")
    │ memory_store({content: "...", type: "iteration_log"})
    │
    ▼
mem9 Plugin (TypeScript, in OpenClaw)
    │
    │ HTTP REST calls
    │
    ▼
mnemo-server (Go REST API)
    │
    │ SQL + Vector queries
    │
    ▼
TiDB (MySQL + Vector)
```

### Types of Memories

| Type | Purpose | Lifecycle |
|------|---------|-----------|
| `pinned` | Important, long-lived memories (architectural decisions, persistent failures) | Kept indefinitely |
| `insight` | Extracted learnings (patterns discovered, code conventions) | Consolidated periodically |
| `digest` | Summaries of recent work (every 10 iterations) | Replaced when consolidated |

### States of Memories

| State | Meaning |
|-------|---------|
| `active` | Currently relevant, returned in searches |
| `paused` | Temporarily irrelevant, excluded from searches |
| `archived` | Historical, preserved but not actively searched |
| `deleted` | Soft-deleted, not returned in any query |

### How Search Works in Practice

When the agent searches for "failed approach do not retry", here's what happens:

1. The mem9 plugin in OpenClaw receives the search request
2. It sends an HTTP GET to `http://mnemo-server:8082/v1alpha2/mem9s/memories?q=failed+approach+do+not+retry`
3. mnemo-server generates an embedding vector from the query by calling the local embed-service's `/v1/embeddings` endpoint
4. mnemo-server runs two parallel searches in TiDB:
   - **Keyword:** `SELECT * FROM memories WHERE content LIKE '%failed%' AND content LIKE '%approach%' AND state = 'active'`
   - **Vector:** `SELECT *, VEC_COSINE_DISTANCE(embedding, ?) AS similarity FROM memories WHERE state = 'active' ORDER BY similarity ASC LIMIT 20`
5. Results are merged, deduplicated, and ranked
6. Top results are returned to the agent as structured JSON

### Memory Consolidation

Every 50 iterations, the agent spends one iteration on memory maintenance instead of code changes:

1. Search for all "iteration_log" memories from the last 50 iterations
2. Summarize patterns: what kinds of changes succeeded, what failed, what the codebase needs
3. Store a consolidated summary
4. Remove duplicate or superseded memories
5. Update the repository state snapshot

This prevents unbounded memory growth. Without consolidation, the agent would accumulate hundreds of individual iteration logs, making search slower and less relevant. Consolidation compresses this into a few high-quality summary memories.

### Memory Versioning

Memories have a `version` field and a `superseded_by` field. When the agent updates a memory (e.g., updating the repository state snapshot), the old version is preserved with a `superseded_by` pointer to the new version. This creates an audit trail of how the agent's understanding changed over time.

Conflict resolution uses last-write-wins semantics. Since there's only one agent writing memories, conflicts are rare, but the versioning system is there for multi-agent scenarios.

---

## The Workspace

The workspace is the agent's home directory. It's bind-mounted from the host at `./workspace/` to `/workspace/` in both the OpenClaw and orchestrator containers.

```
workspace/
├── SKILL.md          # Agent operating protocol (read every iteration)
├── AGENTS.md         # Agent identity, constraints, and communication style
├── repo/             # Git clone of the target repository
├── metrics.json      # Orchestrator metrics (iterations, success rate)
├── orchestrator.log  # Log file from the orchestrator
└── PAUSE             # If this file exists, the agent stops looping
```

### SKILL.md

The master protocol document. 442 lines of structured instructions that define exactly what the agent does each iteration. The orchestrator tells the agent to read this file at the start of every iteration. It's the single source of truth for agent behavior.

### AGENTS.md

The agent's identity and constraints document. 62 lines that define:
- Who the agent is ("You are stilltent, an autonomous repository maintenance agent")
- Core principles (PRs, tests, memory, small changes, pause when uncertain)
- Hard limits (never delete >30%, never push to main, never disable tests)
- Protected files that require human review
- Escalation triggers (when to stop and skip)
- Communication style (conventional commits, structured PR bodies)

### repo/

The git clone of the target repository. This is where the agent does all its work. The agent creates branches, makes changes, runs tests, and pushes from this directory. It's configured with the agent's git identity (`stilltent-agent <agent@stilltent.local>`) and authenticates via the GitHub token injected at container startup.

### metrics.json

A JSON file written by the orchestrator after every iteration. Contains cumulative statistics about iterations, success rates, token usage, and uptime. Survives container restarts because the workspace is a bind mount on the host.

### PAUSE

A sentinel file. If it exists, the orchestrator stops triggering the agent. It's the human override mechanism — `make pause` creates it, `make resume` removes it. The orchestrator also creates it automatically after too many consecutive failures (circuit breaker) or after the total runtime limit is reached (scheduled shutdown).

---

## Docker Compose — How the Stack Fits Together

The `docker-compose.yml` file defines all five services. Here's how they interact:

### Network

All services run on a single Docker bridge network called `stilltent-net`. Services refer to each other by container name (e.g., `tidb:4000`, `mnemo-server:8082`, `openclaw-gateway:18789`).

### Startup Order

Services start in dependency order:

1. **TiDB** starts first (no dependencies)
2. **embed-service** starts (no dependencies, healthy within seconds)
3. **mnemo-server** starts after both TiDB and embed-service are healthy
4. **OpenClaw** starts after mnemo-server has started and TiDB is healthy
5. **Orchestrator** starts after OpenClaw is healthy (verified by health check)

This ordering ensures that when the orchestrator sends its first trigger, all upstream services are ready.

### Health Checks

| Service | Check | Interval | Start Period |
|---------|-------|----------|-------------|
| TiDB | HTTP GET `http://127.0.0.1:10080/status` | 10s | 30s |
| embed-service | `wget -qO- http://127.0.0.1:8090/health` | 10s | 5s |
| OpenClaw | HTTP GET `http://127.0.0.1:18789/healthz` | 30s | 20s |

mnemo-server and orchestrator don't have explicit health checks in the compose file — they rely on their dependencies being healthy.

### Port Bindings

| Service | Internal Port | Host Binding |
|---------|--------------|-------------|
| TiDB | 4000 | `0.0.0.0:4000` |
| embed-service | 8090 | Not exposed |
| mnemo-server | 8082 | Not exposed |
| OpenClaw | 18789 | Not exposed |
| Orchestrator | None | None |

TiDB's port is exposed to the host for `make init-db` (which runs the MySQL client from the host). embed-service, mnemo-server, and OpenClaw are only accessible within the Docker network.

### Volumes

| Volume | Type | Purpose |
|--------|------|---------|
| `tidb-data` | Named | TiDB data persistence |
| `./config/openclaw/` | Bind | OpenClaw configuration and plugins |
| `./workspace/` | Bind | Agent working directory (shared between OpenClaw and orchestrator) |

### Restart Policy

All services use `restart: unless-stopped`. This means they automatically restart after crashes but stay stopped after `docker compose down`.

---

## Make Targets — Operational Commands

The Makefile (145 lines) provides all operational commands. It loads `.env` automatically.

### Core Commands

| Command | What It Does |
|---------|-------------|
| `make up` | `docker compose up -d` — Start all services in the background |
| `make down` | `docker compose down` — Stop all services |
| `make logs` | `docker compose logs -f` — Stream logs from all services |
| `make restart SVC=orchestrator` | Restart a specific service |
| `make status` | `docker compose ps` — Show container status |

### Agent Control

| Command | What It Does |
|---------|-------------|
| `make pause` | Create `workspace/PAUSE` — stops the agent loop |
| `make resume` | Remove `workspace/PAUSE` — resumes the agent loop |
| `make stats` | Show iteration count and success rate from metrics |

### Setup & Initialization

| Command | What It Does |
|---------|-------------|
| `make bootstrap` | First-time setup: clone repo, init DB, start agent |
| `make init-db` | Initialize TiDB schema (run once after first startup) |
| `make health` | Check OpenRouter API connectivity + container status |
| `make preflight` | Full pre-flight check suite |
| `make validate-workspace` | Verify workspace files exist and are valid |

### Testing

| Command | What It Does |
|---------|-------------|
| `make test-mem9` | Smoke test the mnemo-server API |
| `make test-openclaw` | Smoke test the OpenClaw gateway |

### Security

| Command | What It Does |
|---------|-------------|
| `make scan-secrets` | Scan for leaked secrets using gitleaks |
| `make install-hooks` | Install pre-commit hook for secret detection |

### Deployment & Monitoring

| Command | What It Does |
|---------|-------------|
| `make deploy` | Print DigitalOcean deployment instructions |
| `make monitor` | Run the monitoring dashboard |
| `make cost` | Estimate OpenRouter spend from metrics |
| `make clean` | Full teardown: stop containers, remove volumes, delete repo |

---

## Configuration — Every Variable Explained

All configuration flows through the `.env` file. Here is every variable, what it does, and what the default is:

### GitHub

| Variable | Purpose | Example |
|----------|---------|---------|
| `GITHUB_TOKEN` | Fine-grained PAT with `repo` + `workflow` scope | `ghp_xxxx` |
| `TARGET_REPO` | The repository to manage (owner/name format) | `dalinkstone/stilltent` |

### LLM Provider (OpenRouter)

| Variable | Purpose | Default |
|----------|---------|---------|
| `OPENROUTER_API_KEY` | API key from openrouter.ai/keys | — |
| `OPENROUTER_MODEL` | Chat model for agent reasoning | `qwen/qwen3-coder-next` |
| `EMBEDDING_MODEL` | Embedding model name (used by mnemo-server) | `local-embed` |
| `EMBEDDING_PROVIDER` | Embedding provider type | `ollama` |
| `EMBEDDING_DIM` | Embedding vector dimensions | `256` |

### TiDB

| Variable | Purpose | Default |
|----------|---------|---------|
| `TIDB_HOST` | Database hostname | `tidb` (Docker service name) |
| `TIDB_PORT` | Database port | `4000` |
| `TIDB_USER` | Database user | `root` |
| `TIDB_PASSWORD` | Database password | (empty) |
| `TIDB_DATABASE` | Database name | `mnemos` |

### mnemo-server

| Variable | Purpose | Default |
|----------|---------|---------|
| `MEM9_API_PORT` | Memory API port | `8082` |
| `MEM9_API_KEY` | Memory API key (doubles as tenant ID) | `stilltent-local-dev-key` |

### OpenClaw

| Variable | Purpose | Default |
|----------|---------|---------|
| `OPENCLAW_PORT` | Gateway port | `18789` |
| `OPENCLAW_GATEWAY_TOKEN` | Bearer token for API access | — |

### Orchestrator

| Variable | Purpose | Default |
|----------|---------|---------|
| `LOOP_INTERVAL` | Seconds between iterations | `60` |
| `COOLDOWN_SECONDS` | Cooldown pause between iterations | `30` |
| `ITERATION_TIMEOUT` | Max seconds per iteration | `600` |
| `MAX_CONSECUTIVE_FAILURES` | Auto-pause threshold | `25` |
| `TOTAL_RUNTIME_HOURS` | Graceful shutdown limit | `120` (5 days) |

---

## Security Model

### Network Isolation

All services communicate over the internal Docker bridge network (`stilltent-net`). Only TiDB's port (4000) is exposed to the host, and only for initial schema setup via `make init-db`. In production, this port should be bound to localhost only.

### Authentication

Three layers of authentication protect the system:

1. **OpenClaw Gateway:** Token-based authentication. Every request to the gateway requires a `Bearer` token in the `Authorization` header. The token is set via `OPENCLAW_GATEWAY_TOKEN`.

2. **mnemo-server:** API key authentication. Every request to the memory API requires an API key that maps to a tenant. The key is `stilltent-local-dev-key` for local development.

3. **GitHub:** Fine-grained PAT with `repo` and `workflow` scopes. The token is injected via environment variables and configured through git's `url.insteadOf` mechanism — it's never written to a file.

### Secret Scanning

- **gitleaks** integration with custom rules (`.gitleaks.toml`) scans both the working tree and git history
- **Pre-commit hooks** run a pattern-matching script before every commit to catch accidentally staged secrets
- **API key injection** at runtime via `sed` substitution prevents keys from being stored in configuration files

### Agent Constraints

The agent has hard-coded limits (in AGENTS.md) that prevent dangerous behavior:
- Never delete more than 30% of the codebase in a single PR
- Never modify `.env`, secrets, or credential files
- Never push directly to `main` — all changes go through PRs
- Never execute destructive commands outside the workspace
- Never disable tests to make a PR pass

---

## Scripts — What Each One Does

### `scripts/bootstrap.sh`
First-time initialization. Clones the target repository into `workspace/repo/`, initializes the TiDB database, provisions the mem9 tenant, and sends the first trigger to start the agent loop.

### `scripts/clone-target-repo.sh`
Clones the target GitHub repository into `workspace/repo/`. Configures the git remote with the GitHub token for push access. Sets the agent's git identity.

### `scripts/init-tidb.sql`
SQL script that creates the database schema. Creates two databases (`mnemos` and `mnemos_tenant`), creates the `tenants`, `upload_tasks`, and `memories` tables with all indexes, and seeds the local development tenant.

### `scripts/health-check.sh`
Verifies all five services are running and healthy. Checks TiDB connectivity, embed-service health, mnemo-server API response, OpenClaw health endpoint, and orchestrator container status.

### `scripts/preflight.sh`
Full pre-flight check suite. Runs health checks, validates workspace files, tests API connectivity, and verifies the agent can reach GitHub.

### `scripts/validate-workspace.sh`
Verifies that required workspace files (SKILL.md, AGENTS.md) exist and are readable.

### `scripts/test-mem9.py`
Smoke test for the mnemo-server API. Stores a test memory, searches for it, and deletes it. Verifies the full memory lifecycle works.

### `scripts/test-openclaw.py`
Smoke test for the OpenClaw gateway. Sends a simple chat message and verifies a response is returned.

### `scripts/pre-commit-secrets.sh`
Git pre-commit hook that scans staged files for common secret patterns (API keys, tokens, private keys). Blocks the commit if any matches are found.

### `scripts/deploy-digitalocean.sh`
Deployment helper for DigitalOcean VPS. Prints setup instructions for creating a droplet, installing Docker, and configuring the stack.

### `scripts/harden-vps.sh`
Security hardening script for VPS deployment. Configures firewall rules, SSH hardening, and automatic security updates.

### `scripts/monitor.sh`
Monitoring dashboard. Shows real-time container status, recent logs, iteration metrics, and API connectivity.

### `scripts/teardown.sh`
Cleanup script. Stops all containers, removes volumes, and optionally deletes the cloned repository.

---

## The mem9 Plugin Ecosystem

The memory system has plugins for three different AI coding tools. This is because mnemo-server was designed as a general-purpose memory service, not just for stilltent.

### OpenClaw Plugin (`mnemo-server/openclaw-plugin/`)

**This is the plugin used by stilltent.** It's a TypeScript plugin that registers memory tools directly into OpenClaw's tool system:

- `memory_store` — Store a new memory with content, type, and tags
- `memory_search` — Search memories by query (hybrid keyword + vector)
- `memory_get` — Retrieve a specific memory by ID
- `memory_update` — Update an existing memory's content or metadata
- `memory_delete` — Soft-delete a memory

The plugin is installed in the OpenClaw container via `openclaw plugins install @mem9/openclaw` in the Dockerfile. It's configured in `openclaw.json` under the `plugins.entries.openclaw` key with the mnemo-server URL and API key.

### Claude Code Plugin (`mnemo-server/claude-plugin/`)

A plugin for Claude Code (Anthropic's CLI tool). Uses bash hooks and skill files to integrate mem9:

- `session-start.sh` — Load relevant memories into context when a session starts
- `user-prompt-submit.sh` — Auto-inject memory context when the user submits a prompt
- `session-end.sh` — Save session learnings to memory when the session ends
- Skills: `mem9-store`, `mem9-recall`, `mem9-setup`

### OpenCode Plugin (`mnemo-server/opencode-plugin/`)

A TypeScript plugin for OpenCode (another AI coding tool). Uses system.transform hooks for automatic memory injection:

- `backend.ts` / `server-backend.ts` — Backend integration for memory operations
- `hooks.ts` — Lifecycle hooks for automatic context injection
- `tools.ts` — Tool definitions for memory operations

---

## The Benchmarking Suite

The `mnemo-server/benchmark/` directory contains tools for evaluating memory retrieval quality:

### LoCoMo Benchmark
Tests long-context memory retrieval. Evaluates how well the memory system retrieves relevant context from long conversation histories.

### MR-NIAH (Multi-Retrieval Needle in a Haystack)
Tests whether the memory system can find multiple relevant "needles" scattered across a large set of stored memories. This is a stress test for the hybrid search system.

### Benchmark Tools
- `benchmark.sh` — Runner script
- `drive-session.py` — Simulates agent sessions
- `report.py` — Generates quality reports from benchmark results

---

## Key Design Decisions — Why Things Are the Way They Are

### 1. OpenRouter for LLM, Local for Embeddings

stilltent uses a hybrid approach: cloud API for LLM inference (OpenRouter), local service for embeddings (embed-service).

**LLM via OpenRouter:**
- No GPU required — runs on a $24/month DigitalOcean droplet
- Access to code-specialized models (Qwen3 Coder 30B at $0.07/M input, $0.27/M output)
- Can switch models by changing a single environment variable
- Tradeoff: network dependency and per-token API costs

**Embeddings via local embed-service:**
- Zero API cost per embedding (pure CPU computation)
- Sub-millisecond latency (no network round-trip)
- Deterministic output (same text always produces same vector)
- Code-aware: the five-channel algorithm recognizes programming constructs
- Tradeoff: 256-dimensional vectors are lower resolution than cloud models (1536-dim), but sufficient for memory retrieval in this use case

The current default model is `qwen/qwen3-coder-next` — a purpose-built autonomous coding agent with a 262K context window and 65K max output, optimized for agentic coding tasks with failure recovery. At ~$5.50/day estimated cost, a 5-day run costs approximately $27.75 (75M input + 25M output tokens).

### 2. Memory as a Separate Service

The memory system is not embedded in the agent or the orchestrator. It's a standalone REST API with its own database. This is more complex than storing memories in a local file, but it enables:

- **Multiple agents sharing memory** — Different agents can read each other's memories
- **Independent scaling** — The memory service can be deployed separately from the agent
- **Clean API contract** — Memory operations go through a versioned REST API
- **Independent testing** — The memory service has its own test suite and benchmarks
- **Persistence guarantees** — TiDB provides ACID transactions for memory operations

### 3. Protocol-Driven Behavior

The agent's behavior is defined by SKILL.md, not by code. This means:
- **Non-developers can modify agent behavior** — It's just a markdown file
- **Behavior changes don't require rebuilding Docker images** — Edit the file and restart
- **The protocol is self-documenting** — Reading SKILL.md tells you exactly what the agent will do
- **A/B testing is easy** — Swap SKILL.md variants and compare metrics

### 4. Dumb Orchestrator, Smart Agent

The orchestrator is intentionally minimal (~600 lines of Python). It doesn't parse the repository, make decisions about what to work on, or evaluate code quality. It just sends a prompt and counts successes/failures.

All intelligence lives in the agent — which is just the LLM following SKILL.md instructions. This separation means:
- The orchestrator is simple enough to be fully understood in one sitting
- Agent behavior can be changed without touching the orchestrator
- The orchestrator can be reused for completely different agent protocols

### 5. Confidence-Gated Merging

The agent rates its own confidence for each change (0.0 to 1.0). High-confidence changes auto-merge. Low-confidence changes stay open for review.

This creates a spectrum between full autonomy and human oversight:
- **Confidence >= 0.7:** Auto-merge. The agent is sure. (Adding a test, fixing a typo, implementing a straightforward feature.)
- **Confidence 0.5–0.7:** Merge but flag. The agent is mostly sure but wants a human to double-check.
- **Confidence < 0.5:** Don't even try. The agent isn't confident enough to submit a PR.

### 6. Auto-Pause Circuit Breaker

After too many consecutive failures, the system stops itself. This prevents:
- Runaway API costs from repeated failing requests
- Repository pollution from broken PRs
- Log noise from endless error cycles

The exponential backoff provides graceful degradation before the circuit breaker trips.

### 7. Conventional Commits and Structured PRs

The agent uses conventional commits (`feat:`, `fix:`, `test:`, etc.) and structured PR bodies. This means:
- Commit history is readable and parseable
- PR history documents the agent's decision-making
- Automated changelog generation is possible
- Humans reviewing the repository can understand what the agent did and why

---

## Understanding the Flow: A Complete Iteration Walkthrough

Here's what happens during a single iteration, step by step:

### 1. Orchestrator Wakes Up

The orchestrator's main loop wakes up from its 60-second cooldown. It checks:
- Is the PAUSE file present? No → continue
- Have there been too many consecutive failures? No → continue
- Has the total runtime limit been reached? No → continue

### 2. Orchestrator Sends Trigger

The orchestrator builds a prompt:
```
Read and follow /workspace/SKILL.md. This is iteration 42.
Execute the complete iteration protocol (Phase 1 through Phase 7).
When finished, respond with a JSON summary: { ... }
```

It sends this as an HTTP POST to `http://openclaw-gateway:18789/v1/chat/completions`.

### 3. OpenClaw Receives the Message

OpenClaw receives the chat completion request. It creates (or resumes) a session and sends the message to the configured LLM (Qwen3 Coder 30B via OpenRouter).

### 4. LLM Reads SKILL.md

The LLM's first action is to read `/workspace/SKILL.md` using OpenClaw's file read tool. It processes all 442 lines and understands the protocol.

### 5. Phase 1: RECALL

The LLM calls the `memory_search` tool (provided by the mem9 plugin) four times:
```
memory_search("latest test results and CI status")
memory_search("current iteration plan in progress")
memory_search("failed approach do not retry")
memory_search("architectural decision rationale")
```

Each search goes through the mem9 plugin → HTTP to mnemo-server → hybrid query in TiDB → results back to the LLM.

### 6. Phase 2: ASSESS

The LLM calls OpenClaw's shell execution tool to run commands:
```bash
cd /workspace/repo
git checkout main && git pull origin main
git log --oneline -10
find . -type f -not -path './.git/*' | head -100
gh pr list --state open --limit 20
gh issue list --state open --limit 20
go test ./... 2>&1 | tail -30
```

The LLM analyzes the output and determines the highest-priority action.

### 7. Phase 3: PLAN

The LLM writes a structured plan and stores it in memory:
```
memory_store({
  content: "ITERATION PLAN\nIteration: 42\nAction type: test\n...",
  type: "insight",
  tags: ["iteration_plan"]
})
```

### 8. Phase 4: IMPLEMENT

The LLM creates a branch and makes changes:
```bash
git checkout -b "agent/20260321050700-add-config-tests"
```

It writes code using OpenClaw's file write tool, runs tests using the shell tool, and commits using git commands.

### 9. Phase 5: VALIDATE

The LLM runs the full test suite, linter, and build to verify everything works.

### 10. Phase 6: SUBMIT

The LLM pushes the branch and creates a PR:
```bash
git push origin "agent/20260321050700-add-config-tests"
gh pr create --base main --head "agent/20260321050700-add-config-tests" \
  --title "test: add unit tests for YAML config parser" \
  --body "..."
gh pr merge "agent/20260321050700-add-config-tests" --merge --delete-branch
```

### 11. Phase 7: LEARN

The LLM stores an iteration log, updates the repo state snapshot, and records any lessons learned in memory.

### 12. LLM Returns Response

The LLM produces a final response with the JSON summary:
```json
{
  "iteration": 42,
  "action_type": "test",
  "summary": "Added unit tests for YAML config parser, improving coverage from 45% to 62%",
  "result": "success",
  "pr_number": 127,
  "merged": true,
  "confidence": 0.85,
  "error": null
}
```

### 13. OpenClaw Returns to Orchestrator

OpenClaw wraps the response in an OpenAI-compatible format and returns it to the orchestrator.

### 14. Orchestrator Records Result

The orchestrator parses the JSON, sees `"result": "success"`, resets the consecutive failure counter, increments the success counter, writes metrics to `workspace/metrics.json`, and goes back to sleep for 60 seconds.

Then it wakes up and does it all again.

---

## Deployment

### Local Development

```bash
# Prerequisites: Docker, Docker Compose
cp .env.example .env     # Fill in GITHUB_TOKEN and OPENROUTER_API_KEY
make up                   # Start the stack
make init-db              # Create database schema
make bootstrap            # Clone target repo and start the agent
make logs                 # Watch what's happening
make stats                # Check iteration metrics
```

### DigitalOcean VPS

The system is designed to run unattended on a VPS. Recommended spec:
- **OS:** Ubuntu 24.04
- **RAM:** 4GB (2GB minimum)
- **CPU:** 2 vCPU
- **Cost:** ~$24/month (or use free credits)
- **No GPU required** — all LLM inference is via OpenRouter API

```bash
# On the VPS:
curl -fsSL https://get.docker.com | sh    # Install Docker
git clone <repo-url> ~/stilltent           # Clone this project
cd ~/stilltent
cp .env.example .env && nano .env          # Configure
make bootstrap                              # Start everything
```

The system includes a VPS hardening script (`scripts/harden-vps.sh`) for production deployments.

---

## Cost Estimation

All LLM costs go through OpenRouter. The agent runs on Qwen3 Coder 30B by default. Embeddings are free (local embed-service):

| Cost Type | Rate |
|-----------|------|
| Input tokens | $0.07 per million |
| Output tokens | $0.27 per million |
| Embeddings | **$0** (local embed-service) |

Rough estimates for a 5-day run (assuming ~1440 iterations/day, ~10K tokens per iteration):
- Input: 72M tokens × $0.07/M = **$5.04**
- Output: 36M tokens × $0.27/M = **$9.72**
- Embeddings: **$0**
- **Total: ~$15 for 5 days of autonomous operation** (or ~$1.64/day)

At $3/day all-in (including VPS), the agent can make hundreds of commits for less than the price of a coffee.

`make cost` provides real-time spend estimates from the metrics data.

---

## The Agent's Tools — And Why It Must Use Them

The agent has four categories of tools: shell execution, file I/O, persistent memory (mem9), and the GitHub CLI. These are not optional conveniences — they are the foundation of every iteration. The protocol (SKILL.md) and the identity document (AGENTS.md) both enforce a critical principle: **use your tools, do not circumvent them, and if they're holding you back, fix them.**

### Why This Matters

An autonomous agent without strong tool discipline devolves into bad habits:
- It skips memory queries because "it probably won't find anything useful" — and then repeats a failed approach from 20 iterations ago
- It pushes directly to main because "it's a small change" — and breaks the CI workflow
- It skips tests because "this change is trivial" — and introduces a regression
- It hardcodes values instead of reading configuration — and creates drift between environments

Each of these shortcuts saves a few seconds per iteration but compounds into systemic failure over hundreds of iterations. The tool discipline rules prevent this.

### The "Fix the Tool" Principle

The most important rule in the tool discipline section is: **if a tool is producing bad results, fix the tool.** This is what separates a useful autonomous agent from a frustrating one.

Consider the memory system. If `memory_search("failed approach")` returns irrelevant results, there are two responses:
1. **Bad:** Stop using memory search. Work without context. Repeat mistakes.
2. **Good:** Examine why the search is failing. Are memories stored with clear categories? Is the content structured enough for the hybrid search to find? Store better memories going forward — with explicit category tags, specific error messages, and actionable lessons.

The same principle applies to every tool:
- If tests don't catch regressions → write better tests
- If the linter misses problems → add stricter lint rules
- If CI workflows don't fail when they should → improve the workflow
- If PR descriptions are unclear → improve the PR template in the SKILL.md protocol

The agent is not a passive consumer of tools. It is an active maintainer of its own toolchain. The better its tools work, the more effective it becomes, which means the project it's building improves faster.

### Tool Inventory

| Tool | Provider | How the Agent Uses It |
|------|----------|----------------------|
| Shell commands | OpenClaw (native) | git operations, running tests, running builds, examining files |
| File read/write | OpenClaw (native) | Reading source code, writing new code, editing configuration |
| `memory_store` | mem9 plugin | Saving iteration logs, architectural decisions, failed approaches, plans |
| `memory_search` | mem9 plugin | Finding relevant context from previous iterations (hybrid keyword + vector search) |
| `memory_get` | mem9 plugin | Retrieving a specific memory by ID |
| `memory_update` | mem9 plugin | Updating existing memories with new information |
| `memory_delete` | mem9 plugin | Removing outdated or superseded memories |
| `gh pr create` | GitHub CLI | Opening pull requests with structured descriptions |
| `gh pr merge` | GitHub CLI | Merging approved PRs (confidence-gated) |
| `gh pr list` | GitHub CLI | Discovering open PRs from contributors |
| `gh pr review` | GitHub CLI | Reviewing external PRs |
| `gh issue list` | GitHub CLI | Finding reported bugs and feature requests |
| `gh run list` | GitHub CLI | Checking CI status |

---

## Thinking About This Differently

Everything described above is the machinery. But the real point of stilltent is something simpler: **what if you could describe a project in a paragraph, walk away, and come back to a working codebase?**

Not a scaffold. Not a template. A real codebase with tests, CI, documentation, error handling, and edge case coverage. A codebase that was built the way a careful developer would build it — one small commit at a time, with each change tested before it's merged.

The current implementation is a proof of concept. The success rate is 22.9% (8 out of 35 iterations succeeded). That's not great. But it means that out of 35 attempts, 8 real pull requests were created, tested, and merged. That's 8 incremental improvements to the codebase that happened without any human involvement.

And the system is designed to get better. Each iteration records what worked and what didn't. Failed approaches are stored in memory so they aren't retried. Successful patterns are consolidated into digests. Over hundreds of iterations, the agent builds up a detailed understanding of the codebase — its conventions, its pain points, its test gaps.

Think of it like this: the agent is a junior developer who never sleeps, never gets bored, and never forgets a lesson. It's not as smart as a senior developer, but it has infinite patience. It will try something, fail, learn, try something different, and eventually get it right. And then it will do it again, and again, and again — hundreds of times.

The 22.9% success rate means roughly 1 in 4 attempts produces a merged PR. At 100 iterations per day, that's ~25 merged PRs per day. Over 5 days, that's ~125 merged PRs. Each one a small, tested, reviewed change. That's a lot of incremental progress.

---

## Thinking About This Yet Differently: The System as a Feedback Loop

stilltent is really three nested feedback loops:

### Loop 1: The Iteration Loop (minutes)
Every 60 seconds, the agent wakes up, assesses, plans, implements, validates, submits, and learns. This is the innermost loop — the mechanical heartbeat of the system.

### Loop 2: The Learning Loop (hours)
Every 10 iterations, the agent writes a digest summarizing what it's learned. Every 25 iterations, it consolidates those digests. This creates a second, slower feedback loop where the agent's understanding of the codebase deepens over time. Early iterations are exploratory — the agent is learning the project structure, conventions, and patterns. Later iterations are more targeted — the agent knows where the weak spots are and focuses its energy there.

### Loop 3: The Self-Improvement Loop (days)
Over days of operation, the agent accumulates hundreds of iteration logs, dozens of digests, and a handful of consolidated summaries. This corpus of experience makes the agent more effective. It stops trying approaches that failed. It focuses on areas where it has high confidence. It builds on the patterns it discovered.

The memory system is the mechanism that connects these loops. Without memory, each iteration would be independent — the agent would be just as effective (or ineffective) on iteration 500 as on iteration 1. With memory, the agent improves over time. It becomes more familiar with the codebase, more aware of its own strengths and weaknesses, and more efficient in its use of time and tokens.

---

## Thinking About This From the Perspective of the Code Being Built

The target repository doesn't know it's being built by an autonomous agent. From the repository's perspective, it's receiving a steady stream of well-formatted pull requests from a developer named "stilltent-agent" with email "agent@stilltent.local". Each PR has a clear title, a structured description, test results, and a confidence score.

The commit history looks like any well-maintained open source project:
```
feat: add YAML configuration parser for environment definitions
test: add unit tests for config parser edge cases
fix: handle missing file extension in config path
refactor: extract Docker client initialization into separate function
docs: add usage examples to README
test: add integration test for environment lifecycle
feat: implement 'start' command for Docker environments
fix: correct container name collision when starting multiple envs
```

Each commit is small, focused, and tested. The branch names follow a consistent pattern (`agent/YYYYMMDDHHMMSS-slug`). The PR bodies explain what changed and why. This means a human reviewing the repository can understand the agent's work without knowing it was written by an AI.

---

## Thinking About This From the Perspective of Scaling

The current system runs one agent on one repository. But the architecture supports more:

**Multiple repositories:** Run multiple stilltent instances, each with its own `.env` and target repo. They can share the same TiDB and mnemo-server instances (different tenants).

**Multiple agents on one repository:** Run multiple orchestrator instances pointing to the same OpenClaw gateway but with different SKILL.md variants. One agent focuses on tests, another on documentation, another on features.

**Cloud-native deployment:** Replace the local TiDB with TiDB Cloud. Replace the local mnemo-server with a hosted instance. Run OpenClaw on a cloud VM or container service. The architecture is already service-oriented — scaling means deploying more instances, not rewriting code.

---

## Thinking About the Economics

An autonomous coding agent is only useful if it's cheaper than the alternative. Here's the math:

**Cost of stilltent for 5 days:**
- VPS: $24/month × (5/30) = $4
- OpenRouter API (Qwen3 Coder): ~$8 (estimated at $1.64/day)
- Embeddings: $0 (local embed-service)
- **Total: ~$12**

**Output of stilltent for 5 days:**
- ~7,200 iterations (1,440/day at 60s interval)
- ~1,800 merged PRs (at 25% success rate)
- ~36,000 lines of tested, reviewed code

**Cost per merged PR:** ~$0.007
**Cost per line of code:** ~$0.0003

That's less than a tenth of a cent per line of tested, reviewed code. The economics are compelling for certain use cases:
- Bootstrapping new projects from a description
- Maintaining test coverage on existing projects
- Adding documentation and error handling
- Routine refactoring and code cleanup

The agent is not replacing senior engineers who design architectures and make strategic decisions. It's replacing the tedious, incremental work that often doesn't get done because humans get bored or run out of time.

---

## What Could Go Wrong (and How the System Handles It)

### The agent produces bad code
**Mitigation:** Confidence-gated merging. Low-confidence changes don't auto-merge. All changes go through PRs, so a human can review and revert if needed.

### The agent gets stuck in a loop
**Mitigation:** The SKILL.md protocol includes emergency procedures. If the same error occurs 3+ consecutive iterations, the agent stops trying that approach and switches to a different task. After 25 consecutive failures, the orchestrator auto-pauses.

### The agent breaks the test suite
**Mitigation:** The agent never pushes to main directly. All changes go through branches and PRs. If tests fail on a branch, the agent abandons the branch. If tests fail on main (someone else broke them), fixing them becomes the highest priority.

### The agent runs up a huge API bill
**Mitigation:** Token tracking and spend estimation. Exponential backoff on failures. Scheduled shutdown after 5 days. `make cost` for real-time estimates.

### The agent modifies sensitive files
**Mitigation:** AGENTS.md defines protected files that require human review. The agent never modifies `.env`, secrets, or credential files (hard limit). Pre-commit hooks catch leaked secrets.

### The agent deletes too much code
**Mitigation:** AGENTS.md hard limit: never delete more than 30% of the codebase in a single PR.

---

## Summary

stilltent is a system for autonomous software development. It takes a project description, builds the project one pull request at a time, and continuously improves it over days of unattended operation. The system combines five services (database, local embedding server, memory API, agent runtime, loop driver) with a protocol-driven agent that remembers what it learned across hundreds of iterations. Embeddings are generated locally by a custom C service at zero API cost. LLM inference runs through OpenRouter on a code-specialized model (Qwen3 Coder 30B) for ~$1.64/day. The architecture is modular, the behavior is transparent (defined by markdown files, not code), and the safety mechanisms prevent the most common failure modes. The agent is explicitly instructed to use its tools — memory, testing, GitHub CLI, shell — and to fix them if they're holding it back.

The most important thing about stilltent is not the technology — it's the idea. Software development is fundamentally iterative: assess, plan, implement, test, review, merge, repeat. stilltent automates this loop. Not perfectly, not yet. But well enough that a paragraph of project description can become a working codebase with hundreds of tested, reviewed commits.

That is what this project does. That is why it exists.
