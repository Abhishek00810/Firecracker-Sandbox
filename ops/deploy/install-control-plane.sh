#!/usr/bin/env bash
# Ships the control-plane binary to the basic server and restarts it.
# Runs inside GitHub Actions — the SSH key never leaves the runner. No human
# SSHes in to deploy; this script is the only path that touches the box.
#
# Usage: install-control-plane.sh <local-binary-path>
# Env:   SSH_KEY (private key contents), SSH_HOST, SSH_USER
set -euo pipefail

BINARY="${1:?usage: install-control-plane.sh <binary>}"
: "${SSH_KEY:?SSH_KEY required}" "${SSH_HOST:?SSH_HOST required}" "${SSH_USER:?SSH_USER required}"

REMOTE_DIR=/opt/renderops
SERVICE=renderops-control-plane
HEALTH_URL=http://127.0.0.1:8080/health

# Write the deploy key to a locked-down temp file.
KEYFILE="$(mktemp)"
trap 'rm -f "$KEYFILE"' EXIT
printf '%s\n' "$SSH_KEY" > "$KEYFILE"
chmod 600 "$KEYFILE"

SSH="ssh -i $KEYFILE -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20 ${SSH_USER}@${SSH_HOST}"

echo "==> uploading control-plane binary"
scp -i "$KEYFILE" -o StrictHostKeyChecking=accept-new "$BINARY" "${SSH_USER}@${SSH_HOST}:/tmp/renderops-control-plane.new"

echo "==> swapping binary and restarting service"
# shellcheck disable=SC2087
$SSH "sudo bash -s" <<REMOTE
set -euo pipefail
sudo install -D -m 0755 /tmp/renderops-control-plane.new ${REMOTE_DIR}/renderops-control-plane
rm -f /tmp/renderops-control-plane.new
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

echo "    control-plane did not become healthy — rolling status:"
$SSH "sudo systemctl status ${SERVICE} --no-pager -l | tail -20 || true"
exit 1
