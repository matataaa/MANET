from ..data.topology import assemble_status_data, assemble_local_data
from ..data.voice import get_voice_status
from ..data.system import get_peer_local_data
import ipaddress


def handle_data(handler):
    try:
        data = assemble_status_data()
        handler.send_json(data)
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)}, 500)


def handle_local(handler):
    try:
        data = assemble_local_data()
        handler.send_json(data)
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)}, 500)


def handle_peer(handler, path):
    parts = path.split('/')
    if len(parts) < 4:
        handler.send_json({'ok': False, 'error': 'Missing peer IP'}, 400)
        return
    peer_ip = parts[3]
    try:
        ipaddress.ip_address(peer_ip)
    except ValueError:
        handler.send_json({'ok': False, 'error': 'Invalid IP'}, 400)
        return
    data = get_peer_local_data(peer_ip, timeout=2.0)
    handler.send_json(data)


def handle_voice(handler):
    try:
        data = get_voice_status()
        handler.send_json(data)
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)}, 500)
