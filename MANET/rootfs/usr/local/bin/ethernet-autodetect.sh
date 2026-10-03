#!/bin/bash
# ==============================================================================
# Ethernet Auto-Detection Script
# ==============================================================================
# Sole owner of the built-in Ethernet port's role. Decides, once per physical
# connection, what is on the other end of the cable:
#
#   gateway    a lease plus internet: handed to manet-uplink-dispatch.sh, which
#              owns gateway setup (NAT, batman gw_mode, radvd, NTP serving)
#   wired-eud  no lease and no sign of a network: one or more EUDs (e.g. a
#              team's devices on an unmanaged switch) or a silent cable.
#              Bridged into br0 so they get addresses from this node's dnsmasq
#   network    an attached LAN (router adverts, STP/LLDP/CDP, or a lease
#              without internet): left unbridged, no DHCP
#              served onto it. Its DHCP client keeps running, so a late lease
#              with internet is picked up by the dispatcher's routable hook.
#
# The role is recorded in $ROLE_FILE with the port's carrier_down_count.
# Bridging the port and networkd reconfiguring it produce fresh carrier
# events without the link going down, so a carrier event is only acted on
# when that counter has changed (a real unplug, re-plug or link flap).
#
# Other scripts read $ROLE_FILE and never change the port's role:
# manet-uplink-dispatch.sh skips ports bridged into br0 or marked wired-eud /
# detecting, and the networkd-dispatcher unplug hook calls --unplug here.
#
# wlan1/AP handling (when /var/lib/ap_interface is set):
#   - wired EUD in auto/wired mode: the AP radio returns to the mesh
#   - no cable in auto/wireless mode: the AP is (re)started
#
# Usage: ethernet-autodetect.sh [--hotplug] [--unplug] [--iface IF]
#                               [--mode wired-eud|network]
# ==============================================================================

# Full xtrace + tee-to-journal only when debugging: set -x sends every traced
# line to journald via the dispatcher, which is real load when events loop.
if [ -f /etc/eth-detect-debug ]; then
    exec > >(tee -a /var/log/ethernet-detect.log) 2>&1
    set -x
else
    exec >> /var/log/ethernet-detect.log 2>&1
fi

ROLE_FILE="/run/manet-eth-role"
LOCK_FILE="/run/manet-eth.lock"
NETWORKD_DIR="/etc/systemd/network"
DISPATCH="/usr/local/bin/manet-uplink-dispatch.sh"
UPLINK_SPEED="${MANET_UPLINK_SPEED:-/usr/local/bin/manet-uplink-speed.sh}"
ETH_STATE_FILE="/var/run/ethernet_detection_state"

# Passive capture taken while waiting for a lease. Bounded in time, packet
# count and snap length so a busy LAN can't grow it past ~60 KiB.
LAN_CAPTURE_FILE="/run/eth-detect.pcap"
LAN_CAPTURE_PID=""
LEASE_WAIT_SECS=20

log() {
    printf '[%(%Y-%m-%d %H:%M:%S)T] - ETH-DETECT: %s\n' -1 "$1" >&2
}

# systemctl unmask always triggers a full daemon-reload, and enable on units
# with sysv shims (dnsmasq, hostapd) spawns update-rc.d which reloads again.
# Guard them so repeated runs of this script don't churn PID 1.
unmask_if_masked() {
    [ "$(systemctl is-enabled "$1" 2>/dev/null)" = "masked" ] && \
        systemctl unmask "$1" 2>/dev/null
    return 0
}

enable_if_disabled() {
    systemctl is-enabled --quiet "$1" 2>/dev/null || \
        systemctl enable "$1" 2>/dev/null
    return 0
}

# Which port to manage: --iface, else end0, else the first USB Ethernet port.
resolve_eth_iface() {
    if [ -n "${FORCE_IFACE:-}" ]; then
        echo "$FORCE_IFACE"
        return
    fi
    if ip link show end0 &>/dev/null; then
        echo "end0"
        return
    fi
    local path iface bus
    for path in /sys/class/net/*; do
        iface=${path##*/}
        bus=$(readlink "$path/device/subsystem" 2>/dev/null | grep -o 'usb' || true)
        if [ "$bus" = "usb" ] && [[ "$iface" != wlan* ]] && [[ "$iface" != bat* ]] && [[ "$iface" != br* ]]; then
            echo "$iface"
            return
        fi
    done
    echo "end0"
}

has_carrier() {
    [ "$(cat "/sys/class/net/$ETH_IFACE/carrier" 2>/dev/null || echo 0)" = "1" ]
}

down_count() {
    cat "/sys/class/net/$ETH_IFACE/carrier_down_count" 2>/dev/null || echo na
}

eud_mode() {
    local mode
    mode=$(grep "^eud=" /etc/mesh.conf 2>/dev/null | cut -d'=' -f2)
    case "$mode" in
        wireless|wired|auto|none) echo "$mode" ;;
        *) echo auto ;;
    esac
}

ap_interface() {
    cat /var/lib/ap_interface 2>/dev/null || true
}

# --- role record --------------------------------------------------------------

write_role() {
    cat > "$ROLE_FILE.tmp" <<EOF
IFACE=$ETH_IFACE
ROLE=$1
DOWN_COUNT=$(down_count)
SINCE=$(date +%s)
EOF
    mv "$ROLE_FILE.tmp" "$ROLE_FILE"
}

# The role recorded for this port during the current physical connection, if
# any. Empty when the link has gone down since (or nothing was recorded).
current_connection_role() {
    [ -f "$ROLE_FILE" ] || return 0
    local IFACE="" ROLE="" DOWN_COUNT=""
    # shellcheck disable=SC1090
    . "$ROLE_FILE"
    if [ "$IFACE" = "$ETH_IFACE" ] && [ "$DOWN_COUNT" = "$(down_count)" ] && [ "$DOWN_COUNT" != na ]; then
        echo "$ROLE"
    fi
}

# --- networkd -------------------------------------------------------------------

WIRED_EUD_CONFIG=""

# networkd applies the first matching .network file and, on every
# reconfigure, resets the master of a link whose file has no Bridge=. The
# node's 10-<iface>.network is a DHCP client, so a port bridged only with
# `ip link set master` would be detached again on the next reconfigure. This
# file sorts first, so while it exists networkd itself keeps the port in br0.
write_wired_eud_config() {
    cat > "$WIRED_EUD_CONFIG" <<EOF
[Match]
Name=$ETH_IFACE

[Link]
RequiredForOnline=no

[Network]
Bridge=br0
DHCP=no
LinkLocalAddressing=no
IPv6AcceptRA=no
EOF
    networkctl reload 2>/dev/null || true
}

remove_wired_eud_config() {
    [ -f "$WIRED_EUD_CONFIG" ] || return 0
    rm -f "$WIRED_EUD_CONFIG"
    networkctl reload 2>/dev/null || true
}

# --- AP radio -------------------------------------------------------------------

# Return the AP candidate radio to the mesh (auto/wired mode with a wired EUD).
ap_to_mesh() {
    local ap
    ap=$(ap_interface)
    [ -n "$ap" ] || return 0
    batctl if | grep -q "$ap" && return 0

    # AP-only hardware (e.g. an onboard Wi-Fi radio with no mesh supplicant
    # config) never joins the mesh: just stop the AP.
    if [ ! -f "/etc/wpa_supplicant/wpa_supplicant-$ap.conf" ]; then
        log "$ap is AP-only (no mesh config); stopping the AP"
        systemctl stop hostapd.service ap-txpower.service 2>/dev/null
        systemctl disable hostapd.service 2>/dev/null
        ip link set "$ap" nomaster 2>/dev/null || true
        return 0
    fi

    log "Returning $ap from AP to mesh"
    systemctl stop hostapd.service ap-txpower.service 2>/dev/null
    systemctl disable hostapd.service 2>/dev/null
    ip link set "$ap" down
    ip link set "$ap" nomaster 2>/dev/null || true
    iw dev "$ap" set type mesh
    ip link set "$ap" up
    systemctl restart "wpa_supplicant@$ap.service" 2>/dev/null
    sleep 2
    if batctl if add "$ap" 2>/dev/null; then
        log "$ap added to bat0"
    else
        log "Failed to add $ap to bat0"
    fi
    # The AP radio changed role, so mesh-manager must rebuild its ebtables
    # and dnsmasq view. Only on this transition: a plain wired EUD needs no
    # service restarts (dnsmasq already serves br0).
    systemctl restart mesh-manager 2>/dev/null || true
}

# Make sure the AP serves EUDs (auto/wireless mode without a wired EUD).
ap_serve() {
    local ap
    ap=$(ap_interface)
    [ -n "$ap" ] || return 0

    if batctl if | grep -q "$ap"; then
        log "Removing $ap from bat0 (it serves the AP)"
        batctl if del "$ap" 2>/dev/null || true
    fi
    unmask_if_masked dnsmasq.service
    enable_if_disabled hostapd.service
    systemctl start hostapd.service 2>/dev/null
    enable_if_disabled dnsmasq.service
    systemctl start dnsmasq.service 2>/dev/null
    systemctl start ap-txpower.service 2>/dev/null
    if ! ip link show "$ap" | grep -q "master br0"; then
        ip link set "$ap" master br0
        ip link set "$ap" up
    fi
}

# --- LAN capture (attached network vs. single EUD) ---------------------------

# Record inbound frames on the port while DHCP runs. -Q in keeps our own
# DHCP requests out of the source-MAC count.
start_lan_capture() {
    rm -f "$LAN_CAPTURE_FILE"
    command -v tcpdump >/dev/null 2>&1 || return 0
    timeout 30 tcpdump -i "$ETH_IFACE" -Q in -n -U -s 256 -c 200 \
        -w "$LAN_CAPTURE_FILE" >/dev/null 2>&1 200>&- &
    LAN_CAPTURE_PID=$!
}

stop_lan_capture() {
    [ -n "$LAN_CAPTURE_PID" ] || return 0
    kill "$LAN_CAPTURE_PID" 2>/dev/null || true
    wait "$LAN_CAPTURE_PID" 2>/dev/null || true
    LAN_CAPTURE_PID=""
}

# Classify what the far end sent while no lease arrived. Prints "network"
# when the port is attached to a LAN, "eud" for EUDs or a silent cable, or
# "unknown" when there is no capture.
#
# Router advertisements or switch control frames (STP, LLDP, CDP) mean a
# network is attached; bridging it would extend the mesh into that LAN and
# put our dnsmasq on it as a second DHCP server. The number of devices is
# deliberately not a signal: with no lease and none of those frames, several
# devices are far more likely a team's EUDs on an unmanaged switch than a
# foreign network (and one EUD can use several MACs, e.g. VMs). A foreign
# static-IP LAN with no router or managed switch is the remaining miss; use
# --mode network for that.
lan_capture_verdict() {
    # tcpdump -w writes the pcap header on start, so an empty or missing file
    # means it never ran; a header-only file is a silent cable.
    if [ ! -s "$LAN_CAPTURE_FILE" ]; then
        echo unknown
        return 0
    fi

    local control_frames sources
    control_frames=$(tcpdump -r "$LAN_CAPTURE_FILE" -n \
        'ether dst 01:80:c2:00:00:00 or ether dst 01:80:c2:00:00:0e or ether proto 0x88cc or ether dst 01:00:0c:cc:cc:cc or (icmp6 and ip6[40] == 134)' \
        2>/dev/null | wc -l)
    sources=$(tcpdump -r "$LAN_CAPTURE_FILE" -n -e 2>/dev/null \
        | awk '{print $2}' | sort -u | wc -l)

    log "Capture on $ETH_IFACE: $control_frames router/switch control frame(s), $sources source MAC(s)"
    if [ "$control_frames" -gt 0 ]; then
        echo network
    else
        echo eud
    fi
}

wait_for_ip() {
    local ip
    for _ in $(seq 1 "$LEASE_WAIT_SECS"); do
        ip=$(ip -4 addr show dev "$ETH_IFACE" | grep -oP 'inet \K[\d.]+' | head -1)
        if [ -n "$ip" ]; then
            echo "$ip"
            return 0
        fi
        sleep 1
    done
    return 1
}

# --- roles ----------------------------------------------------------------------

apply_wired_eud() {
    log "Configuring $ETH_IFACE as wired EUD (bridged into br0)"
    "$DISPATCH" release "$ETH_IFACE"
    rm -f "$NETWORKD_DIR/20-$ETH_IFACE.network"
    ip addr flush dev "$ETH_IFACE" 2>/dev/null || true
    write_wired_eud_config
    ip link set "$ETH_IFACE" master br0 2>/dev/null || true
    ip link set "$ETH_IFACE" up
    write_role wired-eud

    case "$(eud_mode)" in
        auto|wired) ap_to_mesh ;;
    esac
    unmask_if_masked dnsmasq.service
    systemctl start dnsmasq.service 2>/dev/null || true

    cat > "$ETH_STATE_FILE" <<EOF
ETH_MODE=WIRED_EUD
ETH_BRIDGE=br0
DETECTED_AT=$(date +%s)
DETECTION_METHOD=CARRIER_NO_DHCP
EOF
    log "Wired EUD configuration complete"
}

# Attached network: unbridged, no DHCP served onto it; its DHCP client keeps
# running so a late lease reaches the dispatcher through the routable hook.
apply_network() {
    log "$1 - leaving $ETH_IFACE unbridged with no DHCP service"
    log "To bridge it as a wired EUD anyway, run: ethernet-autodetect.sh --mode wired-eud"
    write_role network
    cat > "$ETH_STATE_FILE" <<EOF
ETH_MODE=NETWORK
DETECTED_AT=$(date +%s)
EOF
}

apply_gateway() {
    log "Lease and internet on $ETH_IFACE - handing it to the uplink dispatcher as gateway"
    write_role gateway
    "$DISPATCH" promote "$ETH_IFACE"
}

detect_and_apply() {
    log "Carrier present on $ETH_IFACE - detecting role"
    write_role detecting

    remove_wired_eud_config
    ip link set "$ETH_IFACE" nomaster 2>/dev/null || true
    ip addr flush dev "$ETH_IFACE" 2>/dev/null || true

    start_lan_capture
    networkctl reconfigure "$ETH_IFACE" 2>/dev/null || true
    local ip
    ip=$(wait_for_ip || true)
    stop_lan_capture

    if [ -n "$ip" ]; then
        rm -f "$LAN_CAPTURE_FILE"
        log "IP acquired on $ETH_IFACE: $ip"
        # The speed test is the internet check that matters: a captive portal
        # or a filtered network passes ping but cannot complete it, and such a
        # port must not become a gateway. Its result is what the gateway
        # announces. Exit 2 (not an Ethernet port) means not tested. A later
        # pass of the dispatcher's reconcile retries a failure.
        local speed_rc=1
        if ping -c 3 -W 2 -I "$ETH_IFACE" 8.8.8.8 >/dev/null 2>&1; then
            speed_rc=0
            "$UPLINK_SPEED" measure "$ETH_IFACE" >/dev/null || speed_rc=$?
        fi
        if [ "$speed_rc" -eq 0 ] || [ "$speed_rc" -eq 2 ]; then
            apply_gateway
        else
            apply_network "Lease but no internet (or the speed test failed)"
        fi
        return
    fi

    local verdict
    verdict=$(lan_capture_verdict)
    rm -f "$LAN_CAPTURE_FILE"
    case "$verdict" in
        network)
            apply_network "No lease, but $ETH_IFACE is attached to a network"
            ;;
        unknown)
            log "WARN: no capture available (tcpdump missing or failed); treating no lease as a wired EUD"
            apply_wired_eud
            ;;
        *)
            apply_wired_eud
            ;;
    esac
}

# Cable gone (or the link went down): return the port to its baseline.
unplug() {
    if has_carrier; then
        log "Unplug event for $ETH_IFACE but carrier is back - ignoring stale event"
        return 0
    fi
    log "No carrier on $ETH_IFACE - returning it to baseline"
    rm -f "$ROLE_FILE" "$ETH_STATE_FILE"
    remove_wired_eud_config
    rm -f "$NETWORKD_DIR/20-$ETH_IFACE.network"
    # Restore the provisioned DHCP client config if something replaced it.
    local default="$NETWORKD_DIR/10-$ETH_IFACE.network.dhcp-default"
    if [ -f "$default" ] && ! cmp -s "$default" "$NETWORKD_DIR/10-$ETH_IFACE.network"; then
        cp "$default" "$NETWORKD_DIR/10-$ETH_IFACE.network"
        networkctl reload 2>/dev/null || true
    fi
    ip link set "$ETH_IFACE" nomaster 2>/dev/null || true
    ip addr flush dev "$ETH_IFACE" 2>/dev/null || true
    "$DISPATCH" release "$ETH_IFACE"

    case "$(eud_mode)" in
        auto|wireless) ap_serve ;;
    esac
}

# ==============================================================================

ACTION=hotplug
FORCED_ROLE=""
FORCE_IFACE=""
while [ $# -gt 0 ]; do
    case "$1" in
        --iface) FORCE_IFACE="$2"; shift ;;
        --mode) FORCED_ROLE="$2"; shift ;;
        --unplug) ACTION=unplug ;;
        --hotplug) ACTION=hotplug ;;
    esac
    shift
done

ETH_IFACE=$(resolve_eth_iface)
WIRED_EUD_CONFIG="$NETWORKD_DIR/05-$ETH_IFACE-wired-eud.network"

if ! ip link show "$ETH_IFACE" &>/dev/null; then
    log "Interface $ETH_IFACE not found"
    exit 1
fi

# One policy decision at a time. Events queued behind a running detection
# wait (bounded) and then re-check the connection state themselves.
exec 200>"$LOCK_FILE"
if ! flock -w 90 200; then
    log "Timed out waiting for another detection on $ETH_IFACE; giving up"
    exit 0
fi

if [ "$ACTION" = unplug ] || ! has_carrier; then
    unplug
    exit 0
fi

if [ -n "$FORCED_ROLE" ]; then
    log "Called with mode: $FORCED_ROLE"
    case "$FORCED_ROLE" in
        wired-eud) apply_wired_eud ;;
        network) apply_network "Forced" ;;
        *) log "ERROR: unknown mode $FORCED_ROLE (use wired-eud or network)"; exit 1 ;;
    esac
    exit 0
fi

role=$(current_connection_role)
if [ -n "$role" ] && [ "$role" != detecting ]; then
    log "Role '$role' is current for this connection on $ETH_IFACE; skipping re-detection"
    exit 0
fi

detect_and_apply
exit 0
