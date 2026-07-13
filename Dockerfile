# ---- Build stage ----
FROM golang:1.25 AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libc6-dev \
    libopus-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# Cache Go module downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build the binary
ENV GOTOOLCHAIN=auto
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /sawt ./cmd/sawt/

# ---- Runtime stage ----
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    ca-certificates \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user
RUN useradd -m -d /app sawt

WORKDIR /app

# Copy binary from builder
COPY --from=builder /sawt /app/sawt

# Create default directories
RUN mkdir -p /music /data && chown -R sawt:sawt /app

# Set environment defaults
ENV SERVER="" \
    USERNAME="Sawt Bot" \
    PASSWORD="" \
    CHANNEL="Music" \
    MUSIC_DIR="/music" \
    DATA_DIR="/data" \
    PREFIX="!" \
    STEREO="false" \
    JITTER="false" \
    WEBUI_PORT="7071" \
    WEBUI_ADDR="0.0.0.0"

# Switch to non-root user
USER sawt

EXPOSE 7071

ENTRYPOINT ["/app/sawt"]
