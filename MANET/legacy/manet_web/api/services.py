import json

from ..data.services import get_all_services, service_action


def handle_list(handler):
    services = get_all_services()
    handler.send_json({'services': services})


def handle_action(handler, service_id):
    body = handler.read_json_body()
    action = body.get('action', '')
    if action not in ('start', 'stop', 'restart', 'reload', 'enable', 'disable'):
        handler.send_json({'ok': False, 'error': 'invalid action'}, 400)
        return
    ok, err = service_action(service_id, action)
    handler.send_json({'ok': ok, 'error': err if not ok else None})
