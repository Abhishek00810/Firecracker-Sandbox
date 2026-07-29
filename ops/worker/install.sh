#!/usr/bin/env bash
# Ships the worker binary + systemd unit to the bare-metal host and restarts it.
# Idempotent. Runs inside GitHub Actions — the SSH key never leaves the runner.
#
# Assumes the ONE-TIME host setup is already done (worker.env with WORKER_TOKEN,
# and Firecracker assets under /opt/renderops/worker/assets). This installer
# verifies those exist and fails clearly if not — it does NOT create secrets or
# provision assets.
#
# Usage: install-worker.sh <local-binary-path>
# Env:   SSH_KEY (private key contents), SSH_HOST, SSH_USER
#        WORKER_SLOT_COUNT, WORKER_MAX_SESSIONS (optional non-secret capacity)
set -euo pipefail

BINARY="${1:?usage: install-worker.sh <binary>}"
: "${SSH_KEY:?SSH_KEY required}" "${SSH_HOST:?SSH_HOST required}" "${SSH_USER:?SSH_USER required}"
UNIT="$(dirname "$0")/worker.service"

for value in "${WORKER_SLOT_COUNT:-}" "${WORKER_MAX_SESSIONS:-}"; do
	[ -z "$value" ] && continue
	case "$value" in
	*[!0-9]* | 0)
		echo "worker capacity values must be positive integers" >&2
		exit 1
		;;
	esac
done

SERVICE=renderops-worker
HEALTH_URL=http://127.0.0.1:9876/worker/health

KEYFILE="$(mktemp)"
trap 'rm -f "$KEYFILE"' EXIT
printf '%s\n' "$SSH_KEY" > "$KEYFILE"
chmod 600 "$KEYFILE"

SCP="scp -i $KEYFILE -o StrictHostKeyChecking=accept-new"
SSH="ssh -i $KEYFILE -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20 ${SSH_USER}@${SSH_HOST}"

echo "==> uploading worker binary + service unit"
$SCP "$BINARY" "${SSH_USER}@${SSH_HOST}:/tmp/renderops-worker.new"
$SCP "$UNIT"   "${SSH_USER}@${SSH_HOST}:/tmp/renderops-worker.service.new"

echo "==> installing (idempotent) and restarting"
# shellcheck disable=SC2087
$SSH "sudo WORKER_SLOT_COUNT='${WORKER_SLOT_COUNT:-}' WORKER_MAX_SESSIONS='${WORKER_MAX_SESSIONS:-}' bash -s" <<'REMOTE'
set -euo pipefail
# Preconditions from the one-time host setup — fail clearly if missing.
if [ ! -f /etc/renderops/worker.env ]; then
  echo "ERROR: /etc/renderops/worker.env missing — run the one-time worker host setup first." >&2
  exit 1
fi
if [ ! -f /opt/renderops/worker/assets/manifest.sha256 ] &&
   [ ! -f /opt/renderops/worker/renderops-assets.tar.gz ]; then
  echo "ERROR: Firecracker assets or renderops-assets.tar.gz are missing — run worker setup first." >&2
  exit 1
fi

set_env_value() {
  local key="$1"
  local value="$2"
  [ -n "$value" ] || return 0
  if grep -q "^${key}=" /etc/renderops/worker.env; then
    sed -i "s|^${key}=.*|${key}=${value}|" /etc/renderops/worker.env
  else
    printf '%s=%s\n' "$key" "$value" >> /etc/renderops/worker.env
  fi
}

# Capacity is deployment configuration, not a secret. Keeping it here ensures
# network provisioning and advertised max sessions change together on restart.
set_env_value SLOT_COUNT "$WORKER_SLOT_COUNT"
set_env_value WORKER_MAX_SESSIONS "$WORKER_MAX_SESSIONS"

install -D -m 0755 /tmp/renderops-worker.new /opt/renderops/renderops-worker
install -D -m 0644 /tmp/renderops-worker.service.new /etc/systemd/system/renderops-worker.service
rm -f /tmp/renderops-worker.new /tmp/renderops-worker.service.new
systemctl daemon-reload
systemctl enable renderops-worker >/dev/null 2>&1 || true
systemctl restart renderops-worker
REMOTE

echo "==> health check (allow time for snapshot template warmup)"
for i in $(seq 1 "${WORKER_HEALTH_ATTEMPTS:-150}"); do
  if $SSH "curl -fsS -m 4 ${HEALTH_URL} >/dev/null 2>&1"; then
    echo "    worker healthy after $((i*2))s"
    exit 0
  fi
  sleep 2
done

echo "    worker did not become healthy — status:"
$SSH "sudo systemctl status ${SERVICE} --no-pager -l | tail -25 || true"
exit 1
