# OpenClaw agent runtime
# Configured via stilltent.yml agent section
FROM ghcr.io/openclaw/openclaw:latest

COPY openclaw.json /etc/openclaw/config.json

ENTRYPOINT ["openclaw", "serve"]
