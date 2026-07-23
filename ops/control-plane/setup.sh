#!/usr/bin/env bash
# One-time setup for the VPS that runs the Compose control-plane stack.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root: sudo RUNNER_USER=<github-runner-user> bash $0" >&2
	exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR=/opt/renderops
DEPLOY_GROUP=renderops-deploy
RUNNER_USER="${RUNNER_USER:-${SUDO_USER:-}}"
WORKER_SSH_HOST="${WORKER_SSH_HOST:-20.228.220.165}"

if [ -z "$RUNNER_USER" ] || ! id "$RUNNER_USER" >/dev/null 2>&1; then
	echo "RUNNER_USER must name the existing GitHub runner account" >&2
	exit 1
fi
if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
	echo "Docker Engine with the Compose plugin must be installed first" >&2
	exit 1
fi

getent group "$DEPLOY_GROUP" >/dev/null || groupadd --system "$DEPLOY_GROUP"
usermod -aG "$DEPLOY_GROUP" "$RUNNER_USER"
if getent group docker >/dev/null; then
	usermod -aG docker "$RUNNER_USER"
fi

install -d -o root -g "$DEPLOY_GROUP" -m 0770 "$APP_DIR"
install -d -o root -g "$DEPLOY_GROUP" -m 0770 "$APP_DIR/backups/daily"
install -m 0660 -o root -g "$DEPLOY_GROUP" "$SCRIPT_DIR/docker-compose.yml" "$APP_DIR/docker-compose.yml"
install -m 0660 -o root -g "$DEPLOY_GROUP" "$SCRIPT_DIR/Caddyfile" "$APP_DIR/Caddyfile"
install -m 0750 -o root -g "$DEPLOY_GROUP" "$SCRIPT_DIR/backup/backup.sh" "$APP_DIR/backup.sh"

if [ ! -f "$APP_DIR/.env" ]; then
	install -m 0640 -o root -g "$DEPLOY_GROUP" "$SCRIPT_DIR/.env.example" "$APP_DIR/.env"
	echo "created $APP_DIR/.env; fill in every required secret before deployment"
else
	chown root:"$DEPLOY_GROUP" "$APP_DIR/.env"
	chmod 0640 "$APP_DIR/.env"
	echo "preserved existing $APP_DIR/.env"
fi

# The container runs as uid 10001. Host ownership lets it read the mounted key
# without making that private key readable to the deploy runner.
install -d -o 10001 -g 10001 -m 0700 "$APP_DIR/keys"
KEY="$APP_DIR/keys/worker_key"
if [ ! -f "$KEY" ]; then
	ssh-keygen -q -t ed25519 -N '' -f "$KEY" -C "renderops-control-plane-tunnel"
fi
KNOWN_HOSTS_TMP="$(mktemp)"
trap 'rm -f "$KNOWN_HOSTS_TMP"' EXIT
if ssh-keyscan -H "$WORKER_SSH_HOST" > "$KNOWN_HOSTS_TMP" 2>/dev/null &&
	[ -s "$KNOWN_HOSTS_TMP" ]; then
	mv "$KNOWN_HOSTS_TMP" "$APP_DIR/keys/known_hosts"
elif [ ! -s "$APP_DIR/keys/known_hosts" ]; then
	echo "could not record the worker SSH host key for $WORKER_SSH_HOST" >&2
	exit 1
fi
chown -R 10001:10001 "$APP_DIR/keys"
chmod 0600 "$KEY"
chmod 0644 "$KEY.pub" "$APP_DIR/keys/known_hosts"

install -m 0644 "$SCRIPT_DIR/backup/renderops-backup.service" /etc/systemd/system/renderops-backup.service
install -m 0644 "$SCRIPT_DIR/backup/renderops-backup.timer" /etc/systemd/system/renderops-backup.timer
systemctl daemon-reload
systemctl enable --now renderops-backup.timer

echo
echo "Control-plane VPS prepared."
echo "1. Add this public key to the worker account's authorized_keys:"
cat "$KEY.pub"
echo "2. Fill in $APP_DIR/.env."
echo "3. Re-login $RUNNER_USER so new group membership applies."
echo "4. Register the GitHub runner with label: control-plane."
echo "5. Run the Deploy workflow with target: control-plane."
