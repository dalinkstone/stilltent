# repokeeper

An autonomous AI agent that develops, manages, and improves a GitHub repository using locally-hosted models and persistent memory.

## Quick Start

1. Copy `.env.example` to `.env` and fill in your values
2. Start Ollama on the host with the required models
3. `make up` to start the Docker Compose stack
4. `make health` to verify all services
5. `make bootstrap` to initialize and start the agent loop

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

## Architecture

See [DESIGN.md](DESIGN.md) for the full design document.
See [docs/impl/](docs/impl/) for the phased implementation guide.
