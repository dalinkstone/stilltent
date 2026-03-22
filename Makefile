# Load .env vars so Make targets (and their subprocesses) see them.
-include .env
export

.PHONY: up down logs logs-follow restart status health bootstrap clean pause resume stats test-mem9 test-openclaw init-db install-hooks scan-secrets validate-workspace preflight preflight-stack monitor deploy cost ssh-tunnel rebuild reset-metrics build-all start test-run setup-claude dev-loop dev-loop-once

# Start all services (initializes DB on first run if needed)
up:
	@echo "Starting TiDB and embed-service..."
	@docker compose up -d tidb embed-service
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
	@docker compose up -d tidb embed-service mnemo-server openclaw-gateway

# Stop all services
down:
	docker compose down

# Follow logs for all services
logs:
	docker compose logs -f

# Restart a specific service (usage: make restart SVC=orchestrator)
restart:
	docker compose restart $(SVC)

# Show container status
status:
	docker compose ps

# Show clear per-service health status
health:
	@echo "=== Service Health ==="
	@svc_check() { \
		SVC="$$1"; PORT="$$2"; LABEL="$$3"; \
		printf "%-16s" "$$LABEL:"; \
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
	svc_check openclaw-gateway 18789 "openclaw"; \
	svc_check orchestrator    -     "orchestrator"; \
	echo "=== Embedding (required) ==="; \
	curl -sf http://localhost:8090/health > /dev/null \
		&& echo "embed-service API: OK" \
		|| echo "embed-service API: UNREACHABLE (vector search will not work)"; \
	echo "=== OpenRouter API ==="; \
	curl -sf -H "Authorization: Bearer $$OPENROUTER_API_KEY" \
		https://openrouter.ai/api/v1/models | head -c 200 > /dev/null \
		&& echo "OpenRouter API: OK" \
		|| echo "OpenRouter API: UNREACHABLE (check OPENROUTER_API_KEY)"

# First-time setup: clone repo, initialize mem9 tenant, send first prompt
bootstrap:
	@bash scripts/bootstrap.sh

# Start the orchestrator (autonomous mode — run AFTER bootstrap + review)
start:
	@echo "Starting orchestrator (autonomous mode)..."
	docker compose up -d orchestrator

# Single-iteration test run — starts orchestrator, runs 1 iteration, then stops
test-run:
	@echo "Running single test iteration..."
	MAX_ITERATIONS=1 docker compose run --rm -e MAX_ITERATIONS=1 orchestrator
	@echo "Test iteration complete. Check logs: make logs"

# Full teardown: stop containers, remove volumes, delete cloned repo
clean:
	docker compose down -v
	rm -rf workspace/repo

# Pause the agent (creates pause file that orchestrator checks)
pause:
	touch workspace/PAUSE
	@echo "Agent paused. Remove workspace/PAUSE to resume."

# Resume the agent
resume:
	rm -f workspace/PAUSE
	@echo "Agent resumed."

# Show iteration count and success rate from orchestrator logs
stats:
	@python3 orchestrator/stats.py

# Smoke test the mem9 API
test-mem9:
	@python3 scripts/test-mem9.py

# Smoke test the OpenClaw gateway
test-openclaw:
	@python3 scripts/test-openclaw.py

# Install git pre-commit hook for secret detection
install-hooks:
	@cp scripts/pre-commit-secrets.sh .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "Pre-commit secret scanner installed."

# Scan repo for leaked secrets (requires: brew install gitleaks)
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

# Initialize TiDB databases and schema (run once after first tidb startup)
# Requires: brew install mysql-client@8.4
MYSQL_BIN ?= $(shell which mysql 2>/dev/null || echo /opt/homebrew/opt/mysql-client@8.4/bin/mysql)
init-db:
	@echo "Waiting for TiDB to be healthy..."
	@for i in $$(seq 1 30); do \
		if $(MYSQL_BIN) -h 127.0.0.1 -P 4000 -u root -e "SELECT 1" >/dev/null 2>&1; then \
			echo "TiDB is ready."; \
			break; \
		fi; \
		if [ $$i -eq 30 ]; then \
			echo "ERROR: TiDB did not become healthy after 30 attempts."; \
			exit 1; \
		fi; \
		echo "  Attempt $$i/30 — TiDB not ready, waiting 2s..."; \
		sleep 2; \
	done
	@echo "Initializing TiDB databases..."
	$(MYSQL_BIN) -h 127.0.0.1 -P 4000 -u root < scripts/init-tidb.sql
	@echo "Verifying embedding infrastructure..."
	@echo "  VECTOR(256) column: OK (created in schema)"
	@echo "  embed-service: checking..."
	@if curl -sf http://127.0.0.1:8090/health > /dev/null 2>&1; then \
		echo "  embed-service: OK (running on port 8090)"; \
	else \
		echo "  embed-service: NOT RUNNING — start with 'make up' first"; \
		echo "  WARNING: Embedding is required. Vector search will not work without embed-service."; \
	fi
	@echo "Setting up HNSW vector index (if TiFlash available)..."
	@if $(MYSQL_BIN) -h 127.0.0.1 -P 4000 -u root -e \
		"ALTER TABLE mnemos_tenant.memories SET TIFLASH REPLICA 1" 2>/dev/null; then \
		echo "  TiFlash replica configured."; \
		$(MYSQL_BIN) -h 127.0.0.1 -P 4000 -u root -e \
			"ALTER TABLE mnemos_tenant.memories ADD VECTOR INDEX IF NOT EXISTS idx_vec_embedding (embedding) USING HNSW COMMENT 'distance_metric=cosine'" 2>/dev/null \
			&& echo "  HNSW vector index created (accelerated ANN search)." \
			|| echo "  HNSW index deferred (TiFlash syncing) — vector search uses brute-force scan until ready."; \
	else \
		echo "  TiFlash not available — vector search uses brute-force scan (still functional)."; \
	fi
	@echo "Done. Databases, tables, and embedding infrastructure ready."

# Validate workspace files
validate-workspace:
	@bash scripts/validate-workspace.sh

# Final pre-flight check: start stack, run all checks, stop orchestrator
preflight-stack:
	@bash scripts/preflight.sh

# Pre-flight validation: ensure all prerequisites are met before starting
preflight:
	@echo "=== Preflight Checks ==="
	@FAIL=0; \
	echo -n "Docker daemon:    "; \
	if docker info >/dev/null 2>&1; then echo "OK"; else echo "FAIL — Docker is not running"; FAIL=1; fi; \
	echo -n ".env file:        "; \
	if [ -f .env ]; then echo "OK"; else echo "FAIL — .env not found (cp .env.example .env)"; FAIL=1; fi; \
	echo -n "OPENROUTER_API_KEY: "; \
	if [ -n "$${OPENROUTER_API_KEY:-}" ]; then echo "OK (set)"; else echo "FAIL — not set in .env"; FAIL=1; fi; \
	echo -n "GITHUB_TOKEN:     "; \
	if [ -n "$${GITHUB_TOKEN:-}" ]; then echo "OK (set)"; else echo "FAIL — not set in .env"; FAIL=1; fi; \
	echo -n "TARGET_REPO:      "; \
	if [ -n "$${TARGET_REPO:-}" ]; then echo "OK ($$TARGET_REPO)"; else echo "FAIL — not set in .env"; FAIL=1; fi; \
	echo -n "Port 4000 (TiDB): "; \
	if ! lsof -iTCP:4000 -sTCP:LISTEN >/dev/null 2>&1; then echo "OK (free)"; else echo "WARN — already in use"; fi; \
	echo -n "Port 8090 (embed): "; \
	if ! lsof -iTCP:8090 -sTCP:LISTEN >/dev/null 2>&1; then echo "OK (free)"; else echo "WARN — already in use"; fi; \
	echo "==="; \
	if [ $$FAIL -ne 0 ]; then echo "Preflight FAILED — fix the above issues before running 'make up'."; exit 1; \
	else echo "All preflight checks passed."; fi

# Run the monitoring dashboard
monitor:
	@bash scripts/monitor.sh

# Print DigitalOcean deployment instructions
deploy:
	@echo "=== DigitalOcean Deployment ==="
	@echo ""
	@echo "1. Create a Droplet (Ubuntu 24.04, 8GB RAM / 2 vCPU / 160GB disk):"
	@echo "   doctl compute droplet create stilltent \\"
	@echo "     --image ubuntu-24-04-x64 --size s-2vcpu-8gb-intel \\"
	@echo "     --region nyc1 --ssh-keys \$$SSH_KEY_ID"
	@echo ""
	@echo "2. SSH into the Droplet:"
	@echo "   ssh root@\$$DROPLET_IP"
	@echo ""
	@echo "3. Install Docker:"
	@echo "   curl -fsSL https://get.docker.com | sh"
	@echo ""
	@echo "4. Clone and configure:"
	@echo "   git clone <repo-url> ~/stilltent && cd ~/stilltent"
	@echo "   cp .env.example .env && nano .env"
	@echo ""
	@echo "5. Start the stack:"
	@echo "   make bootstrap"

# Real-time cost report from workspace/metrics.json
cost:
	@python3 -c "import json; m=json.load(open('workspace/metrics.json')); print(f'Iterations: {m.get(\"total_iterations\",0)}'); print(f'Spend: \$${m.get(\"total_spend_usd\",0):.4f}'); print(f'Projected: \$${m.get(\"projected_total_usd\",0):.2f}'); print(f'Budget: \$${m.get(\"budget_remaining_usd\",0):.2f} remaining')"

# Print SSH command to connect to VPS
ssh-tunnel:
	@echo "ssh root@$$DROPLET_IP"

# Follow logs with tail context (last 50 lines per service)
logs-follow:
	docker compose logs -f --tail=50

# Force rebuild all images with no cache (use after code changes)
rebuild:
	docker compose build --no-cache

# Build all images in parallel
build-all:
	docker compose build --parallel

# Reset metrics and unpause the agent
reset-metrics:
	@echo '{}' > workspace/metrics.json
	@rm -f workspace/PAUSE
	@echo "Metrics cleared and PAUSE removed."

# Install Claude Code on the VPS (run on VPS)
setup-claude:
	@bash scripts/vps-install-claude.sh

# Run Claude Code dev loop (forever)
dev-loop:
	@bash scripts/dev-loop.sh

# Run a single Claude Code iteration (for testing)
dev-loop-once:
	@bash scripts/dev-loop.sh --once
