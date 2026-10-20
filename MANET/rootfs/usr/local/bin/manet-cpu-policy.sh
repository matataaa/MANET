#!/usr/bin/env bash
# ==============================================================================
# CPU policy: all cores online, demand-driven frequency scaling
# ==============================================================================
# Replaces the old cpu-powersave.service (powersave governor, 1.0 GHz cap,
# cpu2/cpu3 offline), which left mesh nodes at minimum clock on two cores for
# good: nothing ever stopped it. schedutil (or ondemand when the kernel lacks
# it) idles at low clock and still gives batman-adv, hostapd and the UI the
# full CPU when there is work.
#
# Frequency ceiling: the board's own maximum (cpuinfo_max_freq). To hold a
# node lower, e.g. a CM4 in a sealed enclosure, set CPU_MAX_FREQ_KHZ in a
# drop-in, which software updates leave alone:
#   /etc/systemd/system/manet-cpu-policy.service.d/override.conf
#     [Service]
#     Environment=CPU_MAX_FREQ_KHZ=800000
# The value is clamped to [cpuinfo_min_freq, cpuinfo_max_freq].
#
# Run by manet-cpu-policy.service at boot. Kernels without CPU hotplug or
# cpufreq controls are fine: those steps are skipped. A failed write is
# reported and the rest still applied; the exit status is non-zero if any
# step failed.

set -euo pipefail

SYS_CPU="${MANET_SYS_CPU:-/sys/devices/system/cpu}"
SYSTEMD_DIR="${MANET_SYSTEMD_DIR:-/etc/systemd/system}"
CPU_MAX_FREQ_KHZ="${CPU_MAX_FREQ_KHZ:-}"

failed=0
fail() {
    echo "ERROR: $*" >&2
    failed=1
}

# Print one positive integer read from a sysfs file, or fail.
read_khz() {
    local value
    value=$(cat "$1" 2>/dev/null) || return 1
    [[ "$value" =~ ^[1-9][0-9]*$ ]] || return 1
    echo "$value"
}

# Retire cpu-powersave.service on nodes provisioned before this unit existed.
# The software update ships cpu-powersave.service as a mask (-> /dev/null), so
# it no longer starts; drop the stale enable symlink radio-setup left behind.
rm -f "$SYSTEMD_DIR/multi-user.target.wants/cpu-powersave.service" \
    || fail "cannot remove stale cpu-powersave enable link"

# Bring every hot-pluggable core online. cpu0 has no online file on the Pi.
for online in "$SYS_CPU"/cpu[0-9]*/online; do
    [ -f "$online" ] || continue
    if [ "$(cat "$online" 2>/dev/null)" != "1" ]; then
        if echo 1 > "$online"; then
            echo "CPU policy: brought ${online%/online} online"
        else
            fail "cannot bring ${online%/online} online"
        fi
    fi
done

if [ -n "$CPU_MAX_FREQ_KHZ" ] && ! [[ "$CPU_MAX_FREQ_KHZ" =~ ^[1-9][0-9]*$ ]]; then
    fail "CPU_MAX_FREQ_KHZ='$CPU_MAX_FREQ_KHZ' is not a kHz value; using board maximum"
    CPU_MAX_FREQ_KHZ=""
fi

found=0
for policy in "$SYS_CPU"/cpufreq/policy*; do
    [ -d "$policy" ] || continue
    found=1
    name="${policy##*/}"

    # Governor first: the old unit's powersave would pin the clock at minimum
    # no matter what ceiling is set below.
    governors=" $(cat "$policy/scaling_available_governors" 2>/dev/null || true) "
    governor=""
    for g in schedutil ondemand; do
        if [[ "$governors" == *" $g "* ]]; then
            governor=$g
            break
        fi
    done
    if [ -n "$governor" ]; then
        echo "$governor" > "$policy/scaling_governor" \
            || fail "$name: cannot set governor $governor"
    else
        echo "CPU policy: $name offers neither schedutil nor ondemand; keeping kernel governor"
    fi

    if ! max=$(read_khz "$policy/cpuinfo_max_freq"); then
        fail "$name: cannot read cpuinfo_max_freq"
        continue
    fi
    cap=$max
    if [ -n "$CPU_MAX_FREQ_KHZ" ]; then
        cap=$CPU_MAX_FREQ_KHZ
        (( cap > max )) && cap=$max
        if min=$(read_khz "$policy/cpuinfo_min_freq"); then
            (( cap < min )) && cap=$min
        fi
    fi
    echo "$cap" > "$policy/scaling_max_freq" \
        || fail "$name: cannot set scaling_max_freq $cap"

    echo "CPU policy: $name governor=$(cat "$policy/scaling_governor" 2>/dev/null || echo unknown)" \
        "max=$(cat "$policy/scaling_max_freq" 2>/dev/null || echo unknown) kHz"
done
[ "$found" -eq 1 ] || echo "CPU policy: no cpufreq policies; frequency scaling left to the kernel"

exit "$failed"
