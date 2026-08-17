#!/bin/bash
#
#  Re-vendors the CM4 SBC overlay (kernel, DTBs, mt7915e/morse/dot11ah
#  modules, HaLow firmware) from the externally-hosted cm4-install.tar.gz.
#  This directory is intentionally untracked in git (see VENDORED_FROM.md
#  and .gitignore at the repo root) — run this once after cloning, or
#  again later to pick up an updated build, before build-cm4-tarball.sh's
#  SBC_OVERLAY_DIR auto-detection has anything to find.
#
#  Usage: ./fetch-overlay.sh [path-to-cm4-install.tar.gz]
#  With no argument, downloads fresh from the URL below.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_URL="https://www.colorado-governor.com/manet/cm4-install.tar.gz"
TARBALL="${1:-}"

# Exactly the subset that was vendored originally — this fork's own
# userspace (/etc, /root, /usr/local) is deliberately excluded so this
# overlay only ever supplies kernel-layer content, never overwriting
# anything the fork actively develops. SBC_OVERLAY_DIR is applied last in
# build-cm4-tarball.sh, so anything included here wins on a path conflict.
INCLUDE_PATHS=(
    './boot/firmware/kernel8.img'
    './boot/firmware/bcm2711-rpi-4-b.dtb'
    './boot/firmware/bcm2711-rpi-400.dtb'
    './boot/firmware/bcm2711-rpi-cm4.dtb'
    './boot/firmware/bcm2711-rpi-cm4-io.dtb'
    './boot/firmware/bcm2711-rpi-cm4s.dtb'
    './boot/firmware/overlays/mm610x-spi.dtbo'
    './usr/lib/modules'
    './usr/lib/firmware/morse'
    './etc/manet_version.txt'
)

cleanup=()
trap 'for f in "${cleanup[@]:-}"; do [ -n "$f" ] && rm -rf "$f"; done' EXIT

if [ -z "$TARBALL" ]; then
    echo "Downloading $SOURCE_URL ..."
    TARBALL="$(mktemp /tmp/cm4-install.XXXXXX.tar.gz)"
    cleanup+=("$TARBALL")
    curl -fSL "$SOURCE_URL" -o "$TARBALL"
else
    echo "Using local tarball: $TARBALL"
fi

echo "Extracting overlay subset..."
rm -rf "$SCRIPT_DIR/boot" "$SCRIPT_DIR/usr"
mkdir -p "$SCRIPT_DIR/boot/firmware/overlays" "$SCRIPT_DIR/usr/lib"

WORKDIR="$(mktemp -d)"
cleanup+=("$WORKDIR")
tar -xzf "$TARBALL" -C "$WORKDIR" "${INCLUDE_PATHS[@]}"

mv "$WORKDIR/boot/firmware"/*.dtb "$SCRIPT_DIR/boot/firmware/"
mv "$WORKDIR/boot/firmware/kernel8.img" "$SCRIPT_DIR/boot/firmware/"
mv "$WORKDIR/boot/firmware/overlays/mm610x-spi.dtbo" "$SCRIPT_DIR/boot/firmware/overlays/"
mv "$WORKDIR/usr/lib/modules" "$SCRIPT_DIR/usr/lib/modules"
mkdir -p "$SCRIPT_DIR/usr/lib/firmware"
mv "$WORKDIR/usr/lib/firmware/morse" "$SCRIPT_DIR/usr/lib/firmware/morse"

BUNDLED_VERSION="$(tr '\n' ' ' < "$WORKDIR/etc/manet_version.txt" 2>/dev/null | sed -E 's/^(\S+)\s+(\S+)\s*$/\1 (\2)/' || echo "unknown")"
[ -z "$BUNDLED_VERSION" ] && BUNDLED_VERSION="unknown"
LAST_MODIFIED="$(curl -sI --max-time 10 "$SOURCE_URL" 2>/dev/null | grep -i '^last-modified:' | cut -d' ' -f2- | tr -d '\r' || echo "unknown")"

cat > "$SCRIPT_DIR/VENDORED_FROM.md" << EOF
# Vendored SBC overlay — CM4

Extracted from the externally-hosted \`cm4-install.tar.gz\`, which is not
published anywhere in the very-srs/MANET GitHub repo. Contains the
kernel, DTBs, and driver modules (mt7915e, morse, dot11ah) this fork
depends on for CM4 WiFi + HaLow, none of which ship in stock Raspberry
Pi OS.

- Source: $SOURCE_URL
- Bundled version (from ./etc/manet_version.txt inside that tarball): $BUNDLED_VERSION
- HTTP Last-Modified: $LAST_MODIFIED
- Vendored: $(date -u +"%Y-%m-%d %H:%M UTC")

## Re-checking for updates

No lightweight version endpoint exists on that host (\`manet_version.txt\`
alone 404s — the version file only lives inside the tarball). Re-run this
script (\`fetch-overlay.sh\`) to pick up whatever is currently hosted; it
regenerates this file with the new version/timestamp automatically. To
check without downloading the full ~46MB first, compare the current
Last-Modified header (\`curl -sI $SOURCE_URL\`) against the value above.
EOF

echo ""
echo "Done. Vendored version: $BUNDLED_VERSION"
echo "  $SCRIPT_DIR"
