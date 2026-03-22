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

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# ── Helpers ──────────────────────────────────────────────────────────

# Given a build error line like "internal/hypervisor/hvf/hvf_darwin.go:161:5: ...",
# extract the file path and line number, then show surrounding source code.
extract_source_context() {
    local build_output="$1"
    local context_blocks=""

    # Get unique file:line pairs from errors
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

            local snippet
            snippet=$(sed -n "${start},${end}p" "$full_path" | cat -n | sed "s/^ */$start\t/" | awk -v target="$line" -v start="$start" '{
                current_line = start + NR - 1
                if (current_line == target) printf ">>> %s\n", $0
                else printf "    %s\n", $0
            }')

            # Actually, simpler approach — just use sed with line numbers
            snippet=$(awk -v s="$start" -v e="$end" -v t="$line" 'NR>=s && NR<=e { if(NR==t) printf ">>> %4d | %s\n", NR, $0; else printf "    %4d | %s\n", NR, $0 }' "$full_path")

            context_blocks+="
### \`$file\` (line $line)
\`\`\`go
$snippet
\`\`\`
"
        fi
    done

    echo "$context_blocks"
}

# Get the directory structure of the project for context
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

if $BUILD_OK; then
    echo -e "${GREEN}Build succeeded.${NC}"
else
    echo -e "${RED}Build FAILED:${NC}"
    echo "$BUILD_OUTPUT"
    echo ""

    # Get source context for the errors
    SOURCE_CONTEXT=$(extract_source_context "$BUILD_OUTPUT")

    # Get go.mod for dependency context
    GOMOD_CONTENT=$(cat "$PROJECT_DIR/go.mod" 2>/dev/null || echo "go.mod not found")

    # Get project structure
    PROJECT_STRUCTURE=$(get_project_structure)

    echo -e "${YELLOW}Want to file a GitHub issue so the agent fixes this? [y/N]${NC}"
    read -r REPLY
    if [[ "$REPLY" =~ ^[Yy]$ ]]; then
        FIRST_ERROR=$(echo "$BUILD_OUTPUT" | grep -E "\.go:[0-9]+" | head -1)
        SHORT_DESC=$(echo "$FIRST_ERROR" | sed 's|.*/||' | head -c 70)

        gh issue create --repo "$REPO" \
            --title "macOS build failure: $SHORT_DESC" \
            --label "agent-fix" \
            --body "$(cat <<EOF
## macOS build failure

The project does not compile on macOS. This is the #1 priority — fix these errors before doing anything else.

### Compiler output
\`\`\`
$BUILD_OUTPUT
\`\`\`

### Source code at error locations
$SOURCE_CONTEXT

### Platform
- OS: $PLATFORM
- Architecture: $ARCH
- Go: $GO_VERSION
- CGO enabled: $(go env CGO_ENABLED 2>/dev/null || echo 'unknown')
- CC: $(go env CC 2>/dev/null || echo 'unknown')

### go.mod
\`\`\`
$GOMOD_CONTENT
\`\`\`

### Project file structure
\`\`\`
$PROJECT_STRUCTURE
\`\`\`

### How to reproduce
\`\`\`bash
cd project && go build -o tent ./cmd/tent
\`\`\`

### Instructions for the agent
1. Read each error and the source code shown above carefully.
2. The source code lines marked with \`>>>\` are the exact lines causing the error.
3. Fix ALL the errors in a single PR — do not fix one and leave others broken.
4. If the error is in cgo code (C bindings), remember that Go's cgo has specific syntax rules — you cannot use raw C expressions like \`C.-1\`, you must use \`C.int(-1)\`. You cannot use C-style \`if\` blocks inside Go functions.
5. After fixing, verify with \`GOOS=darwin go vet ./...\` on the Linux VPS to catch obvious issues.
6. Comment on this issue with the PR number when done.
EOF
)"
        echo -e "${GREEN}Issue filed. The agent will pick it up next iteration.${NC}"
    fi
fi

# ── Vet ──────────────────────────────────────────────────────────────

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
            FIRST_ERROR=$(echo "$VET_OUTPUT" | grep -E "\.go:[0-9]+" | head -1)
            SHORT_DESC=$(echo "$FIRST_ERROR" | sed 's|.*/||' | head -c 70)

            gh issue create --repo "$REPO" \
                --title "macOS vet failure: $SHORT_DESC" \
                --label "agent-fix" \
                --body "$(cat <<EOF
## go vet failure on macOS

The project builds but \`go vet\` reports issues.

### Vet output
\`\`\`
$VET_OUTPUT
\`\`\`

### Source code at error locations
$SOURCE_CONTEXT

### Platform
- OS: $PLATFORM
- Architecture: $ARCH
- Go: $GO_VERSION

### How to reproduce
\`\`\`bash
cd project && go vet ./...
\`\`\`

### Instructions for the agent
Fix all vet warnings in a single PR. Comment on this issue with the PR number when done.
EOF
)"
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
    HELP_OUTPUT=$("$PROJECT_DIR/tent" --help 2>&1) && echo -e "${GREEN}OK${NC}" || { echo -e "${RED}FAIL${NC}"; FAILURES+="tent --help failed:\n$HELP_OUTPUT\n\n"; }

    # Test: tent list
    echo -n "  tent list: "
    LIST_OUTPUT=$("$PROJECT_DIR/tent" list 2>&1) && echo -e "${GREEN}OK${NC}" || { echo -e "${RED}FAIL${NC}"; FAILURES+="tent list failed:\n$LIST_OUTPUT\n\n"; }

    # Test: tent create (expect a meaningful error, not a crash)
    echo -n "  tent create testvm: "
    CREATE_OUTPUT=$("$PROJECT_DIR/tent" create testvm 2>&1) ; CREATE_EXIT=$?
    if [[ $CREATE_EXIT -eq 0 ]]; then
        echo -e "${GREEN}OK${NC}"
        # Clean up
        "$PROJECT_DIR/tent" destroy testvm 2>/dev/null || true
    elif echo "$CREATE_OUTPUT" | grep -qi "segfault\|panic\|signal\|fatal"; then
        echo -e "${RED}CRASH${NC}"
        FAILURES+="tent create testvm CRASHED:\n$CREATE_OUTPUT\n\n"
    else
        echo -e "${YELLOW}error (expected at this stage)${NC}"
        echo "    $CREATE_OUTPUT" | head -3
    fi

    # Test: tent network status (new command)
    echo -n "  tent network status testvm: "
    NET_OUTPUT=$("$PROJECT_DIR/tent" network status testvm 2>&1); NET_EXIT=$?
    if [[ $NET_EXIT -eq 0 ]]; then
        echo -e "${GREEN}OK${NC}"
    elif echo "$NET_OUTPUT" | grep -qi "segfault\|panic\|signal\|fatal"; then
        echo -e "${RED}CRASH${NC}"
        FAILURES+="tent network status CRASHED:\n$NET_OUTPUT\n\n"
    else
        echo -e "${YELLOW}error (expected)${NC}"
    fi

    # Test: tent compose up with a test file
    echo -n "  tent compose (check command exists): "
    COMPOSE_OUTPUT=$("$PROJECT_DIR/tent" compose 2>&1 || true)
    if echo "$COMPOSE_OUTPUT" | grep -qi "usage\|available\|compose"; then
        echo -e "${GREEN}OK${NC}"
    else
        echo -e "${RED}FAIL${NC}"
        FAILURES+="tent compose not recognized:\n$COMPOSE_OUTPUT\n\n"
    fi

    # Report failures
    if [[ -n "$FAILURES" ]]; then
        echo ""
        echo -e "${RED}Some smoke tests failed or crashed.${NC}"
        echo ""
        echo -e "${YELLOW}Want to file a GitHub issue for the failures? [y/N]${NC}"
        read -r REPLY
        if [[ "$REPLY" =~ ^[Yy]$ ]]; then
            gh issue create --repo "$REPO" \
                --title "macOS runtime: smoke test failures" \
                --label "agent-fix" \
                --body "$(cat <<EOF
## Runtime failures on macOS

The project builds, but some commands fail or crash at runtime.

### Failures
\`\`\`
$(echo -e "$FAILURES")
\`\`\`

### Platform
- OS: $PLATFORM
- Architecture: $ARCH
- Go: $GO_VERSION

### All commands tested
- \`tent --help\`
- \`tent list\`
- \`tent create testvm\`
- \`tent network status testvm\`
- \`tent compose\`

### Instructions for the agent
1. Fix all crashes and panics first — these are highest priority.
2. Expected errors (like "VM not found") are fine at this stage.
3. Crashes, panics, and segfaults are NOT fine — fix these.
4. Comment on this issue with the PR number when done.
EOF
)"
            echo -e "${GREEN}Issue filed.${NC}"
        fi
    else
        echo ""
        echo -e "${GREEN}All smoke tests passed.${NC}"
    fi
fi

echo ""
echo -e "${CYAN}═══ Summary ═══${NC}"
echo "  Build: $($BUILD_OK && echo -e "${GREEN}OK${NC}" || echo -e "${RED}FAILED${NC}")"
$BUILD_OK && echo "  Vet:   $($VET_OK && echo -e "${GREEN}OK${NC}" || echo -e "${RED}FAILED${NC}")"
echo ""
