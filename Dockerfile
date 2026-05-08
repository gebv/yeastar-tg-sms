# =============================================================
# Builder stage
# =============================================================
FROM golang:1.24-alpine AS builder

ARG VERSION=dev
WORKDIR /src

# Cache dependency downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build static binary (CGO_ENABLED=0 works with modernc.org/sqlite)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/yeastar-tg-bot ./cmd/yeastar-tg-bot

# =============================================================
# Runtime stage
# =============================================================
FROM alpine:3.21

# Install CA certificates for TLS (Telegram API) and timezone data
RUN apk --no-cache add ca-certificates tzdata

# Copy binary from builder
COPY --from=builder /out/yeastar-tg-bot /app/yeastar-tg-bot

# Create data directory for persistent SQLite database.
# When bind-mounting ./data:/app/data from docker-compose,
# Docker creates the host directory as root — so we run as root
# to avoid permission issues with the SQLite WAL file.
WORKDIR /app
RUN mkdir -p /app/data && chmod +x /app/yeastar-tg-bot

# No ports to expose — this is an outbound-only service

# Run the binary
CMD ["/app/yeastar-tg-bot"]
