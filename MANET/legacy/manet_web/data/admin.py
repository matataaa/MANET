import json
import os
import hashlib
import subprocess

from .config import load_kv_file, MESH_CONF_FILE, ALFRED_CONFIG_TYPE, PENDING_CONFIG_FILE
from .registry import parse_registry
from .system import get_my_hostname


def broadcast_config_package(pkg):
    """Write config package to Alfred type 70."""
    payload = json.dumps(pkg, separators=(',', ':'))
    try:
        r = subprocess.run(
            ['alfred', '-s', str(ALFRED_CONFIG_TYPE)],
            input=payload, capture_output=True, text=True, timeout=5)
        return r.returncode == 0
    except Exception:
        return False

def get_pending_config():
    """Read pending config package from disk, or None."""
    try:
        with open(PENDING_CONFIG_FILE) as f:
            return json.load(f)
    except Exception:
        return None

def save_pending_config(pkg):
    try:
        with open(PENDING_CONFIG_FILE, 'w') as f:
            json.dump(pkg, f)
        return True
    except Exception:
        return False

def clear_pending_config():
    try:
        os.remove(PENDING_CONFIG_FILE)
    except Exception:
        pass

def make_config_version(config_dict):
    """8-char SHA-256 prefix of the JSON config (deterministic)."""
    s = json.dumps(config_dict, sort_keys=True, separators=(',', ':'))
    return hashlib.sha256(s.encode()).hexdigest()[:8]

def assemble_admin_status():
    """Return current config, pending config, and per-node ACK status."""
    conf       = load_kv_file(MESH_CONF_FILE)
    nodes_raw  = parse_registry()
    pending    = get_pending_config()

    node_status = []
    for nid, nd in nodes_raw.items():
        node_status.append({
            'hostname':   nd.get('HOSTNAME', 'unknown'),
            'ip':         nd.get('IPV4_ADDRESS', ''),
            'ack':        nd.get('CONFIG_ACK_VERSION', ''),
            'last_seen':  nd.get('LAST_SEEN_TIMESTAMP', '0'),
            'node_state': nd.get('NODE_STATE', 'ACTIVE'),
        })
    node_status.sort(key=lambda n: n['hostname'])

    return {
        'current_config': {
            'node_hostname':    conf.get('node_hostname', ''),
            'eud':              conf.get('eud', 'wired'),
            'lan_ap_ssid':      conf.get('lan_ap_ssid', ''),
            'lan_ap_key':       conf.get('lan_ap_key', ''),
            'max_euds_per_node':conf.get('max_euds_per_node', '0'),
            'mesh_ssid':        conf.get('mesh_ssid', ''),
            'mesh_key':         conf.get('mesh_key', ''),
            'ipv4_network':     conf.get('ipv4_network', ''),
            'regulatory_domain':conf.get('regulatory_domain', 'US'),
            'acs':              conf.get('acs', 'n'),
            'mtx':              conf.get('mtx', 'n'),
            'mumble':           conf.get('mumble', 'n'),
            'auto_update':      conf.get('auto_update', 'n'),
            'admin_password':   conf.get('admin_password', ''),
        },
        'pending':      pending,
        'nodes':        node_status,
        'total_nodes':  len(node_status),
        'active_nodes': sum(1 for n in node_status if n['node_state'] == 'ACTIVE'),
    }
