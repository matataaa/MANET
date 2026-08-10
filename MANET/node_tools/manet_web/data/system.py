import json
import os
import time
import socket
import subprocess
import ipaddress
import urllib.request

from .config import load_kv_file, MESH_CONF_FILE, MESH_STATE_FILE
from .registry import norm_mac, parse_registry


def get_my_mac():
    try:
        r = subprocess.run(['cat', '/sys/class/net/bat0/address'],
                           capture_output=True, text=True, timeout=3)
        return norm_mac(r.stdout.strip())
    except Exception:
        return None


def get_my_hostname():
    try:
        return socket.gethostname()
    except Exception:
        return 'unknown'


def get_battery():
    """Return battery dict or None."""
    try:
        with open('/run/battery_status.json') as f:
            data = json.load(f)
        if data.get('percentage') is not None:
            return data
    except Exception:
        pass
    for root, dirs, files in os.walk('/sys/class/power_supply'):
        for d in dirs:
            cap_path = os.path.join(root, d, 'capacity')
            type_path = os.path.join(root, d, 'type')
            try:
                with open(type_path) as f:
                    if f.read().strip().lower() != 'battery':
                        continue
                with open(cap_path) as f:
                    return {'percentage': int(f.read().strip()), 'status': 'unknown',
                            'voltage_v': None, 'current_ma': None, 'power_w': None,
                            'charging': None, 'timestamp': None}
            except Exception:
                continue
    return None


def get_connected_euds():
    """Return list of active {mac, ip, hostname} from dnsmasq leases."""
    euds = []
    now = int(time.time())
    lease_paths = [
        '/var/lib/misc/dnsmasq.leases',
        '/tmp/dnsmasq.leases',
        '/run/dnsmasq.leases',
    ]
    for path in lease_paths:
        try:
            with open(path) as f:
                for line in f:
                    parts = line.strip().split()
                    if len(parts) >= 4:
                        try:
                            expiry = int(parts[0])
                        except Exception:
                            expiry = 0
                        if expiry and expiry < now:
                            continue
                        euds.append({
                            'mac': parts[1],
                            'ip': parts[2],
                            'hostname': parts[3] if parts[3] != '*' else '',
                            'expires_in': max(0, expiry - now) if expiry else None,
                        })
            break
        except Exception:
            continue
    return euds


def get_running_services():
    """Return dict of service_name -> bool for elected/running mesh services."""
    checks = {
        'mumble': ['mumble-server', 'murmur', 'mumble'],
        'mediamtx': ['mediamtx', 'rtsp-server'],
        'ntp': ['chrony', 'chronyd', 'ntp', 'ntpd'],
        'syncthing': ['syncthing'],
        'tak': ['tak-server', 'takserver'],
    }
    results = {}
    for svc_name, unit_names in checks.items():
        active = False
        for unit in unit_names:
            try:
                r = subprocess.run(
                    ['systemctl', 'is-active', '--quiet', unit],
                    timeout=2
                )
                if r.returncode == 0:
                    active = True
                    break
            except Exception:
                pass
        results[svc_name] = active
    return results


def get_local_uptime():
    try:
        with open('/proc/uptime') as f:
            secs = float(f.read().split()[0])
        return fmt_uptime(secs)
    except Exception:
        return ''


def fmt_uptime(seconds):
    try:
        s = int(float(seconds))
        h, rem = divmod(s, 3600)
        m, sec = divmod(rem, 60)
        if h > 0:
            return f"{h}h {m}m"
        return f"{m}m {sec}s"
    except Exception:
        return seconds


def get_peer_local_data(peer_ip, timeout=1.0):
    """Fetch live /api/local from a peer over the mesh; return {} on failure."""
    if not peer_ip:
        return {}
    try:
        ipaddress.ip_address(peer_ip)
        req = urllib.request.Request(
            f'http://{peer_ip}:80/api/local',
            headers={'User-Agent': 'manet-status/1'}
        )
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read().decode())
    except Exception:
        return {}


def is_allowed_ip(client_ip, conf):
    if client_ip in ('127.0.0.1', '::1'):
        return True
    try:
        network = ipaddress.ip_network(conf.get('ipv4_network', '10.30.2.0/24'), strict=False)
        if ipaddress.ip_address(client_ip) in network:
            return True
    except Exception:
        pass
    return False
