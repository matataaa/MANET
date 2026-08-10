import hmac
from ..data.config import get_perf_auth_token, PERF_AUTH_COOKIE, PERF_AUTH_COOKIE_MAX_AGE


def is_valid_perf_auth_token(token):
    expected = get_perf_auth_token()
    return bool(expected and token) and hmac.compare_digest(str(token), expected)


def parse_cookie_header(header):
    cookies = {}
    if not header:
        return cookies
    for part in header.split(';'):
        if '=' not in part:
            continue
        key, value = part.split('=', 1)
        cookies[key.strip()] = value.strip()
    return cookies


def handle_login(handler):
    try:
        body = handler.read_json_body()
        password = body.get('password', '')
        from ..data.config import get_provisioned_manage_password
        expected = get_provisioned_manage_password()
        if not expected or password != expected:
            handler.send_json({'ok': False, 'error': 'Invalid password'}, 401)
            return
        token = get_perf_auth_token()
        handler.send_response(200)
        handler.send_header('Content-Type', 'application/json; charset=utf-8')
        handler.send_header(
            'Set-Cookie',
            f'{PERF_AUTH_COOKIE}={token}; Path=/; Max-Age={PERF_AUTH_COOKIE_MAX_AGE}; SameSite=Strict'
        )
        body_bytes = b'{"ok":true}'
        handler.send_header('Content-Length', str(len(body_bytes)))
        handler.end_headers()
        handler.wfile.write(body_bytes)
    except Exception as e:
        handler.send_json({'ok': False, 'error': str(e)}, 500)
