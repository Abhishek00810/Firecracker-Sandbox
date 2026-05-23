#!/usr/bin/env bash
# server.sh — start the sandbox backend on Azure
# Usage: bash server.sh
# Run from: ~/Firecracker-Sandbox/backend

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$SCRIPT_DIR/backend"
ENV_FILE="$BACKEND_DIR/.env"

echo "[server] === Sandbox Backend Startup ==="

# ── 1. Kill any running server or firecracker processes ──────────────────────
echo "[server] Killing stale processes..."
sudo pkill -f "go run ./cmd/api" 2>/dev/null || true
sudo pkill -f firecracker          2>/dev/null || true
sleep 1

# ── 2. Clean up ALL stale TAP devices ────────────────────────────────────────
# Firecracker leaves TAP devices behind on crash/kill. Also handles the
# "TAP rename failing" case: if a previous run died mid-restore, fctap0
# (the template TAP) may still exist and block the next restore.
echo "[server] Cleaning up stale TAP devices..."
for tap in $(ip link show 2>/dev/null | grep -o 'fctap[0-9]*' | sort -u); do
    echo "[server]   deleting $tap"
    sudo ip link delete "$tap" 2>/dev/null || true
done

# ── 3. Clean up stale vsock sockets ──────────────────────────────────────────
echo "[server] Cleaning up stale vsock sockets..."
sudo rm -f /tmp/fc-sockets/*.sock 2>/dev/null || true

# ── 3.5 Pre-create network namespaces and TAP slots ──────────────────────────
# Each slot gets an isolated netns with a TAP (same IP as template host so
# the guest's baked-in gateway works on restore without any reconfiguration)
# and a veth pair for outbound internet access from user code.
SLOT_COUNT=500
TEMPLATE_HOST_IP="172.16.0.1"   # must match allocateNetwork() index-0 host IP

echo "[server] Setting up $SLOT_COUNT network slots..."
echo 1 | sudo tee /proc/sys/net/ipv4/ip_forward > /dev/null

MAIN_IF=$(ip route show default 2>/dev/null | awk '/^default/{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1); exit}')
echo "[server]   outbound interface: ${MAIN_IF:-<none>}"

# Clean up any slots left over from a previous run
for i in $(seq 0 $((SLOT_COUNT-1))); do
    sudo ip netns del fc-ns-$i 2>/dev/null || true
    sudo ip link del veth-fc-$i 2>/dev/null || true
done

# Recreate custom iptables chains (tear down first for idempotency)
sudo iptables -t nat -D POSTROUTING -j FC_SNAT 2>/dev/null || true
sudo iptables -D FORWARD -j FC_FWD 2>/dev/null || true
sudo iptables -t nat -F FC_SNAT 2>/dev/null || true
sudo iptables -F FC_FWD 2>/dev/null || true
sudo iptables -t nat -X FC_SNAT 2>/dev/null || true
sudo iptables -X FC_FWD 2>/dev/null || true
sudo iptables -t nat -N FC_SNAT
sudo iptables -N FC_FWD
sudo iptables -t nat -A POSTROUTING -j FC_SNAT
sudo iptables -A FORWARD -j FC_FWD

for i in $(seq 0 $((SLOT_COUNT-1))); do
    # Isolated network namespace
    sudo ip netns add fc-ns-$i
    sudo ip netns exec fc-ns-$i ip link set lo up

    # TAP with the template's host-side IP so the guest's baked-in gateway
    # (172.16.0.1) resolves without any post-restore reconfiguration
    sudo ip netns exec fc-ns-$i ip tuntap add fc-tap-$i mode tap
    sudo ip netns exec fc-ns-$i ip addr add $TEMPLATE_HOST_IP/30 dev fc-tap-$i
    sudo ip netns exec fc-ns-$i ip link set fc-tap-$i up

    # veth pair: veth-fc-$i in default ns, veth-ns-$i in fc-ns-$i
    H_IP="10.66.$i.1"
    N_IP="10.66.$i.2"
    sudo ip link add veth-fc-$i type veth peer name veth-ns-$i
    sudo ip link set veth-ns-$i netns fc-ns-$i
    sudo ip addr add $H_IP/30 dev veth-fc-$i
    sudo ip link set veth-fc-$i up
    sudo ip netns exec fc-ns-$i ip addr add $N_IP/30 dev veth-ns-$i
    sudo ip netns exec fc-ns-$i ip link set veth-ns-$i up
    sudo ip netns exec fc-ns-$i ip route add default via $H_IP

    # Outbound NAT for user code making network requests inside the sandbox
    if [ -n "$MAIN_IF" ]; then
        sudo iptables -t nat -A FC_SNAT -s $N_IP/32 -o $MAIN_IF -j MASQUERADE
        sudo iptables -A FC_FWD -i veth-fc-$i -j ACCEPT
        sudo iptables -A FC_FWD -o veth-fc-$i -m state --state RELATED,ESTABLISHED -j ACCEPT
    fi
done
echo "[server]   $SLOT_COUNT network slots ready"

# ── 4. Load env vars ─────────────────────────────────────────────────────────
if [[ ! -f "$ENV_FILE" ]]; then
    echo "[server] ERROR: .env not found at $ENV_FILE"
    exit 1
fi
set -a
source "$ENV_FILE"
set +a
echo "[server] Environment loaded from $ENV_FILE"

# ── 5. Build guest-agent and update rootfs ───────────────────────────────────
echo "[server] Building guest-agent..."
cd "$SCRIPT_DIR/guest-agent"
GOOS=linux GOARCH=amd64 /usr/local/go/bin/go build -o guest-agent-amd64 .

echo "[server] Updating rootfs..."
ROOTFS="${ASSETS_PATH:-$SCRIPT_DIR/assets}/rootfs/rootfs-alpine.ext4"
sudo umount /mnt 2>/dev/null || true
sudo mount -o loop "$ROOTFS" /mnt
sudo cp guest-agent-amd64 /mnt/usr/local/bin/guest-agent
sudo chmod +x /mnt/usr/local/bin/guest-agent
sudo umount /mnt
echo "[server] Rootfs updated"

# ── 6. Start the server ───────────────────────────────────────────────────────
echo "[server] Starting backend (logs below)..."
cd "$BACKEND_DIR"
exec sudo -E env PATH="$PATH" /usr/local/go/bin/go run ./cmd/api/main.go
