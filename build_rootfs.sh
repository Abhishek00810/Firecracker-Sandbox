#!/bin/bash
# build_rootfs.sh — build the Firecracker rootfs from Dockerfile.rootfs
# Run this once on the server whenever packages need to be added/updated.
# Output: assets/rootfs/rootfs-alpine.ext4

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOTFS_PATH="$SCRIPT_DIR/assets/rootfs/rootfs-alpine.ext4"
IMAGE_NAME="sandbox-rootfs"
CONTAINER_NAME="tmp-sandbox-rootfs"
SIZE_MB=2048

echo "[rootfs] Building Docker image..."
docker build -f "$SCRIPT_DIR/Dockerfile.rootfs" -t "$IMAGE_NAME" "$SCRIPT_DIR"

echo "[rootfs] Creating blank ${SIZE_MB}MB ext4 image..."
dd if=/dev/zero of="$ROOTFS_PATH" bs=1M count=$SIZE_MB status=progress
mkfs.ext4 -F "$ROOTFS_PATH"

echo "[rootfs] Extracting filesystem into ext4..."
sudo mount -o loop "$ROOTFS_PATH" /mnt
docker create --name "$CONTAINER_NAME" "$IMAGE_NAME"
docker export "$CONTAINER_NAME" | sudo tar -xf - -C /mnt --exclude="dev/*"
sudo umount /mnt
docker rm "$CONTAINER_NAME"

echo "[rootfs] Done: $ROOTFS_PATH"
echo "[rootfs] Size: $(du -h "$ROOTFS_PATH" | cut -f1)"
