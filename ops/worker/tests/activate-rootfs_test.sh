#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

ROOT="$TMP/worker"
ASSETS="$ROOT/assets"
mkdir -p "$ASSETS/rootfs" "$TMP/build"
printf 'old-rootfs' > "$ASSETS/rootfs/rootfs-alpine.ext4"
printf 'kernel' > "$ASSETS/kernel"
OLD_SUM="$(sha256sum "$ASSETS/rootfs/rootfs-alpine.ext4" | awk '{ print $1 }')"
KERNEL_SUM="$(sha256sum "$ASSETS/kernel" | awk '{ print $1 }')"
printf '%s  rootfs/rootfs-alpine.ext4\n%s  kernel\n' "$OLD_SUM" "$KERNEL_SUM" > "$ASSETS/manifest.sha256"
printf 'stale-bundle' > "$ROOT/renderops-assets.tar.gz"
printf 'stale-marker' > "$ASSETS/.installed-bundle"

printf 'new-rootfs' > "$TMP/build/rootfs-alpine.ext4"
NEW_SUM="$(sha256sum "$TMP/build/rootfs-alpine.ext4" | awk '{ print $1 }')"
printf '%s  rootfs-alpine.ext4\n' "$NEW_SUM" > "$TMP/rootfs.sha256"
tar -C "$TMP/build" -czf "$TMP/rootfs.tar.gz" rootfs-alpine.ext4

ROOT_DIRECTORY="$ROOT" "$SCRIPT_DIR/activate-rootfs.sh" \
	"$TMP/rootfs.tar.gz" "$TMP/rootfs.sha256" test-version

test -L "$ASSETS/rootfs/rootfs-alpine.ext4"
test "$(cat "$ASSETS/rootfs/rootfs-alpine.ext4")" = 'new-rootfs'
test -f "$ASSETS/rootfs/versions/rootfs-test-version.ext4"
test -f "$ASSETS/rootfs/versions/rootfs-legacy-${OLD_SUM:0:12}.ext4"
grep -q "^${NEW_SUM}  rootfs/rootfs-alpine.ext4$" "$ASSETS/manifest.sha256"
grep -q "^${KERNEL_SUM}  kernel$" "$ASSETS/manifest.sha256"
test ! -e "$ROOT/renderops-assets.tar.gz"
test ! -e "$ASSETS/.installed-bundle"

# Reinstalling the same immutable version is idempotent.
ROOT_DIRECTORY="$ROOT" "$SCRIPT_DIR/activate-rootfs.sh" \
	"$TMP/rootfs.tar.gz" "$TMP/rootfs.sha256" test-version

printf '%064d  rootfs-alpine.ext4\n' 0 > "$TMP/bad.sha256"
if ROOT_DIRECTORY="$ROOT" "$SCRIPT_DIR/activate-rootfs.sh" \
	"$TMP/rootfs.tar.gz" "$TMP/bad.sha256" bad-version; then
	echo 'activation accepted a bad checksum' >&2
	exit 1
fi

echo 'activate-rootfs tests passed'
