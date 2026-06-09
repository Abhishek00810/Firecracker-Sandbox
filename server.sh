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
# `go run ./cmd/api` compiles to a temp binary and execs it as a child named
# `main`; killing only the `go run` wrapper leaves that child holding :8080,
# so the next start fails with "address already in use" and its template build
# collides on fctap0. Kill the wrapper, the compiled child, and firecracker.
echo "[server] Killing stale processes..."
sudo pkill -f "go run ./cmd/api"   2>/dev/null || true
sudo pkill -f "go-build/.*/main"   2>/dev/null || true
sudo pkill -f firecracker          2>/dev/null || true
sleep 1

# Hard guard: refuse to start a second backend if :8080 is still held.
PORT_PIDS=$(sudo ss -ltnp 'sport = :8080' 2>/dev/null | grep -oE 'pid=[0-9]+' | cut -d= -f2 | sort -u || true)
if [ -n "$PORT_PIDS" ]; then
    echo "[server]   :8080 still held by PID(s): $PORT_PIDS — killing"
    for p in $PORT_PIDS; do sudo kill "$p" 2>/dev/null || true; done
    sleep 1
fi
if sudo ss -ltn 'sport = :8080' 2>/dev/null | grep -q LISTEN; then
    echo "[server] ERROR: :8080 still in use after cleanup; refusing to start." >&2
    exit 1
fi

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
export SLOT_COUNT=50            # exported so the Go backend's slot pool matches exactly
TEMPLATE_HOST_IP="172.16.0.1"   # must match allocateNetwork() index-0 host IP

echo "[server] Setting up $SLOT_COUNT network slots..."
echo 1 | sudo tee /proc/sys/net/ipv4/ip_forward > /dev/null

MAIN_IF=$(ip route show default 2>/dev/null | awk '/^default/{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1); exit}')
echo "[server]   outbound interface: ${MAIN_IF:-<none>}"

# Clean up ALL leftover slots by PATTERN (not just the current SLOT_COUNT range), so
# orphan netns/veth from a previously-larger SLOT_COUNT can't accumulate across runs.
# (Deleting a netns also removes its veth peer; the veth loop is a safety net for strays.)
for ns in $(ip netns list 2>/dev/null | awk '/^fc-ns-/{print $1}'); do
    sudo ip netns del "$ns" 2>/dev/null || true
done
for veth in $(ip -o link show 2>/dev/null | grep -oE 'veth-fc-[0-9]+' | sort -u); do
    sudo ip link del "$veth" 2>/dev/null || true
done
# Sweep leaked per-VM cgroups (procs were killed above, so the dirs are empty).
sudo find /sys/fs/cgroup/sandbox -type d -name 'vm-*' -exec rmdir {} + 2>/dev/null || true

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

# Create all slots in parallel — each slot uses unique names/IPs so no conflicts.
# Per-namespace iptables are also parallelized (each runs in its own netns).
# Host-level iptables rules are applied sequentially after all slots are ready.
for i in $(seq 0 $((SLOT_COUNT-1))); do
    (
        H_IP="10.$((66 + i/256)).$((i % 256)).1"
        N_IP="10.$((66 + i/256)).$((i % 256)).2"

        sudo ip netns add fc-ns-$i
        sudo ip netns exec fc-ns-$i ip link set lo up
        sudo ip netns exec fc-ns-$i ip tuntap add fc-tap-$i mode tap
        sudo ip netns exec fc-ns-$i ip addr add $TEMPLATE_HOST_IP/30 dev fc-tap-$i
        sudo ip netns exec fc-ns-$i ip link set fc-tap-$i up

        sudo ip link add veth-fc-$i type veth peer name veth-ns-$i
        sudo ip link set veth-ns-$i netns fc-ns-$i
        sudo ip addr add $H_IP/30 dev veth-fc-$i
        sudo ip link set veth-fc-$i up
        sudo ip netns exec fc-ns-$i ip addr add $N_IP/30 dev veth-ns-$i
        sudo ip netns exec fc-ns-$i ip link set veth-ns-$i up
        sudo ip netns exec fc-ns-$i ip route add default via $H_IP
        sudo ip netns exec fc-ns-$i iptables -t nat -A POSTROUTING -s 172.16.0.0/30 -o veth-ns-$i -j MASQUERADE
    ) &
done
wait

# Host-level iptables rules — sequential to avoid concurrent table lock conflicts
if [ -n "$MAIN_IF" ]; then
    for i in $(seq 0 $((SLOT_COUNT-1))); do
        N_IP="10.$((66 + i/256)).$((i % 256)).2"
        sudo iptables -t nat -A FC_SNAT -s $N_IP/32 -o $MAIN_IF -j MASQUERADE
        sudo iptables -A FC_FWD -i veth-fc-$i -j ACCEPT
        sudo iptables -A FC_FWD -o veth-fc-$i -m state --state RELATED,ESTABLISHED -j ACCEPT
    done
fi
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

# ── 5. Rootfs is self-contained (no runtime mount) ───────────────────────────
# The guest-agent, pip config (/etc/pip.conf), DNS, and network identity
# (/etc/vm-network.env) are baked into the rootfs at build time via Dockerfile.rootfs
# / build_rootfs.sh. The server no longer mounts the rootfs at runtime — that removes
# the loop-mount that could leak and break VM networking.
# To change the guest-agent or rootfs contents, run: bash build_rootfs.sh
echo "[server] Using prebuilt rootfs (guest-agent + configs baked in)"

# ── 6. Start the server ───────────────────────────────────────────────────────
echo "[server] Starting backend (logs below)..."
cd "$BACKEND_DIR"
exec sudo -E env PATH="$PATH" /usr/local/go/bin/go run ./cmd/api/main.go
