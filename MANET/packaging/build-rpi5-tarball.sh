#!/usr/bin/env bash
set -euo pipefail

OUT="${1:-rpi5-install.tar.gz}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ROOTFS="$REPO_ROOT/rootfs"
SRC="$REPO_ROOT/src"
BINARIES="$REPO_ROOT/binaries_arm64"
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

# ---------------------------------------------------------------------------
#  Rootfs overlay — scripts, services, configs, web UI, udev rules
# ---------------------------------------------------------------------------
install_tree "$ROOTFS/usr" "$STAGE/usr"
install_tree "$ROOTFS/etc" "$STAGE/etc"
install_tree "$ROOTFS/root" "$STAGE/root"

# provision-mesh.sh is generated per-build from firstrun.sh.template and is
# ALREADY RUNNING (as /usr/local/bin/provision-mesh.sh) at the moment this
# tarball is extracted over /. Shipping it would overwrite the executing
# script in place — bash reads scripts lazily by byte offset, so it can then
# resume at a bogus offset. The rootfs copy is also a pre-rendered rpi5 build
# (hardcoded max_euds_per_node), which would be wrong on any other board.
rm -f "$STAGE/usr/local/bin/provision-mesh.sh"

chmod -R a+rX "$STAGE/usr/local/bin"
find "$STAGE/usr/local/bin" -type f \
    \( -name '*.sh' -o -name '*.py' -o -name 'mesh' \) \
    -exec chmod 0755 {} +

# ---------------------------------------------------------------------------
#  Cross-compile Go services for linux/arm64
# ---------------------------------------------------------------------------
GO_SERVICES=(
    atak-overlay
    battery-reader
    cot-emitter
    gateway-manager
    gps-reader
    halow-mcs-summary
    manet-ctrl
    mesh-chat
    mesh-hook
    mesh-manager
    mesh-radio-state
    mesh-registry
    node-manager
    node-update
)

BUILD_VERSION="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo 'unknown')"

for svc in "${GO_SERVICES[@]}"; do
    echo "Building $svc for linux/arm64..."
    LDFLAGS="-s -w"
    if [ "$svc" = "manet-ctrl" ]; then
        LDFLAGS="-s -w -X main.Version=${BUILD_VERSION}"
    fi
    (cd "$SRC/$svc" && \
        GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="$LDFLAGS" -o "$svc" .)
    install -m 0755 "$SRC/$svc/$svc" "$STAGE/usr/local/bin/$svc"
done

# mesh-voice needs CGO (opus/miniaudio) — use pre-built binary
install_file 0755 "$SRC/mesh-voice/bin/mesh-voice-linux-arm64" "$STAGE/usr/local/bin/mesh-voice"

# Patched wpa_supplicant (mesh noscan fix) — this project's own build
# artifact, not a vendor-supplied binary, so it lives under $SRC (see
# src/wpa-supplicant-mesh/README.md), not $BINARIES. Installed alongside
# (not over) the system wpa_supplicant — see
# rootfs/etc/systemd/system/wpa_supplicant@.service.d/20-mesh-binary.conf.
install_file 0755 "$SRC/wpa-supplicant-mesh/bin/wpa_supplicant_mesh-linux-arm64" "$STAGE/usr/sbin/wpa_supplicant_mesh"

# ---------------------------------------------------------------------------
#  Applets — copy applet binaries into their applet dirs
# ---------------------------------------------------------------------------
APPLET_CHAT="$STAGE/usr/local/share/manet/applets/mesh-chat"
if [ -d "$APPLET_CHAT" ]; then
    install -m 0755 "$SRC/mesh-chat/mesh-chat" "$APPLET_CHAT/mesh-chat"
fi
APPLET_ATAK="$STAGE/usr/local/share/manet/applets/atak-overlay"
if [ -d "$APPLET_ATAK" ]; then
    install -m 0755 "$SRC/atak-overlay/atak-overlay" "$APPLET_ATAK/atak-overlay"
fi

# ---------------------------------------------------------------------------
#  Android APK
# ---------------------------------------------------------------------------
ANDROID_DIR="$SRC/mesh-ctrl-android"
if [ -f "$ANDROID_DIR/gradlew" ]; then
    echo "Building mesh-ctrl APK..."
    (cd "$ANDROID_DIR" && ./gradlew assembleDebug -q)
    APK="$ANDROID_DIR/app/build/outputs/apk/debug/app-debug.apk"
    install_file 0644 "$APK" "$STAGE/usr/local/share/manet/mesh-ctrl.apk"
    install_file 0644 "$APK" "$STAGE/usr/local/share/manet/www/assets/mesh-ctrl.apk"
fi

# ---------------------------------------------------------------------------
#  Pre-built arm64 binaries (alfred, batctl, wpa_supplicant_s1g, etc.)
# ---------------------------------------------------------------------------
install_file 0755 "$BINARIES/alfred"              "$STAGE/usr/sbin/alfred"
install_file 0755 "$BINARIES/batctl"              "$STAGE/usr/sbin/batctl"
install_file 0755 "$BINARIES/wpa_cli_s1g"         "$STAGE/usr/sbin/wpa_cli_s1g"
install_file 0755 "$BINARIES/wpa_supplicant_s1g"  "$STAGE/usr/sbin/wpa_supplicant_s1g"
install_file 0755 "$BINARIES/morse_cli"           "$STAGE/usr/local/bin/morse_cli"
install_file 0755 "$BINARIES/chronyc"             "$STAGE/usr/local/bin/chronyc"
install_file 0755 "$BINARIES/openvlm"             "$STAGE/usr/local/bin/openvlm"

# ---------------------------------------------------------------------------
#  networkd-dispatcher scripts (placed into .d/ subdirs)
# ---------------------------------------------------------------------------
NDDIR="$STAGE/etc/networkd-dispatcher"
mkdir -p "$NDDIR"/{carrier,routable,off,no-carrier,degraded}.d

cat > "$NDDIR/carrier.d/50-ethernet-detect" <<'EOF'
#!/bin/bash
set -euo pipefail

/usr/local/bin/manet-uplink-dispatch.sh carrier "${IFACE:-}"

if grep -qi '^auto_update=1' /etc/mesh.conf 2>/dev/null && ping -c 1 -W 2 -I "$IFACE" 8.8.8.8 >/dev/null 2>&1; then
    systemctl reload node-update 2>/dev/null || true
fi
EOF

cat > "$NDDIR/routable.d/50-manet-uplink" <<'EOF'
#!/bin/bash
set -euo pipefail

/usr/local/bin/manet-uplink-dispatch.sh routable "${IFACE:-}"
EOF

install -m 0755 "$ROOTFS/etc/networkd-dispatcher/off" "$NDDIR/off.d/50-gateway-disable"
install -m 0755 "$ROOTFS/etc/networkd-dispatcher/off" "$NDDIR/no-carrier.d/50-gateway-disable"
install -m 0755 "$ROOTFS/etc/networkd-dispatcher/off" "$NDDIR/degraded.d/50-gateway-disable"
chmod 0755 "$NDDIR/carrier.d/50-ethernet-detect" "$NDDIR/routable.d/50-manet-uplink"

# Remove the flat networkd-dispatcher source files (they don't belong on the node)
rm -f "$NDDIR"/{carrier,degraded,no-carrier,off,routable,README.md} 2>/dev/null || true

# ---------------------------------------------------------------------------
#  Systemd enable symlinks
# ---------------------------------------------------------------------------
mkdir -p "$STAGE/etc/systemd/system/multi-user.target.wants"
for unit in \
    battery-reader.service \
    cot-emitter.service \
    ebtables-restore.service \
    ethernet-autodetect.service \
    gateway-manager.service \
    gps-reader.service \
    manet-ctrl.service \
    manet-txpower.service \
    mesh-chat.service \
    mesh-manager.service \
    mesh-registry.service \
    mesh-voice.service \
    morse-spi-watchdog.service \
    node-manager.service \
    node-update.service \
    sae-watchdog.service
do
    if [ -f "$STAGE/etc/systemd/system/$unit" ]; then
        ln -sf "../$unit" "$STAGE/etc/systemd/system/multi-user.target.wants/$unit"
    fi
done

# ---------------------------------------------------------------------------
#  SBC overlay (kernel/modules/firmware) — passed via SBC_OVERLAY_DIR env var
# ---------------------------------------------------------------------------
if [ -n "${SBC_OVERLAY_DIR:-}" ]; then
    [ -d "$SBC_OVERLAY_DIR" ] || { echo "Missing SBC overlay: $SBC_OVERLAY_DIR" >&2; exit 1; }
    install_tree "$SBC_OVERLAY_DIR" "$STAGE"
fi

[ -e "$STAGE/lib" ] || ln -s usr/lib "$STAGE/lib"

# Fix permissions
[ -f "$STAGE/etc/sudoers.d/perf" ] && chmod 0440 "$STAGE/etc/sudoers.d/perf"

# ---------------------------------------------------------------------------
#  Pack
# ---------------------------------------------------------------------------
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
