#!/bin/bash
# Live fixup for an already-provisioned node whose eud= just changed away
# from wireless/auto (see the eud apply-block in manet-ctrl's api.go).
#
# radio-setup.sh's per-interface wpa_supplicant setup and its ap-txpower.service
# generation both only ever run once, at first-ever provisioning
# (radio-setup-run-once, which never re-fires) — using whichever interface
# was classified mesh/AP *at that time*. Editing eud= later correctly
# updates /var/lib/mesh_if (see radio-setup.sh's classification), but
# leaves two things stale for a radio that just stopped being the AP:
#   1. It never had a mesh wpa_supplicant config generated (it was AP then).
#   2. ap-txpower.service still targets it, holding its txpower fixed at a
#      low AP-appropriate value (radio-setup.sh bakes the interface name
#      literally into that unit's ExecStart line at generation time).
#
# This script is idempotent — safe to run any time, does nothing if
# everything already matches /var/lib/mesh_if and /var/lib/ap_interface.
set -u
export PATH="/usr/sbin:/sbin:/usr/bin:/bin:${PATH:-}"

MESH_IF_FILE=/var/lib/mesh_if
AP_IF_FILE=/var/lib/ap_interface
AP_TXPOWER_UNIT=/etc/systemd/system/ap-txpower.service

iface_phy() {
    local iface="$1"
    iw dev "$iface" info 2>/dev/null | awk '/wiphy/ {print "phy"$2; exit}'
}

phys_iface() {
    local logical="$1" phys
    phys=$(grep "^${logical}:" /var/lib/iface_map 2>/dev/null | cut -d: -f2)
    echo "${phys:-$logical}"
}

iface_mesh_freq() {
    local iface="$1" phyname
    phyname="$(iface_phy "$iface")"
    [[ -z "$phyname" ]] && return
    if iw phy "$phyname" info 2>/dev/null | grep -q "2412\.0 MHz"; then
        echo "2412"
    elif iw phy "$phyname" info 2>/dev/null | grep -q "5180\.0 MHz"; then
        echo "5180"
    fi
}

MESH_NAME=$(grep '^mesh_ssid=' /etc/mesh.conf 2>/dev/null | cut -d= -f2-)
KEY=$(grep '^mesh_key=' /etc/mesh.conf 2>/dev/null | cut -d= -f2-)
CFG80211_REGDOM=$(grep '^regulatory_domain=' /etc/mesh.conf 2>/dev/null | cut -d= -f2-)
CFG80211_REGDOM="${CFG80211_REGDOM:-US}"
AP_INTERFACE=""
[ -f "$AP_IF_FILE" ] && AP_INTERFACE=$(head -1 "$AP_IF_FILE" | tr -d '\r')

# --- 1. Generate a missing mesh wpa_supplicant config for any interface
#        /var/lib/mesh_if now claims but never got one written for.
[ -f "$MESH_IF_FILE" ] || exit 0
for WLAN in $(cat "$MESH_IF_FILE"); do
    [ -n "$AP_INTERFACE" ] && [ "$WLAN" = "$AP_INTERFACE" ] && continue
    [ -e "/etc/wpa_supplicant/wpa_supplicant-$WLAN.conf" ] && continue

    FREQ=$(iface_mesh_freq "$WLAN")
    if [ -z "$FREQ" ]; then
        echo "manet-wlan-reconcile: WARNING cannot determine band for $WLAN, skipping" >&2
        continue
    fi

    echo "manet-wlan-reconcile: generating mesh config for $WLAN (${FREQ} MHz)"

    cat <<-EOF > "/etc/wpa_supplicant/wpa_supplicant-$WLAN-lobby.conf"
	ctrl_interface=/var/run/wpa_supplicant
	country=$CFG80211_REGDOM
	update_config=1
	sae_pwe=0
	ap_scan=2
	network={
	    ssid="$MESH_NAME"
	    mode=5
	    frequency=${FREQ}
	    key_mgmt=SAE
	    sae_password="$KEY"
	    ieee80211w=2
	    mesh_fwding=0
	    group_rekey=0
	}
	EOF

    cat <<-EOF > "/etc/systemd/network/30-$WLAN.network"
	[Match]
	MACAddress=$(ip a | grep -A1 "$(phys_iface "$WLAN")" | awk '/ether/ {print $2}')

	[Network]

	[Link]
	RequiredForOnline=no
	MTUBytes=1432
	EOF

    cp "/etc/wpa_supplicant/wpa_supplicant-$WLAN-lobby.conf" "/etc/wpa_supplicant/wpa_supplicant-$WLAN.conf"
    systemctl enable --now "wpa_supplicant@$WLAN.service"
done

# --- 2. If ap-txpower.service is still holding a radio's txpower fixed
#        low for a radio that isn't the AP anymore, reset it and retire
#        the stale unit.
if [ -f "$AP_TXPOWER_UNIT" ]; then
    STALE_TARGET=$(grep -oP '(?<=/sys/class/net/)[a-zA-Z0-9]+(?=/phy80211/name)' "$AP_TXPOWER_UNIT" | head -1)
    if [ -n "$STALE_TARGET" ] && [ "$STALE_TARGET" != "$AP_INTERFACE" ]; then
        STALE_PHY=$(iface_phy "$STALE_TARGET")
        if [ -n "$STALE_PHY" ]; then
            echo "manet-wlan-reconcile: resetting txpower on $STALE_TARGET ($STALE_PHY), no longer the AP interface"
            iw phy "$STALE_PHY" set txpower auto
        fi
        systemctl disable --now ap-txpower.service 2>/dev/null || true
    fi
fi

exit 0
