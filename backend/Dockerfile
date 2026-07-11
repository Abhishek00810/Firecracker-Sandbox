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

# ca-certificates: HTTPS; e2fsprogs: mkfs.ext4 for per-VM writable disks;
# iproute2: full ip with netns/tuntap (busybox ip lacks both); iptables: slot
# NAT rules; util-linux-misc: unshare/setpriv with the flags the VM launcher
# uses when FC_RUN_UID > 0; coreutils: GNU cp with --reflink/--sparse for the
# golden-disk clone in snapshot restores (busybox cp lacks both → cold-boot
# fallback and broken session resume).
RUN apk --no-cache add ca-certificates e2fsprogs iproute2 iptables util-linux-misc coreutils

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

# Network-slot bootstrap (netns/TAP/veth/NAT) that the Go slot pool expects,
# then execs the API binary.
COPY backend/docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

# Expose port
EXPOSE 8080

ENTRYPOINT ["/app/docker-entrypoint.sh"]