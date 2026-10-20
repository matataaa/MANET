#!/bin/bash
#
#  Re-vendors the CM4 SBC overlay (kernel, DTBs, mt7915e/morse/dot11ah
#  modules, HaLow firmware) from upstream's cm4-install.tar.gz (very-srs/MANET
#  GitHub releases) into kernel-work/packages/cm4-sbc-overlay, or $OVERLAY_DIR
#  when set (e.g. manet-releases' inputs/cm4-sbc-overlay) — untracked in git (see
#  VENDORED_FROM.md there and .gitignore at the repo root). Run this once
#  after cloning, or again later to pick up an updated build, before
#  build-cm4-tarball.sh's SBC_OVERLAY_DIR auto-detection has anything to
#  find.
#
#  Usage: ./fetch-cm4-overlay.sh <path-to-cm4-install.tar.gz>
#         [UPSTREAM_TAG=v0.559] ./fetch-cm4-overlay.sh --from-url
#
#  MORSE_FIRMWARE_DIR=<morse-firmware checkout> is required while upstream's
#  overlay carries modified BCFs (all releases up to 0.578); see below.
#
#  A local tarball is the normal route — copy install_packages/ or a
#  cm4-install.tar.gz from a machine that already has one. Downloading is
#  opt-in via --from-url (checked against the release's .sha256) and never
#  happens by default.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OVERLAY_DIR="${OVERLAY_DIR:-$(cd "$SCRIPT_DIR/../.." && pwd)/kernel-work/packages/cm4-sbc-overlay}"
# Upstream publishes cm4-install.tar.gz (+ .sha256) on its GitHub releases.
UPSTREAM_TAG="${UPSTREAM_TAG:-v0.559}"
SOURCE_URL="https://github.com/very-srs/MANET/releases/download/$UPSTREAM_TAG/cm4-install.tar.gz"
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

FROM_URL=0
if [ "$TARBALL" = "--from-url" ]; then
    FROM_URL=1
    TARBALL=""
fi

if [ "$FROM_URL" = "1" ]; then
    echo "Downloading $SOURCE_URL ..."
    TARBALL="$(mktemp /tmp/cm4-install.XXXXXX.tar.gz)"
    cleanup+=("$TARBALL")
    curl -fSL "$SOURCE_URL" -o "$TARBALL"
    expected="$(curl -fsSL "$SOURCE_URL.sha256" | cut -d' ' -f1)"
    actual="$(sha256sum "$TARBALL" | cut -d' ' -f1)"
    if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
        echo "ERROR: checksum mismatch for $SOURCE_URL (expected '${expected:-none}', got $actual)" >&2
        exit 1
    fi
    echo "Checksum OK ($actual)"
elif [ -z "$TARBALL" ]; then
    echo "ERROR: no source tarball given." >&2
    echo "" >&2
    echo "Usage: $0 <path-to-cm4-install.tar.gz>" >&2
    echo "       $0 --from-url    # opt in to $SOURCE_URL" >&2
    echo "" >&2
    echo "Copy a known-good cm4-install.tar.gz (or install_packages/" >&2
    echo "cm4-tools.tar.gz) from a machine that already has one and pass" >&2
    echo "its path. This build step does not reach the network by default." >&2
    exit 1
elif [ ! -f "$TARBALL" ]; then
    echo "ERROR: no such file: $TARBALL" >&2
    exit 1
else
    echo "Using local tarball: $TARBALL"
fi

echo "Extracting overlay subset to $OVERLAY_DIR ..."
rm -rf "$OVERLAY_DIR/boot" "$OVERLAY_DIR/usr"
mkdir -p "$OVERLAY_DIR/boot/firmware/overlays" "$OVERLAY_DIR/usr/lib"

WORKDIR="$(mktemp -d)"
cleanup+=("$WORKDIR")
tar -xzf "$TARBALL" -C "$WORKDIR" "${INCLUDE_PATHS[@]}"

mv "$WORKDIR/boot/firmware"/*.dtb "$OVERLAY_DIR/boot/firmware/"
mv "$WORKDIR/boot/firmware/kernel8.img" "$OVERLAY_DIR/boot/firmware/"
mv "$WORKDIR/boot/firmware/overlays/mm610x-spi.dtbo" "$OVERLAY_DIR/boot/firmware/overlays/"
mv "$WORKDIR/usr/lib/modules" "$OVERLAY_DIR/usr/lib/modules"
mkdir -p "$OVERLAY_DIR/usr/lib/firmware"
mv "$WORKDIR/usr/lib/firmware/morse" "$OVERLAY_DIR/usr/lib/firmware/morse"

# Morse Micro licenses the BCFs for distribution complete and unmodified, and
# requires its license to travel with them. Overlays up to upstream 0.578 ship
# them pre-patched; swap those for the published files (the node patches them
# at boot with manet-morse-bcf.py). Each swap is checked: the stock file,
# patched, must equal the overlay's file, so a wrong release cannot slip in.
BCF_TOOL="$SCRIPT_DIR/../rootfs/usr/local/bin/manet-morse-bcf.py"
MORSE_DIR="$OVERLAY_DIR/usr/lib/firmware/morse"
BCF_STATUS="stock as vendored"
if [ -n "${MORSE_FIRMWARE_DIR:-}" ] && [ ! -f "$MORSE_FIRMWARE_DIR/firmware/LICENSE" ]; then
    echo "ERROR: MORSE_FIRMWARE_DIR is not a morse-firmware checkout: $MORSE_FIRMWARE_DIR" >&2
    exit 1
fi
if ! python3 -I "$BCF_TOOL" --directory "$MORSE_DIR" --check-stock 2>/dev/null; then
    if [ -z "${MORSE_FIRMWARE_DIR:-}" ]; then
        echo "ERROR: this overlay's Morse BCFs are modified. Point MORSE_FIRMWARE_DIR at a" >&2
        echo "       checkout of the matching published files, e.g.:" >&2
        echo "  git clone https://github.com/MorseMicro/morse-firmware.git" >&2
        echo "  git -C morse-firmware checkout ea18605f53387281c1029ea9eb8e0de2bda7bae2  # 1.17.9" >&2
        exit 1
    fi
    STOCK="$WORKDIR/bcf-stock" CHECK="$WORKDIR/bcf-check"
    mkdir -p "$STOCK" "$CHECK"
    for f in "$MORSE_DIR"/bcf_*.bin; do
        b="$(basename "$f")"
        s="$(find "$MORSE_FIRMWARE_DIR/bcf" -name "$b" | head -1)"
        [ -n "$s" ] || { echo "ERROR: $b is not in $MORSE_FIRMWARE_DIR/bcf" >&2; exit 1; }
        cp -L "$s" "$STOCK/$b"
        cp -L "$s" "$CHECK/$b"
    done
    python3 -I "$BCF_TOOL" --directory "$CHECK" >/dev/null
    for f in "$MORSE_DIR"/bcf_*.bin; do
        b="$(basename "$f")"
        cmp -s "$f" "$CHECK/$b" || {
            echo "ERROR: $b from $MORSE_FIRMWARE_DIR does not patch to the overlay's file" >&2
            exit 1
        }
        install -m 0644 "$STOCK/$b" "$f"
    done
    BCF_STATUS="restored to stock from morse-firmware $(git -C "$MORSE_FIRMWARE_DIR" rev-parse --short HEAD 2>/dev/null || echo "(unknown commit)")"
fi
python3 -I "$BCF_TOOL" --directory "$MORSE_DIR" --check-stock
if [ -n "${MORSE_FIRMWARE_DIR:-}" ]; then
    install -m 0644 "$MORSE_FIRMWARE_DIR/firmware/LICENSE" "$MORSE_DIR/LICENSE"
fi
[ -f "$MORSE_DIR/LICENSE" ] || {
    echo "ERROR: the overlay has no Morse license; set MORSE_FIRMWARE_DIR to add it" >&2
    exit 1
}

BUNDLED_VERSION="$(tr '\n' ' ' < "$WORKDIR/etc/manet_version.txt" 2>/dev/null | sed -E 's/^(\S+)\s+(\S+)\s*$/\1 (\2)/' || echo "unknown")"
[ -z "$BUNDLED_VERSION" ] && BUNDLED_VERSION="unknown"
TARBALL_SHA256="$(sha256sum "$TARBALL" | cut -d' ' -f1)"
if [ "$FROM_URL" = "1" ]; then
    SOURCE="$SOURCE_URL"
else
    SOURCE="local file $(basename "$TARBALL")"
fi

cat > "$OVERLAY_DIR/VENDORED_FROM.md" << EOF
# Vendored SBC overlay — CM4

Extracted from upstream's \`cm4-install.tar.gz\`, published with a
\`.sha256\` on the very-srs/MANET GitHub releases. Contains the
kernel, DTBs, and driver modules (mt7915e, morse, dot11ah) this fork
depends on for CM4 WiFi + HaLow, none of which ship in stock Raspberry
Pi OS.

- Source: $SOURCE
- Bundled version (from ./etc/manet_version.txt inside that tarball): $BUNDLED_VERSION
- Tarball SHA-256: $TARBALL_SHA256
- Morse BCFs: $BCF_STATUS
- Vendored: $(date -u +"%Y-%m-%d %H:%M UTC")

## Re-checking for updates

\`gh release list -R very-srs/MANET\` shows upstream's releases. Re-run
\`UPSTREAM_TAG=<tag> MANET/packaging/fetch-cm4-overlay.sh --from-url\` to
vendor one; it verifies the published \`.sha256\` and regenerates this file.
EOF

echo ""
echo "Done. Vendored version: $BUNDLED_VERSION"
echo "  $OVERLAY_DIR"
