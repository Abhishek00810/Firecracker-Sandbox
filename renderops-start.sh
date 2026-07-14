#!/usr/bin/env bash
# renderops-start.sh — host setup + launch the compiled backend binary.
# Intended to be run AS ROOT by systemd (renderops.service). Does the one-time
# host setup (slots, fcvm user, snapshot prep) then execs the compiled binary
# in the same process, so all exported env (FC_RUN_UID/GID, SLOT_COUNT) and the
# sourced .env propagate to the binary. server.sh remains the manual fallback.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$SCRIPT_DIR/backend"
ENV_FILE="$BACKEND_DIR/.env"

echo "[server] === Sandbox Backend Startup ==="

# ── 1. Kill any running server or firecracker processes ──────────────────────
# systemd normally stops the previous binary by tracked PID, but we still sweep
# stale processes defensively: the compiled binary (renderops-api), any leftover
# `go run` wrapper + its /tmp/go-build child from manual server.sh runs (the
# pattern matches the real path .../go-build<NNN>/.../exe/main — the old
# `go-build/.*/main` pattern missed it because nothing follows "go-build" with a
# slash), and firecracker.
echo "[server] Killing stale processes..."
sudo pkill -f "renderops-api"      2>/dev/null || true
sudo pkill -f "go run ./cmd/api"   2>/dev/null || true
sudo pkill -f "go-build.*/exe/main" 2>/dev/null || true
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

# ── 3.4 Dedicated non-root user for Firecracker VMMs ─────────────────────────
# Firecracker child processes drop from root to this unprivileged user (via setpriv
# in the Go launcher); the backend itself stays root for netns/iptables/cgroup/mounts.
# This contains an escaped guest — it lands as a no-login nobody, not root. The user
# must be in the kvm group to open /dev/kvm, own the slot TAPs, and own the socket +
# snapshot dirs so the VMM can create its API/vsock sockets and snapshot files.
FC_USER="fcvm"
if ! id -u "$FC_USER" >/dev/null 2>&1; then
    echo "[server] Creating non-root Firecracker user '$FC_USER'..."
    sudo useradd --system --no-create-home --shell /usr/sbin/nologin "$FC_USER"
fi
sudo usermod -aG kvm "$FC_USER"
export FC_RUN_UID="$(id -u "$FC_USER")"
export FC_RUN_GID="$(id -g "$FC_USER")"

sudo mkdir -p /tmp/fc-sockets /dev/shm/fc-snapshots
sudo chown -R "$FC_RUN_UID:$FC_RUN_GID" /tmp/fc-sockets /dev/shm/fc-snapshots

# Let fcvm reach the kernel/rootfs/initrd. Production approach: a POSIX ACL granting
# ONLY fcvm traverse on the path + read on the assets — least privilege, unlike a plain
# chmod o+x which would open the home dir to every user. Falls back to chmod if the acl
# tools aren't installed. (Cleaner long-term: host assets under /opt or /srv instead of a
# home dir, so no per-user grant is needed at all.)
ASSET_FILES=(
    "$SCRIPT_DIR/assets/kernel/vmlinux"
    "$SCRIPT_DIR/assets/rootfs/rootfs-alpine.ext4"
    "$SCRIPT_DIR/assets/initramfs.cpio.gz"
)
if command -v setfacl >/dev/null 2>&1; then
    p="$SCRIPT_DIR"
    while [ "$p" != "/" ]; do sudo setfacl -m u:"$FC_USER":--x "$p" 2>/dev/null || true; p="$(dirname "$p")"; done
    for f in "${ASSET_FILES[@]}"; do sudo setfacl -m u:"$FC_USER":r-- "$f" 2>/dev/null || true; done
else
    echo "[server]   setfacl not found; using chmod fallback (install 'acl' for least-privilege)"
    p="$SCRIPT_DIR"
    while [ "$p" != "/" ]; do sudo chmod o+x "$p" 2>/dev/null || true; p="$(dirname "$p")"; done
    for f in "${ASSET_FILES[@]}"; do sudo chmod o+r "$f" 2>/dev/null || true; done
fi
echo "[server]   Firecracker runs as $FC_USER (uid=$FC_RUN_UID gid=$FC_RUN_GID, kvm group)"

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

# Tear down any legacy FC_SNAT/FC_FWD iptables chains left by a previous (pre-nftables) boot,
# so they can't linger and double-NAT / interfere with the nftables table below.
sudo iptables -t nat -D POSTROUTING -j FC_SNAT 2>/dev/null || true
sudo iptables -D FORWARD -j FC_FWD 2>/dev/null || true
sudo iptables -t nat -F FC_SNAT 2>/dev/null || true; sudo iptables -t nat -X FC_SNAT 2>/dev/null || true
sudo iptables -F FC_FWD 2>/dev/null || true; sudo iptables -X FC_FWD 2>/dev/null || true

# Egress firewall: nftables (replaces the per-slot FC_SNAT/FC_FWD iptables — ~150 rules -> ~6).
# One table, O(1) sets, DEFAULT-DENY forward owned by nftables, conntrack fast-path.
# Idempotent: destroy+recreate atomically. slot_veths is populated below; blocked_veths is
# toggled at runtime by SetSlotEgress (Go) for the internet on/off control.
sudo nft -f - <<NFT
destroy table inet fc
table inet fc {
	set slot_veths    { type ifname; }
	set blocked_veths { type ifname; }
	chain forward {
		type filter hook forward priority filter; policy drop;
		iifname @blocked_veths drop
		oifname @blocked_veths drop
		ct state established,related accept
		ct state invalid drop
		iifname @slot_veths accept
	}
	chain postrouting {
		type nat hook postrouting priority srcnat;
		ip saddr 10.66.0.0/16 oifname "$MAIN_IF" masquerade
	}
}
NFT
# Neutralize the legacy iptables FORWARD drop so the nftables policy-drop is authoritative.
sudo iptables -P FORWARD ACCEPT

# Create all slots in parallel — each slot uses unique names/IPs so no conflicts.
# Per-namespace iptables are also parallelized (each runs in its own netns).
# Host-level iptables rules are applied sequentially after all slots are ready.
for i in $(seq 0 $((SLOT_COUNT-1))); do
    (
        H_IP="10.$((66 + i/256)).$((i % 256)).1"
        N_IP="10.$((66 + i/256)).$((i % 256)).2"

        sudo ip netns add fc-ns-$i
        sudo ip netns exec fc-ns-$i ip link set lo up
        sudo ip netns exec fc-ns-$i ip tuntap add fc-tap-$i mode tap user "$FC_RUN_UID" group "$FC_RUN_GID"
        sudo ip netns exec fc-ns-$i ip addr add $TEMPLATE_HOST_IP/30 dev fc-tap-$i
        sudo ip netns exec fc-ns-$i ip link set fc-tap-$i up

        sudo ip link add veth-fc-$i type veth peer name veth-ns-$i
        sudo ip link set veth-ns-$i netns fc-ns-$i
        sudo ip addr add $H_IP/30 dev veth-fc-$i
        sudo ip link set veth-fc-$i up
        sudo ip netns exec fc-ns-$i ip addr add $N_IP/30 dev veth-ns-$i
        sudo ip netns exec fc-ns-$i ip link set veth-ns-$i up
        sudo ip netns exec fc-ns-$i ip route add default via $H_IP
        sudo ip netns exec fc-ns-$i nft "add table ip fc_ns; add chain ip fc_ns postrouting { type nat hook postrouting priority srcnat; }; add rule ip fc_ns postrouting ip saddr 172.16.0.0/30 masquerade"
    ) &
done
wait

# Register every slot's veth in the nftables set — one masquerade + forward rule now serve
# ALL slots via @slot_veths (replaces the per-slot FC_SNAT/FC_FWD host rules). Added as a
# single element list (atomic).
ELEMS=$(for i in $(seq 0 $((SLOT_COUNT-1))); do printf '"veth-fc-%d", ' "$i"; done | sed 's/, $//')
sudo nft add element inet fc slot_veths "{ $ELEMS }"
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
# Exec the prebuilt binary (built by deploy: `go build -o renderops-api ./cmd/api`).
# Running under systemd as root, so no sudo wrapper. exec replaces this script so
# systemd tracks the binary's PID directly — clean restarts, no orphaned child.
echo "[server] Starting backend binary (logs below)..."
cd "$BACKEND_DIR"
exec "$BACKEND_DIR/renderops-api"
