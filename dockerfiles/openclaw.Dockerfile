FROM ghcr.io/openclaw/openclaw:latest

USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends gh \
 && apt-get clean && rm -rf /var/lib/apt/lists/*
USER node
