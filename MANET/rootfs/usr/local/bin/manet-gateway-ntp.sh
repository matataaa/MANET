#!/bin/bash
# manet-gateway-ntp.sh start|stop
#
# A gateway syncs its clock over its uplink and then serves time to the mesh.
# Started (asynchronously) by manet-uplink-dispatch.sh when it promotes a
# gateway, stopped when it demotes. /var/run/mesh-ntp.state marks a node
# that serves time; one-shot-time-sync.sh leaves such a node alone.
#
# Internet time comes only from NTS-authenticated servers: the whole mesh
# takes its clock from the gateway, so a spoofed plain-NTP reply on the uplink
# would set every node's time.
set -u

NTP_STATE=/var/run/mesh-ntp.state
CHRONY_DIR=/etc/chrony
# One server per operator, so three remain to vote if one is unreachable:
# Cloudflare, Netnod, PTB and SIDN Labs.
NTS_SERVERS="time.cloudflare.com sth1.nts.netnod.se ptbtime1.ptb.de ntppool1.time.nl"

log() {
    printf '[%(%Y-%m-%d %H:%M:%S)T] - GATEWAY-NTP: %s\n' -1 "$*" >&2
}

# use_nts_config <template> [initial]
# Install <template> as chrony.conf with its internet sources replaced by the
# NTS servers. The templates are written at provisioning and older ones carry
# a plain "pool" line; their other lines (GPS refclock, mesh allow) are kept.
# "initial" waives certificate dates until the first clock update: the clock
# can be far off at boot, and signatures, hostname and trust chain are still
# checked. Once synced the server config runs without that exception.
use_nts_config() {
    local template=$1 initial=${2:-} host
    {
        # Some nodes' templates carry a run of NUL bytes from a power cut
        # during an earlier append; grep would treat them as binary and drop
        # every line.
        tr -d '\000' < "$template" |
            grep -Ev '^[[:space:]]*(pool|server|peer|ntsdumpdir|nocerttimecheck)([[:space:]]|$)'
        for host in $NTS_SERVERS; do
            echo "server $host iburst nts"
        done
        # Keeps the NTS cookies across the restart into the server config.
        echo "ntsdumpdir /var/lib/chrony"
        if [ "$initial" = initial ]; then
            echo "nocerttimecheck 1"
        fi
    } > "$CHRONY_DIR/chrony.conf.new" && mv "$CHRONY_DIR/chrony.conf.new" "$CHRONY_DIR/chrony.conf"
}

case "${1:-}" in
    start)
        log "Syncing time over the uplink (NTS)"
        use_nts_config "$CHRONY_DIR/chrony-test.conf" initial
        systemctl restart chrony.service 2>/dev/null
        # waitsync blocks until chronyd has actually disciplined the clock.
        # Resolving the servers, the NTS key exchange, taking four samples and
        # settling on one takes longer than a fixed short sleep, so don't
        # shorten this.
        chronyc -a 'burst 4/4' >/dev/null 2>&1
        if timeout 90 chronyc waitsync 60 0 0 1 >/dev/null 2>&1; then
            log "Time sync successful; serving time to the mesh"
            touch "$NTP_STATE"
            systemctl stop chrony.service
            use_nts_config "$CHRONY_DIR/chrony-server.conf"
            systemctl start chrony.service
        else
            log "Time sync failed (no NTS server reachable?); not serving time to the mesh"
            rm -f "$NTP_STATE"
            systemctl stop chrony.service
            cp "$CHRONY_DIR/chrony-default.conf" "$CHRONY_DIR/chrony.conf"
        fi
        ;;
    stop)
        [ -f "$NTP_STATE" ] || exit 0
        log "No longer a gateway; stopping the mesh time server"
        rm -f "$NTP_STATE"
        systemctl stop chrony.service 2>/dev/null
        cp "$CHRONY_DIR/chrony-default.conf" "$CHRONY_DIR/chrony.conf"
        ;;
    *)
        echo "usage: $0 start|stop" >&2
        exit 2
        ;;
esac
