#!/usr/bin/env bash
set -euo pipefail

# Build a deployment tarball for x86_64 Linux nodes.
# Unlike ARM builds, this does NOT bundle alfred/batctl/wpa_supplicant_s1g —
# those come from system packages (apt) on x86.

OUT="${1:-x86-install.tar.gz}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
STAGE="$(mktemp -d)"

cleanup() {
    rm -rf "$STAGE"
}
trap cleanup EXIT

install_tree() {
    local src="$1"
    local dst="$2"
    if [ -d "$src" ]; then
        mkdir -p "$dst"
        cp -a "$src"/. "$dst"/
    fi
}

install_file() {
    local mode="$1"
    local src="$2"
    local dst="$3"
    if [ -f "$src" ]; then
        mkdir -p "$(dirname "$dst")"
        install -m "$mode" "$src" "$dst"
    fi
}

mkdir -p "$STAGE/usr/local/bin" "$STAGE/etc/systemd/system"

# --- Cross-compile Go services for amd64 ---
echo "Building manet-ctrl for linux/amd64..."
(cd "$REPO_ROOT/MANET/cmd/manet-ctrl" && \
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o manet-ctrl .)
install_file 0755 "$REPO_ROOT/MANET/cmd/manet-ctrl/manet-ctrl" "$STAGE/usr/local/bin/manet-ctrl"
install_file 0644 "$REPO_ROOT/MANET/cmd/manet-ctrl/manet-ctrl.service" "$STAGE/etc/systemd/system/manet-ctrl.service"

echo "Building mesh-registry for linux/amd64..."
(cd "$REPO_ROOT/MANET/cmd/mesh-registry" && \
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o mesh-registry .)
install_file 0755 "$REPO_ROOT/MANET/cmd/mesh-registry/mesh-registry" "$STAGE/usr/local/bin/mesh-registry"

if [ -f "$REPO_ROOT/MANET/mesh-voice/bin/mesh-voice-linux-amd64" ]; then
    install_file 0755 "$REPO_ROOT/MANET/mesh-voice/bin/mesh-voice-linux-amd64" "$STAGE/usr/local/bin/mesh-voice"
fi

# --- Scripts — all subdirs flatten into /usr/local/bin ---
for subdir in core elections radio network system; do
    install_tree "$REPO_ROOT/MANET/scripts/$subdir" "$STAGE/usr/local/bin"
done
chmod -R a+rX "$STAGE/usr/local/bin"
find "$STAGE/usr/local/bin" -type f \( -name '*.sh' -o -name '*.py' -o -name 'mesh' \) -exec chmod 0755 {} +

# --- Web frontend ---
install_tree "$REPO_ROOT/MANET/www" "$STAGE/usr/local/share/manet/www"

# --- Systemd units ---
install_tree "$REPO_ROOT/MANET/systemd" "$STAGE/etc/systemd/system"

# --- System config (mesh.conf defaults, avahi, sudoers) ---
install_tree "$REPO_ROOT/MANET/etc" "$STAGE/etc"

# --- Networkd configs (bat0, br0 bridge) ---
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

# --- Networkd-dispatcher hooks ---
if [ -d "$REPO_ROOT/MANET/networkd-dispatcher" ]; then
    mkdir -p \
        "$STAGE/etc/networkd-dispatcher/carrier.d" \
        "$STAGE/etc/networkd-dispatcher/routable.d" \
        "$STAGE/etc/networkd-dispatcher/off.d" \
        "$STAGE/etc/networkd-dispatcher/no-carrier.d" \
        "$STAGE/etc/networkd-dispatcher/degraded.d"

    cat > "$STAGE/etc/networkd-dispatcher/carrier.d/50-ethernet-detect" <<'EOF'
#!/bin/bash
set -euo pipefail
/usr/local/bin/manet-uplink-dispatch.sh carrier "${IFACE:-}"
if grep -qi '^auto_update=1' /etc/mesh.conf 2>/dev/null && ping -c 1 -W 2 -I "$IFACE" 8.8.8.8 >/dev/null 2>&1; then
    /usr/local/bin/node-update.sh --routine
fi
EOF

    cat > "$STAGE/etc/networkd-dispatcher/routable.d/50-manet-uplink" <<'EOF'
#!/bin/bash
set -euo pipefail
/usr/local/bin/manet-uplink-dispatch.sh routable "${IFACE:-}"
EOF

    install_file 0755 "$REPO_ROOT/MANET/networkd-dispatcher/off" \
        "$STAGE/etc/networkd-dispatcher/off.d/50-gateway-disable"
    install_file 0755 "$REPO_ROOT/MANET/networkd-dispatcher/off" \
        "$STAGE/etc/networkd-dispatcher/no-carrier.d/50-gateway-disable"
    install_file 0755 "$REPO_ROOT/MANET/networkd-dispatcher/off" \
        "$STAGE/etc/networkd-dispatcher/degraded.d/50-gateway-disable"
    chmod 0755 \
        "$STAGE/etc/networkd-dispatcher/carrier.d/50-ethernet-detect" \
        "$STAGE/etc/networkd-dispatcher/routable.d/50-manet-uplink"
fi

# --- Enable key services ---
mkdir -p "$STAGE/etc/systemd/system/multi-user.target.wants"
for unit in \
    gateway-route-manager.service \
    sae-watchdog.service \
    manet-ctrl.service \
    mesh-registry.service
do
    if [ -f "$STAGE/etc/systemd/system/$unit" ]; then
        ln -s "../$unit" "$STAGE/etc/systemd/system/multi-user.target.wants/$unit"
    fi
done

if [ -f "$STAGE/etc/sudoers.d/perf" ]; then
    chmod 0440 "$STAGE/etc/sudoers.d/perf"
fi

# --- Build tarball ---
mkdir -p "$(dirname "$OUT")"
tar --owner=0 --group=0 --numeric-owner -czf "$OUT" -C "$STAGE" .
echo "Built x86 tarball: $OUT ($(du -h "$OUT" | cut -f1))"
