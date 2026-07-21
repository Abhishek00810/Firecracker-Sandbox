#!/usr/bin/env bash
# ONE-TIME setup for the control-plane host (the basic server).
# Run this ONCE, on the basic server, with sudo. It is idempotent — safe to
# re-run. It prepares the box so the CI self-hosted runner can deploy the
# control-plane binary; it does NOT install the binary (the first deploy does).
#
#   sudo bash setup-basic-server.sh
#
# After it finishes it prints two follow-up actions:
#   1) install the printed public key on the bare-metal worker host
#   2) register the GitHub self-hosted runner (label: control-plane)
set -euo pipefail

SERVICE_USER=renderops          # the control-plane service runs as this user
APP_DIR=/opt/renderops          # binary + service HOME (holds the ssh tunnel key)
CFG_DIR=/etc/renderops          # env file
RUNNER_USER="${SUDO_USER:-$(whoami)}"   # the user the GitHub runner runs as
WORKER_HOST=20.228.220.165      # bare-metal worker (edit if it changes)
WORKER_USER=renderopsadmin

echo "==> [1/6] service user + directories"
id -u "$SERVICE_USER" >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0750 "$APP_DIR" "$APP_DIR/.ssh"
install -d -m 0750 "$CFG_DIR"

echo "==> [2/6] tunnel keypair (control-plane -> worker)"
KEY="$APP_DIR/.ssh/worker_key"
if [ ! -f "$KEY" ]; then
  sudo -u "$SERVICE_USER" ssh-keygen -t ed25519 -N '' -f "$KEY" -C "renderops-control-plane-tunnel"
  echo "    generated $KEY"
else
  echo "    $KEY already exists — keeping it"
fi
# Pre-trust the worker host so ssh never prompts.
sudo -u "$SERVICE_USER" ssh-keyscan -H "$WORKER_HOST" > "$APP_DIR/.ssh/known_hosts" 2>/dev/null || true
chown "$SERVICE_USER:$SERVICE_USER" "$APP_DIR/.ssh/known_hosts"
chmod 600 "$APP_DIR/.ssh/known_hosts"

echo "==> [3/6] env file (fill in the secrets!)"
ENV_FILE="$CFG_DIR/control-plane.env"
if [ ! -f "$ENV_FILE" ]; then
  cat > "$ENV_FILE" <<ENV
# ---- FILL THESE IN ----
DATABASE_URL=
WORKER_TOKEN=
# ------------------------
# The control plane opens its own SSH tunnel to the worker using this command.
SSH_COMMAND=ssh -i $KEY -o UserKnownHostsFile=$APP_DIR/.ssh/known_hosts -o StrictHostKeyChecking=accept-new $WORKER_USER@$WORKER_HOST
AGENT_LOCAL_PORT=19876
AGENT_ADDR=127.0.0.1:9876
PORT=8080
LOG_FORMAT=json
LOG_LEVEL=info
ENV
  chmod 640 "$ENV_FILE"; chown root:"$SERVICE_USER" "$ENV_FILE"
  echo "    wrote $ENV_FILE  (edit it: set DATABASE_URL and WORKER_TOKEN)"
else
  echo "    $ENV_FILE already exists — leaving it"
fi

echo "==> [4/6] systemd unit"
# Expects control-plane.service to sit next to this script (checked out by the runner).
UNIT_SRC="$(dirname "$0")/control-plane.service"
if [ -f "$UNIT_SRC" ]; then
  install -m 0644 "$UNIT_SRC" /etc/systemd/system/renderops-control-plane.service
  systemctl daemon-reload
  systemctl enable renderops-control-plane >/dev/null 2>&1 || true
  echo "    installed + enabled renderops-control-plane.service (not started — no binary yet)"
else
  echo "    control-plane.service not found next to this script — copy it manually"
fi

echo "==> [5/6] let the CI runner restart the service without a password"
SUDOERS=/etc/sudoers.d/renderops-deploy
cat > "$SUDOERS" <<SUDO
$RUNNER_USER ALL=(root) NOPASSWD: /usr/bin/install -D -m 0755 * /opt/renderops/renderops-control-plane, /bin/systemctl restart renderops-control-plane, /bin/systemctl status renderops-control-plane*
SUDO
chmod 440 "$SUDOERS"
visudo -cf "$SUDOERS" >/dev/null && echo "    sudoers rule OK for runner user: $RUNNER_USER"

echo "==> [6/6] done"
echo
echo "########################################################################"
echo "NEXT — two manual follow-ups:"
echo
echo "1) Install this control-plane PUBLIC key on the worker host so the tunnel"
echo "   can connect. On the bare-metal host, append to authorized_keys"
echo "   (restricted to the worker port only):"
echo
echo -n '   command="",no-pty,no-agent-forwarding,permitopen="127.0.0.1:9876" '
cat "$KEY.pub"
echo
echo "2) Register the GitHub self-hosted runner on THIS box:"
echo "   repo -> Settings -> Actions -> Runners -> New self-hosted runner -> Linux"
echo "   then run its ./config.sh with:  --labels control-plane"
echo "   and install as a service:       sudo ./svc.sh install && sudo ./svc.sh start"
echo
echo "3) Edit $ENV_FILE — set DATABASE_URL and WORKER_TOKEN."
echo "########################################################################"
