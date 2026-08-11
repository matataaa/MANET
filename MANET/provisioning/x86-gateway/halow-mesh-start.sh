#!/bin/bash
modprobe dot11ah
modprobe morse
sleep 3

IFACE=$(ip -br link | grep 0c:bf:74:00:28:d0 | awk "{print \$1}")
ip link set "$IFACE" down 2>/dev/null
ip link set "$IFACE" name wlan0 2>/dev/null
ip link set wlan0 up
ip link set wlan0 mtu 1432

mkdir -p /var/run/wpa_supplicant_s1g
wpa_supplicant_s1g -D nl80211 -i wlan0 -c /etc/wpa_supplicant/wpa_supplicant-s1g-wlan0.conf -B -f /tmp/wpa_debug.log -d

# Wait for at least one mesh peer to reach ESTAB before enslaving to bat0.
# Enslaving too early breaks the l2 socket wpa_supplicant uses to receive
# SAE auth frames, causing permanent peering failure until restart.
MAX_WAIT=60
elapsed=0
while [ $elapsed -lt $MAX_WAIT ]; do
    if iw dev wlan0 station dump 2>/dev/null | grep -q "mesh plink:.*ESTAB"; then
        break
    fi
    sleep 2
    elapsed=$((elapsed + 2))
done

modprobe batman-adv
ip link add bat0 type batadv ra BATMAN_V 2>/dev/null
batctl meshif bat0 if add wlan0
ip link set bat0 up
ip link set bat0 mtu 1400

ip link add br0 type bridge 2>/dev/null
ip link set bat0 master br0
ip link set br0 up
ip link set br0 mtu 1400

batctl meshif bat0 gw_mode server
touch /var/run/mesh-gateway.state
