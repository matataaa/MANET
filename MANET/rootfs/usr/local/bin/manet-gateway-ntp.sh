#!/bin/bash
# manet-gateway-ntp.sh start|stop
#
# A gateway syncs its clock over its uplink and then serves time to the mesh.
# Started (asynchronously) by manet-uplink-dispatch.sh when it promotes a
# gateway, stopped when it demotes. /var/run/mesh-ntp.state marks a node
# that serves time; one-shot-time-sync.sh leaves such a node alone.
set -u

NTP_STATE=/var/run/mesh-ntp.state

log() {
    printf '[%(%Y-%m-%d %H:%M:%S)T] - GATEWAY-NTP: %s\n' -1 "$*" >&2
}

case "${1:-}" in
    start)
        log "Syncing time over the uplink"
        cp /etc/chrony/chrony-test.conf /etc/chrony/chrony.conf
        systemctl restart chrony.service 2>/dev/null
        # waitsync blocks until chronyd has actually disciplined the clock.
        # Resolving the pool, taking four samples and settling on one takes
        # longer than a fixed short sleep, so don't shorten this.
        chronyc -a 'burst 4/4' >/dev/null 2>&1
        if timeout 90 chronyc waitsync 60 0 0 1 >/dev/null 2>&1; then
            log "Time sync successful; serving time to the mesh"
            touch "$NTP_STATE"
            systemctl stop chrony.service
            cp /etc/chrony/chrony-server.conf /etc/chrony/chrony.conf
            systemctl start chrony.service
        else
            log "Time sync failed; not serving time to the mesh"
            rm -f "$NTP_STATE"
            systemctl stop chrony.service
            cp /etc/chrony/chrony-default.conf /etc/chrony/chrony.conf
        fi
        ;;
    stop)
        [ -f "$NTP_STATE" ] || exit 0
        log "No longer a gateway; stopping the mesh time server"
        rm -f "$NTP_STATE"
        systemctl stop chrony.service 2>/dev/null
        cp /etc/chrony/chrony-default.conf /etc/chrony/chrony.conf
        ;;
    *)
        echo "usage: $0 start|stop" >&2
        exit 2
        ;;
esac
