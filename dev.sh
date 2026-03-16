#!/bin/bash
set -e

ROOTFS=/Users/abhishekdadwal/nothing/sandbox_env/assets/rootfs/rootfs-alpine.ext4
GUEST_AGENT_DIR=/Users/abhishekdadwal/nothing/sandbox_env/guest-agent
BACKEND_DIR=/Users/abhishekdadwal/nothing/sandbox_env/backend

echo "==> killing old server and VMs..."
pkill -f 'go run cmd/api' 2>/dev/null || true
pkill -f firecracker-v1 2>/dev/null || true
# clean up any leftover TAP devices from a previous run
ip link show | grep -o 'fctap[0-9]*' | xargs -I{} ip link delete {} 2>/dev/null || true
sleep 2

echo "==> configuring networking..."
# enable IP forwarding so the host can route VM traffic to the internet
echo 1 > /proc/sys/net/ipv4/ip_forward

# flush any rules we may have set in a previous run to avoid duplicates
iptables -t nat -D POSTROUTING -s 172.16.0.0/12 ! -d 172.16.0.0/12 -j MASQUERADE 2>/dev/null || true
iptables -D FORWARD -m state --state ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || true

# NAT: masquerade VM traffic going outbound (makes it look like it came from the host)
iptables -t nat -A POSTROUTING -s 172.16.0.0/12 ! -d 172.16.0.0/12 -j MASQUERADE

# allow response packets back into the VM
iptables -I FORWARD 1 -m state --state ESTABLISHED,RELATED -j ACCEPT

# egress allow-list: DNS + HTTP + HTTPS only
iptables -A FORWARD -s 172.16.0.0/12 -p udp --dport 53 -j ACCEPT
iptables -A FORWARD -s 172.16.0.0/12 -p tcp --dport 53 -j ACCEPT
iptables -A FORWARD -s 172.16.0.0/12 -p tcp --dport 80  -j ACCEPT
iptables -A FORWARD -s 172.16.0.0/12 -p tcp --dport 443 -j ACCEPT

# block SSRF: VMs cannot reach private/internal IP ranges
iptables -A FORWARD -s 172.16.0.0/12 -d 10.0.0.0/8       -j DROP
iptables -A FORWARD -s 172.16.0.0/12 -d 192.168.0.0/16   -j DROP
iptables -A FORWARD -s 172.16.0.0/12 -d 169.254.0.0/16   -j DROP  # cloud metadata endpoint
iptables -A FORWARD -s 172.16.0.0/12 -d 172.16.0.0/12    -j DROP  # no VM-to-VM traffic

# drop all other outbound traffic from VMs (no port scanning, no raw sockets, etc.)
iptables -A FORWARD -s 172.16.0.0/12 -j DROP
echo "==> networking configured"

echo "==> building guest-agent..."
cd "$GUEST_AGENT_DIR"
GOOS=linux GOARCH=arm64 go build -o guest-agent-new .

echo "==> updating rootfs..."
umount /mnt 2>/dev/null || true
mount -o loop "$ROOTFS" /mnt
cp guest-agent-new /mnt/usr/local/bin/guest-agent
cp kernel_bridge.py /mnt/usr/local/bin/kernel_bridge.py
chmod +x /mnt/usr/local/bin/guest-agent
chmod +x /mnt/usr/local/bin/kernel_bridge.py

umount /mnt
echo "==> rootfs updated"

echo "==> building initramfs..."
INITRAMFS_WORK=$(mktemp -d)
INITRAMFS_OUT=/Users/abhishekdadwal/nothing/sandbox_env/assets/initramfs.cpio.gz

# Create directory structure needed inside initramfs
mkdir -p "$INITRAMFS_WORK/bin"
mkdir -p "$INITRAMFS_WORK/sbin"
mkdir -p "$INITRAMFS_WORK/dev"
mkdir -p "$INITRAMFS_WORK/proc"
mkdir -p "$INITRAMFS_WORK/sys"
mkdir -p "$INITRAMFS_WORK/lower"
mkdir -p "$INITRAMFS_WORK/upper"
mkdir -p "$INITRAMFS_WORK/merged"
mkdir -p "$INITRAMFS_WORK/oldroot"

# Copy busybox from Alpine rootfs — it provides mount, mkdir, pivot_root, etc.
mount -o loop "$ROOTFS" /mnt
cp /mnt/bin/busybox "$INITRAMFS_WORK/bin/busybox"
mkdir -p "$INITRAMFS_WORK/lib"
cp /mnt/lib/ld-musl-aarch64.so.1 "$INITRAMFS_WORK/lib/"
umount /mnt

# Create symlinks so busybox responds to each command name
for cmd in sh mount umount mkdir rmdir sleep switch_root; do
    ln -sf busybox "$INITRAMFS_WORK/bin/$cmd"
done
ln -sf ../bin/busybox "$INITRAMFS_WORK/sbin/pivot_root"

# Copy init script
cp "$GUEST_AGENT_DIR/initramfs/init" "$INITRAMFS_WORK/init"
chmod +x "$INITRAMFS_WORK/init"

# Bundle into cpio.gz archive
(cd "$INITRAMFS_WORK" && find . | cpio -H newc -o 2>/dev/null | gzip) > "$INITRAMFS_OUT"
rm -rf "$INITRAMFS_WORK"
echo "==> initramfs built: $INITRAMFS_OUT"

echo "==> starting server..."
cd "$BACKEND_DIR"
exec env \
  ASSETS_PATH=/Users/abhishekdadwal/nothing/sandbox_env/assets \
  FIRECRACKER_BINARY=/Users/abhishekdadwal/nothing/sandbox_env/release-v1.7.0-aarch64/firecracker-v1.7.0-aarch64 \
  go run cmd/api/main.go
