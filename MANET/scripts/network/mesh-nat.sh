#!/usr/bin/env bash
set -euo pipefail

POLL_INTERVAL=15
STATE_FILE=/run/manet-uplink.env
LEGACY_GATEWAY_STATE=/var/run/mesh-gateway.state
UPSTREAM_IFACE_FILE=/var/run/upstream_iface

log() {
    local msg="[$(date '+%Y-%m-%d %H:%M:%S')] - MESH-NAT: $*"
    echo "$msg" >&2
    echo "$msg" | systemd-cat -t mesh-nat
}

get_upstream_iface() {
    if [ -f "$STATE_FILE" ]; then
        # shellcheck disable=SC1090
        . "$STATE_FILE" 2>/dev/null || true
        echo "${UPLINK_IFACE:-}"
        return
    fi
    cat "$UPSTREAM_IFACE_FILE" 2>/dev/null || true
}

is_gateway() {
    [ -f "$LEGACY_GATEWAY_STATE" ] || [ -f "$STATE_FILE" ]
}

nat_rules_present() {
    local iface="$1"
    nft list chain ip nat postrouting 2>/dev/null | grep -q "oifname \"$iface\" masquerade"
}

apply_nat() {
    local iface="$1"

    sysctl -q net.ipv4.ip_forward=1

    nft add table ip nat 2>/dev/null || true
    nft add chain ip nat postrouting \
        '{ type nat hook postrouting priority srcnat; policy accept; }' 2>/dev/null || true
    nft flush chain ip nat postrouting 2>/dev/null || true
    nft add rule ip nat postrouting oifname "$iface" masquerade

    nft add table ip mangle 2>/dev/null || true
    nft add chain ip mangle forward \
        '{ type filter hook forward priority mangle; policy accept; }' 2>/dev/null || true
    nft flush chain ip mangle forward 2>/dev/null || true
    nft add rule ip mangle forward tcp flags syn tcp option maxseg size set rt mtu

    log "NAT masquerade active on $iface"
}

clear_nat() {
    nft flush chain ip nat postrouting 2>/dev/null || true
    nft flush chain ip mangle forward 2>/dev/null || true
    log "NAT rules cleared (not a gateway)"
}

log "Starting mesh NAT watchdog (polling every ${POLL_INTERVAL}s)"

while true; do
    if is_gateway; then
        iface=$(get_upstream_iface)
        if [ -n "$iface" ]; then
            if ! nat_rules_present "$iface"; then
                log "Gateway on $iface — NAT rules missing, applying"
                apply_nat "$iface"
            fi
        fi
    else
        if nft list chain ip nat postrouting 2>/dev/null | grep -q "masquerade"; then
            clear_nat
        fi
    fi
    sleep "$POLL_INTERVAL"
done
