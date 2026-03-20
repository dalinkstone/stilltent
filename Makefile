.PHONY: up down logs restart status health bootstrap clean pause resume stats

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
