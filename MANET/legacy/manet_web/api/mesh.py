from ..data.batman import run_batctl_originators, run_batctl_neighbors, run_batctl_gateways
from ..data.config import load_kv_file, MESH_CONF_FILE
import subprocess
import re


def handle_mesh(handler):
    """GET /api/mesh — batman-adv routing internals."""
    tq_map, orig_map = run_batctl_originators()
    neighbors = run_batctl_neighbors()
    gateways = run_batctl_gateways()

    bat0_info = {}
    try:
        r = subprocess.run(['ip', '-br', 'addr', 'show', 'bat0'],
                           capture_output=True, text=True, timeout=3)
        parts = r.stdout.strip().split()
        bat0_info['state'] = parts[1] if len(parts) > 1 else 'unknown'
        bat0_info['addrs'] = [p for p in parts[2:] if '/' in p]
    except Exception:
        pass

    try:
        r = subprocess.run(['batctl', 'routing_algo'], capture_output=True, text=True, timeout=3)
        m = re.search(r'bat0:\s*(\S+)', r.stdout)
        bat0_info['algo'] = m.group(1) if m else r.stdout.strip().split('\n')[0]
    except Exception:
        bat0_info['algo'] = ''

    try:
        r = subprocess.run(['batctl', 'gw_mode'], capture_output=True, text=True, timeout=3)
        bat0_info['gw_mode'] = r.stdout.strip()
    except Exception:
        bat0_info['gw_mode'] = ''

    conf = load_kv_file(MESH_CONF_FILE)

    originators = []
    for mac, entry in orig_map.items():
        originators.append({
            'mac': mac,
            'tq': entry.get('tq', 0),
            'nexthop': entry.get('nexthop', ''),
            'iface': entry.get('iface', ''),
        })
    originators.sort(key=lambda o: o['tq'], reverse=True)

    handler.send_json({
        'bat0': bat0_info,
        'mesh_ssid': conf.get('mesh_ssid', ''),
        'network': conf.get('ipv4_network', ''),
        'originators': originators,
        'neighbors': neighbors,
        'gateways': gateways,
        'originator_count': len(originators),
        'neighbor_count': len(neighbors),
        'gateway_count': len(gateways),
    })
