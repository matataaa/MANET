#!/bin/bash
# morse-spi-watchdog.sh — monitors for morse_spi failures and recovers.
#
# The Morse MM6108 HaLow chip can enter a state where the SPI bus stops
# responding. The driver logs "morse_skbq_tx fail: -19" at runtime, or
# "morse_spi_probe: failed to init SPI with CMD63" at probe time.
#
# Root cause: GPIO17 (reset-gpios) gets stuck LOW after a failed probe,
# holding the chip in reset. Recovery requires:
#   1. Unload morse driver
#   2. Hold GPIO17=1 (release reset) via a daemonized gpioset
#   3. Reload SPI bus + morse driver
#
# Safety: max 2 reboots per hour if GPIO recovery keeps failing.

LOG_TAG="morse-spi-watchdog"
CHECK_INTERVAL=30
REBOOT_MAX=2
REBOOT_WINDOW=3600
STATE_FILE="/var/lib/morse-spi-watchdog-reboots"
GPIO_RECOVER_COUNT=0
MAX_GPIO_RECOVER=3

log() { logger -t $LOG_TAG "$*"; }

check_sick() {
    # Probe failure — wlan2 missing entirely
    if ! ip link show wlan2 >/dev/null 2>&1; then
        if dmesg 2>/dev/null | tail -100 | grep -q "morse_spi_probe.*failed"; then
            return 0
        fi
    fi
    # Runtime SPI failure
    local errors
    errors=$(dmesg 2>/dev/null | tail -200 | grep -c "morse_skbq_tx fail")
    [ "$errors" -ge 5 ] && return 0
    return 1
}

gpio_recover() {
    log "GPIO recovery attempt $((GPIO_RECOVER_COUNT + 1))/$MAX_GPIO_RECOVER"

    systemctl stop wpa_supplicant-s1g-wlan2.service 2>/dev/null
    systemctl stop batman-enslave.service 2>/dev/null
    ip link set wlan2 down 2>/dev/null
    sleep 1

    rmmod morse dot11ah 2>/dev/null
    sleep 1

    # Kill any prior gpioset daemon holding the lines
    pkill -f "gpioset.*-z.*17=1" 2>/dev/null
    sleep 1

    # Hold reset RELEASED (GPIO17=1) and power ON (GPIO3=1, GPIO7=1)
    # -z daemonizes so the GPIO stays driven even after this function returns
    gpioset -c 0 -z 17=1 3=1 7=1
    sleep 2

    # Reset SPI bus controller
    rmmod spi_bcm2835 2>/dev/null
    sleep 2
    modprobe spi_bcm2835
    sleep 3

    # Reload morse with chip out of reset
    modprobe morse
    sleep 8

    if ip link show wlan2 >/dev/null 2>&1; then
        log "GPIO recovery successful — wlan2 is back"
        systemctl start batman-enslave.service 2>/dev/null
        systemctl start wpa_supplicant-s1g-wlan2.service 2>/dev/null
        GPIO_RECOVER_COUNT=0
        return 0
    fi

    GPIO_RECOVER_COUNT=$((GPIO_RECOVER_COUNT + 1))
    log "GPIO recovery failed — wlan2 still missing"
    return 1
}

do_reboot() {
    local now
    now=$(date +%s)
    local recent=0
    if [ -f "$STATE_FILE" ]; then
        while IFS= read -r ts; do
            [ $((now - ts)) -lt "$REBOOT_WINDOW" ] && recent=$((recent + 1))
        done < "$STATE_FILE"
    fi

    if [ "$recent" -ge "$REBOOT_MAX" ]; then
        log "Already rebooted ${recent}x in the last hour — giving up"
        return 1
    fi

    log "GPIO recovery exhausted — rebooting ($((recent + 1))/$REBOOT_MAX)"
    echo "$now" >> "$STATE_FILE"
    sync
    reboot
}

log "Watchdog started (check every ${CHECK_INTERVAL}s, gpio_max=$MAX_GPIO_RECOVER)"

# Wait for system to settle
sleep 60

# Check immediately on startup in case probe already failed
if check_sick; then
    log "Detected failure at startup"
    gpio_recover || gpio_recover || gpio_recover || do_reboot || sleep 300
fi

while true; do
    sleep $CHECK_INTERVAL

    if check_sick; then
        log "Detected SPI/probe failure"
        if [ "$GPIO_RECOVER_COUNT" -lt "$MAX_GPIO_RECOVER" ]; then
            gpio_recover && continue
        fi
        do_reboot || sleep 300
        GPIO_RECOVER_COUNT=0
    fi
done
