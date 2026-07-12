#!/bin/sh
# Container equivalent of server.sh's host bootstrap. The Go slot pool assumes
# fc-ns-0..SLOT_COUNT-1 (netns + TAP + veth + NAT) already exist and never
# creates them itself, so they must be built here before the API starts.
# Requires a privileged container (netns, tuntap, iptables, sysctl).
set -eu

SLOT_COUNT="${SLOT_COUNT:-50}"
# Must match allocateNetwork() index-0 host IP so restored guests keep their
# baked-in gateway.
TEMPLATE_HOST_IP="172.16.0.1"

# The socket dir is a named volume; sockets from a previous run are stale.
rm -f /tmp/fc-sockets/*.sock 2>/dev/null || true

echo 1 > /proc/sys/net/ipv4/ip_forward

MAIN_IF=$(ip route show default 2>/dev/null | awk '/^default/{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1); exit}')
echo "[entrypoint] outbound interface: ${MAIN_IF:-<none>}"

# Recreate custom iptables chains (tear down first for idempotency)
iptables -t nat -D POSTROUTING -j FC_SNAT 2>/dev/null || true
iptables -D FORWARD -j FC_FWD 2>/dev/null || true
iptables -t nat -F FC_SNAT 2>/dev/null || true
iptables -F FC_FWD 2>/dev/null || true
iptables -t nat -X FC_SNAT 2>/dev/null || true
iptables -X FC_FWD 2>/dev/null || true
iptables -t nat -N FC_SNAT
iptables -N FC_FWD
iptables -t nat -A POSTROUTING -j FC_SNAT
iptables -A FORWARD -j FC_FWD

# Clean leftover slots by pattern so a previously-larger SLOT_COUNT can't
# leave orphans (deleting a netns also removes its veth peer).
for ns in $(ip netns list 2>/dev/null | awk '/^fc-ns-/{print $1}'); do
    ip netns del "$ns" 2>/dev/null || true
done
for veth in $(ip -o link show 2>/dev/null | grep -oE 'veth-fc-[0-9]+' | sort -u); do
    ip link del "$veth" 2>/dev/null || true
done

echo "[entrypoint] creating $SLOT_COUNT network slots..."
i=0
while [ "$i" -lt "$SLOT_COUNT" ]; do
    (
        H_IP="10.$((66 + i / 256)).$((i % 256)).1"
        N_IP="10.$((66 + i / 256)).$((i % 256)).2"

        ip netns add "fc-ns-$i"
        ip netns exec "fc-ns-$i" ip link set lo up
        if [ "${FC_RUN_UID:-0}" -gt 0 ]; then
            ip netns exec "fc-ns-$i" ip tuntap add "fc-tap-$i" mode tap \
                user "$FC_RUN_UID" group "${FC_RUN_GID:-$FC_RUN_UID}"
        else
            ip netns exec "fc-ns-$i" ip tuntap add "fc-tap-$i" mode tap
        fi
        ip netns exec "fc-ns-$i" ip addr add "$TEMPLATE_HOST_IP/30" dev "fc-tap-$i"
        ip netns exec "fc-ns-$i" ip link set "fc-tap-$i" up

        ip link add "veth-fc-$i" type veth peer name "veth-ns-$i"
        ip link set "veth-ns-$i" netns "fc-ns-$i"
        ip addr add "$H_IP/30" dev "veth-fc-$i"
        ip link set "veth-fc-$i" up
        ip netns exec "fc-ns-$i" ip addr add "$N_IP/30" dev "veth-ns-$i"
        ip netns exec "fc-ns-$i" ip link set "veth-ns-$i" up
        ip netns exec "fc-ns-$i" ip route add default via "$H_IP"
        ip netns exec "fc-ns-$i" iptables -t nat -A POSTROUTING \
            -s 172.16.0.0/30 -o "veth-ns-$i" -j MASQUERADE
    ) &
    i=$((i + 1))
done
wait

# Host-level rules are sequential to avoid iptables lock contention.
if [ -n "$MAIN_IF" ]; then
    i=0
    while [ "$i" -lt "$SLOT_COUNT" ]; do
        N_IP="10.$((66 + i / 256)).$((i % 256)).2"
        iptables -t nat -A FC_SNAT -s "$N_IP/32" -o "$MAIN_IF" -j MASQUERADE
        iptables -A FC_FWD -i "veth-fc-$i" -j ACCEPT
        iptables -A FC_FWD -o "veth-fc-$i" -m state --state RELATED,ESTABLISHED -j ACCEPT
        i=$((i + 1))
    done
fi
echo "[entrypoint] $SLOT_COUNT network slots ready"

exec /app/app "$@"
