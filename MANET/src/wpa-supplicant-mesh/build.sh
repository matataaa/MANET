#!/usr/bin/env bash
# build.sh — recorded build recipe for bin/wpa_supplicant_mesh-linux-arm64.
#
# This is a record of the exact steps that produced the checked-in binary
# (see ../../docs/wpa-supplicant-mesh-noscan.md sections 5 and 8 for the
# full narrative, including the sysroot/link errors hit along the way and
# how they were resolved) — NOT a hands-off, idempotent build script.
# Running it end-to-end on a fresh host requires a few manual checks called
# out below (the patches don't apply cleanly, see step 3).
#
# Target: Debian Trixie (Raspberry Pi OS "raspios-trixie-arm64-lite" base),
# wpasupplicant 2:2.10-24, cross-compiled from an x86_64 host with
# aarch64-linux-gnu-gcc against a hand-assembled Trixie-arm64 sysroot
# (chroot/QEMU execution is not required — only compiling, not running,
# the arm64 output).
set -euo pipefail

WORK="${WORK:-/tmp/wpa-supplicant-mesh-build}"
SYSROOT="${SYSROOT:-$WORK/sysroot}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

mkdir -p "$WORK"
cd "$WORK"

echo "== 1. Fetch Debian's exact source (not a generic upstream tag) =="
# wpa 2:2.10-24 is what Trixie's wpasupplicant package actually ships —
# matches the "wpa_supplicant v2.10" string found on this fleet's real
# deployed binary. Pulled directly from deb.debian.org's pool (plain files,
# no apt/dpkg source registration needed).
for f in wpa_2.10-24.dsc wpa_2.10-24.debian.tar.xz wpa_2.10.orig.tar.xz; do
    [ -f "$f" ] || curl -fsSLO "https://deb.debian.org/debian/pool/main/w/wpa/$f"
done

echo "== 2. Unpack + apply Debian's own patch series =="
rm -rf wpa-2.10
tar xJf wpa_2.10.orig.tar.xz
tar -C wpa-2.10 -xJf wpa_2.10-24.debian.tar.xz
command -v quilt >/dev/null || { echo "apt-get install quilt" >&2; exit 1; }
( cd wpa-2.10 && QUILT_PATCHES=debian/patches quilt push -a )

echo "== 3. Apply the noscan patches (300 before 301) =="
# EXPECT THIS TO NEED MANUAL PORTING. Both patches are old (2010/2018) and
# target whatever hostap tree OpenWrt tracks, not Debian's exact tree.
# 300-noscan.patch applied clean the one time this was run (line-number
# offsets only). 301-mesh-noscan.patch did NOT: upstream had since inlined
# ibss_mesh_select_40mhz() directly into ibss_mesh_setup_freq(), and one
# hunk landed via patch(1)'s fuzzy matching in a *completely unrelated*
# function (silently, without failing) — see
# ../../docs/wpa-supplicant-mesh-noscan.md section 8.2 for exactly what was
# hand-ported and what was deleted. Re-verify every hunk's actual landing
# spot after applying; do not trust a "succeeded" patch(1) exit status by
# itself, especially on 301.
( cd wpa-2.10 && patch -p1 --fuzz=3 < "$SCRIPT_DIR/300-noscan.patch" )
( cd wpa-2.10 && patch -p1 --fuzz=3 < "$SCRIPT_DIR/301-mesh-noscan.patch" ) || {
    echo "301-mesh-noscan.patch needs manual porting against this source" >&2
    echo "tree — see docs/wpa-supplicant-mesh-noscan.md section 8.2." >&2
    exit 1
}

echo "== 4. Debian's build config for the Linux target =="
cp wpa-2.10/debian/config/wpasupplicant/linux wpa-2.10/wpa_supplicant/.config

echo "== 5. Sysroot =="
# Hand-assembled from real Trixie arm64 .debs (dpkg -x extracted, never
# apt/dpkg -i'd into the host). Not reproduced here — see
# ../../docs/wpa-supplicant-mesh-noscan.md section 8.3 for the exact
# package list (libssl-dev, libnl-3/genl/route-dev, libdbus-1-dev,
# libpcsclite-dev + their runtime counterparts) and the hand-written
# libsystemd.pc stub needed for dbus-1.pc's Requires.private to resolve.
if [ ! -d "$SYSROOT" ]; then
    echo "Expected a pre-built sysroot at $SYSROOT (see section 8.3) — not" >&2
    echo "reproduced by this script. Point SYSROOT at an existing one, or" >&2
    echo "build one by hand first." >&2
    exit 1
fi

echo "== 6. Cross-compile =="
export PKG_CONFIG_PATH="$SYSROOT/usr/lib/aarch64-linux-gnu/pkgconfig:$SYSROOT/usr/lib/pkgconfig"
export PKG_CONFIG_SYSROOT_DIR="$SYSROOT"
export PKG_CONFIG_LIBDIR="$SYSROOT/usr/lib/aarch64-linux-gnu/pkgconfig:$SYSROOT/usr/lib/pkgconfig"
export LDFLAGS="--sysroot=$SYSROOT -L$SYSROOT/usr/lib/aarch64-linux-gnu -Wl,--allow-shlib-undefined"
EXTRA_CFLAGS="--sysroot=$SYSROOT -I$SYSROOT/usr/include -I$SYSROOT/usr/include/aarch64-linux-gnu -I$SYSROOT/usr/include/PCSC"

make -C wpa-2.10/wpa_supplicant \
    CC=aarch64-linux-gnu-gcc \
    EXTRA_CFLAGS="$EXTRA_CFLAGS" \
    wpa_supplicant
# NOTE: CFLAGS must NOT be passed as a bare `make` command-line argument —
# that creates a make override variable that completely replaces the
# Makefile's own `CFLAGS +=` lines (clobbering its `-I../src` includes).
# EXTRA_CFLAGS is the Makefile's designated injection point.

echo "== 7. Verify =="
file wpa-2.10/wpa_supplicant/wpa_supplicant
strings wpa-2.10/wpa_supplicant/wpa_supplicant | grep -x noscan
aarch64-linux-gnu-readelf -d wpa-2.10/wpa_supplicant/wpa_supplicant | grep NEEDED

echo "== 8. Strip + install =="
cp wpa-2.10/wpa_supplicant/wpa_supplicant "$SCRIPT_DIR/bin/wpa_supplicant_mesh-linux-arm64"
aarch64-linux-gnu-strip "$SCRIPT_DIR/bin/wpa_supplicant_mesh-linux-arm64"
echo "Done: $SCRIPT_DIR/bin/wpa_supplicant_mesh-linux-arm64"
