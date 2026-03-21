#!/usr/bin/env bash
set -euo pipefail

source .env 2>/dev/null || true

echo "=== stilltent health check ==="
echo ""

# 1. TiDB (the container has no mysql client — use a TCP port check)
echo -n "TiDB................... "
if bash -c "echo > /dev/tcp/127.0.0.1/${TIDB_PORT:-4000}" 2>/dev/null; then
    echo "✓ UP"
else
    echo "✗ DOWN — check: docker compose logs tidb"
fi

# 2. embed-service (local C-based embedding, 256-dim vectors)
echo -n "embed-service.......... "
if curl -sf "http://localhost:8090/health" > /dev/null 2>&1; then
    echo "✓ UP"
else
    echo "✗ DOWN — check: docker compose logs embed-service"
fi

# 3. mnemo-server (health endpoint)
echo -n "mnemo-server (mem9).... "
if curl -sf "http://localhost:${MEM9_API_PORT:-8082}/health" > /dev/null 2>&1; then
    echo "✓ UP"
else
    echo "✗ DOWN — check: docker compose logs mnemo-server"
fi

# 4. OpenClaw gateway
echo -n "OpenClaw gateway....... "
if curl -sf "http://localhost:${OPENCLAW_PORT:-18789}/healthz" > /dev/null 2>&1; then
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
