#!/bin/bash
# test-local.sh — Pull latest, build on macOS, report failures as GitHub issues
#
# Usage:
#   ./scripts/test-local.sh          # pull, build, report
#   ./scripts/test-local.sh --skip-pull   # just build + report (if you already pulled)

set -euo pipefail

REPO="dalinkstone/stilltent"
PROJECT_DIR="$(cd "$(dirname "$0")/../project" && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Pull unless --skip-pull
if [[ "${1:-}" != "--skip-pull" ]]; then
    echo -e "${YELLOW}Pulling latest...${NC}"
    git -C "$PROJECT_DIR/.." pull origin main --ff-only 2>&1 || true
fi

cd "$PROJECT_DIR"

echo -e "${YELLOW}Building tent...${NC}"
BUILD_OUTPUT=$(go build -o tent ./cmd/tent 2>&1) && BUILD_OK=true || BUILD_OK=false

if $BUILD_OK; then
    echo -e "${GREEN}Build succeeded.${NC}"
    echo ""
    echo "Try running:"
    echo "  cd $PROJECT_DIR && ./tent --help"
    echo "  ./tent create test --from ubuntu:22.04"
    echo "  ./tent list"
    echo ""

    # Also try go vet
    echo -e "${YELLOW}Running go vet...${NC}"
    VET_OUTPUT=$(go vet ./... 2>&1) && VET_OK=true || VET_OK=false
    if $VET_OK; then
        echo -e "${GREEN}go vet clean.${NC}"
    else
        echo -e "${RED}go vet found issues:${NC}"
        echo "$VET_OUTPUT"
    fi
else
    echo -e "${RED}Build FAILED:${NC}"
    echo "$BUILD_OUTPUT"
    echo ""

    # Ask if user wants to file an issue
    echo -e "${YELLOW}Want to file a GitHub issue so the agent fixes this? [y/N]${NC}"
    read -r REPLY
    if [[ "$REPLY" =~ ^[Yy]$ ]]; then
        # Extract first error file and line
        FIRST_ERROR=$(echo "$BUILD_OUTPUT" | grep -E "\.go:[0-9]+" | head -1)
        SHORT_DESC=$(echo "$FIRST_ERROR" | sed 's|.*/||' | head -c 70)

        gh issue create --repo "$REPO" \
            --title "macOS build failure: $SHORT_DESC" \
            --label "agent-fix" \
            --body "$(cat <<EOF
## macOS build failure

\`\`\`
$BUILD_OUTPUT
\`\`\`

## Platform
$(uname -ms), Go $(go version | awk '{print $3}')

## How to reproduce
\`\`\`bash
cd project && go build -o tent ./cmd/tent
\`\`\`

## Priority
This blocks all macOS testing. Fix the compilation errors first, then move on to features.
EOF
)"
        echo -e "${GREEN}Issue filed. The agent will pick it up next iteration.${NC}"
    fi
fi

# If build succeeded, try a smoke test
if $BUILD_OK; then
    echo ""
    echo -e "${YELLOW}Running smoke test...${NC}"
    SMOKE_OUTPUT=$("$PROJECT_DIR/tent" list 2>&1) && SMOKE_OK=true || SMOKE_OK=false
    if $SMOKE_OK; then
        echo -e "${GREEN}tent list: OK${NC}"
        echo "$SMOKE_OUTPUT"
    else
        echo -e "${RED}tent list failed:${NC}"
        echo "$SMOKE_OUTPUT"
        echo ""
        echo -e "${YELLOW}Want to file a GitHub issue for this runtime error? [y/N]${NC}"
        read -r REPLY
        if [[ "$REPLY" =~ ^[Yy]$ ]]; then
            gh issue create --repo "$REPO" \
                --title "macOS runtime: tent list fails" \
                --label "agent-fix" \
                --body "$(cat <<EOF
## Runtime failure on macOS

Command: \`tent list\`

\`\`\`
$SMOKE_OUTPUT
\`\`\`

## Platform
$(uname -ms), Go $(go version | awk '{print $3}')
EOF
)"
            echo -e "${GREEN}Issue filed.${NC}"
        fi
    fi
fi
