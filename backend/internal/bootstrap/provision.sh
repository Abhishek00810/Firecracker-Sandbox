#!/usr/bin/env bash
# provision.sh — make a fresh KVM host ready to run RenderOps microVMs.
#
# EMBEDDED in the agent (go:embed) and run as root on startup. It sets up the
# fcvm user, socket/snapshot dirs, asset ACLs, and SLOT_COUNT network slots
# (netns + TAP + veth + nftables), all under $ROOT_DIRECTORY. Self-contained:
# no sudo (the agent already runs as root), no external scripts, no assumptions
# about prior host state. Idempotent — it destroys and recreates so re-runs
# converge on the same state.
set -euo pipefail

ROOT="${ROOT_DIRECTORY:?ROOT_DIRECTORY required}"
SLOT_COUNT="${SLOT_COUNT:-50}"
FC_USER="${FC_USER:-fcvm}"
ASSETS_DIR="${ASSETS_DIR:-$ROOT/assets}"
SOCKET_DIR="${SOCKET_DIR:-$ROOT/sockets}"
SNAPSHOT_DIR="${SNAPSHOT_DIR:-/dev/shm/fc-snapshots}"
TEMPLATE_HOST_IP="172.16.0.1"   # every slot's TAP; matches the guest's baked-in gateway

echo "[provision] root=$ROOT slots=$SLOT_COUNT user=$FC_USER"

# ── 0. Ensure host tooling (fresh-VM friendly) ───────────────────────────────
# A newly allocated VM may not have nftables/acl installed. Best-effort install
# via apt; on other package managers we warn and let the steps below fail loudly.
MISSING=()
command -v ip      >/dev/null 2>&1 || MISSING+=(iproute2)
command -v nft     >/dev/null 2>&1 || MISSING+=(nftables)
command -v setpriv >/dev/null 2>&1 || MISSING+=(util-linux)
command -v setfacl >/dev/null 2>&1 || MISSING+=(acl)
if [ ${#MISSING[@]} -gt 0 ]; then
    if command -v apt-get >/dev/null 2>&1; then
        echo "[provision] installing: ${MISSING[*]}"
        DEBIAN_FRONTEND=noninteractive apt-get update -qq || true
        DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${MISSING[@]}" || true
    else
        echo "[provision] WARNING: missing ${MISSING[*]} and no apt-get to install them" >&2
    fi
fi

# ── 1. Dedicated non-root user for Firecracker VMMs ──────────────────────────
# VMMs drop from root to this no-login user (via setpriv in the Go launcher), so
# an escaped guest lands as nobody. It must be in the kvm group and own the
# socket/snapshot dirs + the VM assets.
if ! id -u "$FC_USER" >/dev/null 2>&1; then
    echo "[provision] creating user $FC_USER"
    useradd --system --no-create-home --shell /usr/sbin/nologin "$FC_USER"
fi
usermod -aG kvm "$FC_USER" 2>/dev/null || true
FC_RUN_UID="$(id -u "$FC_USER")"
FC_RUN_GID="$(id -g "$FC_USER")"

mkdir -p "$SOCKET_DIR" "$SNAPSHOT_DIR"
chown -R "$FC_RUN_UID:$FC_RUN_GID" "$SOCKET_DIR" "$SNAPSHOT_DIR"

# Least-privilege asset access: traverse on the path to the assets + read on the
# files themselves. POSIX ACL where available, chmod fallback otherwise.
ASSET_FILES=(
    "$ASSETS_DIR/kernel/vmlinux"
    "$ASSETS_DIR/rootfs/rootfs-alpine.ext4"
    "$ASSETS_DIR/initramfs.cpio.gz"
)
if command -v setfacl >/dev/null 2>&1; then
    p="$ASSETS_DIR"; while [ "$p" != "/" ]; do setfacl -m u:"$FC_USER":--x "$p" 2>/dev/null || true; p="$(dirname "$p")"; done
    for f in "${ASSET_FILES[@]}"; do [ -e "$f" ] && setfacl -m u:"$FC_USER":r-- "$f" 2>/dev/null || true; done
else
    p="$ASSETS_DIR"; while [ "$p" != "/" ]; do chmod o+x "$p" 2>/dev/null || true; p="$(dirname "$p")"; done
    for f in "${ASSET_FILES[@]}"; do [ -e "$f" ] && chmod o+r "$f" 2>/dev/null || true; done
fi

# ── 2. Clean up stale TAPs / vsock sockets / per-VM cgroups ───────────────────
for tap in $(ip link show 2>/dev/null | grep -o 'fctap[0-9]*' | sort -u); do ip link delete "$tap" 2>/dev/null || true; done
rm -f "$SOCKET_DIR"/*.sock 2>/dev/null || true
find /sys/fs/cgroup/sandbox -type d -name 'vm-*' -exec rmdir {} + 2>/dev/null || true

# ── 3. Network slots: one netns + TAP + veth per slot ────────────────────────
echo 1 > /proc/sys/net/ipv4/ip_forward
MAIN_IF=$(ip route show default 2>/dev/null | awk '/^default/{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1); exit}')
echo "[provision] outbound interface: ${MAIN_IF:-<none>}"

# Tear down leftover slots by pattern (covers a previously-larger SLOT_COUNT).
for ns in $(ip netns list 2>/dev/null | awk '/^fc-ns-/{print $1}'); do ip netns del "$ns" 2>/dev/null || true; done
for veth in $(ip -o link show 2>/dev/null | grep -oE 'veth-fc-[0-9]+' | sort -u); do ip link del "$veth" 2>/dev/null || true; done

# Egress firewall: one nftables table, default-deny forward, conntrack fast-path,
# O(1) sets. slot_veths is populated below; blocked_veths is toggled at runtime by
# SetSlotEgress (Go) for the per-sandbox internet on/off control. Idempotent via
# `destroy` (no error if the table is absent).
nft -f - <<NFT
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

# Create all slots in parallel — each uses unique names/IPs so there are no
# conflicts. Slot i: netns fc-ns-i, TAP fc-tap-i (172.16.0.1/30, same as template
# so restore needs no reconfig), veth-fc-i (host) <-> veth-ns-i (10.66.x.y).
for i in $(seq 0 $((SLOT_COUNT-1))); do
    (
        H_IP="10.$((66 + i/256)).$((i % 256)).1"
        N_IP="10.$((66 + i/256)).$((i % 256)).2"

        ip netns add fc-ns-$i
        ip netns exec fc-ns-$i ip link set lo up
        ip netns exec fc-ns-$i ip tuntap add fc-tap-$i mode tap user "$FC_RUN_UID" group "$FC_RUN_GID"
        ip netns exec fc-ns-$i ip addr add $TEMPLATE_HOST_IP/30 dev fc-tap-$i
        ip netns exec fc-ns-$i ip link set fc-tap-$i up

        ip link add veth-fc-$i type veth peer name veth-ns-$i
        ip link set veth-ns-$i netns fc-ns-$i
        ip addr add $H_IP/30 dev veth-fc-$i
        ip link set veth-fc-$i up
        ip netns exec fc-ns-$i ip addr add $N_IP/30 dev veth-ns-$i
        ip netns exec fc-ns-$i ip link set veth-ns-$i up
        ip netns exec fc-ns-$i ip route add default via $H_IP
        ip netns exec fc-ns-$i nft "add table ip fc_ns; add chain ip fc_ns postrouting { type nat hook postrouting priority srcnat; }; add rule ip fc_ns postrouting ip saddr 172.16.0.0/30 masquerade"
    ) &
done
wait

# Register every slot's host veth in the nftables set — one masquerade + forward
# rule serves all slots via @slot_veths.
ELEMS=$(for i in $(seq 0 $((SLOT_COUNT-1))); do printf '"veth-fc-%d", ' "$i"; done | sed 's/, $//')
nft add element inet fc slot_veths "{ $ELEMS }"
echo "[provision] $SLOT_COUNT network slots ready"
