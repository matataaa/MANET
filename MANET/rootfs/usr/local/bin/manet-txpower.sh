#!/bin/bash
# manet-txpower.sh — request 30 dBm on every mesh radio, Wi-Fi and HaLow.
#
# MANET sets no power ceiling of its own on mesh radios: each is asked for
# 30 dBm and the firmware, board calibration and regulatory table apply their
# own limits (the SPI morse tx_max_power_mbm cap is radio-setup.sh's). The
# value read back is what the driver reports, not a measurement of RF output.
#
# The request goes on the interface's PHY: mt76 (mt7915e/MT7916) and morse
# both accept a per-netdev txpower request and ignore it. A PHY setting
# changes every interface on that radio, so the EUD AP keeps the low power
# ap-txpower.service gives it, and a PHY that also serves another UP
# interface is left alone.
set -u

SYS_NET=/sys/class/net
STATE=/var/lib
REQUEST_MBM=3000
AP_IFACE=$(head -1 "$STATE/ap_interface" 2>/dev/null | tr -d '\r')

log() {
    printf '[%(%Y-%m-%d %H:%M:%S)T] - TXPOWER: %s\n' -1 "$*" >&2
}

set_power() {
    local iface=$1 phy other flags
    [ -e "$SYS_NET/$iface" ] || { log "$iface: not present"; return 0; }
    if [ "$iface" = "$AP_IFACE" ]; then
        log "$iface is the AP interface; keeping its AP power"
        return 0
    fi
    phy=$(cat "$SYS_NET/$iface/phy80211/name" 2>/dev/null)
    if [[ ! "$phy" =~ ^phy[0-9]+$ ]]; then
        log "$iface: cannot identify radio PHY; not changing its power"
        return 0
    fi
    for other in "$SYS_NET"/*; do
        other=${other##*/}
        [ "$other" = "$iface" ] && continue
        [ "$(cat "$SYS_NET/$other/phy80211/name" 2>/dev/null)" = "$phy" ] || continue
        flags=$(cat "$SYS_NET/$other/flags" 2>/dev/null)
        # Unreadable flags count as UP.
        if [[ ! "$flags" =~ ^0x[0-9a-fA-F]+$ ]] || (( flags & 1 )); then
            log "$iface: $phy also serves $other; not changing its power"
            return 0
        fi
    done
    if timeout 10 /usr/sbin/iw phy "$phy" set txpower fixed "$REQUEST_MBM"; then
        sleep 1
        log "$iface ($phy): requested $((REQUEST_MBM / 100)) dBm, reports $(/usr/sbin/iw dev "$iface" info 2>/dev/null | sed -n 's/^[[:space:]]*txpower \([0-9.]*\) dBm.*/\1/p') dBm"
    else
        log "$iface ($phy): power request refused"
    fi
}

# Older radio-setup.sh generated a halow-txpower-<iface> unit per node that
# requested a fixed power with `iw dev` (ignored by morse). Updates do not
# re-run radio-setup.sh, so retire any left on the node here.
for old_unit in /etc/systemd/system/halow-txpower-wlan*.service; do
    [ -f "$old_unit" ] || continue
    systemctl disable "${old_unit##*/}" 2>/dev/null || true
    rm -f "$old_unit" && log "removed legacy ${old_unit##*/}"
done

seen=" "
for role in mesh_if halow_if; do
    # A role file lists its interfaces space-separated on one line.
    # shellcheck disable=SC2013
    for iface in $(cat "$STATE/$role" 2>/dev/null); do
        [[ "$iface" =~ ^[A-Za-z0-9_.-]{1,15}$ ]] || { log "invalid interface name in $role"; continue; }
        [[ "$seen" == *" $iface "* ]] && continue
        seen+="$iface "
        set_power "$iface"
    done
done
exit 0
