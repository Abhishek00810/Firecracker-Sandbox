#!/usr/bin/env bash
# Ships the worker binary to the bare-metal Firecracker host and restarts it.
# Runs inside GitHub Actions — the SSH key never leaves the runner. No human
# SSHes in to deploy; this script is the only path that touches the box.
#
# Usage: install-worker.sh <local-binary-path>
# Env:   SSH_KEY (private key contents), SSH_HOST, SSH_USER
set -euo pipefail

BINARY="${1:?usage: install-worker.sh <binary>}"
: "${SSH_KEY:?SSH_KEY required}" "${SSH_HOST:?SSH_HOST required}" "${SSH_USER:?SSH_USER required}"

REMOTE_DIR=/opt/renderops
SERVICE=renderops-worker
# The worker binds loopback on the host; health is checked on the host itself.
HEALTH_URL=http://127.0.0.1:9876/worker/health

KEYFILE="$(mktemp)"
trap 'rm -f "$KEYFILE"' EXIT
printf '%s\n' "$SSH_KEY" > "$KEYFILE"
chmod 600 "$KEYFILE"

SSH="ssh -i $KEYFILE -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20 ${SSH_USER}@${SSH_HOST}"

echo "==> uploading worker binary"
scp -i "$KEYFILE" -o StrictHostKeyChecking=accept-new "$BINARY" "${SSH_USER}@${SSH_HOST}:/tmp/renderops-worker.new"

echo "==> swapping binary and restarting service"
# shellcheck disable=SC2087
$SSH "sudo bash -s" <<REMOTE
set -euo pipefail
sudo install -D -m 0755 /tmp/renderops-worker.new ${REMOTE_DIR}/renderops-worker
rm -f /tmp/renderops-worker.new
sudo systemctl restart ${SERVICE}
REMOTE

echo "==> health check"
for i in $(seq 1 15); do
  if $SSH "curl -fsS -m 4 ${HEALTH_URL} >/dev/null 2>&1"; then
    echo "    healthy after ${i}s"
    exit 0
  fi
  sleep 1
done

echo "    worker did not become healthy — rolling status:"
$SSH "sudo systemctl status ${SERVICE} --no-pager -l | tail -20 || true"
exit 1
