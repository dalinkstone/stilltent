# ═══════════════════════════════════════════════════════════════════
# stilltent — Master Makefile
# Single entry point for the entire system.
# Usage: cp .env.example .env && vim stilltent.yml && make bootstrap
# ═══════════════════════════════════════════════════════════════════

# Load .env vars so Make targets (and their subprocesses) see them.
-include .env
export

SHELL := /bin/bash
.DEFAULT_GOAL := help

MYSQL_BIN ?= $(shell which mysql 2>/dev/null || echo /opt/homebrew/opt/mysql-client@8.4/bin/mysql)

# Read config values once (used by multiple targets)
AGENT_RUNTIME  := $(shell python3 -c "import yaml; print(yaml.safe_load(open('stilltent.yml')).get('agent',{}).get('runtime','openclaw'))" 2>/dev/null || echo openclaw)
MEMORY_BACKEND := $(shell python3 -c "import yaml; print(yaml.safe_load(open('stilltent.yml')).get('memory',{}).get('backend','mem9'))" 2>/dev/null || echo mem9)
DEPLOY_TARGET  := $(shell python3 -c "import yaml; print(yaml.safe_load(open('stilltent.yml')).get('deploy',{}).get('target','local'))" 2>/dev/null || echo local)

.PHONY: help generate build up down bootstrap clean \
        pause resume test-run start \
        logs health stats cost \
        deploy teardown \
        preflight rebuild reset-metrics scan-secrets validate-config \
        generate-prompts init-db status restart monitor \
        setup-claude dev-loop dev-loop-once dev-loop-opus dev-loop-sonnet dev-logs dev-stats dev-clean-branches

# ── Help ──────────────────────────────────────────────────────────

help:
	@echo "stilltent — autonomous engineering harness"
	@echo ""
	@echo "  Quick start:"
	@echo "    cp .env.example .env   # add your API keys"
	@echo "    vim stilltent.yml      # point at your repo"
	@echo "    make bootstrap         # walk away"
	@echo ""
	@echo "  Core workflow:"
	@echo "    make generate          Generate docker-compose.yml from stilltent.yml"
	@echo "    make build             Generate compose + build all images"
	@echo "    make up                Generate compose + start stack"
	@echo "    make down              Stop stack"
	@echo "    make bootstrap         Full first-time setup (zero to running agent)"
	@echo "    make clean             Full teardown including volumes"
	@echo ""
	@echo "  Agent control:"
	@echo "    make pause             Pause the orchestrator loop"
	@echo "    make resume            Resume the orchestrator loop"
	@echo "    make test-run          Run a single iteration for testing"
	@echo "    make start             Start autonomous mode"
	@echo ""
	@echo "  Monitoring:"
	@echo "    make logs              Follow all service logs"
	@echo "    make health            Check all service health endpoints"
	@echo "    make stats             Show iteration count, success rate, spend"
	@echo "    make cost              Show current spend vs budget"
	@echo ""
	@echo "  Deployment:"
	@echo "    make deploy            Deploy based on stilltent.yml deploy.target"
	@echo "    make teardown          Tear down deployment"
	@echo ""
	@echo "  Utilities:"
	@echo "    make preflight         Check all prerequisites"
	@echo "    make rebuild           Force rebuild all images"
	@echo "    make reset-metrics     Clear metrics + unpause"
	@echo "    make scan-secrets      Run gitleaks"
	@echo "    make validate-config   Validate stilltent.yml schema"
	@echo ""
	@echo "  Config: runtime=$(AGENT_RUNTIME) memory=$(MEMORY_BACKEND) deploy=$(DEPLOY_TARGET)"

# ── Core Workflow ─────────────────────────────────────────────────

# Generate agent prompts from target repo's README.md + stilltent.yml
generate-prompts:
	@python3 core/prompt_builder.py

# Generate docker-compose.yml from stilltent.yml + composable fragments
generate:
	@python3 core/compose.py

# Generate compose + build all images
build: generate
	@echo "Building all images..."
	@docker compose build --parallel

# Generate compose + start the stack
up: generate
	@echo "Starting TiDB and embed-service..."
	@docker compose up -d tidb embed-service 2>/dev/null || docker compose up -d
	@echo "Waiting for TiDB to be healthy (up to 3 minutes)..."
	@for i in $$(seq 1 90); do \
		if curl -sf http://127.0.0.1:10080/status >/dev/null 2>&1; then \
			echo "TiDB is ready."; \
			break; \
		fi; \
		if [ $$i -eq 90 ]; then \
			echo "ERROR: TiDB did not become healthy after 90 attempts (3 min)."; \
			echo "Check logs: docker compose logs tidb"; \
			exit 1; \
		fi; \
		sleep 2; \
	done
	@if ! $(MYSQL_BIN) -h 127.0.0.1 -P 4000 -u root -e "USE mnemos" >/dev/null 2>&1; then \
		echo "Database 'mnemos' not found — running init-db..."; \
		$(MYSQL_BIN) -h 127.0.0.1 -P 4000 -u root < scripts/init-tidb.sql; \
		echo "Database initialized."; \
	else \
		echo "Database 'mnemos' exists — skipping init."; \
	fi
	@echo "Starting remaining services (excluding orchestrator)..."
	@docker compose up -d --no-recreate 2>/dev/null || true
	@docker compose stop orchestrator 2>/dev/null || true

# Stop all services
down:
	@docker compose down

# Full first-time setup: zero to running agent
bootstrap: preflight validate-config
	@python3 core/harness.py

# Full teardown including volumes
clean:
	@echo "Stopping all services and removing volumes..."
	@docker compose down -v 2>/dev/null || true
	@rm -rf workspace/repo
	@rm -f workspace/metrics.json workspace/orchestrator.log workspace/PAUSE
	@rm -f docker-compose.yml
	@echo "Clean complete."

# ── Agent Control ─────────────────────────────────────────────────

# Pause the orchestrator loop
pause:
	@touch workspace/PAUSE
	@echo "Agent paused. Run 'make resume' to continue."

# Resume the orchestrator loop
resume:
	@rm -f workspace/PAUSE
	@echo "Agent resumed."

# Run a single iteration for testing
test-run:
	@echo "Running single test iteration..."
	@MAX_ITERATIONS=1 docker compose run --rm -e MAX_ITERATIONS=1 orchestrator
	@echo "Test iteration complete. Check logs: make logs"

# Start autonomous mode (alias for resume + start orchestrator)
start:
	@rm -f workspace/PAUSE
	@echo "Starting orchestrator (autonomous mode)..."
	@docker compose up -d orchestrator

# ── Monitoring ────────────────────────────────────────────────────

# Follow all service logs
logs:
	@docker compose logs -f --tail=50

# Check all service health endpoints + LLM API + Daytona API
health:
	@echo "=== Service Health ==="
	@svc_check() { \
		SVC="$$1"; PORT="$$2"; LABEL="$$3"; \
		printf "  %-20s" "$$LABEL"; \
		STATE=$$(docker compose ps --format '{{.State}}' "$$SVC" 2>/dev/null || echo ""); \
		HEALTH=$$(docker compose ps --format '{{.Health}}' "$$SVC" 2>/dev/null || echo ""); \
		if [ "$$HEALTH" = "healthy" ]; then \
			echo "healthy (port $$PORT)"; \
		elif [ "$$STATE" = "running" ]; then \
			echo "running (port $$PORT)"; \
		else \
			echo "DOWN"; \
		fi; \
	}; \
	svc_check tidb            4000  "tidb"; \
	svc_check embed-service   8090  "embed-service"; \
	svc_check mnemo-server    8082  "mnemo-server"; \
	svc_check openclaw-gateway 18789 "openclaw-gateway"; \
	svc_check orchestrator    -     "orchestrator"
	@echo ""
	@echo "=== External APIs ==="
	@printf "  %-20s" "LLM API"; \
	if [ -n "$${OPENROUTER_API_KEY:-}" ]; then \
		curl -sf -H "Authorization: Bearer $$OPENROUTER_API_KEY" \
			https://openrouter.ai/api/v1/models 2>/dev/null | head -c 1 > /dev/null \
			&& echo "OK (OpenRouter)" \
			|| echo "UNREACHABLE"; \
	elif [ -n "$${ANTHROPIC_API_KEY:-}" ]; then \
		echo "OK (Anthropic key set)"; \
	else \
		echo "NO KEY SET"; \
	fi
	@printf "  %-20s" "GitHub API"; \
	curl -sf -H "Authorization: token $${GITHUB_TOKEN:-}" \
		https://api.github.com/user > /dev/null 2>&1 \
		&& echo "OK" \
		|| echo "UNREACHABLE"
	@printf "  %-20s" "Daytona API"; \
	if [ -n "$${DAYTONA_API_KEY:-}" ]; then \
		curl -sf -H "Authorization: Bearer $$DAYTONA_API_KEY" \
			"$${DAYTONA_BASE_URL:-https://app.daytona.io}/api/health" > /dev/null 2>&1 \
			&& echo "OK" \
			|| echo "KEY SET (endpoint unreachable)"; \
	else \
		echo "NOT CONFIGURED"; \
	fi

# Show iteration count, success rate, spend
stats:
	@python3 -c "\
import json, sys; \
f='workspace/metrics.json'; \
try: m=json.load(open(f)); \
except: print('No metrics yet.'); sys.exit(0); \
total=m.get('total_iterations',0); \
succ=m.get('successful_iterations',0); \
fail=m.get('failed_iterations',0); \
rate=(succ/total*100) if total else 0; \
print(f'Iterations:   {total} ({succ} success, {fail} failed)'); \
print(f'Success rate: {rate:.1f}%'); \
print(f'Total spend:  \$${m.get(\"total_spend_usd\",0):.4f}'); \
up=m.get('uptime_seconds',0); \
print(f'Uptime:       {int(up//3600)}h {int((up%3600)//60)}m') if up else None; \
"

# Show current spend vs budget, projected total
cost:
	@python3 -c "\
import json, sys; \
f='workspace/metrics.json'; \
try: m=json.load(open(f)); \
except: print('No metrics yet.'); sys.exit(0); \
spent=m.get('total_spend_usd',0); \
budget=m.get('budget_remaining_usd', float('$${BUDGET_LIMIT:-50}')); \
projected=m.get('projected_total_usd',0); \
total=m.get('total_iterations',0); \
print(f'Iterations:  {total}'); \
print(f'Spent:       \$${spent:.4f}'); \
print(f'Projected:   \$${projected:.2f}'); \
print(f'Budget:      \$${budget:.2f} remaining'); \
daily=m.get('daily_spend_usd',0); \
print(f'Daily rate:  \$${daily:.4f}/day') if daily else None; \
"

# ── Deployment ────────────────────────────────────────────────────

# Deploy based on stilltent.yml deploy.target
deploy:
	@TARGET=$(DEPLOY_TARGET); \
	echo "Deploy target: $$TARGET"; \
	case "$$TARGET" in \
		digitalocean) $(MAKE) deploy-do ;; \
		vultr)        $(MAKE) deploy-vultr ;; \
		railway)      $(MAKE) deploy-railway ;; \
		render)       $(MAKE) deploy-render ;; \
		heroku)       $(MAKE) deploy-heroku ;; \
		local)        echo "Target is 'local' — use 'make up' to start locally." ;; \
		*)            echo "Unknown deploy target: $$TARGET"; exit 1 ;; \
	esac

deploy-do:
	@if [ -z "$${DROPLET_IP:-}" ]; then echo "ERROR: DROPLET_IP not set."; exit 1; fi
	@echo "Deploying to DigitalOcean droplet at $$DROPLET_IP..."
	@scp .env "root@$$DROPLET_IP:/root/.env.stilltent"
	@scp deploy/scripts/harden-vps.sh deploy/scripts/deploy-digitalocean.sh "root@$$DROPLET_IP:/tmp/"
	@ssh "root@$$DROPLET_IP" "bash /tmp/deploy-digitalocean.sh"

deploy-vultr:
	@if [ -z "$${VULTR_IP:-}" ]; then echo "ERROR: VULTR_IP not set."; exit 1; fi
	@echo "Deploying to Vultr VPS at $$VULTR_IP..."
	@scp .env "root@$$VULTR_IP:/root/.env.stilltent"
	@scp deploy/scripts/harden-vps.sh deploy/scripts/deploy-vultr.sh "root@$$VULTR_IP:/tmp/"
	@ssh "root@$$VULTR_IP" "bash /tmp/deploy-vultr.sh"

deploy-railway:
	@bash deploy/scripts/deploy-paas.sh --provider railway

deploy-render:
	@bash deploy/scripts/deploy-paas.sh --provider render

deploy-heroku:
	@bash deploy/scripts/deploy-paas.sh --provider heroku

# Tear down deployment
teardown:
	@bash deploy/scripts/teardown.sh

# ── Utilities ─────────────────────────────────────────────────────

# Check all prerequisites (Docker, .env, API keys, ports, Daytona if configured)
preflight:
	@echo "=== Preflight Checks ==="
	@FAIL=0; \
	printf "  %-24s" "Docker daemon:"; \
	if docker info >/dev/null 2>&1; then echo "OK"; else echo "FAIL — Docker is not running"; FAIL=1; fi; \
	printf "  %-24s" "Python 3:"; \
	if python3 --version >/dev/null 2>&1; then echo "OK ($$(python3 --version 2>&1))"; else echo "FAIL — python3 not found"; FAIL=1; fi; \
	printf "  %-24s" ".env file:"; \
	if [ -f .env ]; then echo "OK"; else echo "FAIL — run: cp .env.example .env"; FAIL=1; fi; \
	printf "  %-24s" "stilltent.yml:"; \
	if [ -f stilltent.yml ]; then echo "OK"; else echo "FAIL — stilltent.yml not found"; FAIL=1; fi; \
	printf "  %-24s" "GITHUB_TOKEN:"; \
	if [ -n "$${GITHUB_TOKEN:-}" ]; then echo "OK (set)"; else echo "FAIL — not set in .env"; FAIL=1; fi; \
	printf "  %-24s" "LLM API key:"; \
	if [ -n "$${OPENROUTER_API_KEY:-}" ] || [ -n "$${ANTHROPIC_API_KEY:-}" ]; then echo "OK"; else echo "FAIL — set OPENROUTER_API_KEY or ANTHROPIC_API_KEY"; FAIL=1; fi; \
	printf "  %-24s" "TARGET_REPO:"; \
	if [ -n "$${TARGET_REPO:-}" ]; then echo "OK ($$TARGET_REPO)"; else echo "WARN — not set (set in .env or stilltent.yml)"; fi; \
	printf "  %-24s" "Port 4000 (TiDB):"; \
	if ! lsof -iTCP:4000 -sTCP:LISTEN >/dev/null 2>&1; then echo "OK (free)"; else echo "WARN — already in use"; fi; \
	printf "  %-24s" "Port 8090 (embed):"; \
	if ! lsof -iTCP:8090 -sTCP:LISTEN >/dev/null 2>&1; then echo "OK (free)"; else echo "WARN — already in use"; fi; \
	SANDBOX=$$(python3 -c "import yaml; print(yaml.safe_load(open('stilltent.yml')).get('sandbox',{}).get('provider','none'))" 2>/dev/null || echo "none"); \
	if [ "$$SANDBOX" = "daytona" ]; then \
		printf "  %-24s" "DAYTONA_API_KEY:"; \
		if [ -n "$${DAYTONA_API_KEY:-}" ]; then echo "OK (set)"; else echo "FAIL — required when sandbox.provider=daytona"; FAIL=1; fi; \
	fi; \
	MEMORY=$$(python3 -c "import yaml; print(yaml.safe_load(open('stilltent.yml')).get('memory',{}).get('backend','mem9'))" 2>/dev/null || echo "mem9"); \
	if [ "$$MEMORY" = "supermemory" ]; then \
		printf "  %-24s" "SUPERMEMORY_API_KEY:"; \
		if [ -n "$${SUPERMEMORY_API_KEY:-}" ]; then echo "OK (set)"; else echo "FAIL — required when memory.backend=supermemory"; FAIL=1; fi; \
	fi; \
	echo "==="; \
	if [ $$FAIL -ne 0 ]; then echo "Preflight FAILED — fix the above issues."; exit 1; \
	else echo "All preflight checks passed."; fi

# Force rebuild all images with no cache
rebuild: generate
	@docker compose build --no-cache

# Clear metrics and unpause
reset-metrics:
	@echo '{}' > workspace/metrics.json
	@rm -f workspace/PAUSE
	@echo "Metrics cleared and PAUSE removed."

# Scan repo for leaked secrets
scan-secrets:
	@if command -v gitleaks >/dev/null 2>&1; then \
		echo "Scanning working tree..."; \
		gitleaks detect --source . --config .gitleaks.toml --no-git; \
		echo "Scanning git history..."; \
		gitleaks detect --source . --config .gitleaks.toml; \
	else \
		echo "gitleaks not found. Install with: brew install gitleaks"; \
		echo "Running built-in pattern scan instead..."; \
		bash scripts/pre-commit-secrets.sh; \
	fi

# Validate stilltent.yml schema
validate-config:
	@python3 core/validate.py

# ── Extras ────────────────────────────────────────────────────────

# Initialize TiDB databases and schema
init-db:
	@echo "Waiting for TiDB to be healthy..."
	@for i in $$(seq 1 30); do \
		if $(MYSQL_BIN) -h 127.0.0.1 -P 4000 -u root -e "SELECT 1" >/dev/null 2>&1; then \
			echo "TiDB is ready."; \
			break; \
		fi; \
		if [ $$i -eq 30 ]; then \
			echo "ERROR: TiDB did not become healthy."; \
			exit 1; \
		fi; \
		echo "  Attempt $$i/30 — waiting 2s..."; \
		sleep 2; \
	done
	@echo "Initializing TiDB databases..."
	@$(MYSQL_BIN) -h 127.0.0.1 -P 4000 -u root < scripts/init-tidb.sql
	@echo "Done."

# Show container status
status:
	@docker compose ps

# Restart a specific service (usage: make restart SVC=orchestrator)
restart:
	@docker compose restart $(SVC)

# Run monitoring dashboard
monitor:
	@bash scripts/monitor.sh

# Install git pre-commit hook for secret detection
install-hooks:
	@cp scripts/pre-commit-secrets.sh .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "Pre-commit secret scanner installed."

# ── Claude Code Agent ─────────────────────────────────────────────

setup-claude:
	@bash scripts/vps-install-claude.sh

dev-loop:
	@bash scripts/dev-loop.sh

dev-loop-once:
	@bash scripts/dev-loop.sh --once

dev-loop-opus:
	@bash scripts/dev-loop.sh --model opus

dev-loop-sonnet:
	@bash scripts/dev-loop.sh --model sonnet

dev-logs:
	@ls -t scripts/loop-logs/*.log 2>/dev/null | head -5 | xargs -I{} sh -c 'echo "=== {} ===" && tail -20 "{}" && echo ""'

dev-stats:
	@echo "=== Dev Loop Stats ==="
	@echo "Total iterations: $$(ls scripts/loop-logs/*.log 2>/dev/null | wc -l | tr -d ' ')"
	@echo "Last 10 commits:"
	@git log --oneline -10

dev-clean-branches:
	@echo "Deleting merged agent branches..."
	@git branch -r | grep "origin/agent/" | sed 's|origin/||' | xargs -I{} git push origin --delete "{}" 2>/dev/null || true
	@echo "Done."
