# Stage 1: Build
FROM golang:1.25-alpine AS builder

# Set working directory
WORKDIR /app

# Copy go mod files first (for better caching)
# Note: Building from parent directory, so backend/ prefix is needed
COPY backend/go.mod backend/go.sum ./

# Download dependencies
RUN go mod download

# Copy source code (backend directory)
COPY backend/ ./

# Build the binary
# CGO_ENABLED=0 creates a static binary (no C dependencies)
# -o app is the output binary name
RUN CGO_ENABLED=0 GOOS=linux go build -o app ./cmd/api/main.go

# Stage 2: Runtime (minimal image)
FROM alpine:latest

# Install ca-certificates for HTTPS (if you make external API calls)
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /app/app .

# Create directories for Firecracker and assets
RUN mkdir -p /app/firecracker /app/assets

# Copy Firecracker binary (assuming Docker build context is parent directory)
COPY release-v1.7.0-aarch64/firecracker-v1.7.0-aarch64 /app/firecracker/firecracker-v1.7.0-aarch64
RUN chmod +x /app/firecracker/firecracker-v1.7.0-aarch64

# Copy assets (kernel and rootfs)
COPY assets/ /app/assets/

# Expose port
EXPOSE 8080

# Run the binary
CMD ["./app"]