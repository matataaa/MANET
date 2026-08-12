#!/usr/bin/env bash
set -euo pipefail

# build-tools-tarball.sh — assemble a tools-only update tarball.
#
# Contains scripts, Go binaries, web frontend, systemd units, and etc files.
# Does NOT include the SBC overlay (kernel/modules/firmware) or pre-built
# binaries (alfred, batctl, wpa_supplicant_s1g).
#
# Usage:
#   build-tools-tarball.sh [output.tar.gz]

OUT="${1:-tools.tar.gz}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ROOTFS="$REPO_ROOT/rootfs"
SRC="$REPO_ROOT/src"
STAGE="$(mktemp -d)"

cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT

install_tree() {
    local src="$1" dst="$2"
    [ -d "$src" ] || return 0
    mkdir -p "$dst"
    cp -a "$src"/. "$dst"/
}

install_file() {
    local mode="$1" src="$2" dst="$3"
    if [ -f "$src" ]; then
        mkdir -p "$(dirname "$dst")"
        install -m "$mode" "$src" "$dst"
    else
        echo "WARNING: missing $src" >&2
    fi
}

mkdir -p "$STAGE/usr/local/bin" "$STAGE/etc"

# Scripts + web frontend + systemd units from rootfs overlay
install_tree "$ROOTFS/usr" "$STAGE/usr"
install_tree "$ROOTFS/etc/systemd" "$STAGE/etc/systemd"
install_tree "$ROOTFS/etc/udev" "$STAGE/etc/udev"

chmod -R a+rX "$STAGE/usr/local/bin"
find "$STAGE/usr/local/bin" -type f \
    \( -name '*.sh' -o -name '*.py' -o -name 'mesh' \) \
    -exec chmod 0755 {} +

# Cross-compile Go services for arm64
GO_SERVICES=(
    battery-reader
    cot-emitter
    gateway-manager
    gps-reader
    halow-mcs-summary
    manet-ctrl
    mesh-chat
    mesh-manager
    mesh-radio-state
    mesh-registry
    node-manager
)

for svc in "${GO_SERVICES[@]}"; do
    echo "Building $svc for linux/arm64..."
    (cd "$SRC/$svc" && \
        GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "$svc" .)
    install -m 0755 "$SRC/$svc/$svc" "$STAGE/usr/local/bin/$svc"
done

# mesh-voice needs CGO — use pre-built binary
install_file 0755 "$SRC/mesh-voice/bin/mesh-voice-linux-arm64" "$STAGE/usr/local/bin/mesh-voice"

install_file 0644 "$ROOTFS/etc/manet_version.txt" "$STAGE/etc/manet_version.txt"

mkdir -p "$(dirname "$OUT")"
# macOS: strip extended attributes from the stage and disable AppleDouble
# generation before packing. Without this, bsdtar emits a ._<name> member for
# every xattr-carrying file; macOS tar hides them when listing, but GNU tar on
# the node extracts all of them as real 163-byte junk files.
export COPYFILE_DISABLE=1
if command -v xattr >/dev/null 2>&1; then
    xattr -rc "$STAGE" 2>/dev/null || true
fi
find "$STAGE" -name '._*' -delete 2>/dev/null || true

tar --owner=0 --group=0 --numeric-owner -czf "$OUT" -C "$STAGE" .
echo "Built: $OUT  ($(du -sh "$OUT" | cut -f1))"
