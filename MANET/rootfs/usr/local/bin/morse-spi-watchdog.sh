#!/bin/bash
# morse-spi-watchdog.sh — monitors dmesg for morse_spi TX failures.
# When the HaLow radio chip stops responding on SPI, the driver logs
# "morse_skbq_tx fail: -19" (ENODEV). If we see repeated failures,
# the radio is dead and won't recover without a reboot.
#
# Safety: max 2 reboots per hour. After that, stop rebooting and just log —
# persistent SPI failure means a hardware problem (loose cable, dead chip).

LOG_TAG="morse-spi-watchdog"
CHECK_INTERVAL=30
FAIL_THRESHOLD=5
REBOOT_MAX=2
REBOOT_WINDOW=3600
STATE_FILE="/run/morse-spi-watchdog-reboots"

logger -t $LOG_TAG "Watchdog started (threshold=${FAIL_THRESHOLD}/${CHECK_INTERVAL}s, max ${REBOOT_MAX} reboots/${REBOOT_WINDOW}s)"

# Wait for system to settle after boot
sleep 90

while true; do
    sleep $CHECK_INTERVAL

    errors=$(dmesg 2>/dev/null | tail -200 | grep -c "morse_skbq_tx fail")

    if [ "$errors" -ge "$FAIL_THRESHOLD" ]; then
        now=$(date +%s)

        # Read recent reboot timestamps, prune old ones
        recent=0
        if [ -f "$STATE_FILE" ]; then
            while IFS= read -r ts; do
                if [ $((now - ts)) -lt "$REBOOT_WINDOW" ]; then
                    recent=$((recent + 1))
                fi
            done < "$STATE_FILE"
        fi

        if [ "$recent" -ge "$REBOOT_MAX" ]; then
            logger -t $LOG_TAG "SPI errors ($errors) but already rebooted ${recent}x in the last hour — hardware fault, not rebooting"
            sleep 300
            continue
        fi

        logger -t $LOG_TAG "Detected $errors SPI TX failures — radio chip unresponsive, rebooting ($((recent+1))/$REBOOT_MAX)"
        echo "$now" >> "$STATE_FILE"
        sync
        reboot
    fi
done
