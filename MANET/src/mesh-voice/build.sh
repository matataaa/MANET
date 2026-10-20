#!/usr/bin/env bash
# build.sh — rebuild bin/mesh-voice-linux-<arch>, the CGO binaries the
# packaging scripts ship as-is (arm64: build-tools-tarball.sh,
# build-rpi5-tarball.sh; amd64: build-x86-tarball.sh).
#
# mesh-voice links libopus/libopusfile (hraban/opus) and libudev (go-hid), so
# it cannot go through the CGO_ENABLED=0 cross-compile loop the other Go
# services use. It is compiled from an x86_64 host against a sysroot of real
# Debian trixie .debs for the target arch (dpkg-deb -x'd, never installed
# into the host), so the binary links the same library versions the nodes
# run and needs no newer glibc than trixie's. amd64 uses the same sysroot
# approach so the host distro's libraries never leak in.
#
# Rerun this after ANY change to *.go, go.mod, go.sum or vendor/ here, for
# both arches, and commit the binaries in the same commit:
# packaging/check-prebuilt-fresh.sh fails the tarball build when a binary is
# older than its sources.
#
# Host needs: go, curl, xz, dpkg-deb, pkg-config, sha256sum, and the target
# toolchain (aarch64-linux-gnu-gcc / x86_64-linux-gnu-gcc + binutils).
# Network access to deb.debian.org on first run (sysroot cached).
#
# Usage: ./build.sh [arm64|amd64]   (default arm64; SYSROOT/WORK overridable)
set -euo pipefail

ARCH="${1:-arm64}"
case "$ARCH" in
    arm64) TRIPLE=aarch64-linux-gnu ;;
    amd64) TRIPLE=x86_64-linux-gnu ;;
    *) echo "usage: $0 [arm64|amd64]" >&2; exit 2 ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="${WORK:-/tmp/mesh-voice-build}"
SYSROOT="${SYSROOT:-$WORK/sysroot-$ARCH}"
MIRROR="https://deb.debian.org/debian"
SUITE="trixie"
OUT="$SCRIPT_DIR/bin/mesh-voice-linux-$ARCH"
CC="$TRIPLE-gcc"

# Runtime libs + headers for every #cgo flag in vendor/ (opus, opusfile ->
# ogg, udev) plus the C runtime and kernel headers (evdev's linux/input.h).
# libcap2 is only there so libudev.so's own NEEDED resolves at link time.
PKGS=(libc6 libc6-dev linux-libc-dev
      libopus0 libopus-dev libopusfile0 libopusfile-dev libogg0 libogg-dev
      libudev1 libudev-dev libcap2)

for tool in go "$CC" "$TRIPLE-readelf" "$TRIPLE-objdump" curl xz dpkg-deb pkg-config sha256sum; do
    command -v "$tool" >/dev/null || { echo "missing host tool: $tool" >&2; exit 1; }
done

if [ ! -d "$SYSROOT/usr/lib/$TRIPLE" ]; then
    echo "== Assembling $SUITE $ARCH sysroot in $SYSROOT =="
    mkdir -p "$WORK/debs-$ARCH" "$SYSROOT"
    curl -fsSL "$MIRROR/dists/$SUITE/main/binary-$ARCH/Packages.xz" -o "$WORK/Packages-$ARCH.xz"
    xz -dc "$WORK/Packages-$ARCH.xz" > "$WORK/Packages-$ARCH"
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
            END { if (lf != "") print lf, ls }' "$WORK/Packages-$ARCH")
        if [ -z "${fn:-}" ]; then
            echo "package $pkg not found in $SUITE $ARCH index" >&2
            exit 1
        fi
        deb="$WORK/debs-$ARCH/$(basename "$fn")"
        curl -fsSL "$MIRROR/$fn" -o "$deb"
        echo "$sha  $deb" | sha256sum -c -
        dpkg-deb -x "$deb" "$SYSROOT"
    done
    # trixie is merged-/usr; libc.so's linker script names /lib/... paths.
    [ -e "$SYSROOT/lib" ] || ln -s usr/lib "$SYSROOT/lib"
    [ "$ARCH" != amd64 ] || [ -e "$SYSROOT/lib64" ] || ln -s usr/lib64 "$SYSROOT/lib64"
fi

echo "== Compiling mesh-voice for linux/$ARCH =="
export PKG_CONFIG_SYSROOT_DIR="$SYSROOT"
export PKG_CONFIG_LIBDIR="$SYSROOT/usr/lib/$TRIPLE/pkgconfig:$SYSROOT/usr/share/pkgconfig"
export PKG_CONFIG_PATH=""
export CGO_ENABLED=1 GOOS=linux GOARCH="$ARCH" CC
export CGO_CFLAGS="--sysroot=$SYSROOT -O2"
export CGO_LDFLAGS="--sysroot=$SYSROOT -L$SYSROOT/usr/lib/$TRIPLE -Wl,-rpath-link,$SYSROOT/usr/lib/$TRIPLE"
mkdir -p "$SCRIPT_DIR/bin"
# Same flags as the Makefile; -buildvcs=false keeps the output independent of
# the checkout's dirty state.
(cd "$SCRIPT_DIR" && go build -mod=vendor -trimpath -buildvcs=false \
    -ldflags="-s -w" -o "$OUT" .)

echo "== Verify =="
file "$OUT"
"$TRIPLE-readelf" -d "$OUT" | grep NEEDED
# Highest glibc symbol version required; must not exceed trixie's 2.41.
maxglibc=$("$TRIPLE-objdump" -T "$OUT" | grep -o 'GLIBC_[0-9.]*' | sort -uV | tail -1)
echo "max glibc symbol: $maxglibc"
if [ "$(printf '%s\n' "$maxglibc" GLIBC_2.41 | sort -V | tail -1)" != GLIBC_2.41 ]; then
    echo "binary needs $maxglibc, newer than trixie's glibc 2.41" >&2
    exit 1
fi
echo "Done: $OUT"
