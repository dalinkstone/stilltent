# stilltent

Point stilltent at any git repo with a `README.md`, walk away, and come back to a working project with hundreds of commits and PRs. stilltent is an autonomous engineering harness that reads your project description and builds it — writing code, running tests, opening PRs, and learning from every iteration.

## Quick Start

```bash
cp .env.example .env          # add your API keys
vim stilltent.yml              # point at your repo
make bootstrap                 # walk away
```

That's it. The agent reads your README, scaffolds the project, writes code, runs tests, opens PRs, stores what it learned, and repeats — continuously — until the project is built.

## How It Works

1. **You** write a `README.md` describing what you want built (goals, tech stack, architecture)
2. **You** set `target.repo` in `stilltent.yml` to point at the repo
3. **stilltent** clones the repo, generates agent prompts from the README, and starts an autonomous loop
4. **Each iteration**, the agent: recalls prior knowledge → assesses the repo → plans work → implements → tests → submits a PR → stores what it learned
5. **You** review PRs when you want — or let it run unattended for days

The agent remembers everything across iterations using persistent vector-backed memory. Every cycle leaves the project strictly better than before.

## Configuration

All configuration lives in two files:

- **`stilltent.yml`** — What to build, how to build it, and where to deploy
- **`.env`** — API keys and secrets (see `.env.example`)

### Agent Runtimes

| Runtime | Description |
|---------|-------------|
| `openclaw` | Full-featured agent with LLM routing, tools, and memory plugins |
| `nanoclaw` | Lightweight minimal-footprint variant |
| `nemoclaw` | GPU-accelerated variant for local inference (requires NVIDIA GPU) |
| `claude-code` | Anthropic Claude Code as the agent backend |

### Memory Backends

| Backend | Description |
|---------|-------------|
| `mem9` | Self-hosted: embed-service (C, 256-dim) + mnemo-server (Go) + TiDB |
| `supermemory` | Supermemory SaaS |
| `asmr` | Parallel observer/searcher/ensemble agents for multi-perspective memory |

### Sandbox Providers

| Provider | Description |
|----------|-------------|
| `daytona` | Isolated cloud sandboxes via Daytona SDK |
| `local` | Run directly on host (no isolation) |
| `none` | Disable sandboxing |

### Deploy Targets

| Target | Description |
|--------|-------------|
| `digitalocean` | DigitalOcean droplet |
| `vultr` | Vultr VPS |
| `railway` | Railway PaaS |
| `render` | Render PaaS |
| `heroku` | Heroku PaaS |
| `local` | Local Docker Compose (default) |

## Commands

### Core Workflow

| Command | Description |
|---------|-------------|
| `make generate` | Generate `docker-compose.yml` from `stilltent.yml` |
| `make build` | Generate compose + build all images |
| `make up` | Generate compose + start stack |
| `make down` | Stop stack |
| `make bootstrap` | Full first-time setup: validate, build, start, init DB, clone, generate prompts, seed memory, run first iteration |
| `make clean` | Full teardown including volumes |

### Agent Control

| Command | Description |
|---------|-------------|
| `make pause` | Pause the orchestrator loop |
| `make resume` | Resume the orchestrator loop |
| `make test-run` | Run a single iteration for testing |
| `make start` | Start autonomous mode |

### Monitoring

| Command | Description |
|---------|-------------|
| `make logs` | Follow all service logs |
| `make health` | Check all service health endpoints + LLM API + Daytona API |
| `make stats` | Show iteration count, success rate, spend |
| `make cost` | Show current spend vs budget, projected total |

### Deployment

| Command | Description |
|---------|-------------|
| `make deploy` | Deploy based on `stilltent.yml` `deploy.target` |
| `make teardown` | Tear down deployment |

### Utilities

| Command | Description |
|---------|-------------|
| `make preflight` | Check all prerequisites (Docker, `.env`, API keys, ports) |
| `make rebuild` | Force rebuild all images (no cache) |
| `make reset-metrics` | Clear metrics + unpause |
| `make scan-secrets` | Run gitleaks secret scanner |
| `make validate-config` | Validate `stilltent.yml` schema and cross-references |

## Architecture

```
stilltent.yml + .env
       │
       ▼
   ┌────────┐     ┌─────────────┐     ┌──────────────┐
   │ harness │────▶│ compose.py  │────▶│ docker-compose│
   │  .py    │     │ (fragments) │     │    .yml       │
   └────┬───┘     └─────────────┘     └──────┬───────┘
        │                                      │
        ▼                                      ▼
   ┌─────────┐     ┌────────────┐     ┌──────────────┐
   │ prompt   │     │ orchestrator│◀───│  agent       │
   │ builder  │     │   loop.py   │───▶│  (openclaw/  │
   └─────────┘     └──────┬─────┘     │  claude-code)│
                          │            └──────────────┘
                          ▼
                   ┌──────────────┐
                   │   memory     │
                   │ (mem9/asmr)  │
                   └──────────────┘
```

**Orchestrator** sends trigger prompts to the **agent** on a timer. Each iteration follows a 7-phase protocol (SKILL.md): Recall → Assess → Plan → Implement → Validate → Submit → Learn. The agent stores what it learned in **memory**, which it queries at the start of the next iteration.

## Project Structure

```
stilltent/
├── stilltent.yml          # Master configuration
├── .env                   # Secrets (from .env.example)
├── Makefile               # Single entry point for all operations
├── core/
│   ├── harness.py         # Bootstrap orchestration (make bootstrap)
│   ├── compose.py         # Docker Compose generator
│   ├── validate.py        # Config validation
│   ├── prompt_builder.py  # README → SKILL.md, AGENTS.md, LEARNING.md
│   └── orchestrator/      # Autonomous loop driver
├── config/
│   ├── agents/            # Agent runtime configs (openclaw, nanoclaw, etc.)
│   └── prompts/           # Jinja2 prompt templates
├── memory/                # Memory backends (mem9, ASMR)
├── sandbox/               # Sandbox integration (Daytona)
├── deploy/
│   ├── docker-compose/    # Composable YAML fragments
│   └── scripts/           # Deploy scripts (DigitalOcean, Vultr, PaaS)
├── scripts/               # Operational scripts
├── templates/             # Example project descriptions
├── tests/                 # Integration tests
└── workspace/             # Runtime: cloned repo, rendered prompts, metrics
```

## Writing Your README

Your target repo's `README.md` is the spec. The more detail you provide, the better the agent performs. Include:

- **Title and description** — what this project is
- **Goals** — what it should do (bullet list)
- **Non-Goals** — what it should NOT do
- **Tech Stack** — languages, frameworks, databases
- **Architecture** — how components fit together

See `templates/input/README.md.example` for a full template.

## Cost

Default configuration uses `qwen/qwen3-coder-next` via OpenRouter:
- $0.12/M input tokens, $0.75/M output tokens
- Budget: ~$50 for a 5-day continuous run (120 hours)
- Monitor spend: `make cost`

## Documentation

See [EXPLANATION.md](EXPLANATION.md) for the full technical deep-dive.
