#!/bin/bash
# test-local.sh — Pull latest, build on macOS, report failures as GitHub issues
#
# Usage:
#   ./scripts/test-local.sh              # pull, build, smoke test, report
#   ./scripts/test-local.sh --skip-pull  # just build + report (if you already pulled)

set -uo pipefail

REPO="dalinkstone/stilltent"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../project" && pwd)"
PLATFORM="$(uname -ms)"
GO_VERSION="$(go version 2>/dev/null | awk '{print $3}' || echo 'unknown')"
ARCH="$(uname -m)"
CGO_ENABLED="$(go env CGO_ENABLED 2>/dev/null || echo 'unknown')"
CC="$(go env CC 2>/dev/null || echo 'unknown')"
TMPFILE=$(mktemp /tmp/tent-issue-XXXXXX.md)
trap 'rm -f "$TMPFILE"' EXIT

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# ── Helpers ──────────────────────────────────────────────────────────

# Given build output, extract source code around each error location
extract_source_context() {
    local build_output="$1"
    local files_and_lines
    files_and_lines=$(echo "$build_output" | grep -oE '[a-zA-Z0-9_/.-]+\.go:[0-9]+' | sort -u | head -10)

    for file_line in $files_and_lines; do
        local file="${file_line%%:*}"
        local line="${file_line##*:}"
        local full_path="$PROJECT_DIR/$file"

        if [[ -f "$full_path" ]]; then
            local start=$((line - 5))
            [[ $start -lt 1 ]] && start=1
            local end=$((line + 5))

            echo ""
            echo "### ${file} (line ${line})"
            echo '```go'
            awk -v s="$start" -v e="$end" -v t="$line" \
                'NR>=s && NR<=e { if(NR==t) printf ">>> %4d | %s\n", NR, $0; else printf "    %4d | %s\n", NR, $0 }' \
                "$full_path"
            echo '```'
        fi
    done
}

get_project_structure() {
    find "$PROJECT_DIR" -name "*.go" -type f | sed "s|$PROJECT_DIR/||" | sort
}

# ── Pull ─────────────────────────────────────────────────────────────

if [[ "${1:-}" != "--skip-pull" ]]; then
    echo -e "${YELLOW}Pulling latest...${NC}"
    git -C "$PROJECT_DIR/.." pull origin main --ff-only 2>&1 || true
    echo ""
fi

cd "$PROJECT_DIR"

# ── Build ────────────────────────────────────────────────────────────

echo -e "${YELLOW}Building tent...${NC}"
BUILD_OUTPUT=$(go build -o tent ./cmd/tent 2>&1) && BUILD_OK=true || BUILD_OK=false

echo -e "${YELLOW}Building tent-agent...${NC}"
AGENT_BUILD_OUTPUT=$(go build -o tent-agent ./cmd/tent-agent 2>&1) && AGENT_BUILD_OK=true || AGENT_BUILD_OK=false

if $BUILD_OK; then
    echo -e "${GREEN}tent build succeeded.${NC}"
else
    echo -e "${RED}Build FAILED:${NC}"
    echo "$BUILD_OUTPUT"
    echo ""

    SOURCE_CONTEXT=$(extract_source_context "$BUILD_OUTPUT")
    GOMOD_CONTENT=$(cat "$PROJECT_DIR/go.mod" 2>/dev/null || echo "go.mod not found")
    PROJECT_STRUCTURE=$(get_project_structure)

    echo -e "${YELLOW}Want to file a GitHub issue so the agent fixes this? [y/N]${NC}"
    read -r REPLY
    if [[ "$REPLY" =~ ^[Yy]$ ]]; then
        FIRST_ERROR=$(echo "$BUILD_OUTPUT" | grep -E '\.go:[0-9]+' | head -1)
        SHORT_DESC=$(echo "$FIRST_ERROR" | sed 's|.*/||' | head -c 70)

        cat > "$TMPFILE" <<ISSUE_BODY
## macOS build failure

The project does not compile on macOS. This is the #1 priority — fix these errors before doing anything else.

### Compiler output
\`\`\`
${BUILD_OUTPUT}
\`\`\`

### Source code at error locations
${SOURCE_CONTEXT}

### Platform
- OS: ${PLATFORM}
- Architecture: ${ARCH}
- Go: ${GO_VERSION}
- CGO enabled: ${CGO_ENABLED}
- CC: ${CC}

### go.mod
\`\`\`
${GOMOD_CONTENT}
\`\`\`

### Project file structure
\`\`\`
${PROJECT_STRUCTURE}
\`\`\`

### How to reproduce
\`\`\`bash
cd project && go build -o tent ./cmd/tent
\`\`\`

### Instructions for the agent
1. Read each error and the source code shown above carefully.
2. The source code lines marked with \`>>>\` are the exact lines causing the error.
3. Fix ALL the errors in a single PR — do not fix one and leave others broken.
4. If the error is in cgo code (C bindings), remember that Go cgo has specific syntax rules — you cannot use raw C expressions like \`C.-1\`, you must use \`C.int(-1)\`. You cannot use C-style if/for blocks inside Go functions.
5. After fixing, verify with \`GOOS=darwin go vet ./...\` on the Linux VPS to catch obvious issues.
6. Comment on this issue with the PR number when done.
ISSUE_BODY

        gh issue create --repo "$REPO" \
            --title "macOS build failure: $SHORT_DESC" \
            --label "agent-fix" \
            --body-file "$TMPFILE"
        echo -e "${GREEN}Issue filed. The agent will pick it up next iteration.${NC}"
    fi
fi

# ── tent-agent Build ─────────────────────────────────────────────────

if $AGENT_BUILD_OK; then
    echo -e "${GREEN}tent-agent build succeeded.${NC}"
else
    echo -e "${RED}tent-agent build FAILED:${NC}"
    echo "$AGENT_BUILD_OUTPUT"
fi

# ── Vet ──────────────────────────────────────────────────────────────

VET_OK=true
if $BUILD_OK; then
    echo ""
    echo -e "${YELLOW}Running go vet...${NC}"
    VET_OUTPUT=$(go vet ./... 2>&1) && VET_OK=true || VET_OK=false
    if $VET_OK; then
        echo -e "${GREEN}go vet clean.${NC}"
    else
        echo -e "${RED}go vet found issues:${NC}"
        echo "$VET_OUTPUT"

        SOURCE_CONTEXT=$(extract_source_context "$VET_OUTPUT")

        echo ""
        echo -e "${YELLOW}Want to file a GitHub issue for vet errors? [y/N]${NC}"
        read -r REPLY
        if [[ "$REPLY" =~ ^[Yy]$ ]]; then
            FIRST_ERROR=$(echo "$VET_OUTPUT" | grep -E '\.go:[0-9]+' | head -1)
            SHORT_DESC=$(echo "$FIRST_ERROR" | sed 's|.*/||' | head -c 70)

            cat > "$TMPFILE" <<ISSUE_BODY
## go vet failure on macOS

The project builds but \`go vet\` reports issues.

### Vet output
\`\`\`
${VET_OUTPUT}
\`\`\`

### Source code at error locations
${SOURCE_CONTEXT}

### Platform
- OS: ${PLATFORM}
- Architecture: ${ARCH}
- Go: ${GO_VERSION}

### How to reproduce
\`\`\`bash
cd project && go vet ./...
\`\`\`

### Instructions for the agent
Fix all vet warnings in a single PR. Comment on this issue with the PR number when done.
ISSUE_BODY

            gh issue create --repo "$REPO" \
                --title "macOS vet failure: $SHORT_DESC" \
                --label "agent-fix" \
                --body-file "$TMPFILE"
            echo -e "${GREEN}Issue filed.${NC}"
        fi
    fi
fi

# ── Unit Tests ───────────────────────────────────────────────────────

TEST_OK=true
if $BUILD_OK; then
    echo ""
    echo -e "${YELLOW}Running unit tests (short mode)...${NC}"
    TEST_OUTPUT=$(go test ./... -short -count=1 -timeout 120s 2>&1) && TEST_OK=true || TEST_OK=false

    # Count results
    TEST_PASS=$(echo "$TEST_OUTPUT" | grep -c '^ok' || true)
    TEST_FAIL=$(echo "$TEST_OUTPUT" | grep -c '^FAIL' || true)
    TEST_SKIP=$(echo "$TEST_OUTPUT" | grep -c '^\[no test' || true)

    if $TEST_OK; then
        echo -e "${GREEN}All tests passed (${TEST_PASS} packages).${NC}"
    else
        echo -e "${RED}Tests FAILED (${TEST_PASS} passed, ${TEST_FAIL} failed):${NC}"
        # Show only the failing packages and their output
        echo "$TEST_OUTPUT" | grep -E '^(FAIL|---\s*FAIL|panic:)' | head -20
        echo ""

        echo -e "${YELLOW}Want to file a GitHub issue for test failures? [y/N]${NC}"
        read -r REPLY
        if [[ "$REPLY" =~ ^[Yy]$ ]]; then
            # Trim output to avoid gigantic issues
            TRIMMED_TEST_OUTPUT=$(echo "$TEST_OUTPUT" | tail -80)

            cat > "$TMPFILE" <<ISSUE_BODY
## Unit test failures on macOS

\`go test ./... -short\` has failures.

### Test output (last 80 lines)
\`\`\`
${TRIMMED_TEST_OUTPUT}
\`\`\`

### Platform
- OS: ${PLATFORM}
- Architecture: ${ARCH}
- Go: ${GO_VERSION}

### How to reproduce
\`\`\`bash
cd project && go test ./... -short -count=1
\`\`\`

### Instructions for the agent
1. Fix all test failures — build errors in test files count too.
2. Do not delete or skip tests to make them pass. Fix the underlying code.
3. Run \`go test ./... -short\` to verify before submitting the PR.
ISSUE_BODY

            gh issue create --repo "$REPO" \
                --title "macOS test failures: ${TEST_FAIL} package(s)" \
                --label "agent-fix" \
                --body-file "$TMPFILE"
            echo -e "${GREEN}Issue filed.${NC}"
        fi
    fi
fi

# ── Smoke tests ──────────────────────────────────────────────────────

if $BUILD_OK; then
    echo ""
    echo -e "${CYAN}═══ Smoke Tests ═══${NC}"
    FAILURES=""

    # Test: tent --help
    echo -n "  tent --help: "
    if HELP_OUTPUT=$("$PROJECT_DIR/tent" --help 2>&1); then
        echo -e "${GREEN}OK${NC}"
    else
        echo -e "${RED}FAIL${NC}"
        FAILURES="${FAILURES}tent --help failed:
${HELP_OUTPUT}

"
    fi

    # Test: tent list
    echo -n "  tent list: "
    if LIST_OUTPUT=$("$PROJECT_DIR/tent" list 2>&1); then
        echo -e "${GREEN}OK${NC}"
    else
        echo -e "${RED}FAIL${NC}"
        FAILURES="${FAILURES}tent list failed:
${LIST_OUTPUT}

"
    fi

    # Test: tent create (expect an error, but not a crash)
    echo -n "  tent create testvm: "
    CREATE_OUTPUT=$("$PROJECT_DIR/tent" create testvm 2>&1) ; CREATE_EXIT=$?
    if [[ $CREATE_EXIT -eq 0 ]]; then
        echo -e "${GREEN}OK${NC}"
        "$PROJECT_DIR/tent" destroy testvm 2>/dev/null || true
    elif echo "$CREATE_OUTPUT" | grep -qi "segfault\|panic\|signal.*killed\|fatal error"; then
        echo -e "${RED}CRASH${NC}"
        FAILURES="${FAILURES}tent create testvm CRASHED (exit ${CREATE_EXIT}):
${CREATE_OUTPUT}

"
    else
        echo -e "${YELLOW}error (expected at this stage)${NC}"
        echo "    $(echo "$CREATE_OUTPUT" | head -1)"
    fi

    # Test: tent network status
    echo -n "  tent network status testvm: "
    NET_OUTPUT=$("$PROJECT_DIR/tent" network status testvm 2>&1); NET_EXIT=$?
    if [[ $NET_EXIT -eq 0 ]]; then
        echo -e "${GREEN}OK${NC}"
    elif echo "$NET_OUTPUT" | grep -qi "segfault\|panic\|signal.*killed\|fatal error"; then
        echo -e "${RED}CRASH${NC}"
        FAILURES="${FAILURES}tent network status CRASHED (exit ${NET_EXIT}):
${NET_OUTPUT}

"
    else
        echo -e "${YELLOW}error (expected)${NC}"
    fi

    # Test: tent compose exists
    echo -n "  tent compose (command exists): "
    COMPOSE_OUTPUT=$("$PROJECT_DIR/tent" compose 2>&1 || true)
    if echo "$COMPOSE_OUTPUT" | grep -qi "usage\|available\|compose\|sandbox"; then
        echo -e "${GREEN}OK${NC}"
    else
        echo -e "${RED}FAIL${NC}"
        FAILURES="${FAILURES}tent compose not recognized:
${COMPOSE_OUTPUT}

"
    fi

    # Report failures if any crashes happened
    if [[ -n "$FAILURES" ]]; then
        echo ""
        echo -e "${RED}Some smoke tests failed or crashed.${NC}"
        echo ""
        echo -e "${YELLOW}Want to file a GitHub issue for the failures? [y/N]${NC}"
        read -r REPLY
        if [[ "$REPLY" =~ ^[Yy]$ ]]; then
            cat > "$TMPFILE" <<ISSUE_BODY
## Runtime failures on macOS

The project builds, but some commands fail or crash at runtime.

### Failures
\`\`\`
${FAILURES}
\`\`\`

### Platform
- OS: ${PLATFORM}
- Architecture: ${ARCH}
- Go: ${GO_VERSION}

### All commands tested
- tent --help
- tent list
- tent create testvm
- tent network status testvm
- tent compose

### Instructions for the agent
1. Fix all crashes and panics first — these are highest priority.
2. Expected errors (like "VM not found") are fine at this stage.
3. Crashes, panics, and segfaults are NOT fine — fix these.
4. Comment on this issue with the PR number when done.
ISSUE_BODY

            gh issue create --repo "$REPO" \
                --title "macOS runtime: smoke test failures" \
                --label "agent-fix" \
                --body-file "$TMPFILE"
            echo -e "${GREEN}Issue filed.${NC}"
        fi
    else
        echo ""
        echo -e "${GREEN}All smoke tests passed.${NC}"
    fi
fi

# ── Summary ──────────────────────────────────────────────────────────

echo ""
echo -e "${CYAN}═══ Summary ═══${NC}"
EXIT_CODE=0
if $BUILD_OK; then
    echo -e "  tent build:       ${GREEN}OK${NC}"
else
    echo -e "  tent build:       ${RED}FAILED${NC}"
    EXIT_CODE=1
fi
if $AGENT_BUILD_OK; then
    echo -e "  tent-agent build: ${GREEN}OK${NC}"
else
    echo -e "  tent-agent build: ${RED}FAILED${NC}"
    EXIT_CODE=1
fi
if $BUILD_OK; then
    echo -e "  vet:              $($VET_OK && echo -e "${GREEN}OK${NC}" || echo -e "${RED}FAILED${NC}")"
    echo -e "  unit tests:       $($TEST_OK && echo -e "${GREEN}OK${NC}" || echo -e "${RED}FAILED${NC}")"
    $VET_OK || EXIT_CODE=1
    $TEST_OK || EXIT_CODE=1
fi
echo ""
exit $EXIT_CODE
