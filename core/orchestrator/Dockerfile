FROM python:3.12-alpine

# Metadata
LABEL maintainer="stilltent"
LABEL description="Orchestrator — drives the stilltent autonomous agent loop"

# Install curl for healthcheck (procps not needed on alpine)
RUN apk add --no-cache curl

# Create non-root user
RUN addgroup -S orchestrator && adduser -S orchestrator -G orchestrator

WORKDIR /app

# Copy orchestrator scripts
COPY loop.py stats.py ./

# Own the app directory
RUN chown -R orchestrator:orchestrator /app

# The orchestrator needs no external dependencies — stdlib only

# Switch to non-root user
USER orchestrator

# Health check: verify the Python process is running
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD pgrep -f "python loop.py" > /dev/null || exit 1

# Clean shutdown signal
STOPSIGNAL SIGTERM

# -u ensures Python output is unbuffered, so docker logs shows output in real time
ENTRYPOINT ["python", "-u", "loop.py"]
