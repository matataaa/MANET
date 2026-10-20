#!/usr/bin/env bash
# build.sh — rebuild bin/mesh-voice-linux-arm64, the CGO binary the packaging
# scripts ship as-is (build-tools-tarball.sh, build-rpi5-tarball.sh).
#
# mesh-voice links libopus/libopusfile (hraban/opus) and libudev (go-hid), so
# it cannot go through the CGO_ENABLED=0 cross-compile loop the other Go
# services use. It is cross-compiled from an x86_64 host with
# aarch64-linux-gnu-gcc against a sysroot of real Debian trixie arm64 .debs
# (dpkg-deb -x'd, never installed into the host), so the binary links the
# same library versions the nodes run and needs no newer glibc than trixie's.
#
# Rerun this after ANY change to *.go, go.mod, go.sum or vendor/ here, and
# commit the binary in the same commit: packaging/check-prebuilt-fresh.sh
# fails the tarball build when the binary is older than its sources.
#
# Host needs: go, aarch64-linux-gnu-gcc, curl, xz, dpkg-deb, pkg-config,
# sha256sum. Network access to deb.debian.org on first run (sysroot cached).
#
# Usage: ./build.sh            (SYSROOT/WORK overridable via environment)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="${WORK:-/tmp/mesh-voice-build}"
SYSROOT="${SYSROOT:-$WORK/sysroot}"
MIRROR="https://deb.debian.org/debian"
SUITE="trixie"
OUT="$SCRIPT_DIR/bin/mesh-voice-linux-arm64"

# Runtime libs + headers for every #cgo flag in vendor/ (opus, opusfile ->
# ogg, udev) plus the C runtime and kernel headers (evdev's linux/input.h).
# libcap2 is only there so libudev.so's own NEEDED resolves at link time.
PKGS=(libc6 libc6-dev linux-libc-dev
      libopus0 libopus-dev libopusfile0 libopusfile-dev libogg0 libogg-dev
      libudev1 libudev-dev libcap2)

for tool in go aarch64-linux-gnu-gcc curl xz dpkg-deb pkg-config sha256sum; do
    command -v "$tool" >/dev/null || { echo "missing host tool: $tool" >&2; exit 1; }
done

if [ ! -d "$SYSROOT/usr/lib/aarch64-linux-gnu" ]; then
    echo "== Assembling $SUITE arm64 sysroot in $SYSROOT =="
    mkdir -p "$WORK/debs" "$SYSROOT"
    curl -fsSL "$MIRROR/dists/$SUITE/main/binary-arm64/Packages.xz" -o "$WORK/Packages.xz"
    xz -dc "$WORK/Packages.xz" > "$WORK/Packages"
    for pkg in "${PKGS[@]}"; do
        # Newest Filename/SHA256 for this exact package name (the index can
        # carry more than one version, e.g. linux-libc-dev).
        read -r fn sha < <(awk -v p="$pkg" 'BEGIN { RS = ""; FS = "\n" }
            { n = f = s = ""
              for (i = 1; i <= NF; i++) {
                  if ($i ~ /^Package: /)  n = substr($i, 10)
                  if ($i ~ /^Filename: /) f = substr($i, 11)
                  if ($i ~ /^SHA256: /)   s = substr($i, 9)
              }
              if (n == p) { lf = f; ls = s } }
            END { if (lf != "") print lf, ls }' "$WORK/Packages")
        if [ -z "${fn:-}" ]; then
            echo "package $pkg not found in $SUITE arm64 index" >&2
            exit 1
        fi
        deb="$WORK/debs/$(basename "$fn")"
        curl -fsSL "$MIRROR/$fn" -o "$deb"
        echo "$sha  $deb" | sha256sum -c -
        dpkg-deb -x "$deb" "$SYSROOT"
    done
    # trixie is merged-/usr; libc.so's linker script names /lib/... paths.
    [ -e "$SYSROOT/lib" ] || ln -s usr/lib "$SYSROOT/lib"
fi

echo "== Cross-compiling mesh-voice for linux/arm64 =="
export PKG_CONFIG_SYSROOT_DIR="$SYSROOT"
export PKG_CONFIG_LIBDIR="$SYSROOT/usr/lib/aarch64-linux-gnu/pkgconfig:$SYSROOT/usr/share/pkgconfig"
export PKG_CONFIG_PATH=""
export CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=aarch64-linux-gnu-gcc
export CGO_CFLAGS="--sysroot=$SYSROOT -O2"
export CGO_LDFLAGS="--sysroot=$SYSROOT -L$SYSROOT/usr/lib/aarch64-linux-gnu -Wl,-rpath-link,$SYSROOT/usr/lib/aarch64-linux-gnu"
mkdir -p "$SCRIPT_DIR/bin"
# Same flags as the Makefile; -buildvcs=false keeps the output independent of
# the checkout's dirty state.
(cd "$SCRIPT_DIR" && go build -mod=vendor -trimpath -buildvcs=false \
    -ldflags="-s -w" -o "$OUT" .)

echo "== Verify =="
file "$OUT"
aarch64-linux-gnu-readelf -d "$OUT" | grep NEEDED
# Highest glibc symbol version required; must not exceed trixie's 2.41.
maxglibc=$(aarch64-linux-gnu-objdump -T "$OUT" | grep -o 'GLIBC_[0-9.]*' | sort -uV | tail -1)
echo "max glibc symbol: $maxglibc"
if [ "$(printf '%s\n' "$maxglibc" GLIBC_2.41 | sort -V | tail -1)" != GLIBC_2.41 ]; then
    echo "binary needs $maxglibc, newer than trixie's glibc 2.41" >&2
    exit 1
fi
echo "Done: $OUT"
