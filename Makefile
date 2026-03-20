# Load .env vars so Make targets (and their subprocesses) see them.
-include .env
export

.PHONY: up down logs restart status health bootstrap clean pause resume stats test-mem9 test-openclaw init-db install-hooks scan-secrets validate-workspace preflight

# Start all services
up:
	docker compose up -d

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

# Run health checks against all services
health:
	@bash scripts/health-check.sh

# First-time setup: clone repo, initialize mem9 tenant, send first prompt
bootstrap:
	@bash scripts/bootstrap.sh

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
MYSQL_BIN ?= /opt/homebrew/opt/mysql-client@8.4/bin/mysql
init-db:
	@echo "Initializing TiDB databases..."
	$(MYSQL_BIN) -h 127.0.0.1 -P 4000 -u root < scripts/init-tidb.sql
	@echo "Done. Databases and tables created."

# Validate workspace files
validate-workspace:
	@bash scripts/validate-workspace.sh

# Final pre-flight check: start stack, run all checks, stop orchestrator
preflight:
	@bash scripts/preflight.sh
