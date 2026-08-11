#!/usr/bin/env bash
set -euo pipefail

OUT="${1:-rpi5-install.tar.gz}"
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

require_dir() {
    local path="$1"
    local label="$2"
    if [ ! -d "$path" ]; then
        echo "Missing $label directory: $path" >&2
        exit 1
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

mkdir -p "$STAGE/usr/local/bin" "$STAGE/usr/sbin" "$STAGE/etc/systemd/system"

# Scripts — all subdirs flatten into /usr/local/bin on the node
for subdir in core elections radio network system; do
    install_tree "$REPO_ROOT/MANET/scripts/$subdir" "$STAGE/usr/local/bin"
done
chmod -R a+rX "$STAGE/usr/local/bin"
find "$STAGE/usr/local/bin" -type f \( -name '*.sh' -o -name '*.py' -o -name 'morse_cli' -o -name 'chronyc' -o -name 'mesh' \) -exec chmod 0755 {} +

# Cross-compile Go services for arm64
echo "Building manet-ctrl for linux/arm64..."
(cd "$REPO_ROOT/MANET/cmd/manet-ctrl" && \
    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o manet-ctrl .)
install_file 0755 "$REPO_ROOT/MANET/cmd/manet-ctrl/manet-ctrl" "$STAGE/usr/local/bin/manet-ctrl"
install_file 0644 "$REPO_ROOT/MANET/cmd/manet-ctrl/manet-ctrl.service" "$STAGE/etc/systemd/system/manet-ctrl.service"
install_file 0755 "$REPO_ROOT/MANET/mesh-voice/bin/mesh-voice-linux-arm64" "$STAGE/usr/local/bin/mesh-voice"

echo "Building mesh-registry for linux/arm64..."
(cd "$REPO_ROOT/MANET/cmd/mesh-registry" && \
    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o mesh-registry .)
install_file 0755 "$REPO_ROOT/MANET/cmd/mesh-registry/mesh-registry" "$STAGE/usr/local/bin/mesh-registry"

# Pre-built arm64 binaries
install_file 0755 "$REPO_ROOT/MANET/binaries_arm64/alfred" "$STAGE/usr/sbin/alfred"
install_file 0755 "$REPO_ROOT/MANET/binaries_arm64/batctl" "$STAGE/usr/sbin/batctl"
install_file 0755 "$REPO_ROOT/MANET/binaries_arm64/wpa_cli_s1g" "$STAGE/usr/sbin/wpa_cli_s1g"
install_file 0755 "$REPO_ROOT/MANET/binaries_arm64/wpa_supplicant_s1g" "$STAGE/usr/sbin/wpa_supplicant_s1g"
install_file 0755 "$REPO_ROOT/MANET/binaries_arm64/morse_cli" "$STAGE/usr/local/bin/morse_cli"
install_file 0755 "$REPO_ROOT/MANET/binaries_arm64/chronyc" "$STAGE/usr/local/bin/chronyc"

# Web frontend
install_tree "$REPO_ROOT/MANET/www" "$STAGE/usr/local/share/manet/www"

# System config
install_tree "$REPO_ROOT/MANET/systemd" "$STAGE/etc/systemd/system"
install_tree "$REPO_ROOT/MANET/systemd-network" "$STAGE/etc/systemd/network"
install_tree "$REPO_ROOT/MANET/udev/rules.d" "$STAGE/etc/udev/rules.d"
install_tree "$REPO_ROOT/MANET/etc" "$STAGE/etc"

install_file 0644 "$REPO_ROOT/MANET/root/regulatory.db" "$STAGE/root/regulatory.db"

mkdir -p "$STAGE/root/networkd-dispatcher"
install_file 0755 "$REPO_ROOT/MANET/networkd-dispatcher/carrier" "$STAGE/root/networkd-dispatcher/carrier"
install_file 0755 "$REPO_ROOT/MANET/networkd-dispatcher/off" "$STAGE/root/networkd-dispatcher/off"

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

install -m 0755 "$REPO_ROOT/MANET/networkd-dispatcher/off" "$STAGE/etc/networkd-dispatcher/off.d/50-gateway-disable"
install -m 0755 "$REPO_ROOT/MANET/networkd-dispatcher/off" "$STAGE/etc/networkd-dispatcher/no-carrier.d/50-gateway-disable"
install -m 0755 "$REPO_ROOT/MANET/networkd-dispatcher/off" "$STAGE/etc/networkd-dispatcher/degraded.d/50-gateway-disable"
chmod 0755 "$STAGE/etc/networkd-dispatcher/carrier.d/50-ethernet-detect" "$STAGE/etc/networkd-dispatcher/routable.d/50-manet-uplink"

mkdir -p "$STAGE/etc/systemd/system/multi-user.target.wants"
for unit in \
    ethernet-autodetect.service \
    manet-txpower.service \
    mesh-default-route-fix.service \
    sae-watchdog.service \
    ebtables-restore.service \
    manet-ctrl.service \
    mesh-registry.service \
    node-manager.service
do
    if [ -f "$STAGE/etc/systemd/system/$unit" ]; then
        ln -sf "../$unit" "$STAGE/etc/systemd/system/multi-user.target.wants/$unit"
    fi
done

if [ -n "${SBC_OVERLAY_DIR:-}" ]; then
    require_dir "$SBC_OVERLAY_DIR" "SBC overlay"
    install_tree "$SBC_OVERLAY_DIR" "$STAGE"
fi

if [ ! -e "$STAGE/lib" ]; then
    ln -s usr/lib "$STAGE/lib"
fi

if [ -f "$STAGE/etc/sudoers.d/perf" ]; then
    chmod 0440 "$STAGE/etc/sudoers.d/perf"
fi

mkdir -p "$(dirname "$OUT")"
tar --owner=0 --group=0 --numeric-owner -czf "$OUT" -C "$STAGE" .
