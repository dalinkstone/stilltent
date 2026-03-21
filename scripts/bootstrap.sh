#!/usr/bin/env bash
set -euo pipefail

echo "========================================="
echo "  stilltent bootstrap"
echo "========================================="
echo ""

# Load config
if [ ! -f .env ]; then
    echo "ERROR: .env file not found. Copy .env.example to .env and fill in values."
    exit 1
fi
set -a
source .env
set +a

# Step 1: Verify services
echo "[1/6] Checking service health..."
bash scripts/health-check.sh
echo ""
read -p "All services healthy? Press Enter to continue, Ctrl+C to abort."

# Step 2: Clone target repo
echo ""
echo "[2/6] Cloning target repository..."
# scripts/ is not mounted into the container, so pipe clone-target-repo.sh in.
docker compose exec -T openclaw-gateway bash -c '
    REPO_DIR="/workspace/repo"
    GITHUB_TOKEN="${GITHUB_TOKEN:?GITHUB_TOKEN must be set}"
    TARGET_REPO="${TARGET_REPO:?TARGET_REPO must be set}"

    if [ -d "$REPO_DIR/.git" ]; then
        echo "Repository already cloned at $REPO_DIR"
        echo "Pulling latest changes..."
        cd "$REPO_DIR"
        git checkout main
        git pull origin main
        echo "Updated to $(git rev-parse --short HEAD)"
    else
        echo "Cloning $TARGET_REPO into $REPO_DIR..."
        git clone "https://${GITHUB_TOKEN}@github.com/${TARGET_REPO}.git" "$REPO_DIR"
        cd "$REPO_DIR"
        echo "Cloned at $(git rev-parse --short HEAD)"
    fi

    cd "$REPO_DIR"
    git remote set-url origin "https://${GITHUB_TOKEN}@github.com/${TARGET_REPO}.git"
    echo "Target repository ready."
'
echo "Repository ready."

# Step 3: Initialize mem9 tenant and store seed memory
echo ""
echo "[3/6] Initializing memory system..."
SEED_MEMORY="stilltent initialized. Target repository: ${TARGET_REPO}. This is the first iteration. No prior history exists. Start by reading the repository README and following SKILL.md Phase 2 (Assess)."

curl -sf -X POST "http://localhost:${MEM9_API_PORT:-8082}/v1alpha2/mem9s/memories" \
    -H "Content-Type: application/json" \
    -H "X-API-Key: ${MEM9_API_KEY:-stilltent-local-dev-key}" \
    -H "X-Mnemo-Agent-Id: stilltent-agent" \
    -d "{
        \"content\": \"${SEED_MEMORY}\",
        \"tags\": [\"bootstrap\"],
        \"metadata\": {\"source\": \"bootstrap\", \"target_repo\": \"${TARGET_REPO}\"}
    }" && echo " Seed memory created." || echo "WARNING: Could not create seed memory. Check mem9 API. Continuing anyway."

# Step 4: Verify workspace files are accessible inside the container
echo ""
echo "[4/6] Verifying workspace..."
docker compose exec -T openclaw-gateway sh -c '
    echo "SKILL.md:    $([ -f /workspace/SKILL.md ] && echo OK || echo MISSING)"
    echo "AGENTS.md:   $([ -f /workspace/AGENTS.md ] && echo OK || echo MISSING)"
    echo "LEARNING.md: $([ -f /workspace/LEARNING.md ] && echo OK || echo MISSING)"
    echo "Target repo: $([ -d /workspace/repo/.git ] && echo OK || echo MISSING)"
'

# Step 5: Run a single test iteration
echo ""
echo "[5/6] Running first iteration (this may take several minutes)..."
echo "Sending trigger prompt to OpenClaw gateway..."

PROMPT='Read and follow /workspace/SKILL.md. This is iteration 1 (bootstrap). Execute the complete iteration protocol (Phase 1 through Phase 7). When finished, respond with a JSON summary: {"iteration": 1, "action_type": "bootstrap", "summary": "<description>", "result": "<success|failure>", "pr_number": null, "merged": null, "confidence": 0.0, "error": null}'

# Escape the prompt for JSON using python
PROMPT_JSON=$(python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))' <<< "$PROMPT")

RESPONSE=$(curl -sf --max-time "${ITERATION_TIMEOUT:-600}" \
    -X POST "http://localhost:${OPENCLAW_PORT:-18789}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${OPENCLAW_GATEWAY_TOKEN}" \
    -d "{
        \"model\": \"openclaw:main\",
        \"messages\": [{\"role\": \"user\", \"content\": ${PROMPT_JSON}}]
    }" 2>&1) || true

echo ""
echo "========================================="
echo "  FIRST ITERATION RESULT"
echo "========================================="
echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"
echo "========================================="

# Step 6: Prompt operator review
echo ""
echo "[6/6] Review"
echo ""
echo "Check the following before enabling autonomous mode:"
echo "  1. Review the response above — did the agent understand SKILL.md?"
echo "  2. Check the target repo for new branches/PRs: gh pr list"
echo "  3. Check mem9 for stored memories"
echo "  4. Check orchestrator log: cat workspace/orchestrator.log"
echo ""
echo "If everything looks good, start autonomous operation with:"
echo "  docker compose up -d orchestrator"
echo ""
echo "Monitor with:"
echo "  make logs          # follow all logs"
echo "  make stats         # show iteration metrics"
echo "  make health        # check service status"
echo "  make pause         # emergency stop"
echo ""
echo "Bootstrap complete."
