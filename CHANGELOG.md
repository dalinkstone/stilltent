# Changelog

## [0.2.0] — 2026-03-27 — Modular Architecture Refactor

Complete rewrite of stilltent from a single-configuration system into a fully
modular, pluggable architecture. The system now supports multiple agent runtimes,
memory backends, sandbox providers, and deployment targets — all configured from
a single `stilltent.yml` file.

### Added

- **`stilltent.yml`** — Master configuration file with pluggable axes:
  - Agent runtimes: `openclaw`, `nanoclaw`, `nemoclaw`, `claude-code`
  - Memory backends: `mem9` (self-hosted), `supermemory` (SaaS), `asmr` (parallel agents)
  - Sandbox providers: `daytona` (cloud), `local`, `none`
  - Deploy targets: `digitalocean`, `vultr`, `railway`, `render`, `heroku`, `local`

- **`core/compose.py`** — Docker Compose fragment assembler. Reads `stilltent.yml`,
  selects the right fragments from `deploy/docker-compose/`, deep-merges them, and
  rewires `depends_on` and environment variables for the selected runtime/memory combo.

- **`core/prompt_builder.py`** — README-to-prompt pipeline. Parses any repo's
  `README.md` into structured metadata (title, goals, tech stack), detects test
  commands from marker files, and renders `SKILL.md`, `AGENTS.md`, `LEARNING.md`
  via Jinja2 templates.

- **`core/harness.py`** — Master bootstrap orchestration. 12-step pipeline:
  validate config, generate compose, build, start stack, health checks, init DB,
  clone repo, generate prompts, setup sandbox, seed memory, first iteration, summary.

- **`core/validate.py`** — Configuration validator with enum checking, required
  field validation, and cross-validation (Daytona needs API key, NemoClaw needs GPU,
  Supermemory needs API key, Claude Code needs Anthropic key).

- **`core/orchestrator/loop.py`** — Autonomous loop driver with circuit breaker,
  per-iteration cost tracking, budget guard, idle detection with exponential backoff,
  and 25-failure auto-pause.

- **`memory/asmr/`** — ASMR parallel agent memory system with observer (6-vector
  knowledge extraction), searcher (multi-perspective queries), ensemble (synthesis),
  and router (coordination).

- **Docker Compose fragments** in `deploy/docker-compose/`:
  - `base.yml`, `memory-mem9.yml`, `memory-supermemory.yml`
  - `agent-openclaw.yml`, `agent-nanoclaw.yml`, `agent-nemoclaw.yml`, `agent-claude-code.yml`
  - `orchestrator.yml`, `oversight-claude-code.yml`

- **Agent runtime configs** in `config/agents/` for all four runtimes.

- **Deployment scripts**: `deploy-digitalocean.sh`, `deploy-vultr.sh`, `deploy-paas.sh`
  (Railway/Render/Heroku), `harden-vps.sh`, `teardown.sh`.

- **Jinja2 prompt templates** in `config/prompts/`: `SKILL.md.tmpl`, `AGENTS.md.tmpl`,
  `LEARNING.md.tmpl` — project-agnostic, injected with per-repo context.

- **Integration tests** at `tests/integration/test_harness.py`: 47 tests covering
  all 12 runtime×memory combinations, prompt generation, config validation,
  and Daytona client lifecycle.

- **Claude Code oversight sidecar** — optional reviewer that runs alongside a
  non-Claude primary agent, reviewing work every N iterations.

### Changed

- **Makefile** rewritten as single entry point with organized target groups:
  core workflow, agent control, monitoring, deployment, utilities. `make help`
  shows all targets. `make bootstrap` is now the one-command setup.

- **`make bootstrap`** now runs `preflight` + `validate-config` before calling
  `core/harness.py` (was: `scripts/bootstrap.sh` with manual steps).

- **`make preflight`** now conditionally checks `DAYTONA_API_KEY` (when
  `sandbox.provider=daytona`) and `SUPERMEMORY_API_KEY` (when
  `memory.backend=supermemory`).

- **`make health`** now checks LLM API connectivity and Daytona API in addition
  to Docker service health.

- **`stilltent.yml` default** `sandbox.provider` changed from `daytona` to `local`
  so the system works out of the box without Daytona credentials.

- **`compose.py`** now rewires agent `depends_on` to the correct memory service
  when using `supermemory` backend (was: hardcoded to `mnemo-server`).

- **README.md** rewritten with 3-command quick start, full command reference,
  architecture diagram, and clear project pitch.

- **EXPLANATION.md** updated to document the modular architecture, pluggable
  components, bootstrap pipeline, compose fragment system, and config validator.

### Fixed

- Agent fragments (`nanoclaw`, `nemoclaw`, `claude-code`) had hardcoded
  `depends_on: mnemo-server` which broke when using `supermemory` backend.
  `compose.py` now rewires these automatically.

## [0.1.0] — 2026-03-22 — Initial Release

First working version of stilltent with the original five-service architecture:
TiDB + embed-service + mnemo-server + OpenClaw Gateway + Orchestrator.
Single-target configuration for the `mytool` project. Deployed on DigitalOcean.
