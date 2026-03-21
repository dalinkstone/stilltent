###############################################
# Stage 1: Build
###############################################
FROM alpine:latest AS builder

RUN apk add --no-cache gcc musl-dev make

WORKDIR /build

COPY main.c embedder.c embedder.h http.c http.h json.c json.h Makefile ./

# Build with static linking for a portable binary
# Disable -march=native since we're cross-compiling in Docker
RUN make NATIVE=0 CC="gcc" CFLAGS="-std=c11 -O3 -Wall -Wextra -Wpedantic -Wno-unused-parameter -D_POSIX_C_SOURCE=200809L -D_DEFAULT_SOURCE -pthread -static" LDFLAGS="-pthread -lm -static"

###############################################
# Stage 2: Runtime (minimal image)
###############################################
FROM alpine:latest

# Add non-root user for security
RUN addgroup -S embed && adduser -S embed -G embed

WORKDIR /app

COPY --from=builder /build/embed-service .

RUN chown embed:embed /app/embed-service

USER embed

# Default port
ENV EMBED_PORT=8090

EXPOSE 8090

STOPSIGNAL SIGTERM

HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:8090/health || exit 1

CMD ["./embed-service"]
