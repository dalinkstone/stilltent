# stilltent

An always-on autonomous AI agent that takes a simple project description and builds an entire software project from scratch — making hundreds of commits, opening hundreds of pull requests, and continuously improving the codebase without human intervention.

## How It Works

You write a project description in `project/README.md`. The agent reads it, scaffolds the project, writes code, writes tests, opens PRs, merges them, and repeats — forever. It remembers what it learned across iterations using persistent vector-backed memory.

## Quick Start

```bash
cp .env.example .env          # fill in your values
make up                        # start the stack
make init-db                   # initialize database (first time only)
make bootstrap                 # clone target repo and start the agent
```

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

## Stack

Five Docker Compose services on an internal bridge network:

- **TiDB** — MySQL-compatible database for persistent memory storage
- **embed-service** — C-based local embedding server (256-dim vectors, zero API cost)
- **mnemo-server** — Go REST API for storing and searching agent memories
- **OpenClaw** — Agent runtime with LLM routing, tool execution, and memory plugins
- **Orchestrator** — Python loop driver that triggers the agent on a schedule

LLM inference via [OpenRouter](https://openrouter.ai) (Qwen3 Coder Next). No GPU required. Embeddings are fully local via embed-service.

## Configuration

All configuration is in `.env`. See `.env.example` for all variables and recommended values.

## Documentation

See [EXPLANATION.md](EXPLANATION.md) for the full technical deep-dive.
