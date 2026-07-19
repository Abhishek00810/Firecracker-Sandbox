#!/usr/bin/env bash
# agent-run.sh — start the RenderOps host agent on a KVM host, detached.
# Pushed to the worker host by `make agent`. The agent self-bootstraps on
# startup: it unpacks the pushed asset bundle into $ROOT_DIRECTORY/assets,
# provisions the host (fcvm user, network slots, nftables), then serves. No
# reliance on any pre-existing setup — a freshly allocated VM works.
set -euo pipefail

ROOT="${ROOT_DIRECTORY:-$HOME/aman}"
TOKEN="${WORKER_TOKEN:-devtoken123}"
BIND="${WORKER_BIND:-127.0.0.1:9876}"
LOG="$ROOT/logs/agent.log"

mkdir -p "$ROOT/logs"
: > "$LOG"   # truncate + ensure it exists (owned by us, root can append)

# Stop any prior agent holding the port. Kill by port (not pkill -f, which would
# also match this script's own command line and kill the SSH session).
sudo fuser -k "${BIND##*:}/tcp" 2>/dev/null || true
sleep 1

# Detached start as root (KVM + netns + nftables + cgroup). setsid + </dev/null
# so it survives this SSH session closing. Paths derive from ROOT_DIRECTORY —
# the agent resolves assets/sockets under it and keeps snapshots on tmpfs.
sudo env \
  ROOT_DIRECTORY="$ROOT" \
  WORKER_TOKEN="$TOKEN" \
  WORKER_BIND="$BIND" \
  SNAPSHOT_DIR=/dev/shm/fc-snapshots \
  LOG_FORMAT=text \
  setsid "$ROOT/bin/renderops-agent" >"$LOG" 2>&1 </dev/null &

echo "agent starting; root=$ROOT bind=$BIND log=$LOG"
