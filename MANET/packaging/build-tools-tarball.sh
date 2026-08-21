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
    atak-overlay
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
    VER="$(git -C "$REPO_ROOT" describe --tags --always --dirty --match "${svc}-v*" 2>/dev/null || echo dev)"
    echo "Building $svc for linux/arm64 (version $VER)..."
    (cd "$SRC/$svc" && \
        GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=$VER" -o "$svc" .)
    install -m 0755 "$SRC/$svc/$svc" "$STAGE/usr/local/bin/$svc"
done

# mesh-voice needs CGO — use pre-built binary
install_file 0755 "$SRC/mesh-voice/bin/mesh-voice-linux-arm64" "$STAGE/usr/local/bin/mesh-voice"

install_file 0644 "$ROOTFS/etc/manet_version.txt" "$STAGE/etc/manet_version.txt"

mkdir -p "$(dirname "$OUT")"
OUT_ABS="$(cd "$(dirname "$OUT")" && pwd)/$(basename "$OUT")"

# macOS: strip extended attributes from the stage and disable AppleDouble
# generation before packing. Without this, bsdtar emits a ._<name> member for
# every xattr-carrying file; macOS tar hides them when listing, but GNU tar on
# the node extracts all of them as real 163-byte junk files.
export COPYFILE_DISABLE=1
if command -v xattr >/dev/null 2>&1; then
    xattr -rc "$STAGE" 2>/dev/null || true
fi
find "$STAGE" -name '._*' -delete 2>/dev/null || true

# Directory modes are recorded in the archive and applied to the target by an
# extractor running as root, so they have to be right here. Two rules:
#
#  1. Never emit an entry for the stage root. Tar stores its mode against "./"
#     and restores it onto the extraction directory — which is "/" on a node.
#     mktemp -d gives 0700, so shipping "./" once set / to 0700 and locked
#     every non-root process out of the filesystem (no traversal, no shared
#     libraries, no ssh logins). Archive the contents instead.
#  2. Strip group/other write. These dirs inherit the build machine's umask,
#     and a umask of 002 ships /usr, /etc and /usr/local as 0775 root:root.
# Bytecode compiled on the build machine has no business on a node.
find "$STAGE" -type d -name __pycache__ -prune -exec rm -rf {} +

find "$STAGE" -type d -exec chmod go-w {} +
( cd "$STAGE" && find . -mindepth 1 -maxdepth 1 -printf './%P\0' \
    | tar --owner=0 --group=0 --numeric-owner --null -T - -czf "$OUT_ABS" )
echo "Built: $OUT  ($(du -sh "$OUT" | cut -f1))"
