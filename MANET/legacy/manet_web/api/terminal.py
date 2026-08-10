import subprocess
import re
import shlex
import json
import http.client
import os
import sys
import pty
import select
import signal
import struct
import fcntl
import termios
import threading
from urllib.parse import urlparse, parse_qs

from ..websocket import ws_handshake, ws_recv, ws_send, ws_close, OP_TEXT, OP_BINARY, OP_CLOSE, OP_PING, OP_PONG


BLOCKED_PATTERNS = re.compile(
    r'\b(rm\s+-rf\s+/|mkfs|dd\s+if=|shutdown|halt|poweroff)\b', re.I
)


def _log(msg):
    try:
        sys.stderr.write(f'[ws-term] {msg}\n')
        sys.stderr.flush()
    except Exception:
        pass


def handle_ws(handler):
    """WebSocket terminal — full PTY with interactive bash."""
    parsed = urlparse(handler.path)
    params = parse_qs(parsed.query)
    target = params.get('target', [''])[0]
    protocol = params.get('protocol', [''])[0]
    user = params.get('user', ['root'])[0]
    password = params.get('password', [''])[0]

    _log(f'WS upgrade target={target!r} proto={protocol!r}')

    ws_handshake(handler)
    sock = handler.connection
    sock.settimeout(None)
    sock_fd = sock.fileno()

    _log(f'Handshake done, sock fd={sock_fd}')

    pid, master_fd = pty.fork()
    if pid == 0:
        os.environ['TERM'] = 'xterm-256color'
        os.environ['LANG'] = 'en_US.UTF-8'
        if target and protocol == 'ssh':
            ssh_args = ['ssh', '-tt',
                        '-o', 'StrictHostKeyChecking=no',
                        '-o', 'ConnectTimeout=5',
                        f'{user}@{target}']
            if password:
                os.execlp('sshpass', 'sshpass', '-p', password, *ssh_args)
            else:
                os.execlp('ssh', *ssh_args)
        else:
            os.execlp('bash', 'bash', '-l')

    _log(f'PTY forked pid={pid} master_fd={master_fd}')

    try:
        _ws_pty_bridge(sock, sock_fd, master_fd, pid)
    except Exception as e:
        _log(f'Bridge exception: {e}')
        import traceback
        traceback.print_exc(file=sys.stderr)
    finally:
        try:
            os.close(master_fd)
        except OSError:
            pass
        try:
            os.kill(pid, signal.SIGTERM)
        except OSError:
            pass
        try:
            os.waitpid(pid, os.WNOHANG)
        except Exception:
            pass
        ws_close(sock)
        _log('Session ended')

    handler.close_connection = True


def _ws_pty_bridge(sock, sock_fd, master_fd, pid):
    """Bridge WebSocket ↔ PTY using two threads.

    Thread 1 (main): reads WebSocket frames, writes to PTY
    Thread 2 (daemon): reads PTY output, sends as WebSocket binary frames

    Using threads instead of select() avoids buffered-IO conflicts between
    BaseHTTPRequestHandler's rfile/wfile and raw socket operations.
    """
    alive = True
    send_lock = threading.Lock()

    def _ws_send_safe(data, opcode=OP_BINARY):
        with send_lock:
            ws_send(sock, data, opcode)

    def pty_to_ws():
        nonlocal alive
        while alive:
            try:
                data = os.read(master_fd, 16384)
            except OSError:
                break
            if not data:
                break
            try:
                _ws_send_safe(data)
            except Exception:
                break
        alive = False

    reader = threading.Thread(target=pty_to_ws, daemon=True)
    reader.start()

    _log('Bridge running')

    while alive:
        try:
            opcode, payload = ws_recv(sock)
        except Exception as e:
            _log(f'ws_recv error: {e}')
            break

        if opcode is None or opcode == OP_CLOSE:
            _log(f'WS closed by client (opcode={opcode})')
            break
        elif opcode == OP_PING:
            try:
                _ws_send_safe(payload or b'', OP_PONG)
            except Exception:
                break
        elif opcode == OP_TEXT:
            try:
                os.write(master_fd, payload)
            except OSError:
                _log('PTY write failed')
                break
        elif opcode == OP_BINARY and payload:
            if payload[0] == 1 and len(payload) >= 5:
                cols = struct.unpack('>H', payload[1:3])[0]
                rows = struct.unpack('>H', payload[3:5])[0]
                try:
                    winsize = struct.pack('HHHH', rows, cols, 0, 0)
                    fcntl.ioctl(master_fd, termios.TIOCSWINSZ, winsize)
                    os.kill(pid, signal.SIGWINCH)
                except Exception:
                    pass

    alive = False
    reader.join(timeout=2)
    _log('Bridge stopped')


def handle_exec(handler):
    """POST /api/terminal/exec — run a command and stream output (HTTP fallback)."""
    req = handler.read_json_body()
    cmd = req.get('cmd', '').strip()
    target = req.get('target', '')
    protocol = req.get('protocol', 'ssh')
    user = req.get('user', 'root')
    password = req.get('password', '')

    if not cmd:
        handler.send_json({'ok': False, 'error': 'Empty command'}, 400)
        return

    if BLOCKED_PATTERNS.search(cmd):
        handler.send_json({'ok': False, 'error': 'Command blocked'}, 403)
        return

    if target and protocol == 'http':
        _proxy_http(handler, target, cmd)
        return

    if target:
        if not re.match(r'^[a-zA-Z0-9._:-]+$', target):
            handler.send_json({'ok': False, 'error': 'Invalid target'}, 400)
            return
        ssh_opts = '-o StrictHostKeyChecking=no -o ConnectTimeout=5'
        remote_cmd = f'bash -l -c {shlex.quote(cmd)}'
        if password:
            shell_cmd = f'sshpass -p {shlex.quote(password)} ssh {ssh_opts} {shlex.quote(user)}@{shlex.quote(target)} {remote_cmd}'
        else:
            shell_cmd = f'ssh {ssh_opts} {shlex.quote(user)}@{shlex.quote(target)} {remote_cmd}'
    else:
        shell_cmd = f'bash -l -c {shlex.quote(cmd)}'

    handler.send_response(200)
    handler.send_header('Content-Type', 'text/plain; charset=utf-8')
    handler.send_header('X-Content-Type-Options', 'nosniff')
    handler.send_header('Cache-Control', 'no-cache')
    handler.end_headers()

    try:
        proc = subprocess.Popen(
            shell_cmd, shell=True,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        )
        for line in proc.stdout:
            handler.wfile.write(line)
            handler.wfile.flush()
        proc.wait()
        if proc.returncode and proc.returncode != 0:
            handler.wfile.write(f'\n[exit code {proc.returncode}]\n'.encode())
            handler.wfile.flush()
    except (BrokenPipeError, ConnectionResetError, OSError):
        pass
    except Exception as e:
        try:
            handler.wfile.write(f'\nError: {e}\n'.encode())
            handler.wfile.flush()
        except Exception:
            pass


def handle_complete(handler):
    """POST /api/terminal/complete — bash tab completion."""
    req = handler.read_json_body()
    line = req.get('line', '')
    pos = req.get('pos', len(line))

    text_before = line[:pos]
    parts = text_before.split()
    word = parts[-1] if parts else ''
    is_first_word = len(parts) <= 1 or (len(parts) == 1 and text_before.endswith(word))

    if is_first_word:
        comp_cmd = f'compgen -A command -- {shlex.quote(word)}'
    else:
        comp_cmd = f'compgen -A file -- {shlex.quote(word)}'

    try:
        r = subprocess.run(
            ['bash', '-l', '-c', comp_cmd],
            capture_output=True, text=True, timeout=3
        )
        seen = set()
        matches = []
        for m in r.stdout.strip().split('\n'):
            if m and m not in seen:
                seen.add(m)
                matches.append(m)
    except Exception:
        matches = []

    handler.send_json({'matches': matches, 'word': word})


def handle_reboot(handler):
    """POST /api/terminal/reboot — reboot this node."""
    handler.send_json({'ok': True, 'message': 'Rebooting...'})
    try:
        subprocess.Popen(['systemctl', 'reboot'])
    except Exception:
        pass


def _proxy_http(handler, target, cmd):
    """Proxy command to remote node's web API and stream response back."""
    handler.send_response(200)
    handler.send_header('Content-Type', 'text/plain; charset=utf-8')
    handler.send_header('X-Content-Type-Options', 'nosniff')
    handler.send_header('Cache-Control', 'no-cache')
    handler.end_headers()

    try:
        body = json.dumps({'cmd': cmd}).encode()
        conn = http.client.HTTPConnection(target, 80, timeout=10)
        conn.request('POST', '/api/terminal/exec', body=body,
                     headers={'Content-Type': 'application/json'})
        resp = conn.getresponse()
        while True:
            chunk = resp.read(4096)
            if not chunk:
                break
            handler.wfile.write(chunk)
            handler.wfile.flush()
        conn.close()
    except (BrokenPipeError, ConnectionResetError, OSError):
        pass
    except Exception as e:
        try:
            handler.wfile.write(f'\nHTTP error: {e}\n'.encode())
            handler.wfile.flush()
        except Exception:
            pass
