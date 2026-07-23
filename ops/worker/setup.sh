#!/usr/bin/env bash
# One-time setup for a bare-metal Firecracker worker.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root: sudo WORKER_TOKEN=<shared-token> bash $0" >&2
	exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR=/opt/renderops
WORKER_ROOT="$APP_DIR/worker"
ENV_FILE=/etc/renderops/worker.env

if [ ! -e /dev/kvm ]; then
	echo "/dev/kvm is missing; this host cannot run Firecracker" >&2
	exit 1
fi
if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
	echo "cgroup v2 is required" >&2
	exit 1
fi

if command -v apt-get >/dev/null 2>&1; then
	apt-get update -qq
	DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
		acl ca-certificates curl iproute2 nftables tar util-linux
fi

install -d -m 0750 /etc/renderops
install -d -m 0750 "$APP_DIR" "$WORKER_ROOT"

if [ ! -f "$ENV_FILE" ]; then
	: "${WORKER_TOKEN:?WORKER_TOKEN is required for the first setup}"
	umask 077
	{
		printf 'ROOT_DIRECTORY=%s\n' "$WORKER_ROOT"
		printf 'WORKER_BIND=127.0.0.1:9876\n'
		printf 'WORKER_TOKEN=%s\n' "$WORKER_TOKEN"
		printf 'HOST_VALIDATION_MODE=strict\n'
		printf 'SLOT_COUNT=%s\n' "${SLOT_COUNT:-50}"
		printf 'MAX_CONCURRENT_PROVISIONS=%s\n' "${MAX_CONCURRENT_PROVISIONS:-8}"
	} > "$ENV_FILE"
else
	echo "preserved existing $ENV_FILE"
fi
chown root:root "$ENV_FILE"
chmod 0600 "$ENV_FILE"

if [ -n "${ASSET_BUNDLE:-}" ]; then
	if [ ! -f "$ASSET_BUNDLE" ]; then
		echo "ASSET_BUNDLE does not exist: $ASSET_BUNDLE" >&2
		exit 1
	fi
	install -m 0600 "$ASSET_BUNDLE" "$WORKER_ROOT/renderops-assets.tar.gz"
fi

install -m 0644 "$SCRIPT_DIR/worker.service" /etc/systemd/system/renderops-worker.service
systemctl daemon-reload
systemctl enable renderops-worker >/dev/null

if [ ! -f "$WORKER_ROOT/assets/manifest.sha256" ] &&
	[ ! -f "$WORKER_ROOT/renderops-assets.tar.gz" ]; then
	echo "worker host prepared, but Firecracker assets are still required" >&2
	echo "copy renderops-assets.tar.gz to $WORKER_ROOT before deployment" >&2
	exit 2
fi

echo "Worker host prepared. Run the Deploy workflow with target: worker."
