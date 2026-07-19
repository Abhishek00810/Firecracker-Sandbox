#!/usr/bin/env bash
# fetch-kernel.sh — download a Firecracker-compatible guest kernel (vmlinux) from
# the official Firecracker CI bucket. No dependency on any private/old asset tree.
# Usage: fetch-kernel.sh <output-path>
#   FC_ARCH        (default x86_64)
#   FC_CI_VERSION  (default v1.10)   — the firecracker-ci kernel line
#   KERNEL_URL     — set to bypass discovery and download a specific vmlinux
set -euo pipefail

OUT="${1:?usage: fetch-kernel.sh <output-path>}"
ARCH="${FC_ARCH:-x86_64}"
CI_VER="${FC_CI_VERSION:-v1.10}"
BUCKET="https://s3.amazonaws.com/spec.ccfc.min"

if [ -n "${KERNEL_URL:-}" ]; then
    echo "[kernel] $KERNEL_URL"
    curl -fsSL "$KERNEL_URL" -o "$OUT"
    exit 0
fi

# List the CI kernels for this arch and take the newest vmlinux-X.Y.Z.
key=$(curl -fsSL "${BUCKET}/?list-type=2&prefix=firecracker-ci/${CI_VER}/${ARCH}/vmlinux-" \
      | grep -oE "firecracker-ci/${CI_VER}/${ARCH}/vmlinux-[0-9]+\.[0-9]+\.[0-9]+" \
      | sort -V | tail -1)
if [ -z "$key" ]; then
    echo "[kernel] no vmlinux found under ${BUCKET}/firecracker-ci/${CI_VER}/${ARCH}/" >&2
    echo "[kernel] set KERNEL_URL to a specific vmlinux to override" >&2
    exit 1
fi

echo "[kernel] $key"
curl -fsSL "${BUCKET}/${key}" -o "$OUT"
