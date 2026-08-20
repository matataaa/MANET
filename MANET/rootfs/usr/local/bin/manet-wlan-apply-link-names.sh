#!/bin/bash
# Apply /etc/systemd/network/10-wlan*.link MAC→Name mapping at runtime.
# systemd .link Name= can fail when two interfaces need to swap names; this
# script performs swap-safe ip(8) renames and rewrites MANET role files by MAC.
set -u
export PATH="/usr/sbin:/sbin:/usr/bin:/bin:${PATH:-}"

NETDIR=/etc/systemd/network
declare -A mac_to_target=()
declare -A pre_rename_mac=()

# Role files (/var/lib/mesh_if etc.) hold *raw*, pre-pinning interface names
# only immediately after radio-setup.sh writes fresh .link files — that one
# call sets this to translate them via the pre_rename_mac snapshot below. On
# every other invocation (the manet-wlan-apply-link-names.service unit, which
# runs on every subsequent boot), the role files already hold stable target
# names from a previous successful translation, and by the time this script's
# remap step runs, apply_renames() has already converged every interface to
# its correct target name — so those names are trustworthy as-is and must
# NOT be re-translated. Raw names and target names share the same string
# pool (wlan0/wlan1/wlan2), so this can't be told apart from file content
# alone; it has to be an explicit signal from the caller. Getting this wrong
# is exactly the bug this flag fixes: re-translating an already-stable
# "wlan1" (the AP radio) through *this boot's* raw pre-rename snapshot can
# resolve it to whatever device that raw name happened to belong to before a
# multi-way rotation, silently reassigning it to the wrong radio. Confirmed
# live: a 3-way wlan0/wlan1/wlan2 rotation between two MT7916 radios and a
# USB HaLow dongle turned a correct ap_interface=wlan1 into wlan0, crashing
# hostapd ("Match already configured") for days until caught.
RAW_ROLE_FILES="${MANET_WLAN_APPLY_RAW_ROLE_FILES:-0}"

read_mac() {
    tr '[:upper:]' '[:lower:]' <"/sys/class/net/$1/address" 2>/dev/null
}

iface_for_mac() {
    local m target="$1" d mac
    m="${target,,}"
    for d in /sys/class/net/wlan[0-9]*; do
        [[ -d "$d" ]] || continue
        mac=$(read_mac "$(basename "$d")")
        [[ "$mac" == "$m" ]] && echo "$(basename "$d")" && return 0
    done
    return 1
}

wait_for_macs() {
    # SDIO/USB radios may register after sysinit; do not rename until every
    # MAC from 10-wlan*.link exists on some wlan* (any name).
    local deadline=$(( $(date +%s) + ${MANET_WLAN_APPLY_WAIT_MAX:-60} ))
    local mac all
    while (( $(date +%s) < deadline )); do
        all=1
        for mac in "${!mac_to_target[@]}"; do
            iface_for_mac "$mac" >/dev/null || { all=0; break; }
        done
        (( all == 1 )) && return 0
        sleep 0.25
    done
    echo "manet-wlan-apply-link-names: WARN some .link MACs still missing after ${MANET_WLAN_APPLY_WAIT_MAX:-60}s" >&2
    return 0
}

load_targets() {
    mac_to_target=()
    local f mac name
    shopt -s nullglob
    for f in "$NETDIR"/10-wlan*.link; do
        [[ -f "$f" ]] || continue
        mac=$(grep -E '^MACAddress=' "$f" | head -1 | cut -d= -f2- | tr -d '\r' | tr '[:upper:]' '[:lower:]')
        name=$(grep -E '^Name=' "$f" | head -1 | cut -d= -f2- | tr -d '\r')
        [[ -n "$mac" && -n "$name" ]] || continue
        mac_to_target["$mac"]="$name"
    done
    shopt -u nullglob
}

alloc_tmp() {
    # IFNAMSIZ 15 — keep temp names short (avoid wlmtmp$RANDOM$RANDOM).
    local t
    while true; do
        t="wt${RANDOM}"
        [[ ${#t} -le 15 ]] || continue
        [[ ! -e /sys/class/net/$t ]] && echo "$t" && return
    done
}

fix_one() {
    local dev="$1" m want occ_m tmp=""
    [[ -e "/sys/class/net/$dev" ]] || return 0
    m=$(read_mac "$dev")
    want="${mac_to_target[$m]:-}"
    [[ -z "$want" ]] && return 0
    [[ "$dev" == "$want" ]] && return 0

    if [[ -e "/sys/class/net/$want" ]]; then
        occ_m=$(read_mac "$want")
        # Two-cycle: occupant wants this device's current name.
        if [[ "${mac_to_target[$occ_m]:-}" == "$dev" ]]; then
            ip link set "$dev" down 2>/dev/null || true
            ip link set "$want" down 2>/dev/null || true
            tmp=$(alloc_tmp)
            ip link set "$dev" name "$tmp" || return 1
            ip link set "$want" name "$dev" || return 1
            ip link set "$tmp" name "$want" || return 1
            ip link set "$dev" up 2>/dev/null || true
            ip link set "$want" up 2>/dev/null || true
            return 0
        fi
        tmp=$(alloc_tmp)
        ip link set "$want" down 2>/dev/null || true
        ip link set "$want" name "$tmp" || return 1
    fi
    ip link set "$dev" down 2>/dev/null || true
    ip link set "$dev" name "$want" || return 1
    ip link set "$want" up 2>/dev/null || true
    if [[ -n "$tmp" ]] && [[ -e "/sys/class/net/$tmp" ]]; then
        fix_one "$tmp" || return 1
    fi
    return 0
}

apply_renames() {
    local round dev m want any
    for ((round = 0; round < 48; round++)); do
        any=0
        for dev in /sys/class/net/wlan[0-9]*; do
            [[ -d "$dev" ]] || continue
            dev=$(basename "$dev")
            [[ "$dev" =~ ^wlan[0-9]+$ ]] || continue
            m=$(read_mac "$dev")
            want="${mac_to_target[$m]:-}"
            [[ -z "$want" ]] && continue
            if [[ "$dev" != "$want" ]]; then
                any=1
                fix_one "$dev" || return 1
                break
            fi
        done
        [[ "$any" -eq 0 ]] && return 0
    done
    echo "manet-wlan-apply-link-names: rename did not converge" >&2
    return 1
}

remap_lines_by_mac() {
    local f="$1" m new lines=()
    [[ -f "$f" ]] || return 0
    while read -r line; do
        [[ -z "${line//[$'\t\r\n ']}" ]] && continue
        # RAW_ROLE_FILES=0 (every invocation except radio-setup.sh's own
        # first call): the file already holds a stable target name and
        # apply_renames() has already converged reality to match it — pass
        # it through untouched. See the RAW_ROLE_FILES comment at the top
        # of this file for why re-translating it here regardless of context
        # is what caused this function to exist as a bug in the first place.
        if [[ "$RAW_ROLE_FILES" != 1 ]]; then
            lines+=("$line")
            continue
        fi
        if [[ -n "${pre_rename_mac[$line]:-}" ]]; then
            m="${pre_rename_mac[$line]}"
        elif [[ -e "/sys/class/net/$line" ]]; then
            m=$(read_mac "$line")
        else
            lines+=("$line")
            continue
        fi
        new=$(iface_for_mac "$m") || { lines+=("$line"); continue; }
        lines+=("$new")
    done <"$f"
    [[ ${#lines[@]} -eq 0 ]] && return 0
    printf '%s\n' "${lines[@]}" >"${f}.manet-new" && mv "${f}.manet-new" "$f"
}

remap_single_line() {
    local f="$1" line m new
    [[ -f "$f" ]] || return 0
    line=$(head -1 "$f" | tr -d '\r')
    [[ -z "${line// }" ]] && return 0
    # See remap_lines_by_mac / the RAW_ROLE_FILES comment at the top.
    if [[ "$RAW_ROLE_FILES" != 1 ]]; then
        return 0
    fi
    if [[ -n "${pre_rename_mac[$line]:-}" ]]; then
        m="${pre_rename_mac[$line]}"
    elif [[ -e "/sys/class/net/$line" ]]; then
        m=$(read_mac "$line")
    else
        return 0
    fi
    new=$(iface_for_mac "$m") || return 1
    printf '%s\n' "$new" >"${f}.manet-new" && mv "${f}.manet-new" "$f"
}

remap_iface_map() {
    local f=/var/lib/iface_map
    [[ -f "$f" ]] || return 0
    local out=() left right nl nr m
    while IFS= read -r line; do
        [[ -z "${line// }" ]] && continue
        left="${line%%:*}"
        right="${line#*:}"
        # See remap_lines_by_mac / the RAW_ROLE_FILES comment at the top.
        if [[ "$RAW_ROLE_FILES" != 1 ]]; then
            nl="$left"
        elif [[ -n "${pre_rename_mac[$left]:-}" ]]; then
            m="${pre_rename_mac[$left]}"
            nl=$(iface_for_mac "$m") || nl="$left"
        elif [[ -e "/sys/class/net/$left" ]]; then
            nl=$(iface_for_mac "$(read_mac "$left")") || return 1
        else
            nl="$left"
        fi
        if [[ "$RAW_ROLE_FILES" != 1 ]]; then
            nr="$right"
        elif [[ -n "${pre_rename_mac[$right]:-}" ]]; then
            m="${pre_rename_mac[$right]}"
            nr=$(iface_for_mac "$m") || nr="$right"
        elif [[ -e "/sys/class/net/$right" ]]; then
            nr=$(iface_for_mac "$(read_mac "$right")") || return 1
        else
            nr="$right"
        fi
        out+=("${nl}:${nr}")
    done <"$f"
    printf '%s\n' "${out[@]}" >"${f}.manet-new" && mv "${f}.manet-new" "$f"
}

patch_hostapd_iface() {
    local apf=/etc/hostapd/hostapd.conf
    [[ -f "$apf" ]] || return 0
    [[ -f /var/lib/ap_interface ]] || return 0
    local ap
    ap=$(head -1 /var/lib/ap_interface | tr -d '\r')
    [[ -z "$ap" ]] && return 0
    if grep -q '^interface=' "$apf"; then
        sed -i "s/^interface=.*/interface=${ap}/" "$apf"
    fi
}

load_targets
if [[ ${#mac_to_target[@]} -eq 0 ]]; then
    exit 0
fi

wait_for_macs

# Snapshot name→MAC before renaming so remap functions can resolve old names
for _d in /sys/class/net/wlan[0-9]*; do
    [[ -d "$_d" ]] || continue
    _dev=$(basename "$_d")
    pre_rename_mac["$_dev"]=$(read_mac "$_dev")
done

apply_renames || exit 1

remap_lines_by_mac /var/lib/mesh_if || exit 1
remap_lines_by_mac /var/lib/halow_if || exit 1
remap_lines_by_mac /var/lib/no_mesh_if || exit 1
remap_single_line /var/lib/mesh_24_if || exit 1
remap_single_line /var/lib/mesh_5_if || exit 1
remap_single_line /var/lib/ap_interface || exit 1
remap_iface_map || exit 1
patch_hostapd_iface

exit 0
