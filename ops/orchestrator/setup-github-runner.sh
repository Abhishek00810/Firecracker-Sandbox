#!/usr/bin/env bash
# Install the renderops-sandbox self-hosted runner on the orchestrator server.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root with sudo" >&2
	exit 1
fi

: "${RUNNER_TOKEN:?temporary renderops-sandbox runner token is required}"

GITHUB_ORG="${GITHUB_ORG:-renderops-ai}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:-renderops-sandbox}"
RUNNER_USER="${RUNNER_USER:-renderops-runner}"
RUNNER_NAME="${RUNNER_NAME:-renderops-orchestrator}"
RUNNER_DIR="${RUNNER_DIR:-/opt/actions-runner-orchestrator}"
DEPLOY_GROUP="${DEPLOY_GROUP:-renderops-deploy}"
RUNNER_VERSION="${RUNNER_VERSION:-2.336.0}"
case "${RUNNER_ARCH:-$(uname -m)}" in
	x86_64 | x64)
		RUNNER_ARCH=x64
		RUNNER_SHA256="${RUNNER_SHA256:-04cf0be1aff4c3ec3554466c39124ca250e3effd8873bb7e8d68535aa9505d5d}"
		;;
	aarch64 | arm64)
		RUNNER_ARCH=arm64
		: "${RUNNER_SHA256:?RUNNER_SHA256 is required for the arm64 runner archive}"
		;;
	*)
		echo "unsupported runner architecture: ${RUNNER_ARCH:-$(uname -m)}" >&2
		exit 1
		;;
esac
RUNNER_ARCHIVE="actions-runner-linux-${RUNNER_ARCH}-${RUNNER_VERSION}.tar.gz"
RUNNER_URL="https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/${RUNNER_ARCHIVE}"

if ! id "$RUNNER_USER" >/dev/null 2>&1; then
	useradd --system --create-home --shell /bin/bash --user-group "$RUNNER_USER"
fi
getent group "$DEPLOY_GROUP" >/dev/null || groupadd --system "$DEPLOY_GROUP"
usermod -aG "$DEPLOY_GROUP" "$RUNNER_USER"
if getent group docker >/dev/null; then
	usermod -aG docker "$RUNNER_USER"
fi

archive="/tmp/$RUNNER_ARCHIVE"
curl --fail --silent --show-error --location "$RUNNER_URL" --output "$archive"
echo "$RUNNER_SHA256  $archive" | sha256sum --check -

install -d -o "$RUNNER_USER" -g "$RUNNER_USER" -m 0750 "$RUNNER_DIR"
if [ ! -x "$RUNNER_DIR/config.sh" ]; then
	tar -xzf "$archive" -C "$RUNNER_DIR"
	chown -R "$RUNNER_USER:$RUNNER_USER" "$RUNNER_DIR"
fi

if [ ! -f "$RUNNER_DIR/.runner" ]; then
	cd "$RUNNER_DIR"
	runuser -u "$RUNNER_USER" -- env HOME="/home/$RUNNER_USER" ./config.sh \
		--unattended \
		--url "https://github.com/$GITHUB_ORG/$GITHUB_REPOSITORY" \
		--token "$RUNNER_TOKEN" \
		--name "$RUNNER_NAME" \
		--labels orchestrator \
		--work _work \
		--replace
fi

cd "$RUNNER_DIR"
if [ ! -f .service ]; then
	./svc.sh install "$RUNNER_USER"
fi
./svc.sh start

unset RUNNER_TOKEN
rm -f "$archive"

echo "Orchestrator GitHub runner installed and started."
