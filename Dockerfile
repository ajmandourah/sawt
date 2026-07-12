# ---- Build stage ----
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /build

# Cache Go module downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build the binary
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /sawt ./cmd/sawt/

# ---- Runtime stage ----
FROM alpine:3.19

RUN apk add --no-cache \
    ffmpeg \
    opus \
    ca-certificates \
    tzdata \
    && rm -rf /var/cache/apk/*

# Create non-root user
RUN adduser -D -h /app sawt

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
CMD ["-music-dir", "/music", "-data-dir", "/data", "-webui-port", "7071", "-webui-addr", "0.0.0.0"]
