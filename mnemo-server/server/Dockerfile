FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static binary: CGO disabled, stripped symbols for smaller image
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /mnemo-server ./cmd/mnemo-server

# Minimal runtime: alpine ~7MB, includes wget for healthcheck + CA certs
FROM alpine:3.21
RUN apk add --no-cache ca-certificates && \
    addgroup -S mnemo && adduser -S mnemo -G mnemo

COPY --from=builder /mnemo-server /mnemo-server

EXPOSE 8082

HEALTHCHECK --interval=15s --timeout=5s --start-period=15s --retries=5 \
  CMD wget -qO- http://127.0.0.1:8082/health || exit 1

USER mnemo

ENTRYPOINT ["/mnemo-server"]
