FROM ghcr.io/openclaw/openclaw:latest

USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends gh \
 && apt-get clean && rm -rf /var/lib/apt/lists/*
USER node

# Install the mnemo memory plugin (@mem9/openclaw)
RUN openclaw plugins install @mem9/openclaw

# Configure git identity for commits inside the container
RUN git config --global user.email "agent@stilltent.local" \
 && git config --global user.name "stilltent-agent"

