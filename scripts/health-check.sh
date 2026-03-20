#!/usr/bin/env bash
set -euo pipefail

source .env 2>/dev/null || true

echo "=== repokeeper health check ==="
echo ""

# 1. Ollama
echo -n "Ollama API............. "
if curl -sf "http://${OLLAMA_HOST:-localhost}:${OLLAMA_PORT:-11434}/api/tags" > /dev/null 2>&1; then
    echo "✓ UP"
else
    echo "✗ DOWN — is Ollama running on the host?"
fi

# 2. TiDB
echo -n "TiDB................... "
if docker compose exec -T tidb mysql -h 127.0.0.1 -P 4000 -u root -e "SELECT 1" > /dev/null 2>&1; then
    echo "✓ UP"
else
    echo "✗ DOWN — check: docker compose logs tidb"
fi

# 3. mnemo-server
echo -n "mnemo-server (mem9).... "
if curl -sf "http://localhost:${MEM9_API_PORT:-8082}/health" > /dev/null 2>&1; then
    echo "✓ UP"
else
    echo "✗ DOWN — check: docker compose logs mnemo-server"
fi

# 4. OpenClaw gateway
echo -n "OpenClaw gateway....... "
if curl -sf "http://localhost:${OPENCLAW_PORT:-3000}/health" > /dev/null 2>&1; then
    echo "✓ UP"
else
    echo "✗ DOWN — check: docker compose logs openclaw-gateway"
fi

# 5. GitHub connectivity
echo -n "GitHub API............. "
if curl -sf -H "Authorization: token ${GITHUB_TOKEN:-none}" "https://api.github.com/user" > /dev/null 2>&1; then
    echo "✓ AUTHENTICATED"
else
    echo "✗ FAILED — check GITHUB_TOKEN in .env"
fi

echo ""
echo "=== done ==="
