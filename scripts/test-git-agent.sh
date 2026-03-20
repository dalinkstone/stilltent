#!/usr/bin/env bash
# test-git-agent.sh — Verify the openclaw-gateway container can clone, commit,
# push, and create PRs on the target repository.
#
# Usage:
#   ./scripts/test-git-agent.sh
#
# Runs inside the openclaw-gateway container via docker compose exec.
# Requires: GITHUB_TOKEN and TARGET_REPO set in .env, stack running.

set -euo pipefail

CONTAINER="openclaw-gateway"
PASS=0
FAIL=0

pass() { ((PASS++)); printf "  \033[32mPASS\033[0m  %s\n" "$1"; }
fail() { ((FAIL++)); printf "  \033[31mFAIL\033[0m  %s\n" "$1"; }

echo "=== Git Agent Integration Tests ==="
echo ""

# ---------------------------------------------------------------------------
# 1. Clone — verify the agent can clone the target repo
# ---------------------------------------------------------------------------
echo "--- Test: git clone ---"
if docker compose exec "$CONTAINER" sh -c '
  rm -rf /workspace/test-repo
  git clone "https://github.com/${TARGET_REPO}.git" /workspace/test-repo
  cd /workspace/test-repo
  git log --oneline -5
' 2>&1; then
    pass "git clone"
else
    fail "git clone"
fi
echo ""

# ---------------------------------------------------------------------------
# 2. gh CLI — verify GitHub CLI authentication
# ---------------------------------------------------------------------------
echo "--- Test: gh CLI auth ---"
if docker compose exec "$CONTAINER" sh -c '
  export GH_TOKEN=${GITHUB_TOKEN}
  gh auth status
  gh repo view ${TARGET_REPO} --json name,defaultBranchRef
' 2>&1; then
    pass "gh CLI auth"
else
    fail "gh CLI auth"
fi
echo ""

# ---------------------------------------------------------------------------
# 3. Branch + push — verify the agent can commit and push a branch
# ---------------------------------------------------------------------------
echo "--- Test: branch creation and push ---"
if docker compose exec "$CONTAINER" sh -c '
  cd /workspace/test-repo
  git checkout -b agent/integration-test
  echo "integration-test $(date -u +%Y-%m-%dT%H:%M:%SZ)" > integration-test.txt
  git add integration-test.txt
  git commit -m "test: verify agent can push branches"
  git push origin agent/integration-test
' 2>&1; then
    pass "branch creation and push"
else
    fail "branch creation and push"
fi
echo ""

# ---------------------------------------------------------------------------
# 4. PR creation — verify the agent can open a pull request
# ---------------------------------------------------------------------------
echo "--- Test: PR creation ---"
if docker compose exec "$CONTAINER" sh -c '
  export GH_TOKEN=${GITHUB_TOKEN}
  cd /workspace/test-repo
  gh pr create \
    --base main \
    --head agent/integration-test \
    --title "test: verify agent can create PRs" \
    --body "Automated integration test PR. Safe to close."
' 2>&1; then
    pass "PR creation"
else
    fail "PR creation"
fi
echo ""

# ---------------------------------------------------------------------------
# 5. Cleanup — close the PR and delete the remote branch
# ---------------------------------------------------------------------------
echo "--- Test: cleanup ---"
if docker compose exec "$CONTAINER" sh -c '
  export GH_TOKEN=${GITHUB_TOKEN}
  cd /workspace/test-repo
  PR_NUM=$(gh pr list --head agent/integration-test --json number --jq ".[0].number")
  if [ -n "$PR_NUM" ]; then
    gh pr close "$PR_NUM" --delete-branch
  fi
  git checkout main
' 2>&1; then
    pass "cleanup (PR closed, branch deleted)"
else
    fail "cleanup"
fi

# Clean up local test clone
docker compose exec "$CONTAINER" sh -c 'rm -rf /workspace/test-repo' 2>/dev/null

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "=== Results ==="
printf "  Passed: %d  |  Failed: %d\n" "$PASS" "$FAIL"
echo ""

if [ "$FAIL" -gt 0 ]; then
    printf "\033[31mGit agent tests FAILED.\033[0m\n"
    exit 1
else
    printf "\033[32mAll git agent tests PASSED.\033[0m\n"
    exit 0
fi
