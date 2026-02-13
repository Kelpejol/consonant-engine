# Dockerfile for Beam API
#
# Multi-stage build for minimal production image
# Final image size: ~20MB

# Stage 1: Build
FROM golang:1.25-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make protobuf-dev

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build binaries
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s -X main.Version=$(git describe --tags --always --dirty 2>/dev/null || echo 'dev') -X main.BuildTime=$(date -u '+%Y-%m-%d_%H:%M:%S')" \
    -a -installsuffix cgo \
    -o /app/bin/beam-api ./cmd/api

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -a -installsuffix cgo \
    -o /app/bin/beam-cli ./cmd/cli

# Stage 2: Runtime
FROM alpine:latest

# Install ca-certificates for HTTPS and timezone data
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 beam && \
    adduser -D -u 1000 -G beam beam

# Set working directory
WORKDIR /app

# Copy binaries from builder
COPY --from=builder /app/bin/beam-api /app/beam-api
COPY --from=builder /app/bin/beam-cli /app/beam-cli

# Create directories for logs and data
RUN mkdir -p /app/logs /app/data && \
    chown -R beam:beam /app

# Switch to non-root user
USER beam

# Expose ports
EXPOSE 9090 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Set environment variables
ENV GRPC_PORT=9090 \
    HTTP_PORT=8080 \
    LOG_LEVEL=info \
    ENVIRONMENT=production

# Run the binary
ENTRYPOINT ["/app/beam-api"]