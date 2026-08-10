import subprocess
import re


def get_voice_status():
    """Return mesh-voice service state and configuration."""
    status = {
        'active': False,
        'uptime': '',
        'ptt_mode': '',
        'mcast_addr': '',
        'port': '',
        'interface': '',
    }

    try:
        r = subprocess.run(
            ['systemctl', 'is-active', '--quiet', 'mesh-voice'],
            timeout=3
        )
        status['active'] = r.returncode == 0
    except Exception:
        pass

    if not status['active']:
        return status

    try:
        r = subprocess.run(
            ['systemctl', 'show', 'mesh-voice', '--property=ExecMainStartTimestamp,ExecStart'],
            capture_output=True, text=True, timeout=3
        )
        for line in r.stdout.splitlines():
            if line.startswith('ExecStart='):
                args = line.split()
                for i, arg in enumerate(args):
                    if arg == '-addr' and i + 1 < len(args):
                        status['mcast_addr'] = args[i + 1].rstrip(';')
                    elif arg == '-port' and i + 1 < len(args):
                        status['port'] = args[i + 1].rstrip(';')
                    elif arg == '-ptt' and i + 1 < len(args):
                        status['ptt_mode'] = args[i + 1].rstrip(';')
                    elif arg == '-iface' and i + 1 < len(args):
                        status['interface'] = args[i + 1].rstrip(';')
    except Exception:
        pass

    try:
        r = subprocess.run(
            ['systemctl', 'status', 'mesh-voice'],
            capture_output=True, text=True, timeout=3
        )
        m = re.search(r'Active:.*;\s*([\dhms ]+)\s*ago', r.stdout)
        if m:
            status['uptime'] = m.group(1).strip()
    except Exception:
        pass

    return status
