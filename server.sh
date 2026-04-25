#!/usr/bin/env bash
# server.sh — start the sandbox backend on Azure
# Usage: bash server.sh
# Run from: ~/Firecracker-Sandbox/backend

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$SCRIPT_DIR/backend"
ENV_FILE="$BACKEND_DIR/.env"

echo "[server] === Sandbox Backend Startup ==="

# ── 1. Kill any running server or firecracker processes ──────────────────────
echo "[server] Killing stale processes..."
sudo pkill -f "go run ./cmd/api" 2>/dev/null || true
sudo pkill -f firecracker          2>/dev/null || true
sleep 1

# ── 2. Clean up ALL stale TAP devices ────────────────────────────────────────
# Firecracker leaves TAP devices behind on crash/kill. Also handles the
# "TAP rename failing" case: if a previous run died mid-restore, fctap0
# (the template TAP) may still exist and block the next restore.
echo "[server] Cleaning up stale TAP devices..."
for tap in $(ip link show 2>/dev/null | grep -o 'fctap[0-9]*' | sort -u); do
    echo "[server]   deleting $tap"
    sudo ip link delete "$tap" 2>/dev/null || true
done

# ── 3. Clean up stale vsock sockets ──────────────────────────────────────────
echo "[server] Cleaning up stale vsock sockets..."
sudo rm -f /tmp/fc-sockets/*.sock 2>/dev/null || true

# ── 4. Load env vars ─────────────────────────────────────────────────────────
if [[ ! -f "$ENV_FILE" ]]; then
    echo "[server] ERROR: .env not found at $ENV_FILE"
    exit 1
fi
set -a
source "$ENV_FILE"
set +a
echo "[server] Environment loaded from $ENV_FILE"

# ── 5. Start the server ───────────────────────────────────────────────────────
echo "[server] Starting backend (logs below)..."
cd "$BACKEND_DIR"
exec sudo -E env PATH="$PATH" /usr/local/go/bin/go run ./cmd/api/main.go
