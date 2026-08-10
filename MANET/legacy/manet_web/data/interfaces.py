import subprocess
import re
import json

from .config import load_kv_file, MESH_CONF_FILE
from .registry import norm_mac
from .radio import get_halow_driver_info, get_iface_txpower_cap, txpower_options_for_iface, parse_phy_txpower_options


def get_interfaces():
    """
    Return list of interface dicts with role, health, and fault details.

    Health values:
      'ok'      — up and doing its job
      'warn'    — up but something is degraded (e.g. wpa_supplicant stopped)
      'fault'   — interface is DOWN or not participating when it should be
      'info'    — informational (bridge, bat0, loopback — no health expectation)
    """
    ifaces = []
    try:
        r = subprocess.run(['ip', '-j', 'addr'], capture_output=True, text=True, timeout=5)
        raw = json.loads(r.stdout)
    except Exception:
        return ifaces

    # ── iw dev info ──
    iw_info = {}
    try:
        r2 = subprocess.run(['iw', 'dev'], capture_output=True, text=True, timeout=5)
        cur_iface = None
        for line in r2.stdout.splitlines():
            m = re.match(r'\s+Interface (\S+)', line)
            if m:
                cur_iface = m.group(1)
                iw_info[cur_iface] = {}
            if cur_iface:
                tm = re.search(r'type (\S+)', line)
                if tm: iw_info[cur_iface]['type'] = tm.group(1)
                sm = re.search(r'ssid (.+)', line)
                if sm: iw_info[cur_iface]['ssid'] = sm.group(1).strip()
                fm = re.search(r'channel (\d+).*MHz', line)
                if fm: iw_info[cur_iface]['channel'] = fm.group(1)
                pm = re.search(r'txpower ([\d.]+) dBm', line)
                if pm: iw_info[cur_iface]['txpower_dbm'] = pm.group(1)
                freqm = re.search(r'([\d.]+) GHz', line)
                if freqm: iw_info[cur_iface]['freq'] = freqm.group(1)
                wm = re.search(r'wiphy (\d+)', line)
                if wm: iw_info[cur_iface]['wiphy'] = wm.group(1)
                dm = re.search(r'wdev (0x[0-9a-fA-F]+)', line)
                if dm: iw_info[cur_iface]['wiphy'] = str(int(dm.group(1), 16) >> 32)
    except Exception:
        pass

    # Build phy→band map: Band 1 = 2.4 GHz, Band 2 = 5 GHz (IEEE 802.11 convention)
    # A phy with both bands → 2.4 GHz takes precedence (dual-band chip)
    phy_band = {}  # wiphy_num_str -> '2.4 GHz' | '5 GHz'
    phy_txpower_options = {}
    try:
        r3 = subprocess.run(['iw', 'phy'], capture_output=True, text=True, timeout=5)
        phy_txpower_options = parse_phy_txpower_options(r3.stdout)
        cur_phy = None
        for line in r3.stdout.splitlines():
            pm = re.match(r'Wiphy phy(\d+)', line)
            if pm:
                cur_phy = pm.group(1)
                continue
            if cur_phy is None:
                continue
            bh = re.match(r'\s+Band (\d+):', line)
            if bh:
                band_num = int(bh.group(1))
                if band_num == 1:
                    phy_band[cur_phy] = '2.4 GHz'  # Band 1 always 2.4 GHz
                elif band_num == 2 and cur_phy not in phy_band:
                    phy_band[cur_phy] = '5 GHz'    # Band 2 = 5 GHz, only if no Band 1
    except Exception:
        pass

    # Read driver via ethtool and assign band_label
    for iname in list(iw_info.keys()):
        driver = ''
        try:
            et = subprocess.run(['ethtool', '-i', iname], capture_output=True, text=True, timeout=3)
            for line in et.stdout.splitlines():
                if line.startswith('driver:'):
                    driver = line.split(':', 1)[1].strip()
                    break
        except Exception:
            pass
        iw_info[iname]['driver'] = driver
        if 'morse' in driver:
            iw_info[iname]['band_label'] = 'HaLow'
        else:
            # Try runtime freq first, fall back to phy capability
            freq_str = iw_info[iname].get('freq', '')
            try:
                freq_f = float(freq_str)
                if freq_f < 2.0:
                    iw_info[iname]['band_label'] = 'HaLow'
                elif freq_f < 3.0:
                    iw_info[iname]['band_label'] = '2.4 GHz'
                else:
                    iw_info[iname]['band_label'] = '5 GHz'
            except ValueError:
                wiphy_num = iw_info[iname].get('wiphy', '')
                iw_info[iname]['band_label'] = phy_band.get(wiphy_num, '')
        cap = get_iface_txpower_cap(iname)
        if cap:
            iw_info[iname]['txpower_cap_dbm'] = cap
            iw_info[iname]['txpower_options_dbm'] = txpower_options_for_iface(
                iname, cap, iw_info[iname].get('txpower_dbm', '')
            )

    # HaLow (morse_usb): iw can report a regular Wi-Fi channel; Morse driver is the runtime source.
    for iname in list(iw_info.keys()):
        if 'morse' in iw_info[iname].get('driver', ''):
            iw_info[iname].update(get_halow_driver_info(iname))

    # ── bat0 slaves (active interfaces per batctl) ──
    bat0_slaves_active   = set()   # confirmed active in batctl
    bat0_slaves_inactive = set()   # listed but NOT active
    try:
        bat_r = subprocess.run(['batctl', 'if'], capture_output=True, text=True, timeout=5)
        for line in bat_r.stdout.splitlines():
            m_act = re.match(r'(\S+):\s+active', line)
            m_ina = re.match(r'(\S+):\s+inactive', line)
            if m_act: bat0_slaves_active.add(m_act.group(1))
            elif m_ina: bat0_slaves_inactive.add(m_ina.group(1))
    except Exception:
        pass
    bat0_all_slaves = bat0_slaves_active | bat0_slaves_inactive

    # ── which wpa_supplicant units are running ──
    wpa_running = set()
    try:
        sp = subprocess.run(
            ['systemctl', 'list-units', '--state=active', '--no-legend',
             'wpa_supplicant*', 'wpa_supplicant-s1g*'],
            capture_output=True, text=True, timeout=5)
        for line in sp.stdout.splitlines():
            # wpa_supplicant@wlan0.service or wpa_supplicant-s1g-wlan2.service
            m = re.search(r'wpa_supplicant[^@]*[@-](wlan\d+|halow\d+)', line)
            if m: wpa_running.add(m.group(1))
    except Exception:
        pass

    conf     = load_kv_file(MESH_CONF_FILE)
    eud_mode = conf.get('eud', 'wired')

    # Non-mesh interfaces (EUD AP) — must not be checked for bat0/wpa_supplicant
    no_mesh_ifaces = set()
    try:
        with open('/var/lib/no_mesh_if') as f:
            no_mesh_ifaces = {l.strip() for l in f if l.strip()}
    except Exception:
        pass

    # Build a set of all iface names present for cross-referencing
    all_names = {d.get('ifname', '') for d in raw}

    for iface_data in raw:
        name   = iface_data.get('ifname', '')
        state  = iface_data.get('operstate', 'UNKNOWN')   # UP / DOWN / UNKNOWN
        flags  = iface_data.get('flags', [])
        link_type = iface_data.get('link_type', '')

        if name == 'lo':
            continue

        addrs = [a['local'] for a in iface_data.get('addr_info', [])
                 if a.get('family') in ('inet', 'inet6') and not a['local'].startswith('fe80')]

        is_up   = state == 'UP'
        is_down = state == 'DOWN'
        iw      = iw_info.get(name, {})

        role   = 'other'
        health = 'ok'
        detail = ''
        faults = []   # list of human-readable problem strings

        if name == 'bat0':
            role   = 'bat'
            health = 'info'
            detail = 'BATMAN-ADV mesh bridge'
            if not is_up and state != 'UNKNOWN':
                health = 'fault'
                faults.append('bat0 is DOWN')
            elif not bat0_all_slaves:
                health = 'warn'
                faults.append('No interfaces enslaved to bat0')

        elif name == 'br0':
            role   = 'bridge'
            health = 'info'
            detail = 'L2 bridge (mesh + EUD)'
            if not addrs:
                health = 'warn'
                faults.append('No IP assigned')

        elif name in bat0_all_slaves or (
            name.startswith('wlan') and
            name not in no_mesh_ifaces and
            iw.get('type') != 'AP' and
            name not in [d.get('ifname') for d in raw if d.get('master') == 'bat0']
        ):
            # Mesh radio
            role = 'mesh'
            freq        = iw.get('freq', '')
            ch          = iw.get('channel', '')
            band_label  = iw.get('band_label', '')
            if band_label:
                detail = f"{band_label} — ch{ch}" if ch else band_label
            elif freq:
                detail = f"Mesh radio — {freq}GHz ch{ch}"
            else:
                detail = 'Mesh radio'
            if iw.get('ssid'):
                detail += f" [{iw['ssid']}]"

            if is_down:
                health = 'fault'
                faults.append(f'{name} is DOWN')
            elif name in bat0_slaves_inactive:
                health = 'fault'
                faults.append(f'Inactive in bat0 (wpa_supplicant issue?)')
            elif name not in bat0_slaves_active:
                health = 'warn'
                faults.append(f'Not active in bat0')

            # wpa_supplicant check only for mesh radios (not AP/no_mesh)
            if name not in wpa_running:
                if health == 'ok': health = 'warn'
                faults.append(f'wpa_supplicant not running for {name}')

        elif iw.get('type') == 'AP' or name in no_mesh_ifaces:
            role = 'ap'
            ssid = iw.get('ssid', '')
            freq = iw.get('freq', '')
            detail = f"EUD AP — {ssid}" + (f" ({freq}GHz)" if freq else '')
            if is_down:
                health = 'fault'
                faults.append(f'{name} AP is DOWN')
            elif not ssid:
                health = 'warn'
                faults.append('AP has no SSID (hostapd issue?)')

        elif name.startswith(('end', 'eth', 'enp', 'ens')):
            has_gw = False
            try:
                rout = subprocess.run(['ip', 'route', 'show', 'dev', name],
                                      capture_output=True, text=True, timeout=3)
                has_gw = 'default' in rout.stdout
            except Exception:
                pass

            if has_gw:
                role   = 'gateway'
                detail = 'Ethernet — Internet gateway'
            elif is_up and eud_mode == 'wired':
                role   = 'eud-bridge'
                detail = 'Ethernet — EUD connection'
            else:
                role   = 'other'
                detail = 'Ethernet'
            # Ethernet DOWN is usually fine (cable unplugged) — just informational
            if is_down:
                health = 'info'
                detail += ' (no cable)' if not detail.endswith(')') else ''

        elif (name.startswith('wlan') or name.startswith(('halow', 'mlan'))) and name not in no_mesh_ifaces and name not in bat0_all_slaves:
            # wlan not in bat0 and not AP — unexpected
            freq = iw.get('freq', '')
            detail = f"Wireless {freq}GHz" if freq else 'Wireless'
            if is_down:
                health = 'fault'
                faults.append(f'{name} is DOWN — not participating in mesh')
            elif name not in bat0_all_slaves and iw.get('type') != 'AP':
                health = 'warn'
                faults.append(f'Not in bat0 and not an AP — check wpa_supplicant')

        # bat0/br0 report operstate=UNKNOWN (virtual iface); derive from UP flag instead
        display_state = state
        if state == 'UNKNOWN' and name in ('bat0', 'br0'):
            display_state = 'UP' if 'UP' in flags else 'DOWN'

        ifaces.append({
            'name':     name,
            'role':     role,
            'health':   health,
            'detail':   detail,
            'faults':   faults,
            'addrs':    addrs,
            'state':    display_state,
            'channel':  iw.get('channel', ''),
            'freq_mhz': iw.get('freq_mhz', ''),
            'txpower_dbm': iw.get('txpower_dbm', ''),
            'txpower_cap_dbm': iw.get('txpower_cap_dbm', ''),
            'txpower_options_dbm': iw.get('txpower_options_dbm', []),
            'halow_bw': iw.get('halow_bw', ''),
            'halow_source': iw.get('halow_source', ''),
        })

    # Sort: bat0, mesh, ap, gateway, eud-bridge, bridge, other
    # Within each role, faulted interfaces sort first (most visible)
    health_order = {'fault': 0, 'warn': 1, 'ok': 2, 'info': 3}
    role_order   = {'bat': 0, 'mesh': 1, 'ap': 2, 'gateway': 3, 'eud-bridge': 4, 'bridge': 5, 'other': 6}
    ifaces.sort(key=lambda x: (role_order.get(x['role'], 9), health_order.get(x['health'], 9)))
    return ifaces


def enrich_interfaces_with_registry_mcs(ifaces, node_data):
    if not ifaces or not isinstance(node_data, dict):
        return ifaces

    mcs_by_iface = {
        'wlan0': {
            'tx_mcs': node_data.get('WIFI_24_TX_MCS', ''),
            'rx_mcs': node_data.get('WIFI_24_RX_MCS', ''),
        },
        'wlan1': {
            'tx_mcs': node_data.get('WIFI_5_TX_MCS', ''),
            'rx_mcs': node_data.get('WIFI_5_RX_MCS', ''),
        },
        'wlan2': {
            'tx_mcs': node_data.get('HALOW_TX_MCS', ''),
            'rx_mcs': node_data.get('HALOW_RX_MCS', ''),
        },
    }

    for iface in ifaces:
        if not isinstance(iface, dict):
            continue
        extra = mcs_by_iface.get(iface.get('name', ''), {})
        iface['tx_mcs'] = extra.get('tx_mcs', '')
        iface['rx_mcs'] = extra.get('rx_mcs', '')
    return ifaces
