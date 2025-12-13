# TokenGo Dockerfile
# Multi-stage build for smaller image size

# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o tokengo ./cmd/tokengo

# Runtime stage
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache ca-certificates curl

# Copy binary from builder
COPY --from=builder /app/tokengo /usr/local/bin/

# Copy configuration files
COPY --from=builder /app/configs /etc/tokengo/configs
COPY --from=builder /app/certs /etc/tokengo/certs
COPY --from=builder /app/keys /etc/tokengo/keys

WORKDIR /etc/tokengo

# Default entrypoint
ENTRYPOINT ["tokengo"]
CMD ["--help"]
