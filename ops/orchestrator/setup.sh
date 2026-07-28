#!/usr/bin/env bash
# One-time filesystem setup for the dedicated orchestrator server.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root: sudo RUNNER_USER=<github-runner-user> bash $0" >&2
	exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR="${APP_DIR:-/opt/renderops-orchestrator}"
DEPLOY_GROUP="${DEPLOY_GROUP:-renderops-deploy}"
RUNNER_USER="${RUNNER_USER:-${SUDO_USER:-}}"

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
install -m 0660 -o root -g "$DEPLOY_GROUP" "$SCRIPT_DIR/docker-compose.yml" "$APP_DIR/docker-compose.yml"

if [ ! -f "$APP_DIR/.env" ]; then
	install -m 0660 -o root -g "$DEPLOY_GROUP" "$SCRIPT_DIR/.env.example" "$APP_DIR/.env"
	echo "created $APP_DIR/.env; populate its private database URL, bind IP, and token"
else
	chown root:"$DEPLOY_GROUP" "$APP_DIR/.env"
	chmod 0660 "$APP_DIR/.env"
	echo "preserved existing $APP_DIR/.env"
fi

echo "Orchestrator server filesystem prepared."
echo "Re-login $RUNNER_USER, then register a repository runner with label: orchestrator."
