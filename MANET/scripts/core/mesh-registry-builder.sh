#!/usr/bin/env bash
# ==============================================================================
# Mesh Registry Builder
# ==============================================================================
# Reads JSON node info from Alfred (published by mesh-registry Go binary),
# writes /var/run/mesh_node_registry and /tmp/claimed_chunks.txt.
# Zero python3 forks — all JSON parsing done in a single awk invocation.
# ==============================================================================

ALFRED_DATA_TYPE=68
REGISTRY_STATE_FILE="/var/run/mesh_node_registry"
CLAIMED_CHUNKS_FILE="/tmp/claimed_chunks.txt"
STALE_AFTER_SECONDS="${MESH_REGISTRY_STALE_AFTER:-300}"

log() {
    printf '[%(%Y-%m-%d %H:%M:%S)T] - REGISTRY: %s\n' -1 "$1"
}

printf -v NOW '%(%s)T' -1

REGISTRY_TMP=$(mktemp)
CLAIMED_CHUNKS_TMP=$(mktemp)

printf '# Mesh Node Registry - Generated %(%Y-%m-%d %H:%M:%S)T\n' -1 > "$REGISTRY_TMP"
echo "# Sourced by other scripts to get network state." >> "$REGISTRY_TMP"
echo "" >> "$REGISTRY_TMP"

ALFRED_RAW=$(alfred -r $ALFRED_DATA_TYPE 2>/dev/null)
NODE_COUNT=$(echo "$ALFRED_RAW" | grep -c '^\s*{' 2>/dev/null || echo 0)
log "Found ${NODE_COUNT} peer payloads from Alfred"

# Single awk invocation: extract JSON from Alfred output, parse fields, emit registry
# Outputs two streams separated by a marker:
#   - Registry entries (to stdout)
#   - Claimed chunks (to fd 3 via marker)
DECODED_COUNT=$(echo "$ALFRED_RAW" | awk -v now="$NOW" -v stale="$STALE_AFTER_SECONDS" \
    -v reg_tmp="$REGISTRY_TMP" -v chunks_tmp="$CLAIMED_CHUNKS_TMP" '
function jval(json, key,    pat, start, rest, endpos) {
    pat = "\"" key "\":\""
    start = index(json, pat)
    if (start == 0) return ""
    rest = substr(json, start + length(pat))
    endpos = index(rest, "\"")
    if (endpos == 0) return ""
    return substr(rest, 1, endpos - 1)
}

function emit(prefix, field, val) {
    printf "%s_%s='"'"'%s'"'"'\n", prefix, field, val >> reg_tmp
}

{
    idx = index($0, "\", \"")
    if (idx == 0) next

    json = substr($0, idx + 4)
    sub(/"[[:space:]]*\}[,[:space:]]*$/, "", json)
    gsub(/\\"/, "\"", json)

    mac = jval(json, "mac")
    if (mac == "") next

    clean = mac
    gsub(/:/, "", clean)
    p = "NODE_" clean

    ts = jval(json, "timestamp")
    if (ts == "") ts = "0"
    state = "ACTIVE"
    if (ts + 0 > 0 && (now - ts) > stale) state = "STALE"

    chunk = jval(json, "ipv4_chunk")
    if (chunk == "") chunk = "0"

    emit(p, "HOSTNAME", jval(json, "hostname"))
    emit(p, "MAC_ADDRESS", mac)
    emit(p, "MAC_ADDRESSES", jval(json, "mac_addresses"))
    emit(p, "IPV4_ADDRESS", jval(json, "ipv4"))
    emit(p, "IPV4_CHUNK", chunk)
    emit(p, "SYNCTHING_ID", jval(json, "syncthing_id"))
    emit(p, "TQ_AVERAGE", jval(json, "tq_average"))
    emit(p, "IS_GATEWAY", jval(json, "is_gateway"))
    emit(p, "GATEWAY_IFACE", jval(json, "gateway_iface"))
    emit(p, "IS_NTP_SERVER", jval(json, "is_ntp"))
    emit(p, "IS_MUMBLE_SERVER", jval(json, "is_mumble"))
    emit(p, "IS_TAK_SERVER", jval(json, "is_tak"))
    emit(p, "IS_MEDIAMTX_SERVER", jval(json, "is_mediamtx"))
    emit(p, "UPTIME_SECONDS", jval(json, "uptime_seconds"))
    emit(p, "BATTERY_PERCENTAGE", jval(json, "battery_percentage"))
    emit(p, "CPU_LOAD_AVERAGE", jval(json, "cpu_load"))
    emit(p, "GPS_LATITUDE", jval(json, "gps_lat"))
    emit(p, "GPS_LONGITUDE", jval(json, "gps_lon"))
    emit(p, "GPS_ALTITUDE", jval(json, "gps_alt"))
    emit(p, "DATA_CHANNEL_2_4", jval(json, "ch_2g"))
    emit(p, "DATA_CHANNEL_5_0", jval(json, "ch_5g"))
    emit(p, "CHANNEL_REPORT_JSON", jval(json, "channel_report"))
    emit(p, "LAST_SEEN_TIMESTAMP", ts)
    printf "%s_LAST_REGISTRY_UPDATE='"'"'%s'"'"'\n", p, now >> reg_tmp
    emit(p, "IS_IN_LIMP_MODE", (jval(json, "is_limp") == "" ? "false" : jval(json, "is_limp")))
    emit(p, "LAST_TOURGUIDE_TIMESTAMP", jval(json, "tourguide_ts"))
    emit(p, "LAST_TOURGUIDE_RADIO", jval(json, "tourguide_radio"))
    emit(p, "NODE_STATE", state)
    emit(p, "CONFIG_ACK_VERSION", jval(json, "config_ack"))
    emit(p, "HALOW_TX_MCS", jval(json, "halow_tx_mcs"))
    emit(p, "HALOW_RX_MCS", jval(json, "halow_rx_mcs"))
    emit(p, "HALOW_MCS_PEER", jval(json, "halow_mcs_peer"))
    emit(p, "WIFI_24_TX_MCS", jval(json, "wifi_24_tx_mcs"))
    emit(p, "WIFI_24_RX_MCS", jval(json, "wifi_24_rx_mcs"))
    emit(p, "WIFI_5_TX_MCS", jval(json, "wifi_5_tx_mcs"))
    emit(p, "WIFI_5_RX_MCS", jval(json, "wifi_5_rx_mcs"))
    printf "\n" >> reg_tmp

    decoded++

    if (state == "ACTIVE" && chunk != "0" && chunk != "") {
        printf "%s,%s\n", chunk, mac >> chunks_tmp
    }
}

END { print decoded + 0 }
')

sort -u "$CLAIMED_CHUNKS_TMP" > "$CLAIMED_CHUNKS_FILE" 2>/dev/null
rm -f "$CLAIMED_CHUNKS_TMP"

# Preserve previously-seen nodes that are no longer in Alfred (mark STALE)
STALE_KEPT=0
if [ -f "$REGISTRY_STATE_FILE" ]; then
  Q="'"
  STALE_KEPT=$(awk -v out="$REGISTRY_TMP" -v q="$Q" '
    NR == FNR {
      if (match($0, /^NODE_[a-f0-9]+/))
        new[substr($0, RSTART, RLENGTH)] = 1
      next
    }
    {
      if (match($0, /^NODE_[a-f0-9]+/)) {
        pfx = substr($0, RSTART, RLENGTH)
        if (pfx != cur) { cur = pfx; keep = !(pfx in new) }
      }
      if (keep) {
        line = $0
        if (line ~ /_NODE_STATE=/) sub(/=.*$/, "=" q "STALE" q, line)
        print line >> out
      }
      if ($0 == "" && keep) { kept++; keep = 0 }
    }
    END { print kept + 0 }
  ' "$REGISTRY_TMP" "$REGISTRY_STATE_FILE")
fi

mv "$REGISTRY_TMP" "$REGISTRY_STATE_FILE"
chmod 644 "$REGISTRY_STATE_FILE"

log "Registry updated with ${DECODED_COUNT}/${NODE_COUNT} nodes (${STALE_KEPT} stale preserved)"

exit 0
