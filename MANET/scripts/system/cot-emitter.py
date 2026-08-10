#!/usr/bin/env python3
"""
cot-emitter.py — emits Cursor-on-Target SA messages to ATAK EUDs.

Reads GPS position from /run/gps_status.json (written by gps-reader.py),
builds CoT XML events, and sends them via UDP to:
  - Each EUD discovered in dnsmasq leases (unicast, port 4349)
  - SA multicast group 239.2.3.1:6969 (rate-limited to once per MCAST_INTERVAL)

Callsign is read from /etc/mesh.conf (callsign=XXX) or falls back to hostname.
"""

import json
import os
import socket
import struct
import sys
import time
from datetime import datetime, timezone, timedelta
from xml.sax.saxutils import escape

GPS_STATUS_PATH = "/run/gps_status.json"
MESH_CONF_PATH = "/etc/mesh.conf"
POLL_INTERVAL = 10
MCAST_INTERVAL = 30
STALE_SECONDS = 120
UNICAST_PORT = 4349
MCAST_GROUP = "239.2.3.1"
MCAST_PORT = 6969
COT_TYPE = "a-f-G-U-C"
DNSMASQ_LEASE_PATHS = [
    "/var/lib/misc/dnsmasq.leases",
    "/tmp/dnsmasq.leases",
    "/run/dnsmasq.leases",
]


def log(msg: str) -> None:
    print(f"[cot-emitter] {msg}", flush=True)


def read_mesh_conf(key: str, default: str = "") -> str:
    try:
        with open(MESH_CONF_PATH) as f:
            for line in f:
                line = line.strip()
                if line.startswith(f"{key}="):
                    return line.split("=", 1)[1]
    except OSError:
        pass
    return default


def get_callsign() -> str:
    cs = read_mesh_conf("callsign")
    if cs:
        return cs
    return socket.gethostname()


def get_uid() -> str:
    return f"MANET-{socket.gethostname()}"


def read_gps() -> dict | None:
    try:
        with open(GPS_STATUS_PATH) as f:
            data = json.load(f)
        if not data.get("has_fix"):
            return None
        return data
    except (OSError, json.JSONDecodeError):
        return None


def iso_utc(ts: float) -> str:
    return datetime.fromtimestamp(ts, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def build_cot_event(gps: dict, uid: str, callsign: str) -> bytes:
    now = time.time()
    stale = now + STALE_SECONDS
    ce = max(5.0, gps.get("hdop", 99.9) * 3.0)

    xml = (
        '<?xml version="1.0" encoding="UTF-8"?>'
        f'<event version="2.0" uid="{escape(uid)}" type="{COT_TYPE}"'
        f' time="{iso_utc(now)}" start="{iso_utc(now)}"'
        f' stale="{iso_utc(stale)}" how="m-g">'
        f'<point lat="{gps["latitude"]}" lon="{gps["longitude"]}"'
        f' hae="{gps["altitude"]}" ce="{ce:.1f}" le="9999999.0"/>'
        f"<detail>"
        f'<contact callsign="{escape(callsign)}"/>'
        f'<__group name="Cyan" role="Team Member"/>'
        f'<precisionlocation altsrc="GPS" geopointsrc="GPS"/>'
        f'<track course="0.0" speed="0.0"/>'
        f"</detail></event>"
    )
    return xml.encode("utf-8")


def get_eud_ips() -> list[str]:
    now = int(time.time())
    ips = []
    for path in DNSMASQ_LEASE_PATHS:
        try:
            with open(path) as f:
                for line in f:
                    parts = line.strip().split()
                    if len(parts) >= 4:
                        try:
                            expiry = int(parts[0])
                        except ValueError:
                            expiry = 0
                        if expiry and expiry < now:
                            continue
                        ips.append(parts[2])
            return ips
        except OSError:
            continue
    return ips


def make_mcast_socket() -> socket.socket:
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM, socket.IPPROTO_UDP)
    sock.setsockopt(socket.IPPROTO_IP, socket.IP_MULTICAST_TTL, 32)
    sock.setsockopt(socket.IPPROTO_IP, socket.IP_MULTICAST_LOOP, 0)
    return sock


def main() -> None:
    callsign = get_callsign()
    uid = get_uid()
    log(f"Starting CoT emitter: uid={uid} callsign={callsign}")

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    mcast_sock = make_mcast_socket()
    last_mcast = 0.0

    while True:
        gps = read_gps()
        if gps is None:
            time.sleep(POLL_INTERVAL)
            continue

        event = build_cot_event(gps, uid, callsign)

        eud_ips = get_eud_ips()
        for ip in eud_ips:
            try:
                sock.sendto(event, (ip, UNICAST_PORT))
            except OSError as e:
                log(f"unicast to {ip}:{UNICAST_PORT} failed: {e}")

        now = time.monotonic()
        if now - last_mcast >= MCAST_INTERVAL:
            try:
                mcast_sock.sendto(event, (MCAST_GROUP, MCAST_PORT))
            except OSError as e:
                log(f"multicast failed: {e}")
            last_mcast = now

        time.sleep(POLL_INTERVAL)


if __name__ == "__main__":
    main()
