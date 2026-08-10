import re
import os
import hashlib
import socket

# Paths
REGISTRY_FILE = "/var/run/mesh_node_registry"
MESH_CONF_FILE = "/etc/mesh.conf"
MESH_STATE_FILE = "/etc/mesh_ipv4_state"
PENDING_CONFIG_FILE = "/var/run/mesh_pending_config.json"

# Alfred
ALFRED_CONFIG_TYPE = 70

# Radio
HALOW_EU_CHANNELS = [863500, 864500, 865500, 866500, 867500]
HALOW_EU_UI_TO_S1G_CHANNEL = {idx: 1 + ((idx - 1) * 2) for idx in range(1, 6)}
HALOW_BW_TXPOWER_CAP_DBM = {'1MHz': '24', '2MHz': '24', '4MHz': '22'}

# UI
REFRESH_MS = 15000
PERF_AUTH_COOKIE = 'manet_perf_auth'
PERF_AUTH_COOKIE_MAX_AGE = 15552000

# Assets
FER_LOGO_FULL_FILE = '/usr/local/share/manet/fer-logo.svg'
FER_LOGO_BLACK_FILE = '/usr/local/share/manet/fer-logo-black.svg'
FER_LOGO_WHITE_FILE = '/usr/local/share/manet/fer-logo-white.svg'
WWW_DIR = os.environ.get('MANET_WWW_DIR', '/usr/local/share/manet/www')


def load_kv_file(path):
    """Parse a key=value or key='value' file into a dict."""
    conf = {}
    try:
        with open(path) as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith('#'):
                    continue
                if '=' in line:
                    k, v = line.split('=', 1)
                    conf[k.strip()] = v.strip().strip('"\'')
    except Exception:
        pass
    return conf


def _machine_token_salt():
    for path in ('/etc/machine-id', '/var/lib/dbus/machine-id'):
        try:
            with open(path) as f:
                value = f.read().strip()
            if value:
                return value
        except Exception:
            pass
    return socket.gethostname()


def get_provisioned_manage_password(conf=None):
    conf = conf or load_kv_file(MESH_CONF_FILE)
    for key in ('admin_password', 'radio_password', 'lan_ap_key'):
        value = conf.get(key, '').strip()
        if value:
            return value
    return ''


def get_perf_auth_token():
    conf = load_kv_file(MESH_CONF_FILE)
    manage_password = get_provisioned_manage_password(conf)
    if not manage_password:
        return ''
    raw = f'{manage_password}|perf-local|v1|{_machine_token_salt()}'
    return hashlib.sha256(raw.encode('utf-8')).hexdigest()
