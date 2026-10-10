#!/usr/bin/env bash
set -euo pipefail

STATE_FILE=/run/manet-uplink.env
LEGACY_GATEWAY_STATE=/var/run/mesh-gateway.state
LEGACY_NTP_STATE=/var/run/mesh-ntp.state
LEGACY_ETH_STATE=/var/run/ethernet_detection_state
UPSTREAM_IFACE_FILE=/var/run/upstream_iface
LOCK_FILE=/run/manet-uplink-dispatch.lock
NETWORKD_DIR=/etc/systemd/network
# Written only by ethernet-autodetect.sh, which owns the Ethernet port's role.
ETH_ROLE_FILE=/run/manet-eth-role
UPLINK_SPEED=${MANET_UPLINK_SPEED:-/usr/local/bin/manet-uplink-speed.sh}

EVENT="${1:-${STATE:-reconcile}}"
IFACE="${2:-${IFACE:-${INTERFACE:-}}}"

# Periodic and hotplug reconciles skip a busy pass; explicit promote/release
# requests from ethernet-autodetect.sh wait for it (bounded).
exec 200>"$LOCK_FILE"
case "$EVENT" in
    promote|release) flock -w 60 200 || exit 0 ;;
    *) flock -n 200 || exit 0 ;;
esac

log() {
    printf '[%(%Y-%m-%d %H:%M:%S)T] - MANET-UPLINK: %s\n' -1 "$*" >&2
}

is_upstream_iface() {
    local iface="$1"

    [ -n "$iface" ] || return 1
    [ -d "/sys/class/net/$iface" ] || return 1

    case "$iface" in
        lo|br*|bat*|wlan*) return 1 ;;
    esac

    if [ "$iface" = "end0" ]; then
        return 0
    fi

    # USB tethering and USB Ethernet dongles normally appear as usbX/enx*/en*
    # but wlan2 is also USB-backed on these nodes, so the name filter above
    # must run before the bus check.
    local bus
    bus=$(readlink "/sys/class/net/$iface/device/subsystem" 2>/dev/null | grep -o 'usb' || true)
    [ "$bus" = "usb" ]
}

# ethernet-autodetect.sh's role for iface, if it manages it.
eth_role() {
    local IFACE="" ROLE=""
    [ -f "$ETH_ROLE_FILE" ] || return 0
    # shellcheck disable=SC1090
    . "$ETH_ROLE_FILE"
    [ "$IFACE" = "$1" ] && echo "$ROLE"
    return 0
}

# A port that is a wired EUD (bridged into br0, or being detected) is never
# an uplink candidate: probing it would detach the EUD and run a DHCP client
# on its cable.
is_eud_port() {
    local iface="$1"
    [ "$(basename "$(readlink "/sys/class/net/$iface/master" 2>/dev/null)")" = "br0" ] && return 0
    case "$(eth_role "$iface")" in
        wired-eud|detecting) return 0 ;;
    esac
    return 1
}

has_carrier() {
    local iface="$1"
    [ "$(cat "/sys/class/net/$iface/carrier" 2>/dev/null || echo 0)" = "1" ]
}

iface_ip() {
    ip -4 -o addr show dev "$1" 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -n1
}

iface_default_gw() {
    ip route show default dev "$1" 2>/dev/null | awk '/^default / {print $3; exit}'
}

write_networkd_dhcp_config() {
    local iface="$1"
    local conf="${NETWORKD_DIR}/20-${iface}.network"

    cat > "$conf" <<EOF
[Match]
Name=${iface}

[Link]
RequiredForOnline=yes

[Network]
DHCP=ipv4
IPv6AcceptRA=yes
Bridge=

[DHCP]
ClientIdentifier=mac
UseDNS=yes
UseNTP=yes
UseRoutes=yes
Timeout=10

[DHCPv4]
UseRoutes=yes
UseGateway=yes
EOF

    networkctl reload 2>/dev/null || true
    networkctl reconfigure "$iface" 2>/dev/null || true
}

wait_for_ipv4() {
    local iface="$1"
    local max_wait="${2:-12}"
    local ip=""

    for _ in $(seq 1 "$max_wait"); do
        ip=$(iface_ip "$iface")
        if [ -n "$ip" ]; then
            echo "$ip"
            return 0
        fi
        sleep 1
    done

    return 1
}

internet_probe() {
    local iface="$1" hits=0

    ping -c 1 -W 3 -I "$iface" 1.1.1.1 >/dev/null 2>&1 && ((hits++))
    ping -c 1 -W 3 -I "$iface" 8.8.8.8 >/dev/null 2>&1 && ((hits++))
    ping -c 1 -W 3 -I "$iface" 9.9.9.9 >/dev/null 2>&1 && ((hits++))

    [ "$hits" -ge 1 ]
}

candidate_ifaces() {
    {
        if [ -n "$IFACE" ] && is_upstream_iface "$IFACE"; then
            echo "$IFACE"
        fi

        ip route get 1.1.1.1 2>/dev/null | awk '
            {
                for (i = 1; i <= NF; i++) {
                    if ($i == "dev") {
                        print $(i + 1)
                        exit
                    }
                }
            }
        '

        ip route show default 2>/dev/null | awk '
            /^default / {
                for (i = 1; i <= NF; i++) {
                    if ($i == "dev") {
                        print $(i + 1)
                    }
                }
            }
        '

        if is_upstream_iface end0; then
            echo end0
        fi

        for path in /sys/class/net/*; do
            local iface
            iface=$(basename "$path")
            is_upstream_iface "$iface" && echo "$iface"
        done
    } | awk '!seen[$0]++'
}

find_working_uplink() {
    local iface ip

    for iface in $(candidate_ifaces); do
        is_upstream_iface "$iface" || continue
        has_carrier "$iface" || continue
        is_eud_port "$iface" && continue

        ip link set "$iface" nomaster 2>/dev/null || true
        if [ ! -f "${NETWORKD_DIR}/20-${iface}.network" ]; then
            write_networkd_dhcp_config "$iface"
        fi
        ip=$(wait_for_ipv4 "$iface" 12 || true)
        [ -n "$ip" ] || continue
        iface_default_gw "$iface" >/dev/null || true

        if internet_probe "$iface"; then
            # An Ethernet uplink must also complete the speed test, which
            # measures what this gateway will announce. A captive portal or a
            # filtered network fails it, and then this is not a gateway yet.
            # Other uplinks are metered and not tested (exit 2).
            local speed_rc=0
            "$UPLINK_SPEED" measure "$iface" >/dev/null || speed_rc=$?
            if [ "$speed_rc" -eq 0 ] || [ "$speed_rc" -eq 2 ]; then
                echo "$iface"
                return 0
            fi
            # 3: a recent failure stands and was not retested; already logged.
            [ "$speed_rc" -eq 3 ] ||
                log "$iface passes the internet probe but not the speed test; not a gateway yet"
            continue
        fi

        log "$iface has IPv4 ($ip) but no verified internet"
    done

    return 1
}

# The firewall every node keeps, gateway or not: input from the mesh side
# and the uplink candidates (the same broad accepts as before; the web UI,
# SSH and iperf ports are manet_ui's), and forwarding only within br0, which
# is how EUDs reach the mesh gateway through this node (ip_forward stays on).
# A non-gateway, including a demoted gateway still on its LAN, must not
# route between that LAN and the mesh. Older code deleted the whole table
# on demotion, which left forwarding wide open.
base_firewall() {
    local candidate

    nft add table inet filter 2>/dev/null || true
    nft add chain inet filter input '{ type filter hook input priority filter; policy drop; }' 2>/dev/null || true
    nft add chain inet filter forward '{ type filter hook forward priority filter; policy drop; }' 2>/dev/null || true
    nft add chain inet filter output '{ type filter hook output priority filter; policy accept; }' 2>/dev/null || true

    nft flush chain inet filter input 2>/dev/null || true
    nft flush chain inet filter forward 2>/dev/null || true

    nft add rule inet filter input ct state established,related accept
    nft add rule inet filter input ct state invalid drop
    # These accepts are deliberately broad. Access to the web UI (80) and the
    # iperf3 daemon (5201) is decided in the manet_ui table, which runs at an
    # earlier hook priority — see manet-ui-firewall.sh. Do not try to encode
    # that policy here as well.
    nft add rule inet filter input iifname "lo" accept
    nft add rule inet filter input iifname "br0" accept
    nft add rule inet filter input iifname "bat0" accept
    for candidate in $(candidate_ifaces); do
        # A client's default route is via br0, already accepted above.
        case "$candidate" in lo|br0|bat0) continue ;; esac
        nft add rule inet filter input iifname "$candidate" accept
    done

    nft add rule inet filter forward iifname "br0" oifname "br0" accept
}

configure_firewall() {
    local iface="$1"

    base_firewall
    nft add rule inet filter forward iifname "br0" oifname "$iface" accept
    nft add rule inet filter forward iifname "$iface" oifname "br0" ct state established,related accept

    nft add table ip nat 2>/dev/null || true
    nft add chain ip nat postrouting '{ type nat hook postrouting priority srcnat; policy accept; }' 2>/dev/null || true
    nft flush chain ip nat postrouting 2>/dev/null || true
    nft add rule ip nat postrouting oifname "$iface" masquerade

    nft add table ip mangle 2>/dev/null || true
    nft add chain ip mangle forward '{ type filter hook forward priority mangle; policy accept; }' 2>/dev/null || true
    nft flush chain ip mangle forward 2>/dev/null || true
    nft add rule ip mangle forward tcp flags syn tcp option maxseg size set rt mtu
}

# True when the forward chain holds exactly base_firewall's one rule.
firewall_is_base() {
    local rules
    rules=$(nft list chain inet filter forward 2>/dev/null | grep -E '(accept|drop|reject)$' | sed 's/^[[:space:]]*//')
    [ "$rules" = 'iifname "br0" oifname "br0" accept' ]
}

clear_firewall() {
    base_firewall
    nft flush chain ip nat postrouting 2>/dev/null || true
    nft flush chain ip mangle forward 2>/dev/null || true
}

eud_mode() {
    awk -F= '$1 == "eud" {print $2; exit}' /etc/mesh.conf 2>/dev/null || true
}

ensure_eud_services() {
    local mode ap_iface=""
    mode=$(eud_mode)
    [ -f /var/lib/ap_interface ] && ap_iface=$(cat /var/lib/ap_interface)

    if { [ "$mode" = "wireless" ] || [ "$mode" = "auto" ]; } && [ -n "$ap_iface" ]; then
        systemctl start ap-interface-setup.service 2>/dev/null || true
        systemctl unmask dnsmasq.service 2>/dev/null || true
        systemctl enable hostapd.service 2>/dev/null || true
        systemctl start hostapd.service 2>/dev/null || true
        systemctl enable dnsmasq.service 2>/dev/null || true
        systemctl start dnsmasq.service 2>/dev/null || true
        systemctl start ap-txpower.service 2>/dev/null || true

        if ! ip link show "$ap_iface" 2>/dev/null | grep -q "master br0"; then
            ip link set "$ap_iface" master br0 2>/dev/null || true
            ip link set "$ap_iface" up 2>/dev/null || true
        fi
    fi
}

promote_gateway() {
    local iface="$1"
    local ip gw

    ip=$(iface_ip "$iface")
    gw=$(iface_default_gw "$iface")

    if [ -z "$ip" ]; then
        log "Refusing gateway promotion on $iface: no IPv4 address"
        return 1
    fi
    if [ -n "$gw" ]; then
        ip route replace default via "$gw" dev "$iface" src "$ip" metric 100 2>/dev/null || true
    fi

    configure_firewall "$iface"
    # Announce the measured bandwidth (Ethernet), gateway_bandwidth, or 10/2.
    "$UPLINK_SPEED" announce "$iface" >/dev/null 2>&1 || true

    touch "$LEGACY_GATEWAY_STATE"
    echo "$iface" > "$UPSTREAM_IFACE_FILE"
    cat > "$STATE_FILE" <<EOF
UPLINK_MODE=gateway
UPLINK_IFACE=$iface
UPLINK_IP=$ip
UPLINK_GW=${gw:-}
UPDATED_AT=$(date +%s)
EOF
    cat > "$LEGACY_ETH_STATE" <<EOF
ETH_MODE=GATEWAY
ETH_IP=$ip
DEFAULT_GW=${gw:-none}
DETECTED_AT=$(date +%s)
DETECTION_METHOD=MANET_UPLINK_DISPATCH
EOF

    cp /etc/radvd-gateway.conf /etc/radvd.conf 2>/dev/null || true
    systemctl restart radvd 2>/dev/null || true
    ensure_eud_services
    systemctl restart mesh-manager 2>/dev/null || true
    systemctl restart gateway-manager 2>/dev/null || true
    # Sync over the uplink and serve time to the mesh. Takes up to ~90s, so
    # it runs as its own transient unit rather than holding this lock.
    systemd-run --no-block --collect --unit=manet-gateway-ntp \
        /usr/local/bin/manet-gateway-ntp.sh start >/dev/null 2>&1 || true

    log "Promoted $iface as MANET gateway (${ip}, gw=${gw:-none})"
}

demote_gateway() {
    local old_iface="${1:-}"

    clear_firewall
    batctl gw_mode client 2>/dev/null || true
    # The uplink is gone; a new one is measured again before it is announced.
    "$UPLINK_SPEED" forget 2>/dev/null || true
    systemctl stop manet-gateway-ntp.service 2>/dev/null || true
    /usr/local/bin/manet-gateway-ntp.sh stop || true
    rm -f "$LEGACY_GATEWAY_STATE" "$LEGACY_NTP_STATE" "$STATE_FILE" "$UPSTREAM_IFACE_FILE"
    # ethernet_detection_state describes the Ethernet port's role; only drop
    # it here when it described this uplink, not a wired EUD on end0.
    grep -q '^ETH_MODE=GATEWAY' "$LEGACY_ETH_STATE" 2>/dev/null && rm -f "$LEGACY_ETH_STATE"

    if [ -n "$old_iface" ] && is_upstream_iface "$old_iface"; then
        ip addr flush dev "$old_iface" 2>/dev/null || true
        ip link set "$old_iface" nomaster 2>/dev/null || true
        rm -f "${NETWORKD_DIR}/20-${old_iface}.network" "${NETWORKD_DIR}/20-${old_iface}-"*.network 2>/dev/null || true
        networkctl reload 2>/dev/null || true
        networkctl reconfigure "$old_iface" 2>/dev/null || true
    fi

    cp /etc/radvd-mesh.conf /etc/radvd.conf 2>/dev/null || true
    systemctl restart radvd 2>/dev/null || true
    ensure_eud_services
    systemctl restart mesh-manager 2>/dev/null || true
    systemctl restart gateway-manager 2>/dev/null || true

    log "Demoted MANET gateway${old_iface:+ on $old_iface}"
}

current_uplink_iface() {
    if [ -f "$STATE_FILE" ]; then
        # shellcheck disable=SC1090
        . "$STATE_FILE" 2>/dev/null || true
        echo "${UPLINK_IFACE:-}"
        return
    fi

    cat "$UPSTREAM_IFACE_FILE" 2>/dev/null || true
}

reconcile() {
    local current working
    current=$(current_uplink_iface)

    if [ -n "$current" ] && [ -f "$STATE_FILE" ] && has_carrier "$current" && internet_probe "$current"; then
        return 0
    fi

    # Don't demote if promoted recently — transient probe failures are common
    # during boot or after mesh topology changes
    if [ -f "$STATE_FILE" ]; then
        local promoted_at now elapsed
        promoted_at=$(awk -F= '$1 == "UPDATED_AT" {print $2; exit}' "$STATE_FILE" 2>/dev/null || echo 0)
        now=$(date +%s)
        elapsed=$(( now - ${promoted_at:-0} ))
        if [ "$elapsed" -lt 120 ] && [ -n "$current" ] && has_carrier "$current"; then
            log "Probe failed but within stabilization window (${elapsed}s < 120s); holding gateway"
            return 0
        fi
    fi

    working=$(find_working_uplink || true)
    if [ -n "$working" ]; then
        promote_gateway "$working"
        return 0
    fi

    # Nothing to demote: never promoted (or a prior demote already cleared
    # all gateway state). Without this, every mesh-only node — anything with
    # no Ethernet/USB uplink — falls through to demote_gateway on every call,
    # and node-manager's gatewayReconcile() calls reconcile() unconditionally
    # on its own ~60-75s timer forever, restarting radvd/mesh-manager/
    # gateway-manager for no reason each time.
    if [ -z "$current" ] && [ ! -f "$LEGACY_GATEWAY_STATE" ]; then
        # The boot ruleset (/etc/nftables.conf) also forwards br0 to end0.
        # Reset a node that was never a gateway to the base firewall once
        # after boot, and again after an nftables reload brings that back.
        firewall_is_base || clear_firewall
        return 0
    fi

    demote_gateway "$current"
}

case "$EVENT" in
    carrier|routable|configured|online|add|reconcile|--hotplug)
        reconcile
        ;;
    promote)
        # ethernet-autodetect.sh found a lease with internet on IFACE.
        is_upstream_iface "$IFACE" || { log "promote: $IFACE is not an uplink interface"; exit 0; }
        promote_gateway "$IFACE" || true
        ;;
    release)
        # ethernet-autodetect.sh is taking IFACE for another role (wired EUD)
        # or the cable is gone: demote only if it was the gateway uplink.
        if [ -n "$IFACE" ] && [ "$IFACE" = "$(current_uplink_iface)" ]; then
            demote_gateway "$IFACE"
        fi
        ;;
    degraded)
        # A degraded event with carrier still present (a DHCP renewal blip,
        # a bridge port without an address) is not a loss of uplink.
        if [ -n "$IFACE" ] && has_carrier "$IFACE"; then
            reconcile
        elif [ -n "$IFACE" ] && [ "$IFACE" = "$(current_uplink_iface)" ]; then
            demote_gateway "$IFACE"
            reconcile
        else
            reconcile
        fi
        ;;
    off|no-carrier|remove|offline)
        current=$(current_uplink_iface)
        if [ -n "$IFACE" ] && [ "$IFACE" != "$current" ]; then
            log "$EVENT on $IFACE is not current uplink (${current:-none}); reconciling"
            reconcile
        else
            demote_gateway "${IFACE:-$current}"
            reconcile
        fi
        ;;
    *)
        log "Unknown event '$EVENT'; running reconcile"
        reconcile
        ;;
esac
