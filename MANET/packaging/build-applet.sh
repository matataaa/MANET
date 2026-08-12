#!/usr/bin/env bash
set -euo pipefail

# Build an applet tarball for installation via /api/applets/install
# Usage: build-applet.sh <applet-name> [output.tar.gz]
#
# Example:
#   ./build-applet.sh mesh-tailscale
#   ./build-applet.sh mesh-wireguard builds/mesh-wireguard.tar.gz

APPLET="${1:?Usage: build-applet.sh <applet-name> [output.tar.gz]}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SRC="$REPO_ROOT/src/$APPLET"
APPLET_FILES="$REPO_ROOT/rootfs/usr/local/share/applets/$APPLET"
OUT="${2:-$APPLET.tar.gz}"

[ -d "$SRC" ] || { echo "No source at $SRC"; exit 1; }
[ -d "$APPLET_FILES" ] || { echo "No applet files at $APPLET_FILES"; exit 1; }

STAGE="$(mktemp -d)"
cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT

DEST="$STAGE/$APPLET"
mkdir -p "$DEST"

echo "Cross-compiling $APPLET for linux/arm64..."
(cd "$SRC" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "$DEST/$APPLET" .)

# Build applet-hooks Go binary if hooks are defined
HOOKS_SRC="$REPO_ROOT/src/applet-hooks"
if [ -d "$HOOKS_SRC" ] && grep -q '"hooks"' "$APPLET_FILES/applet.json" 2>/dev/null; then
    echo "Building applet-hooks for linux/arm64..."
    (cd "$HOOKS_SRC" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "$DEST/applet-hooks" .)
    chmod 0755 "$DEST/applet-hooks"
fi

cp -a "$APPLET_FILES"/. "$DEST"/

chmod 0755 "$DEST/$APPLET"
find "$DEST" -name '*.sh' -exec chmod 0755 {} +

export COPYFILE_DISABLE=1
if command -v xattr >/dev/null 2>&1; then
    xattr -rc "$STAGE" 2>/dev/null || true
fi
find "$STAGE" -name '._*' -delete 2>/dev/null || true

mkdir -p "$(dirname "$OUT")"
tar --owner=0 --group=0 --numeric-owner -czf "$OUT" -C "$STAGE" "$APPLET"
echo "Built: $OUT  ($(du -sh "$OUT" | cut -f1))"
