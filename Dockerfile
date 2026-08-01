# ── Build stage ──────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /src
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /east-validator ./cmd/validator

# ── Runtime stage ────────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -u 1000 east

WORKDIR /app
COPY --from=builder /east-validator /app/east-validator
COPY genesis.json /app/genesis.json

RUN mkdir -p /app/data && chown -R east:east /app
USER east

ENV DATA_DIR=/app/data
ENV GENESIS_PATH=/app/genesis.json
ENV HTTP_ADDR=:8080
ENV KEEP_RECENT_BLOCKS=3000
ENV P2P_ENABLED=true
ENV P2P_PORT=4001
ENV BLOCK_INTERVAL_SEC=120

EXPOSE 8080 4001

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["/app/east-validator"]
