#!/bin/bash
# vps-install-claude.sh — Install and configure Claude Code on the VPS
#
# Run this ON the VPS after SSH-ing in:
#   cd ~/stilltent && bash scripts/vps-install-claude.sh
#
# Idempotent — safe to run multiple times.
# After running, you need to do TWO things:
#   1. claude login    (authenticate Claude Code — opens URL in your local browser)
#   2. gh auth login   (authenticate GitHub CLI — paste a token)
#
# Then start the loop:
#   ./scripts/dev-loop.sh --once    (test single iteration)
#   ./scripts/dev-loop.sh           (run forever)

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

echo -e "${CYAN}=== Claude Code VPS Setup ===${NC}"
echo ""

# ── Node.js ──────────────────────────────────────────────────────────

echo -n "Node.js: "
if command -v node &>/dev/null; then
    echo -e "${GREEN}$(node --version)${NC}"
else
    echo -e "${YELLOW}installing...${NC}"
    curl -fsSL https://deb.nodesource.com/setup_22.x 2>/dev/null | bash - >/dev/null 2>&1
    apt-get install -y -qq nodejs >/dev/null 2>&1
    echo -e "${GREEN}$(node --version)${NC}"
fi

# ── Claude Code CLI ──────────────────────────────────────────────────

echo -n "Claude Code: "
if command -v claude &>/dev/null; then
    echo -e "${GREEN}$(claude --version 2>/dev/null || echo 'installed')${NC}"
else
    echo -e "${YELLOW}installing...${NC}"
    npm install -g @anthropic-ai/claude-code >/dev/null 2>&1
    echo -e "${GREEN}$(claude --version 2>/dev/null || echo 'installed')${NC}"
fi

# ── Go ───────────────────────────────────────────────────────────────

echo -n "Go: "
if command -v go &>/dev/null; then
    echo -e "${GREEN}$(go version | awk '{print $3}')${NC}"
else
    echo -e "${YELLOW}installing...${NC}"
    wget -q https://go.dev/dl/go1.23.8.linux-amd64.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf go1.23.8.linux-amd64.tar.gz
    rm go1.23.8.linux-amd64.tar.gz
    export PATH="/usr/local/go/bin:$PATH"
    if ! grep -q '/usr/local/go/bin' ~/.bashrc 2>/dev/null; then
        echo 'export PATH="/usr/local/go/bin:$PATH"' >> ~/.bashrc
    fi
    echo -e "${GREEN}$(go version | awk '{print $3}')${NC}"
fi

# ── gh CLI ───────────────────────────────────────────────────────────

echo -n "gh CLI: "
if command -v gh &>/dev/null; then
    echo -e "${GREEN}$(gh --version | head -1)${NC}"
else
    echo -e "${YELLOW}installing...${NC}"
    (type -p wget >/dev/null || apt-get install -y -qq wget >/dev/null 2>&1) \
        && mkdir -p -m 755 /etc/apt/keyrings \
        && wget -qO- https://cli.github.com/packages/githubcli-archive-keyring.gpg | tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null \
        && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
        && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | tee /etc/apt/sources.list.d/github-cli-stable.list > /dev/null \
        && apt-get update -qq >/dev/null 2>&1 \
        && apt-get install -y -qq gh >/dev/null 2>&1
    echo -e "${GREEN}$(gh --version | head -1)${NC}"
fi

# ── tmux (for running dev-loop in background) ────────────────────────

echo -n "tmux: "
if command -v tmux &>/dev/null; then
    echo -e "${GREEN}installed${NC}"
else
    echo -e "${YELLOW}installing...${NC}"
    apt-get install -y -qq tmux >/dev/null 2>&1
    echo -e "${GREEN}installed${NC}"
fi

# ── Git identity ─────────────────────────────────────────────────────

echo -n "Git identity: "
GIT_NAME=$(git config --global user.name 2>/dev/null || echo "")
if [[ -n "$GIT_NAME" ]]; then
    echo -e "${GREEN}$GIT_NAME <$(git config --global user.email)>${NC}"
else
    git config --global user.name "stilltent-agent"
    git config --global user.email "agent@stilltent.local"
    echo -e "${GREEN}stilltent-agent <agent@stilltent.local>${NC}"
fi

# ── Script permissions ───────────────────────────────────────────────

chmod +x scripts/dev-loop.sh scripts/test-local.sh 2>/dev/null || true

echo ""
echo -e "${CYAN}=== Installation complete ===${NC}"
echo ""

# ── Auth status checks ───────────────────────────────────────────────

NEEDS_AUTH=false

echo -e "${YELLOW}=== Authentication Status ===${NC}"
echo ""

# Check Claude auth
echo -n "Claude Code auth: "
if claude -p "say OK" --max-turns 1 2>/dev/null | grep -qi "OK" 2>/dev/null; then
    echo -e "${GREEN}authenticated${NC}"
else
    echo -e "${RED}not authenticated${NC}"
    NEEDS_AUTH=true
fi

# Check gh auth
echo -n "GitHub CLI auth: "
if gh auth status &>/dev/null; then
    echo -e "${GREEN}authenticated${NC}"
else
    echo -e "${RED}not authenticated${NC}"
    NEEDS_AUTH=true
fi

echo ""

if $NEEDS_AUTH; then
    echo -e "${YELLOW}=== Next Steps ===${NC}"
    echo ""
    echo "You need to authenticate before running the dev loop."
    echo "Run these commands one at a time:"
    echo ""

    if ! claude -p "say OK" --max-turns 1 2>/dev/null | grep -qi "OK" 2>/dev/null; then
        echo -e "  ${CYAN}1. Claude Code:${NC}"
        echo "     claude login"
        echo "     → Copy the URL it shows, open it in your Mac's browser"
        echo "     → Log in with your Anthropic account and authorize"
        echo ""
    fi

    if ! gh auth status &>/dev/null; then
        echo -e "  ${CYAN}2. GitHub CLI:${NC}"
        echo "     gh auth login"
        echo "     → Choose: GitHub.com → HTTPS → Paste a token"
        echo "     → Use your GITHUB_TOKEN from .env, or create one at"
        echo "       https://github.com/settings/tokens"
        echo ""

        # Try to auto-configure gh from .env if available
        if [[ -f .env ]]; then
            GH_TOKEN=$(grep "^GITHUB_TOKEN=" .env 2>/dev/null | cut -d= -f2)
            if [[ -n "$GH_TOKEN" && "$GH_TOKEN" != "ghp_xxxx"* ]]; then
                echo -e "  ${GREEN}Or auto-configure from .env:${NC}"
                echo "     echo \"\$(grep GITHUB_TOKEN .env | cut -d= -f2)\" | gh auth login --with-token"
                echo ""
            fi
        fi
    fi

    echo "After authenticating, run:"
    echo "  bash scripts/vps-install-claude.sh    # to verify"
    echo ""
else
    echo -e "${GREEN}=== Everything is ready ===${NC}"
    echo ""
    echo "Test with a single iteration:"
    echo "  ./scripts/dev-loop.sh --once"
    echo ""
    echo "Run forever (in tmux so it survives SSH disconnect):"
    echo "  tmux new -s devloop"
    echo "  ./scripts/dev-loop.sh"
    echo "  # Press Ctrl+B then D to detach"
    echo "  # tmux attach -t devloop to reattach"
    echo ""
    echo "Pause:  touch PAUSE"
    echo "Resume: rm PAUSE"
    echo "Stop:   Ctrl+C or kill the tmux session"
fi
