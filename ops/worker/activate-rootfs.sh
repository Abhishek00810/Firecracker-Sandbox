#!/usr/bin/env bash
# Installs one immutable rootfs version and atomically points the worker's stable
# rootfs path at it. The worker must be restarted after this script succeeds.
set -euo pipefail

ARCHIVE="${1:?usage: activate-rootfs.sh <archive> <checksum-file> <version>}"
CHECKSUM_FILE="${2:?usage: activate-rootfs.sh <archive> <checksum-file> <version>}"
VERSION="${3:?usage: activate-rootfs.sh <archive> <checksum-file> <version>}"
ROOT="${ROOT_DIRECTORY:-/opt/renderops/worker}"
ASSETS="$ROOT/assets"
ROOTFS_DIR="$ASSETS/rootfs"
VERSIONS_DIR="$ROOTFS_DIR/versions"
ACTIVE_PATH="$ROOTFS_DIR/rootfs-alpine.ext4"
MANIFEST="$ASSETS/manifest.sha256"

case "$VERSION" in
	*[!A-Za-z0-9._-]* | '')
		echo "invalid rootfs version: $VERSION" >&2
		exit 1
		;;
esac

EXPECTED="$(awk 'NR == 1 { print $1 }' "$CHECKSUM_FILE")"
case "$EXPECTED" in
	*[!0-9a-f]* | '')
		echo "invalid rootfs checksum" >&2
		exit 1
		;;
esac
if [ "${#EXPECTED}" -ne 64 ]; then
	echo "invalid rootfs checksum" >&2
	exit 1
fi

test -f "$MANIFEST"
install -d -m 0755 "$VERSIONS_DIR"
STAGE="$(mktemp -d "$ROOTFS_DIR/.rootfs-stage.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT
tar -xzf "$ARCHIVE" -C "$STAGE"
CANDIDATE="$STAGE/rootfs-alpine.ext4"
test -f "$CANDIDATE"

ACTUAL="$(sha256sum "$CANDIDATE" | awk '{ print $1 }')"
if [ "$ACTUAL" != "$EXPECTED" ]; then
	echo "rootfs checksum mismatch: expected $EXPECTED, got $ACTUAL" >&2
	exit 1
fi

TARGET_NAME="rootfs-${VERSION}.ext4"
TARGET="$VERSIONS_DIR/$TARGET_NAME"
if [ -e "$TARGET" ]; then
	INSTALLED="$(sha256sum "$TARGET" | awk '{ print $1 }')"
	if [ "$INSTALLED" != "$EXPECTED" ]; then
		echo "existing rootfs version $VERSION has a different checksum" >&2
		exit 1
	fi
else
	chmod 0644 "$CANDIDATE"
	mv "$CANDIDATE" "$TARGET"
fi

# Preserve the pre-versioning rootfs as an immutable rollback target. Renaming
# is safe for running Firecracker processes because their open inode remains valid.
if [ -e "$ACTIVE_PATH" ] && [ ! -L "$ACTIVE_PATH" ]; then
	LEGACY_SUM="$(sha256sum "$ACTIVE_PATH" | awk '{ print $1 }')"
	LEGACY="$VERSIONS_DIR/rootfs-legacy-${LEGACY_SUM:0:12}.ext4"
	if [ ! -e "$LEGACY" ]; then
		mv "$ACTIVE_PATH" "$LEGACY"
	else
		rm -f "$ACTIVE_PATH"
	fi
fi

LINK="$ROOTFS_DIR/.rootfs-alpine.ext4.next"
ln -sfn "versions/$TARGET_NAME" "$LINK"
mv -f "$LINK" "$ACTIVE_PATH"

MANIFEST_NEXT="$ASSETS/.manifest.sha256.next"
awk -v sum="$EXPECTED" '
	$2 == "rootfs/rootfs-alpine.ext4" { printf "%s  %s\n", sum, $2; found = 1; next }
	{ print }
	END { if (!found) exit 2 }
' "$MANIFEST" > "$MANIFEST_NEXT"
chmod 0644 "$MANIFEST_NEXT"
mv -f "$MANIFEST_NEXT" "$MANIFEST"

# The original all-assets bundle contains the old rootfs. Keeping it would allow
# a deleted marker to silently reinstall stale guest code on a later restart.
rm -f "$ROOT/renderops-assets.tar.gz" "$ASSETS/.installed-bundle"
sync
echo "activated rootfs version $VERSION ($EXPECTED)"
