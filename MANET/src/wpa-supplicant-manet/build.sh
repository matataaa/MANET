#!/usr/bin/env bash
# Build wpa_supplicant_manet / wpa_cli_manet: hostap 2.11 + patches/series.
# Inputs (.config, source.sha256, patches 0001-0008) are vendored unchanged
# from very-srs/MANET MANET/binaries_arm64/wpa_supplicant-manet/; 0100 is
# fork-only. Usage:
#   native arm64 (Debian 13):  build.sh OUTPUT_DIR
#   cross from x86_64:         CROSS_COMPILE=aarch64-linux-gnu- \
#                              SYSROOT=/path/to/trixie-arm64 build.sh OUTPUT_DIR
# SYSROOT needs libc6(-dev), linux-libc-dev, libnl-3(-dev), libnl-genl-3(-dev)
# and libssl(-dev) from Debian trixie arm64, extracted with dpkg-deb -x.
set -euo pipefail

# Build into a separate output directory. Never install over a live supplicant.
SOURCE_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT="${1:?Usage: build.sh OUTPUT_DIRECTORY}"
mkdir -p "$OUTPUT"
OUTPUT="$(cd -- "$OUTPUT" && pwd)"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT

CROSS_COMPILE="${CROSS_COMPILE:-}"
MAKE_ARGS=()
if [ -n "$CROSS_COMPILE" ]; then
    : "${SYSROOT:?SYSROOT is required with CROSS_COMPILE}"
    export PKG_CONFIG_SYSROOT_DIR="$SYSROOT"
    export PKG_CONFIG_LIBDIR="$SYSROOT/usr/lib/aarch64-linux-gnu/pkgconfig:$SYSROOT/usr/lib/pkgconfig:$SYSROOT/usr/share/pkgconfig"
    MAKE_ARGS=(CC="${CROSS_COMPILE}gcc"
        EXTRA_CFLAGS="--sysroot=$SYSROOT -I$SYSROOT/usr/include/libnl3"
        LDFLAGS="--sysroot=$SYSROOT")
fi

pkg-config --exists libnl-3.0 libnl-genl-3.0 openssl

TARBALL="$BUILD_DIR/wpa_supplicant-2.11.tar.gz"
if [ -n "${WPA_TARBALL:-}" ]; then
    cp -- "$WPA_TARBALL" "$TARBALL"
else
    curl --fail --location --retry 3 --proto '=https' --tlsv1.2 \
        https://w1.fi/releases/wpa_supplicant-2.11.tar.gz -o "$TARBALL"
fi
(cd "$BUILD_DIR" && sha256sum --check "$SOURCE_DIR/source.sha256")
tar -xzf "$TARBALL" -C "$BUILD_DIR"
TREE="$BUILD_DIR/wpa_supplicant-2.11"

while IFS= read -r patch_name; do
    patch --batch --fuzz=0 -d "$TREE" -p1 < "$SOURCE_DIR/patches/$patch_name"
done < "$SOURCE_DIR/patches/series"
cp "$SOURCE_DIR/.config" "$TREE/wpa_supplicant/.config"
make -C "$TREE/wpa_supplicant" -j "${JOBS:-2}" "${MAKE_ARGS[@]}" \
    wpa_supplicant wpa_cli
install -m 0755 "$TREE/wpa_supplicant/wpa_supplicant" \
    "$OUTPUT/wpa_supplicant_manet"
install -m 0755 "$TREE/wpa_supplicant/wpa_cli" "$OUTPUT/wpa_cli_manet"
"${CROSS_COMPILE}strip" "$OUTPUT/wpa_supplicant_manet" "$OUTPUT/wpa_cli_manet"
if [ -z "$CROSS_COMPILE" ]; then
    "$OUTPUT/wpa_supplicant_manet" -v
    "$OUTPUT/wpa_cli_manet" -v
fi
