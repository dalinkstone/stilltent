FROM ghcr.io/openclaw/openclaw:latest

USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends gh wget \
 && wget -q https://go.dev/dl/go1.23.8.linux-amd64.tar.gz \
 && tar -C /usr/local -xzf go1.23.8.linux-amd64.tar.gz \
 && rm go1.23.8.linux-amd64.tar.gz \
 && apt-get clean && rm -rf /var/lib/apt/lists/*
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH="/home/node/go"
ENV PATH="${GOPATH}/bin:${PATH}"
USER node

# Install the mnemo memory plugin (@mem9/openclaw)
RUN openclaw plugins install @mem9/openclaw

# Configure git identity for commits inside the container
RUN git config --global user.email "agent@stilltent.local" \
 && git config --global user.name "stilltent-agent"

