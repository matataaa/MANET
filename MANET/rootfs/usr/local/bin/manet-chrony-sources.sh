#!/bin/bash
# manet-chrony-sources.sh — chrony.service ExecStartPre.
#
# Mesh nodes take internet time only through a gateway's NTS sync
# (manet-gateway-ntp.sh); everything else syncs from the mesh at runtime
# (one-shot-time-sync.sh adds that server with chronyc, not in a file).
# A chrony.conf with any other internet source is left over: the gateway
# server config after a reboot without a clean demote, or Debian's stock
# file on a reflashed node. chronyd would then sync from plain NTP at boot.
# Put the client default back before it starts.
set -u

CONF=/etc/chrony/chrony.conf
DEFAULT=/etc/chrony/chrony-default.conf

[ -f "$CONF" ] && [ -f "$DEFAULT" ] || exit 0

# pool/peer/sourcedir/confdir, or a server line without the nts option.
if tr -d '\000' < "$CONF" | awk '
    $1 ~ /^(pool|peer|sourcedir|confdir)$/ { found = 1 }
    $1 == "server" { nts = 0; for (i = 2; i <= NF; i++) if ($i == "nts") nts = 1; if (!nts) found = 1 }
    END { exit !found }'; then
    if cp "$DEFAULT" "$CONF"; then
        echo "chrony.conf had plain internet time sources; reset to $DEFAULT" >&2
    else
        echo "chrony.conf has plain internet time sources; reset failed" >&2
    fi
fi
exit 0
