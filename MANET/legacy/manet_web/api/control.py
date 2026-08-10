import subprocess
import re
import json
import os
import time

from ..data.radio import (
    _fmt_dbm,
    get_iface_txpower_cap,
    txpower_options_for_iface,
    read_iface_txpower_dbm,
    txpower_request_allowed,
    unsupported_txpower_response,
    set_iface_txpower_verified,
    get_halow_bw_txpower_cap,
    wifi_channel_to_freq,
)
from ..data.config import HALOW_EU_CHANNELS, HALOW_EU_UI_TO_S1G_CHANNEL, MESH_CONF_FILE, load_kv_file


def handle_interface(handler):
    """Toggle a wireless interface up or down."""
    try:
        req = handler.read_json_body()
        iface = req.get('iface', '')
        state = req.get('state', '')  # 'up' or 'down'
        if iface not in ('wlan0', 'wlan1', 'wlan2') or state not in ('up', 'down'):
            handler.send_json({'ok': False, 'error': 'Invalid iface or state'})
            return
        # Detect HaLow (morse_usb) -- requires wpa_supplicant-s1g lifecycle
        et = subprocess.run(['ethtool', '-i', iface], capture_output=True, text=True, timeout=3)
        is_halow = any('morse' in l for l in et.stdout.splitlines() if l.startswith('driver:'))
        if is_halow:
            svc = f'wpa_supplicant-s1g-{iface}.service'
            if state == 'down':
                subprocess.run(['systemctl', 'stop', svc], timeout=15)
                subprocess.run(['ip', 'link', 'set', iface, 'down'], timeout=5)
            else:
                # cfg80211 regulatory must be set before wpa_supplicant_s1g starts.
                # Restarting wpa_supplicant for a standard iface re-asserts country=EU.
                for std_svc in ('wpa_supplicant@wlan0.service', 'wpa_supplicant@wlan1.service'):
                    r = subprocess.run(['systemctl', 'is-active', std_svc],
                                       capture_output=True, text=True, timeout=3)
                    if r.stdout.strip() == 'active':
                        subprocess.run(['systemctl', 'restart', std_svc], timeout=15)
                        time.sleep(3)
                        break
                subprocess.run(['ip', 'link', 'set', iface, 'up'], timeout=5)
                subprocess.run(['systemctl', 'start', svc], timeout=15)
                bat_r = subprocess.run(['batctl', 'if'], capture_output=True, text=True, timeout=5)
                if not any(l.startswith(iface + ':') for l in bat_r.stdout.splitlines()):
                    subprocess.run(['batctl', 'if', 'add', iface], timeout=10)
        else:
            svc = f'wpa_supplicant@{iface}.service'
            if state == 'down':
                subprocess.run(['batctl', 'if', 'del', iface], timeout=10)
                subprocess.run(['systemctl', 'stop', svc], timeout=15)
                subprocess.run(['ip', 'link', 'set', iface, 'down'], timeout=5)
            else:
                subprocess.run(['ip', 'link', 'set', iface, 'up'], timeout=5)
                subprocess.run(['systemctl', 'start', svc], timeout=15)
                bat_r = subprocess.run(['batctl', 'if'], capture_output=True, text=True, timeout=5)
                if not any(l.startswith(iface + ':') for l in bat_r.stdout.splitlines()):
                    subprocess.run(['batctl', 'if', 'add', iface], timeout=10)
        handler.send_json({'ok': True, 'iface': iface, 'state': state})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})


def handle_txpower(handler):
    """Set TX power on a wireless interface."""
    try:
        req = handler.read_json_body()
        iface = req.get('iface', '')
        dbm = req.get('dbm')   # integer dBm
        if not iface or dbm is None:
            handler.send_json({'ok': False, 'error': 'Missing iface or dbm'})
            return
        requested = _fmt_dbm(dbm)
        cap = get_iface_txpower_cap(iface)
        options = txpower_options_for_iface(iface, cap, read_iface_txpower_dbm(iface))
        if cap and not txpower_request_allowed(iface, requested, cap, options):
            handler.send_json(unsupported_txpower_response(iface, requested, cap, options))
            return
        requested, actual = set_iface_txpower_verified(iface, dbm)
        handler.send_json({
            'ok': True,
            'iface': iface,
            'dbm': requested,
            'actual_dbm': actual,
            'cap': cap,
            'options': txpower_options_for_iface(iface, cap, actual) if cap else [],
        })
    except subprocess.CalledProcessError as e:
        err = (e.stderr or e.stdout or str(e)).strip()
        handler.send_json({'ok': False, 'error': err or str(e)})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})


def handle_halow_channel(handler):
    """Set HaLow channel, bandwidth, and optionally TX power."""
    try:
        req = handler.read_json_body()
        channel = req.get('channel')
        bw = req.get('bw', '1MHz')
        dbm = req.get('dbm')
        if not channel:
            handler.send_json({'ok': False, 'error': 'Missing channel'})
            return
        channel = int(channel)
        # EU S1G channel index -> centre frequency in kHz
        eu_s1g_freq_khz = {idx: freq for idx, freq in enumerate(HALOW_EU_CHANNELS, start=1)}
        s1g_channel = HALOW_EU_UI_TO_S1G_CHANNEL.get(channel)
        freq_khz = eu_s1g_freq_khz.get(channel)
        if not freq_khz or not s1g_channel:
            handler.send_json({'ok': False, 'error': f'Invalid EU S1G channel {channel}'})
            return
        bw_mhz = int(str(bw).replace('MHz', ''))
        requested = ''
        actual = ''
        if dbm is not None:
            cap = get_halow_bw_txpower_cap(bw) or get_iface_txpower_cap('wlan2')
            requested = _fmt_dbm(dbm)
            options = txpower_options_for_iface('wlan2', cap, read_iface_txpower_dbm('wlan2'))
            if cap and not txpower_request_allowed('wlan2', requested, cap, options):
                handler.send_json(unsupported_txpower_response('wlan2', requested, cap, options))
                return
        # s1g_prim_chwidth: 0=1MHz primary, 1=2MHz primary
        # For 4MHz operation, primary channel is 2MHz -> chwidth=1
        chwidth = {1: 0, 2: 1, 4: 1}.get(bw_mhz, 0)
        # Write override flag so channel-election.sh doesn't overwrite
        with open('/var/run/halow-channel-override', 'w') as f:
            f.write(f'{channel},{bw}')
        # Update wpa_supplicant conf for persistence across reboots
        wpa_conf = '/etc/wpa_supplicant/wpa_supplicant-wlan2-s1g.conf'
        with open(wpa_conf) as f:
            content = f.read()
        content = re.sub(r'(channel\s*=\s*)\d+', rf'\g<1>{s1g_channel}', content)
        content = re.sub(r'(op_class\s*=\s*)\d+', r'\g<1>66', content)
        content = re.sub(r'(s1g_prim_chwidth\s*=\s*)\d+', rf'\g<1>{chwidth}', content)
        with open(wpa_conf, 'w') as f:
            f.write(content)
        # Apply immediately via morse_cli (needs root; mesh-status runs as root)
        morse_result = subprocess.run(
            ['morse_cli', '-i', 'wlan2', 'channel',
             '-c', str(freq_khz), '-o', str(bw_mhz), '-p', str(bw_mhz)],
            capture_output=True, text=True, timeout=10
        )
        if morse_result.returncode != 0:
            # Fall back to wpa_supplicant restart if morse_cli fails
            subprocess.run(['systemctl', 'restart', 'wpa_supplicant-s1g-wlan2.service'],
                           timeout=15)
        if dbm is not None:
            requested, actual = set_iface_txpower_verified('wlan2', dbm)
        handler.send_json({
            'ok': True,
            'channel': channel,
            'freq_khz': freq_khz,
            'bw': bw,
            'dbm': requested if dbm is not None else '',
            'actual_dbm': actual if dbm is not None else '',
        })
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})


def handle_wifi_channel(handler):
    """Set Wi-Fi channel and optionally TX power on wlan0 or wlan1."""
    try:
        req = handler.read_json_body()
        iface = req.get('interface', req.get('iface', ''))
        channel = req.get('channel')
        dbm = req.get('dbm')
        if iface not in ('wlan0', 'wlan1'):
            handler.send_json({'ok': False, 'error': 'Invalid Wi-Fi interface'})
            return
        freq = wifi_channel_to_freq(iface, channel)
        if not freq:
            handler.send_json({'ok': False, 'error': f'Invalid channel {channel} for {iface}'})
            return

        conf_path = f'/etc/wpa_supplicant/wpa_supplicant-{iface}.conf'
        if not os.path.exists(conf_path):
            handler.send_json({'ok': False, 'error': f'Missing {conf_path}'})
            return
        with open(conf_path) as f:
            content = f.read()
        if re.search(r'frequency=\d+', content):
            content = re.sub(r'frequency=\d+', f'frequency={freq}', content)
        else:
            content = re.sub(r'(network=\{\n)', rf'\1    frequency={freq}\n', content, count=1)
        with open(conf_path, 'w') as f:
            f.write(content)

        subprocess.run(['systemctl', 'restart', f'wpa_supplicant@{iface}.service'],
                       check=True, timeout=15)
        requested = ''
        actual = ''
        if dbm is not None:
            cap = get_iface_txpower_cap(iface)
            requested = _fmt_dbm(dbm)
            options = txpower_options_for_iface(iface, cap)
            if cap and not txpower_request_allowed(iface, requested, cap, options):
                handler.send_json(unsupported_txpower_response(iface, requested, cap, options))
                return
            requested, actual = set_iface_txpower_verified(iface, dbm)
        handler.send_json({
            'ok': True,
            'iface': iface,
            'channel': channel,
            'frequency': freq,
            'dbm': requested if dbm is not None else '',
            'actual_dbm': actual if dbm is not None else '',
        })
    except subprocess.CalledProcessError as e:
        handler.send_json({'ok': False, 'error': str(e)})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})


def handle_iperf_server_start(handler):
    """Start an iperf3 server in one-off mode."""
    try:
        subprocess.run(['pkill', '-f', 'iperf3 -s'], capture_output=True)
        subprocess.Popen(['iperf3', '-s', '--one-off', '-J',
                          '--logfile', '/tmp/iperf3-server.log'])
        handler.send_json({'ok': True})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})


def handle_iperf_server_stop(handler):
    """Stop any running iperf3 server."""
    try:
        subprocess.run(['pkill', '-f', 'iperf3 -s'], capture_output=True)
        handler.send_json({'ok': True})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})


def handle_iperf_client_run(handler):
    """Run an iperf3 client test against a server."""
    try:
        req = handler.read_json_body()
        server_ip = req.get('server_ip', '')
        test_type = req.get('test_type', 'tcp_1stream')
        duration = int(req.get('duration', 30))
        bitrate = req.get('bitrate', '4M')
        parallel = int(req.get('parallel', 1))
        reverse = bool(req.get('reverse', False))

        cmd = ['iperf3', '-c', server_ip, '-t', str(duration), '-J']
        if test_type in ('udp_throughput', 'udp_jitter', 'packet_loss'):
            cmd += ['-u', '-b', bitrate]
        if parallel > 1:
            cmd += ['-P', str(parallel)]
        if reverse:
            cmd += ['-R']

        r = subprocess.run(cmd, capture_output=True, text=True, timeout=duration + 15)
        try:
            result = json.loads(r.stdout)
        except Exception:
            result = {'raw': r.stdout, 'stderr': r.stderr}
        if result.get('error'):
            handler.send_json({'ok': False, 'error': result.get('error'), 'result': result})
        else:
            handler.send_json({'ok': r.returncode == 0, 'error': r.stderr.strip(), 'result': result})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})


_active_streams = {}


def _validate_target(target):
    """Reject anything that isn't a plausible IP or hostname."""
    return bool(target) and bool(re.match(r'^[a-zA-Z0-9._:-]+$', target))


def _start_stream(handler, cmd, key):
    """Run cmd via Popen, stream stdout line-by-line to the client."""
    # Kill any previous stream of same type
    old = _active_streams.pop(key, None)
    if old and old.poll() is None:
        old.kill()
        old.wait()

    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    _active_streams[key] = proc

    handler.send_response(200)
    handler.send_header('Content-Type', 'text/plain; charset=utf-8')
    handler.send_header('X-Content-Type-Options', 'nosniff')
    handler.send_header('Cache-Control', 'no-cache')
    handler.end_headers()

    try:
        for line in proc.stdout:
            handler.wfile.write(line)
            handler.wfile.flush()
    except (BrokenPipeError, ConnectionResetError, OSError):
        pass
    finally:
        if proc.poll() is None:
            proc.kill()
        proc.wait()
        _active_streams.pop(key, None)


def handle_ping_stream(handler):
    """POST /api/ping/stream — stream ping output line by line."""
    req = handler.read_json_body()
    target = req.get('target', '')
    count = req.get('count', 0)
    continuous = req.get('continuous', False)

    if not _validate_target(target):
        handler.send_json({'ok': False, 'error': 'Invalid target'})
        return

    cmd = ['ping', target]
    if not continuous and count and int(count) > 0:
        cmd = ['ping', '-c', str(int(count)), target]

    _start_stream(handler, cmd, 'ping')


def handle_ping_stop(handler):
    """POST /api/ping/stop — kill running ping stream."""
    proc = _active_streams.pop('ping', None)
    if proc and proc.poll() is None:
        proc.kill()
        proc.wait()
    handler.send_json({'ok': True})


def handle_iperf_client_stream(handler):
    """POST /api/iperf/client/stream — stream iperf3 output line by line."""
    req = handler.read_json_body()
    server_ip = req.get('server_ip', '')
    test_type = req.get('test_type', 'tcp_1stream')
    duration = int(req.get('duration', 30))
    bitrate = req.get('bitrate', '4M')

    if not _validate_target(server_ip):
        handler.send_json({'ok': False, 'error': 'Invalid server IP'})
        return

    cmd = ['iperf3', '-c', server_ip, '-t', str(duration), '--forceflush']
    if test_type in ('udp_throughput', 'udp_jitter', 'packet_loss'):
        cmd += ['-u', '-b', bitrate]

    _start_stream(handler, cmd, 'iperf')


def handle_iperf_stop(handler):
    """POST /api/iperf/stop — kill running iperf3 client stream."""
    proc = _active_streams.pop('iperf', None)
    if proc and proc.poll() is None:
        proc.kill()
        proc.wait()
    handler.send_json({'ok': True})


def handle_ping_run(handler):
    """Run a ping test against a target (non-streaming, kept for compatibility)."""
    try:
        req = handler.read_json_body()
        target = req.get('target', '')
        count = int(req.get('count', 100))
        interval = float(req.get('interval', 0.2))
        if not _validate_target(target):
            handler.send_json({'ok': False, 'error': 'Invalid target'})
            return
        r = subprocess.run(
            ['ping', '-c', str(count), '-i', str(interval), target],
            capture_output=True, text=True, timeout=count * interval + 10
        )
        rtt_match = re.search(r'rtt min/avg/max/mdev = ([\d.]+)/([\d.]+)/([\d.]+)/([\d.]+)', r.stdout)
        loss_match = re.search(r'(\d+)% packet loss', r.stdout)
        result = {
            'output': r.stdout,
            'rtt_min': float(rtt_match.group(1)) if rtt_match else None,
            'rtt_avg': float(rtt_match.group(2)) if rtt_match else None,
            'rtt_max': float(rtt_match.group(3)) if rtt_match else None,
            'rtt_mdev': float(rtt_match.group(4)) if rtt_match else None,
            'loss_pct': int(loss_match.group(1)) if loss_match else None,
        }
        handler.send_json({'ok': True, 'result': result})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})


def handle_hostname(handler):
    """POST /api/control/hostname — set node_hostname in mesh.conf and apply to system."""
    try:
        req = handler.read_json_body()
        new_prefix = re.sub(r'[^a-zA-Z0-9_-]', '', req.get('hostname', '').strip())
        if not new_prefix:
            handler.send_json({'ok': False, 'error': 'Empty hostname'})
            return

        conf = load_kv_file(MESH_CONF_FILE)
        mesh_ssid = conf.get('mesh_ssid', 'mesh')

        # Read MAC suffix for hostname construction
        mac_suffix = ''
        try:
            r = subprocess.run(
                ['ip', '-br', 'link', 'show'],
                capture_output=True, text=True, timeout=3)
            for line in r.stdout.splitlines():
                parts = line.split()
                if len(parts) >= 3 and parts[0] not in ('lo', 'bat0', 'br0'):
                    mac = parts[2].replace(':', '')
                    mac_suffix = mac[-4:]
                    break
        except Exception:
            mac_suffix = '0000'

        full_hostname = f'{new_prefix}-{mesh_ssid}-{mac_suffix}'

        # Update node_hostname in mesh.conf
        lines = []
        found = False
        try:
            with open(MESH_CONF_FILE) as f:
                for line in f:
                    if line.strip().startswith('node_hostname='):
                        lines.append(f'node_hostname={new_prefix}\n')
                        found = True
                    else:
                        lines.append(line)
        except FileNotFoundError:
            lines = []
        if not found:
            lines.append(f'node_hostname={new_prefix}\n')
        with open(MESH_CONF_FILE, 'w') as f:
            f.writelines(lines)

        # Apply to running system
        subprocess.run(['hostnamectl', 'set-hostname', full_hostname], timeout=5)

        handler.send_json({'ok': True, 'hostname': full_hostname})
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)})
