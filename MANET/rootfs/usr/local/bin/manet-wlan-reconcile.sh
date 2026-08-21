#!/bin/bash
# Live fixup for an already-provisioned node whose eud= just changed away
# from wireless/auto (see the eud apply-block in manet-ctrl's api.go).
#
# radio-setup.sh's classification (which interface is AP vs mesh) and its
# per-interface wpa_supplicant/ap-txpower.service generation all only ever
# run once, at first-ever provisioning (radio-setup-run-once, which never
# re-fires) — nothing else updates /var/lib/mesh_if or /var/lib/ap_interface
# in response to a later eud= edit. Confirmed live: setting eud=wired and
# rebooting is NOT enough on its own — /var/lib/ap_interface still names
# the original AP radio, /var/lib/mesh_if never gained it, and journalctl
# shows radio-setup.sh never re-ran that boot (only fires again if an
# unrelated interface-rename mismatch happens to trigger radio-setup-rerun).
# So a stopped-hostapd node can still be sitting on entirely stale role
# files, not just a stale wpa_supplicant/txpower config for a radio
# mesh_if already lists.
#
# This script closes both gaps and is idempotent — safe to run any time,
# does nothing once /var/lib/mesh_if, /var/lib/ap_interface, and every
# mesh interface's wpa_supplicant config/txpower all agree with the
# current eud=.
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
EUD=$(grep '^eud=' /etc/mesh.conf 2>/dev/null | cut -d= -f2-)
AP_INTERFACE=""
[ -f "$AP_IF_FILE" ] && AP_INTERFACE=$(head -1 "$AP_IF_FILE" | tr -d '\r')
CHANGED=0

# --- 0. If eud= no longer needs an AP radio but /var/lib/ap_interface
#        still names one, that radio was never actually reclassified —
#        move it into /var/lib/mesh_if and clear ap_interface, so step 1
#        below (and every other consumer of these files) sees it as mesh.
if { [ "$EUD" = "wired" ] || [ "$EUD" = "none" ]; } && [ -n "$AP_INTERFACE" ]; then
    echo "manet-wlan-reconcile: eud=$EUD, reclassifying former AP interface $AP_INTERFACE as mesh"
    if ! grep -qx "$AP_INTERFACE" "$MESH_IF_FILE" 2>/dev/null; then
        echo "$AP_INTERFACE" >> "$MESH_IF_FILE"
    fi
    : > "$AP_IF_FILE"
    AP_INTERFACE=""
    CHANGED=1
fi

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

    CHANGED=1
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

# --- 3. batman-enslave.service is ALSO a first-boot-only oneshot unit
#        (radio-setup.sh's generated ExecStart=/usr/local/bin/batman-if-setup.sh
#        start, WantedBy=multi-user.target, never re-fires) — a radio can end
#        up with a real working 802.11s mesh-point peer link (steps 1-2 above)
#        and still never actually carry mesh traffic, because batman-adv was
#        never told to add it to bat0. Confirmed live: wlan1 had an
#        established mesh plink on both sides while `batctl bat0 if` still
#        only listed the two radios enslaved at first boot.
#        batman-if-setup.sh's own start() is idempotent — it re-reads
#        /var/lib/mesh_if/halow_if fresh every call and only adds interfaces
#        not already enslaved — so it's safe to re-run any time something
#        here actually changed.
if [ "$CHANGED" -eq 1 ] && [ -x /usr/local/bin/batman-if-setup.sh ]; then
    echo "manet-wlan-reconcile: re-running batman-if-setup.sh to enslave newly-configured interfaces"
    /usr/local/bin/batman-if-setup.sh start
fi

exit 0
