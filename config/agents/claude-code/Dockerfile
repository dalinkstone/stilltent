# Claude Code agent adapter
# Python HTTP server that wraps the Anthropic API with tool_use, exposing
# the same gateway interface (port 18789) as OpenClaw/NanoClaw/NemoClaw.
#
# Includes: git, gh CLI, Node.js (for Claude Code CLI if needed)

FROM python:3.12-slim

LABEL maintainer="stilltent"
LABEL description="Claude Code agent adapter — Anthropic API with tool_use"

# System dependencies: git, gh CLI, curl, Node.js (for claude-code CLI)
RUN apt-get update && apt-get install -y --no-install-recommends \
        git \
        curl \
        ca-certificates \
        gnupg \
    && mkdir -p /etc/apt/keyrings \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | gpg --dearmor -o /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && npm install -g @anthropic-ai/claude-code \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# Create non-root user
RUN groupadd -r agent && useradd -r -g agent -m -s /bin/bash agent

WORKDIR /app

# Copy adapter code, oversight sidecar, and tool definitions
COPY adapter.py oversight.py tools.json ./

RUN chown -R agent:agent /app

USER agent

# Configure git identity for the agent
RUN git config --global user.name "stilltent-agent" \
    && git config --global user.email "agent@stilltent.local" \
    && git config --global init.defaultBranch main

EXPOSE 18789

HEALTHCHECK --interval=60s --timeout=5s --start-period=15s --retries=5 \
    CMD curl -sf http://127.0.0.1:18789/healthz || exit 1

STOPSIGNAL SIGTERM

ENTRYPOINT ["python", "-u", "adapter.py"]
