#!/bin/bash
# Cleanly restart HaLow mesh peering on the gateway.
# The l2 socket that wpa_supplicant uses breaks if wlan0 is enslaved
# to bat0 during startup. This script detaches first, waits for SAE
# peering to establish, then re-enslaves.

LOG_TAG="halow-mesh-restart"
WPA_CONF=/etc/wpa_supplicant/wpa_supplicant-s1g-wlan0.conf
WPA_LOG=/tmp/wpa_debug.log
WPA_CTRL=/var/run/wpa_supplicant_s1g
MAX_WAIT=60

logger -t $LOG_TAG "Starting mesh restart"

# 1. Detach from bat0
batctl meshif bat0 if del wlan0 2>/dev/null
sleep 1

# 2. Kill old wpa_supplicant
killall wpa_supplicant_s1g 2>/dev/null
sleep 2

# 3. Start fresh
rm -f $WPA_LOG
wpa_supplicant_s1g -D nl80211 -i wlan0 -c $WPA_CONF -B -f $WPA_LOG -d
if [ $? -ne 0 ]; then
    logger -t $LOG_TAG "ERROR: wpa_supplicant failed to start"
    exit 1
fi

# 4. Wait for at least one peer to reach ESTAB
elapsed=0
while [ $elapsed -lt $MAX_WAIT ]; do
    if iw dev wlan0 station dump 2>/dev/null | grep -q "mesh plink:.*ESTAB"; then
        peer=$(iw dev wlan0 station dump 2>/dev/null | grep -B1 "mesh plink:.*ESTAB" | head -1 | awk '{print $2}')
        logger -t $LOG_TAG "Peer established: $peer (${elapsed}s)"
        break
    fi
    sleep 2
    elapsed=$((elapsed + 2))
done

if [ $elapsed -ge $MAX_WAIT ]; then
    logger -t $LOG_TAG "WARNING: No peer after ${MAX_WAIT}s, enslaving anyway"
fi

# 5. Re-enslave to bat0
batctl meshif bat0 if add wlan0
logger -t $LOG_TAG "wlan0 re-enslaved to bat0"
sleep 3

neighbors=$(batctl bat0 n 2>/dev/null | grep -c wlan0)
logger -t $LOG_TAG "Restart complete, $neighbors batman neighbor(s)"
