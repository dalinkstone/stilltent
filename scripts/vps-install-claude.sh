#!/bin/bash
# vps-install-claude.sh — Install and configure Claude Code on the VPS
#
# Run this ON the VPS (ssh in first):
#   bash scripts/vps-install-claude.sh
#
# What it does:
#   1. Installs Node.js 22 LTS (if not present)
#   2. Installs Claude Code CLI globally
#   3. Guides you through claude login
#   4. Verifies everything works
#   5. Sets up the dev-loop systemd service (optional)

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${YELLOW}=== Claude Code VPS Setup ===${NC}"
echo ""

# ── Step 1: Node.js ──────────────────────────────────────────────────

if command -v node &>/dev/null; then
    NODE_VERSION=$(node --version)
    echo -e "${GREEN}Node.js already installed: $NODE_VERSION${NC}"
else
    echo -e "${YELLOW}Installing Node.js 22 LTS...${NC}"
    curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
    apt-get install -y nodejs
    echo -e "${GREEN}Node.js installed: $(node --version)${NC}"
fi

# ── Step 2: Claude Code CLI ──────────────────────────────────────────

if command -v claude &>/dev/null; then
    CLAUDE_VERSION=$(claude --version 2>/dev/null || echo "unknown")
    echo -e "${GREEN}Claude Code already installed: $CLAUDE_VERSION${NC}"
else
    echo -e "${YELLOW}Installing Claude Code CLI...${NC}"
    npm install -g @anthropic-ai/claude-code
    echo -e "${GREEN}Claude Code installed: $(claude --version 2>/dev/null)${NC}"
fi

# ── Step 3: Go (needed to build the project) ─────────────────────────

if command -v go &>/dev/null; then
    GO_VERSION=$(go version | awk '{print $3}')
    echo -e "${GREEN}Go already installed: $GO_VERSION${NC}"
else
    echo -e "${YELLOW}Installing Go 1.23...${NC}"
    wget -q https://go.dev/dl/go1.23.8.linux-amd64.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf go1.23.8.linux-amd64.tar.gz
    rm go1.23.8.linux-amd64.tar.gz
    export PATH="/usr/local/go/bin:$PATH"
    echo 'export PATH="/usr/local/go/bin:$PATH"' >> ~/.bashrc
    echo -e "${GREEN}Go installed: $(go version | awk '{print $3}')${NC}"
fi

# ── Step 4: Authentication ───────────────────────────────────────────

echo ""
echo -e "${YELLOW}=== Authentication ===${NC}"
echo ""

# Check if already authenticated
if claude -p "echo hello" --max-turns 1 &>/dev/null 2>&1; then
    echo -e "${GREEN}Claude Code is already authenticated.${NC}"
else
    echo -e "${YELLOW}Claude Code needs to be authenticated.${NC}"
    echo ""
    echo "Run the following command and follow the URL it gives you:"
    echo ""
    echo -e "  ${GREEN}claude login${NC}"
    echo ""
    echo "It will display a URL — open that URL in your LOCAL browser,"
    echo "log in with your Anthropic account, and authorize the device."
    echo ""
    echo "After you've done that, re-run this script to verify."
    exit 0
fi

# ── Step 5: Verify ───────────────────────────────────────────────────

echo ""
echo -e "${YELLOW}=== Verification ===${NC}"

echo -n "  Claude Code CLI: "
if command -v claude &>/dev/null; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}MISSING${NC}"
fi

echo -n "  Authentication: "
VERIFY=$(claude -p "respond with exactly: AUTH_OK" --max-turns 1 2>/dev/null || echo "FAIL")
if echo "$VERIFY" | grep -q "AUTH_OK"; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}FAILED — run 'claude login' to authenticate${NC}"
fi

echo -n "  Go compiler: "
if command -v go &>/dev/null; then
    echo -e "${GREEN}OK ($(go version | awk '{print $3}'))${NC}"
else
    echo -e "${RED}MISSING${NC}"
fi

echo -n "  Git: "
if command -v git &>/dev/null; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${RED}MISSING${NC}"
fi

echo -n "  gh CLI: "
if command -v gh &>/dev/null; then
    echo -e "${GREEN}OK${NC}"
else
    echo -e "${YELLOW}MISSING — install with: apt install gh${NC}"
fi

echo ""
echo -e "${GREEN}=== Setup complete ===${NC}"
echo ""
echo "To start the development loop:"
echo "  ./scripts/dev-loop.sh"
echo ""
echo "To run a single iteration (test first):"
echo "  ./scripts/dev-loop.sh --once"
