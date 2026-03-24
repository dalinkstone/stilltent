# stilltent

A production engineering harness that takes any `README.md` and autonomously builds the described project. Point it at a repo, configure the stack, and walk away — stilltent handles code generation, testing, memory, and iteration in a continuous loop.

## How It Works

1. Write a project description in a `README.md` (see `templates/input/README.md.example`)
2. Set `target.repo` in `stilltent.yml` to point at the repo
3. Run `make up` — the agent reads the README, scaffolds the project, writes code, runs tests, opens PRs, and repeats

The agent remembers what it learned across iterations using persistent vector-backed memory. Every cycle leaves the project strictly better than before.

## Quick Start

```bash
cp .env.example .env          # fill in your API keys
vim stilltent.yml              # set target.repo and tweak options
make up                        # start the stack
make init-db                   # initialize database (first time only)
make bootstrap                 # clone target repo and start the agent
```

## Configuration

All configuration lives in two files:

- **`stilltent.yml`** — Stack configuration: agent runtime, memory backend, sandbox provider, deploy target, orchestrator tuning
- **`.env`** — Secrets and API keys (see `.env.example`)

### Agent Runtimes

| Runtime | Description |
|---------|-------------|
| `openclaw` | Full-featured agent with LLM routing, tools, and memory plugins |
| `nanoclaw` | Lightweight minimal-footprint variant |
| `nemoclaw` | GPU-accelerated variant for local inference |

### Memory Backends

| Backend | Description |
|---------|-------------|
| `mem9` | Self-hosted: embed-service (C, 256-dim) + mnemo-server (Go) + TiDB |
| `supermemory` | Supermemory SaaS or self-hosted |
| `asmr` | Parallel observer/searcher/ensemble agents (Supermemory ASMR-style) |

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
| `local` | Local Docker Compose |

## Commands

| Command | Description |
|---------|-------------|
| `make up` | Start all services |
| `make down` | Stop all services |
| `make logs` | Follow all service logs |
| `make health` | Check service health |
| `make bootstrap` | First-time initialization |
| `make pause` | Pause the agent loop |
| `make resume` | Resume the agent loop |
| `make stats` | Show iteration statistics |
| `make clean` | Full teardown (destructive) |

## Project Structure

```
stilltent/
├── config/          # Agent, memory, deploy, and prompt configs
├── core/            # Orchestrator loop and harness entry point
├── memory/          # Memory backends (mem9, ASMR)
├── sandbox/         # Sandbox integration (Daytona)
├── deploy/          # Deploy scripts and docker-compose overlays
├── templates/       # Example project descriptions
├── archive/         # Reference implementations
├── scripts/         # Operational scripts
├── stilltent.yml    # Master configuration
└── .env             # Secrets
```

## Documentation

See [EXPLANATION.md](EXPLANATION.md) for the full technical deep-dive.
