#!/usr/bin/env bash
# Install repository-scoped GitHub Actions runners on the shared deployment VPS.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root with sudo" >&2
	exit 1
fi

: "${SANDBOX_RUNNER_TOKEN:?temporary renderops-sandbox runner token is required}"
: "${PLATFORM_RUNNER_TOKEN:?temporary renderops-platform-1 runner token is required}"

GITHUB_ORG="${GITHUB_ORG:-renderops-ai}"
RUNNER_USER="${RUNNER_USER:-renderops-runner}"
ADMIN_USER="${ADMIN_USER:-${SUDO_USER:-}}"
DEPLOY_GROUP="${DEPLOY_GROUP:-renderops-deploy}"
APP_DIR="${APP_DIR:-/opt/renderops}"
RUNNER_VERSION="${RUNNER_VERSION:-2.336.0}"
RUNNER_SHA256="${RUNNER_SHA256:-04cf0be1aff4c3ec3554466c39124ca250e3effd8873bb7e8d68535aa9505d5d}"
RUNNER_ARCHIVE="actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz"
RUNNER_URL="https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/${RUNNER_ARCHIVE}"

if ! id "$RUNNER_USER" >/dev/null 2>&1; then
	useradd --system --create-home --shell /bin/bash --user-group "$RUNNER_USER"
fi
getent group "$DEPLOY_GROUP" >/dev/null || groupadd --system "$DEPLOY_GROUP"
usermod -aG "docker,$DEPLOY_GROUP" "$RUNNER_USER"
if [ -n "$ADMIN_USER" ] && id "$ADMIN_USER" >/dev/null 2>&1; then
	usermod -aG "$DEPLOY_GROUP" "$ADMIN_USER"
fi

archive="/tmp/$RUNNER_ARCHIVE"
curl --fail --silent --show-error --location "$RUNNER_URL" --output "$archive"
echo "$RUNNER_SHA256  $archive" | sha256sum --check -

install_runner() {
	local repository=$1
	local runner_name=$2
	local label=$3
	local token=$4
	local directory=$5

	install -d -o "$RUNNER_USER" -g "$RUNNER_USER" -m 0750 "$directory"
	if [ ! -x "$directory/config.sh" ]; then
		tar -xzf "$archive" -C "$directory"
		chown -R "$RUNNER_USER:$RUNNER_USER" "$directory"
	fi

	if [ ! -f "$directory/.runner" ]; then
		cd "$directory"
		runuser -u "$RUNNER_USER" -- env HOME="/home/$RUNNER_USER" ./config.sh \
			--unattended \
			--url "https://github.com/$GITHUB_ORG/$repository" \
			--token "$token" \
			--name "$runner_name" \
			--labels "$label" \
			--work _work \
			--replace
	fi

	cd "$directory"
	if [ ! -f .service ]; then
		./svc.sh install "$RUNNER_USER"
	fi
	./svc.sh start
}

install_runner \
	renderops-sandbox \
	renderops-control-plane \
	control-plane \
	"$SANDBOX_RUNNER_TOKEN" \
	/opt/actions-runner-sandbox

install_runner \
	renderops-platform-1 \
	renderops-dashboard \
	dashboard \
	"$PLATFORM_RUNNER_TOKEN" \
	/opt/actions-runner-dashboard

unset SANDBOX_RUNNER_TOKEN PLATFORM_RUNNER_TOKEN
rm -f "$archive"

install -d -o root -g "$DEPLOY_GROUP" -m 0770 "$APP_DIR"
for file in "$APP_DIR/docker-compose.yml" "$APP_DIR/Caddyfile"; do
	if [ -e "$file" ]; then
		chown root:"$DEPLOY_GROUP" "$file"
		chmod 0660 "$file"
	fi
done
if [ -e "$APP_DIR/.env" ]; then
	chown root:"$DEPLOY_GROUP" "$APP_DIR/.env"
	chmod 0640 "$APP_DIR/.env"
fi

echo "Both repository runners are installed and started."
echo "Re-login $ADMIN_USER before accessing $APP_DIR without sudo."
