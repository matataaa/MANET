import json
import os
import time

from ..data.admin import (
    get_pending_config,
    save_pending_config,
    clear_pending_config,
    make_config_version,
    broadcast_config_package,
    assemble_admin_status,
)
import subprocess
import re

from ..data.config import load_kv_file, save_kv_file, MESH_CONF_FILE
from ..data.registry import parse_registry
from ..data.system import get_my_hostname


def handle_status(handler):
    """GET /api/admin/status"""
    try:
        data = assemble_admin_status()
        data['my_hostname'] = get_my_hostname()
        handler.send_json(data)
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)}, 500)


def handle_stage(handler):
    """POST /api/admin/stage -- stage a config package for deployment."""
    try:
        req = handler.read_json_body()
        config = req.get('config', {})
        if not config:
            handler.send_json({'ok': False, 'error': 'No config provided'})
            return

        conf = load_kv_file(MESH_CONF_FILE)

        cur_ssid = conf.get('mesh_ssid', '')
        cur_key = conf.get('mesh_key', '')
        cur_cidr = conf.get('ipv4_network', '')
        dangerous = (
            (config.get('mesh_ssid', cur_ssid) != cur_ssid) or
            (config.get('mesh_key', cur_key) != cur_key) or
            (config.get('ipv4_network', cur_cidr) != cur_cidr)
        )

        version = make_config_version(config)
        pkg = {
            'version': version,
            'issued_by': get_my_hostname(),
            'issued_at': int(time.time()),
            'activate_at': 0,
            'dangerous': dangerous,
            'config': config,
        }
        save_pending_config(pkg)
        broadcast_config_package(pkg)

        try:
            with open('/var/run/mesh_config_ack_version', 'w') as f:
                f.write(version)
        except Exception:
            pass

        handler.send_json({'ok': True, 'version': version, 'dangerous': dangerous})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})


def handle_activate(handler):
    """POST /api/admin/activate -- activate a pending config package."""
    try:
        req = handler.read_json_body()
        force = req.get('force', False)
        pkg = get_pending_config()
        if not pkg:
            handler.send_json({'ok': False, 'error': 'No pending config'})
            return

        if not force:
            nodes_raw = parse_registry()
            version = pkg['version']
            not_acked = [
                nd.get('HOSTNAME', nid)
                for nid, nd in nodes_raw.items()
                if nd.get('CONFIG_ACK_VERSION', '') != version
            ]
            if not_acked:
                handler.send_json({
                    'ok': False,
                    'error': f'{len(not_acked)} nodes have not ACKed: {", ".join(not_acked)}'
                })
                return

        activate_at = int(time.time()) + 60
        pkg['activate_at'] = activate_at
        save_pending_config(pkg)
        broadcast_config_package(pkg)
        handler.send_json({'ok': True, 'activate_at': activate_at})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})


def _get_mac_suffix():
    try:
        r = subprocess.run(['ip', '-br', 'link', 'show'],
                           capture_output=True, text=True, timeout=3)
        for line in r.stdout.splitlines():
            parts = line.split()
            if len(parts) >= 3 and parts[0] not in ('lo', 'bat0', 'br0'):
                return parts[2].replace(':', '')[-4:]
    except Exception:
        pass
    return '0000'


SAVEABLE_KEYS = {
    'node_hostname', 'eud', 'lan_ap_ssid', 'lan_ap_key', 'max_euds_per_node',
    'mesh_ssid', 'mesh_key', 'ipv4_network', 'regulatory_domain',
    'acs', 'mtx', 'mumble', 'auto_update', 'admin_password',
}


def handle_save(handler):
    """POST /api/admin/save -- write config directly to this node's mesh.conf."""
    try:
        req = handler.read_json_body()
        config = req.get('config', {})
        if not config:
            handler.send_json({'ok': False, 'error': 'No config provided'}, 400)
            return

        updates = {k: v for k, v in config.items() if k in SAVEABLE_KEYS}
        if not updates:
            handler.send_json({'ok': False, 'error': 'No valid keys'}, 400)
            return

        save_kv_file(MESH_CONF_FILE, updates)

        applied = {}
        if 'node_hostname' in updates or 'mesh_ssid' in updates:
            conf = load_kv_file(MESH_CONF_FILE)
            prefix = conf.get('node_hostname', '')
            ssid = conf.get('mesh_ssid', 'mesh')
            if prefix:
                prefix = re.sub(r'[^a-zA-Z0-9_-]', '', prefix)
                mac_suffix = _get_mac_suffix()
                full = f'{prefix}-{ssid}-{mac_suffix}'
                subprocess.run(['hostnamectl', 'set-hostname', full], timeout=5)
                applied['hostname'] = full

        handler.send_json({'ok': True, 'saved': list(updates.keys()), 'applied': applied})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)}, 500)


def handle_cancel(handler):
    """POST /api/admin/cancel -- cancel a pending config deployment."""
    try:
        clear_pending_config()
        try:
            os.remove('/var/run/mesh_config_ack_version')
        except Exception:
            pass
        handler.send_json({'ok': True})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})
