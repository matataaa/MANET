"""Minimal WebSocket implementation (RFC 6455) using stdlib only."""
import hashlib
import base64
import struct
import os

WS_MAGIC = b'258EAFA5-E914-47DA-95CA-5AB9FF81C013'

OP_TEXT = 0x01
OP_BINARY = 0x02
OP_CLOSE = 0x08
OP_PING = 0x09
OP_PONG = 0x0A


def ws_accept_key(key):
    digest = hashlib.sha1(key.encode() + WS_MAGIC).digest()
    return base64.b64encode(digest).decode()


def ws_handshake(handler):
    """Perform WebSocket upgrade handshake using raw socket to avoid buffered IO issues."""
    key = handler.headers.get('Sec-WebSocket-Key', '')
    accept = ws_accept_key(key)
    resp = (
        'HTTP/1.1 101 Switching Protocols\r\n'
        'Upgrade: websocket\r\n'
        'Connection: Upgrade\r\n'
        'Sec-WebSocket-Accept: ' + accept + '\r\n'
        '\r\n'
    )
    sock = handler.connection
    sock.sendall(resp.encode())


def _recv_exact(sock, n):
    buf = b''
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            return None
        buf += chunk
    return buf


def ws_recv(sock):
    header = _recv_exact(sock, 2)
    if not header:
        return None, None

    opcode = header[0] & 0x0F
    masked = bool(header[1] & 0x80)
    length = header[1] & 0x7F

    if length == 126:
        raw = _recv_exact(sock, 2)
        if not raw:
            return None, None
        length = struct.unpack('>H', raw)[0]
    elif length == 127:
        raw = _recv_exact(sock, 8)
        if not raw:
            return None, None
        length = struct.unpack('>Q', raw)[0]

    mask = _recv_exact(sock, 4) if masked else None
    payload = _recv_exact(sock, length) if length else b''
    if payload is None:
        return None, None

    if mask and payload:
        payload = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))

    return opcode, payload


def ws_send(sock, data, opcode=OP_BINARY):
    if isinstance(data, str):
        data = data.encode()

    frame = bytearray()
    frame.append(0x80 | opcode)

    length = len(data)
    if length < 126:
        frame.append(length)
    elif length < 65536:
        frame.append(126)
        frame.extend(struct.pack('>H', length))
    else:
        frame.append(127)
        frame.extend(struct.pack('>Q', length))

    frame.extend(data)
    sock.sendall(bytes(frame))


def ws_close(sock):
    try:
        ws_send(sock, b'', OP_CLOSE)
    except Exception:
        pass
