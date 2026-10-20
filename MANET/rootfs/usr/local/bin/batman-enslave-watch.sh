#!/bin/bash
# Watchdog: re-enslaves HaLow (and standard mesh) interfaces into bat0 if they fall out.
# Runs continuously after batman-enslave.service. Safe to restart.

log() { printf '[%(%Y-%m-%d %H:%M:%S)T] ENSLAVE-WATCH: %s\n' -1 "$*" >&2; }

radio_iface_enabled() {
    python3 - "$1" <<'PY'
import json, sys
iface = sys.argv[1]
try:
    with open('/var/lib/mesh_radio_state.json') as f:
        state = json.load(f).get('desired', {}).get(iface, 'up')
except Exception:
    state = 'up'
sys.exit(1 if state == 'down' else 0)
PY
}

batman_mainif() {
    batctl bat0 o 2>/dev/null | sed -n '1s/.*MainIF\/MAC: \([^/]*\)\/.*/\1/p'
}

restore_halow_primary_if_needed() {
    local halow mainif now last cooldown_file

    halow="$(echo "$HALOW_IFS" | awk 'NF {print $1; exit}')"
    [ -n "$halow" ] || return 0
    radio_iface_enabled "$halow" || return 0
    ip link show "$halow" >/dev/null 2>&1 || return 0
    batctl bat0 if 2>/dev/null | grep -q "^${halow}:" || return 0

    mainif="$(batman_mainif)"
    [ -n "$mainif" ] || return 0
    [ "$mainif" = "$halow" ] && return 0

    cooldown_file="/run/batman-enslave-watch-halow-primary-reset"
    now="$(date +%s)"
    last="$(cat "$cooldown_file" 2>/dev/null || echo 0)"
    if [ $((now - last)) -lt 60 ]; then
        return 0
    fi
    echo "$now" > "$cooldown_file"

    log "WARNING: bat0 MainIF is $mainif, expected HaLow $halow; rebuilding bat0 membership"
    /usr/local/bin/batman-if-setup.sh stop 2>/dev/null || true
    sleep 2
    /usr/local/bin/batman-if-setup.sh start 2>/dev/null || true
}

# The S1G supplicant can stay active after its mesh is gone: something downed
# the netdev or the driver disabled the BSS. sae-watchdog only reacts to SAE
# blocks and morse-spi-watchdog only to SPI failures (it stops the supplicant
# while it recovers, so nothing is counted then). Ported from upstream
# very-srs/MANET a5d3237.
HALOW_STRIKES_FILE="/run/batman-enslave-watch-halow-strikes"
HALOW_RESTART_FILE="/run/batman-enslave-watch-halow-restart"

uptime_now() { cut -d. -f1 /proc/uptime; }

# Joined means the netdev is up and mesh_param is readable, which it only is
# while the interface is in a mesh.
halow_is_joined() {
    local flags
    flags=$(cat "/sys/class/net/$1/flags" 2>/dev/null) || return 1
    [[ "$flags" =~ ^0x[0-9a-fA-F]+$ ]] && (( flags & 1 )) || return 1
    timeout 3 iw dev "$1" get mesh_param mesh_plink_timeout >/dev/null 2>&1
}

# Count consecutive passes where an active S1G supplicant has no joined mesh.
note_halow_health() {
    local halow strikes
    halow="$(echo "$HALOW_IFS" | awk 'NF {print $1; exit}')"
    if [ -z "$halow" ] || ! radio_iface_enabled "$halow" || [ ! -e "/sys/class/net/$halow" ] ||
            ! systemctl is-active --quiet "wpa_supplicant-s1g-$halow.service"; then
        rm -f "$HALOW_STRIKES_FILE"
        return 0
    fi
    if halow_is_joined "$halow"; then
        rm -f "$HALOW_STRIKES_FILE" "$HALOW_RESTART_FILE"
        return 0
    fi
    strikes=""
    read -r strikes _ 2>/dev/null < "$HALOW_STRIKES_FILE"
    [[ "$strikes" =~ ^[0-9]+$ ]] || strikes=0
    printf '%s %s\n' "$((strikes + 1))" "$halow" > "$HALOW_STRIKES_FILE"
}

# Restart the S1G supplicant after four unjoined passes (about 32 s), which
# outlasts its own start and a HaLow channel change. Repeat restarts back off
# from 60 s to 16 min, so a wedged USB card is not restarted every pass.
restart_dead_halow_if_needed() {
    local strikes halow now last interval
    read -r strikes halow 2>/dev/null < "$HALOW_STRIKES_FILE" || return 0
    [[ "$strikes" =~ ^[0-9]+$ ]] && [ "$strikes" -ge 4 ] || return 0
    [[ "$halow" =~ ^[A-Za-z0-9_.-]{1,15}$ ]] || return 0
    now="$(uptime_now)" || return 0
    last=""; interval=""
    read -r last interval 2>/dev/null < "$HALOW_RESTART_FILE"
    # Boot-clock seconds. Missing, malformed or future values disable cooldown.
    [[ "$last" =~ ^[0-9]+$ ]] && [ "$last" -le "$now" ] || last=-1000000
    [[ "$interval" =~ ^[0-9]+$ ]] || interval=30
    [ $((now - last)) -ge "$interval" ] || return 0
    interval=$((interval * 2))
    [ "$interval" -le 960 ] || interval=960
    printf '%s %s\n' "$now" "$interval" > "$HALOW_RESTART_FILE"
    rm -f "$HALOW_STRIKES_FILE"
    log "WARNING: wpa_supplicant-s1g-$halow is active but $halow has no joined mesh; restarting it"
    systemctl restart "wpa_supplicant-s1g-$halow.service" 2>/dev/null || true
}

while true; do
    sleep 8

    HALOW_IFS="$(cat /var/lib/halow_if 2>/dev/null)"
    MESH_IFS="$(cat /var/lib/mesh_if 2>/dev/null)"

    # Enforce mesh_plink_timeout=0 on HaLow interfaces. The inactivity-based
    # plink close triggers a Morse firmware stuck state during key teardown
    # (morse_cmd_disable_key ENODEV) that only a hard reboot recovers from.
    # The param resets to default whenever wpa_supplicant_s1g re-forms the
    # mesh, so enforce it here rather than once at boot. The get/set only
    # succeeds after the mesh is joined; failures are silently retried.
    for IFACE in $HALOW_IFS; do
        radio_iface_enabled "$IFACE" || continue
        CUR="$(iw dev "$IFACE" get mesh_param mesh_plink_timeout 2>/dev/null)"
        if [ -n "$CUR" ] && [ "${CUR%% *}" != "0" ]; then
            if iw dev "$IFACE" set mesh_param mesh_plink_timeout 0 2>/dev/null; then
                log "Set mesh_plink_timeout=0 on $IFACE (was: $CUR)"
            fi
        fi
    done

    for IFACE in $HALOW_IFS $MESH_IFS; do
        # Skip if radio-state says down
        radio_iface_enabled "$IFACE" || continue

        # Skip if interface doesn't exist
        ip link show "$IFACE" >/dev/null 2>&1 || continue

        # Check if already in bat0
        if batctl bat0 if 2>/dev/null | grep -q "^${IFACE}:"; then
            continue
        fi

        # Interface should be in bat0 but isn't — re-enslave
        log "WARNING: $IFACE not in bat0, re-enslaving..."
        ip link set "$IFACE" up 2>/dev/null || true
        ip link set "$IFACE" mtu 1532 2>/dev/null || true

        for attempt in 1 2 3; do
            if batctl bat0 if add "$IFACE" 2>/dev/null; then
                sleep 1
                if batctl bat0 if 2>/dev/null | grep -q "^${IFACE}:"; then
                    log "OK: $IFACE re-enslaved to bat0 (attempt $attempt)"
                    break
                fi
            fi
            sleep 2
        done
    done

    restore_halow_primary_if_needed
    note_halow_health
    restart_dead_halow_if_needed
done
