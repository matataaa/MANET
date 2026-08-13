#!/usr/bin/env bash
set -euo pipefail

# Build a deployment tarball for x86_64 Linux nodes.
# Unlike ARM builds, this does NOT bundle alfred/batctl/wpa_supplicant_s1g —
# those come from system packages (apt) on x86.

OUT="${1:-x86-install.tar.gz}"
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

# ---------------------------------------------------------------------------
#  Rootfs overlay — scripts, services, configs, web UI
# ---------------------------------------------------------------------------
install_tree "$ROOTFS/usr" "$STAGE/usr"
install_tree "$ROOTFS/etc" "$STAGE/etc"

chmod -R a+rX "$STAGE/usr/local/bin"
find "$STAGE/usr/local/bin" -type f \
    \( -name '*.sh' -o -name '*.py' -o -name 'mesh' \) \
    -exec chmod 0755 {} +

# ---------------------------------------------------------------------------
#  Cross-compile Go services for amd64
# ---------------------------------------------------------------------------
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

BUILD_VERSION="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo 'unknown')"

for svc in "${GO_SERVICES[@]}"; do
    echo "Building $svc for linux/amd64..."
    LDFLAGS="-s -w"
    if [ "$svc" = "manet-ctrl" ]; then
        LDFLAGS="-s -w -X main.Version=${BUILD_VERSION}"
    fi
    (cd "$SRC/$svc" && \
        GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="$LDFLAGS" -o "$svc" .)
    install -m 0755 "$SRC/$svc/$svc" "$STAGE/usr/local/bin/$svc"
done

# mesh-voice — use pre-built binary if available
if [ -f "$SRC/mesh-voice/bin/mesh-voice-linux-amd64" ]; then
    install_file 0755 "$SRC/mesh-voice/bin/mesh-voice-linux-amd64" "$STAGE/usr/local/bin/mesh-voice"
fi

# ---------------------------------------------------------------------------
#  Networkd configs (bat0, br0 bridge) — x86 nodes don't get these from
#  firstrun.sh.template, so we bundle them here.
# ---------------------------------------------------------------------------
mkdir -p "$STAGE/etc/systemd/network"

cat > "$STAGE/etc/systemd/network/10-bat0.network" <<'EOF'
[Match]
Name=bat0

[Network]
Bridge=br0
LinkLocalAddressing=ipv6
IPv6Token=eui64
IPv6PrivacyExtensions=no

[Link]
MTUBytes=1400
EOF

cat > "$STAGE/etc/systemd/network/10-br0-bridge.netdev" <<'EOF'
[NetDev]
Name=br0
Kind=bridge

[Bridge]
MulticastSnooping=true
MulticastQuerier=true
EOF

cat > "$STAGE/etc/systemd/network/20-br0-bridge.network" <<'EOF'
[Match]
Name=br0

[Network]
DHCP=no
LinkLocalAddressing=ipv6
IPv6AcceptRA=yes
MulticastDNS=yes

[Route]
Destination=224.0.0.0/4
Type=multicast

[Link]
RequiredForOnline=no
MTUBytes=1400
EOF

cat > "$STAGE/etc/systemd/network/90-default-no-mdns.network" <<'EOF'
[Match]
Name=!br0

[Network]
LLMNR=no
MulticastDNS=no
EOF

# ---------------------------------------------------------------------------
#  networkd-dispatcher hooks
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

# Remove flat networkd-dispatcher source files
rm -f "$NDDIR"/{carrier,degraded,no-carrier,off,routable,README.md} 2>/dev/null || true

# ---------------------------------------------------------------------------
#  Systemd enable symlinks
# ---------------------------------------------------------------------------
mkdir -p "$STAGE/etc/systemd/system/multi-user.target.wants"
for unit in \
    ebtables-restore.service \
    gateway-manager.service \
    manet-ctrl.service \
    mesh-registry.service \
    node-manager.service \
    sae-watchdog.service
do
    if [ -f "$STAGE/etc/systemd/system/$unit" ]; then
        ln -sf "../$unit" "$STAGE/etc/systemd/system/multi-user.target.wants/$unit"
    fi
done

[ -f "$STAGE/etc/sudoers.d/perf" ] && chmod 0440 "$STAGE/etc/sudoers.d/perf"

# ---------------------------------------------------------------------------
#  Pack
# ---------------------------------------------------------------------------
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

chmod 755 "$STAGE"
tar --owner=0 --group=0 --numeric-owner -czf "$OUT" -C "$STAGE" .
echo "Built x86 tarball: $OUT ($(du -h "$OUT" | cut -f1))"
