FROM python:3.12-slim

# Metadata
LABEL maintainer="stilltent"
LABEL description="Orchestrator — drives the stilltent autonomous agent loop"

WORKDIR /app

# Copy orchestrator scripts
COPY loop.py stats.py ./

# The orchestrator needs no external dependencies — stdlib only

# Health check: verify the Python process is running
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD pgrep -f "python loop.py" > /dev/null || exit 1

# -u ensures Python output is unbuffered, so docker logs shows output in real time
ENTRYPOINT ["python", "-u", "loop.py"]
