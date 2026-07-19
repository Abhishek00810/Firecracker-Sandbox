#!/usr/bin/env bash
# start.sh — build and run the RenderOps control plane (the Go API server).
#
# Env comes from the repo-root .env if present (see .env.example).
# Required: DATABASE_URL. Optional: PORT (default 8080), LOG_LEVEL, LOG_FORMAT.
set -euo pipefail

cd "$(dirname "$0")"

if [ -f .env ]; then
    set -a
    # shellcheck disable=SC1091
    . ./.env
    set +a
fi

cd backend

echo "[start] building control plane..."
go build -o renderops-control-plane ./cmd/control-plane

echo "[start] starting control plane on :${PORT:-8080}"
exec ./renderops-control-plane
