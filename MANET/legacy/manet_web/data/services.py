import subprocess


SERVICE_REGISTRY = [
    {
        'id': 'mesh-status',
        'name': 'MANET Web Server',
        'units': ['mesh-status'],
        'category': 'core',
        'actions': ['restart'],
        'description': 'This web interface',
    },
    {
        'id': 'alfred',
        'name': 'Alfred',
        'units': ['alfred'],
        'category': 'core',
        'actions': ['start', 'stop', 'restart'],
        'description': 'Mesh data distribution daemon',
    },
    {
        'id': 'batadv',
        'name': 'batman-adv',
        'units': ['batman-adv'],
        'category': 'core',
        'actions': ['restart'],
        'description': 'L2 mesh routing',
    },
    {
        'id': 'wpa-supplicant',
        'name': 'WPA Supplicant',
        'units': ['wpa_supplicant'],
        'category': 'network',
        'actions': ['start', 'stop', 'restart'],
        'description': 'Mesh WiFi authentication',
    },
    {
        'id': 'hostapd',
        'name': 'hostapd',
        'units': ['hostapd'],
        'category': 'network',
        'actions': ['start', 'stop', 'restart'],
        'description': 'Access point daemon',
    },
    {
        'id': 'dnsmasq',
        'name': 'dnsmasq',
        'units': ['dnsmasq'],
        'category': 'network',
        'actions': ['start', 'stop', 'restart', 'reload'],
        'description': 'DHCP and DNS for EUDs',
    },
    {
        'id': 'avahi',
        'name': 'Avahi',
        'units': ['avahi-daemon'],
        'category': 'network',
        'actions': ['start', 'stop', 'restart', 'reload'],
        'description': 'mDNS / service discovery',
    },
    {
        'id': 'mesh-voice',
        'name': 'Mesh Voice',
        'units': ['mesh-voice'],
        'category': 'application',
        'actions': ['start', 'stop', 'restart'],
        'description': 'PTT voice over mesh',
    },
    {
        'id': 'mumble',
        'name': 'Mumble Server',
        'units': ['mumble-server', 'murmur'],
        'category': 'application',
        'actions': ['start', 'stop', 'restart'],
        'description': 'Voice comms server',
    },
    {
        'id': 'mediamtx',
        'name': 'MediaMTX',
        'units': ['mediamtx'],
        'category': 'application',
        'actions': ['start', 'stop', 'restart'],
        'description': 'RTSP/WebRTC media server',
    },
    {
        'id': 'chronyd',
        'name': 'Chrony NTP',
        'units': ['chronyd', 'chrony'],
        'category': 'system',
        'actions': ['start', 'stop', 'restart'],
        'description': 'Network time synchronisation',
    },
    {
        'id': 'syncthing',
        'name': 'Syncthing',
        'units': ['syncthing', 'syncthing@*'],
        'category': 'application',
        'actions': ['start', 'stop', 'restart'],
        'description': 'File synchronisation',
    },
]


def _systemctl(action, unit, timeout=10):
    try:
        r = subprocess.run(
            ['systemctl', action, unit],
            capture_output=True, text=True, timeout=timeout,
        )
        return r.returncode == 0, r.stderr.strip()
    except subprocess.TimeoutExpired:
        return False, 'timeout'
    except Exception as e:
        return False, str(e)


def _unit_status(unit):
    fields = {}
    try:
        r = subprocess.run(
            ['systemctl', 'show', unit,
             '--property=ActiveState,SubState,MainPID,LoadState,'
             'ActiveEnterTimestamp,Description,UnitFileState'],
            capture_output=True, text=True, timeout=5,
        )
        for line in r.stdout.strip().splitlines():
            if '=' in line:
                k, v = line.split('=', 1)
                fields[k] = v
    except Exception:
        pass
    return fields


def _find_active_unit(units):
    for unit in units:
        if '*' in unit:
            continue
        props = _unit_status(unit)
        if props.get('LoadState') != 'not-found':
            return unit, props
    return units[0] if units else None, {}


def get_all_services():
    results = []
    for svc in SERVICE_REGISTRY:
        unit, props = _find_active_unit(svc['units'])
        active_state = props.get('ActiveState', 'unknown')
        sub_state = props.get('SubState', '')
        enabled = props.get('UnitFileState', '')

        if active_state == 'active':
            status = 'running'
        elif active_state == 'inactive':
            status = 'stopped'
        elif active_state == 'failed':
            status = 'failed'
        else:
            status = active_state

        installed = props.get('LoadState', 'not-found') != 'not-found'

        results.append({
            'id':          svc['id'],
            'name':        svc['name'],
            'description': svc['description'],
            'category':    svc['category'],
            'unit':        unit,
            'status':      status,
            'sub_state':   sub_state,
            'enabled':     enabled in ('enabled', 'enabled-runtime'),
            'installed':   installed,
            'pid':         int(props.get('MainPID', 0)) or None,
            'started_at':  props.get('ActiveEnterTimestamp', ''),
            'actions':     svc['actions'],
        })
    return results


def service_action(service_id, action):
    for svc in SERVICE_REGISTRY:
        if svc['id'] == service_id:
            if action not in svc['actions']:
                return False, f'action {action} not allowed for {service_id}'
            unit, _ = _find_active_unit(svc['units'])
            if not unit:
                return False, 'no unit found'
            ok, err = _systemctl(action, unit)
            return ok, err
    return False, 'unknown service'
