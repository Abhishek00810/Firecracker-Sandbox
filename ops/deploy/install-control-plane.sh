#!/usr/bin/env bash
# Ships the control-plane binary to the basic server and restarts it.
# Runs inside GitHub Actions — the password never leaves the runner. No human
# SSHes in to deploy; this script is the only path that touches the box.
#
# TEMPORARY: authenticates with a password via sshpass. Move to a dedicated
# SSH key when the basic server is hardened (then CP_PASSWORD -> CP_SSH_KEY).
#
# Usage: install-control-plane.sh <local-binary-path>
# Env:   CP_PASSWORD (login + sudo password), SSH_HOST, SSH_USER
set -euo pipefail

BINARY="${1:?usage: install-control-plane.sh <binary>}"
: "${CP_PASSWORD:?CP_PASSWORD required}" "${SSH_HOST:?SSH_HOST required}" "${SSH_USER:?SSH_USER required}"

REMOTE_DIR=/opt/renderops
SERVICE=renderops-control-plane
HEALTH_URL=http://127.0.0.1:8080/health

# sshpass reads the password from SSHPASS (never appears in argv or process list).
export SSHPASS="$CP_PASSWORD"
SSHOPTS="-o StrictHostKeyChecking=accept-new -o ConnectTimeout=20"

echo "==> uploading control-plane binary"
sshpass -e scp $SSHOPTS "$BINARY" "${SSH_USER}@${SSH_HOST}:/tmp/renderops-control-plane.new"

echo "==> swapping binary and restarting service"
# The password is fed to `sudo -S` over the remote command's stdin (here-string),
# so it works whether or not the account has passwordless sudo.
sshpass -e ssh $SSHOPTS "${SSH_USER}@${SSH_HOST}" \
  "sudo -S -p '' bash -c '
     set -e
     install -D -m 0755 /tmp/renderops-control-plane.new ${REMOTE_DIR}/renderops-control-plane
     rm -f /tmp/renderops-control-plane.new
     systemctl restart ${SERVICE}'" <<< "$CP_PASSWORD"

echo "==> health check"
for i in $(seq 1 15); do
  if sshpass -e ssh $SSHOPTS "${SSH_USER}@${SSH_HOST}" "curl -fsS -m 4 ${HEALTH_URL} >/dev/null 2>&1"; then
    echo "    healthy after ${i}s"
    exit 0
  fi
  sleep 1
done

echo "    control-plane did not become healthy — recent status:"
sshpass -e ssh $SSHOPTS "${SSH_USER}@${SSH_HOST}" \
  "sudo -S -p '' systemctl status ${SERVICE} --no-pager -l | tail -20 || true" <<< "$CP_PASSWORD"
exit 1
