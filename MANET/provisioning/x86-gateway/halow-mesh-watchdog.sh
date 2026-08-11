#!/bin/bash
# Watchdog for HaLow mesh on the gateway.
# Checks batman neighbors every 60s. If zero neighbors persist for
# 3 consecutive checks (3 minutes), triggers a clean mesh restart.

LOG_TAG="halow-watchdog"
FAIL_COUNT=0
FAIL_THRESHOLD=3
CHECK_INTERVAL=60

logger -t $LOG_TAG "Watchdog started (threshold=${FAIL_THRESHOLD}, interval=${CHECK_INTERVAL}s)"

while true; do
    sleep $CHECK_INTERVAL

    if ! pgrep -x wpa_supplicant_s1g >/dev/null 2>&1; then
        logger -t $LOG_TAG "wpa_supplicant not running, restarting mesh"
        /usr/local/bin/halow-mesh-restart.sh
        FAIL_COUNT=0
        continue
    fi

    neighbors=$(batctl bat0 n 2>/dev/null | grep -c wlan0)
    if [ "$neighbors" -eq 0 ]; then
        FAIL_COUNT=$((FAIL_COUNT + 1))
        logger -t $LOG_TAG "No neighbors (strike $FAIL_COUNT/$FAIL_THRESHOLD)"
        if [ $FAIL_COUNT -ge $FAIL_THRESHOLD ]; then
            logger -t $LOG_TAG "Threshold reached, restarting mesh"
            /usr/local/bin/halow-mesh-restart.sh
            FAIL_COUNT=0
        fi
    else
        if [ $FAIL_COUNT -gt 0 ]; then
            logger -t $LOG_TAG "Neighbors recovered ($neighbors), resetting count"
        fi
        FAIL_COUNT=0
    fi
done
