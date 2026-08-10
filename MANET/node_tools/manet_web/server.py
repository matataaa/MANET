import http.server
import socketserver
import json
import os

from .handlers import resolve_static, WWW
from .api import status as api_status
from .api import control as api_control
from .api import admin as api_admin
from .api import auth as api_auth
from .api import services as api_services
from .api import mesh as api_mesh
from .api import terminal as api_terminal


class MeshHandler(http.server.BaseHTTPRequestHandler):

    def log_message(self, fmt, *args):
        pass

    def send_json(self, obj, status_code=200):
        body = json.dumps(obj, default=str).encode('utf-8')
        self.send_response(status_code)
        self.send_header('Content-Type', 'application/json; charset=utf-8')
        self.send_header('Content-Length', str(len(body)))
        self.send_header('Cache-Control', 'no-cache')
        self.end_headers()
        self.wfile.write(body)

    def send_html(self, body, status_code=200):
        data = body.encode('utf-8') if isinstance(body, str) else body
        self.send_response(status_code)
        self.send_header('Content-Type', 'text/html; charset=utf-8')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def send_file(self, filepath, content_type):
        try:
            with open(filepath, 'rb') as f:
                data = f.read()
            self.send_response(200)
            self.send_header('Content-Type', content_type)
            self.send_header('Content-Length', str(len(data)))
            if content_type.startswith('image/'):
                self.send_header('Cache-Control', 'public, max-age=3600')
            else:
                self.send_header('Cache-Control', 'no-cache')
            self.end_headers()
            self.wfile.write(data)
        except FileNotFoundError:
            self.send_error(404)

    def send_403(self):
        self.send_response(403)
        self.end_headers()
        self.wfile.write(b'Forbidden')

    def read_json_body(self):
        length = int(self.headers.get('Content-Length', 0))
        return json.loads(self.rfile.read(length)) if length else {}

    def _client_ip(self):
        return self.client_address[0] if self.client_address else '127.0.0.1'

    def do_GET(self):
        path = self.path.split('?')[0]

        # WebSocket upgrade
        if path == '/ws/terminal' and self.headers.get('Upgrade', '').lower() == 'websocket':
            api_terminal.handle_ws(self)
            self.close_connection = True
            return

        # API routes
        if path == '/api/data':
            api_status.handle_data(self)
        elif path == '/api/local':
            api_status.handle_local(self)
        elif path.startswith('/api/peer/'):
            api_status.handle_peer(self, path)
        elif path == '/api/admin/status':
            api_admin.handle_status(self)
        elif path == '/api/voice':
            api_status.handle_voice(self)
        elif path == '/api/services':
            api_services.handle_list(self)
        elif path == '/api/mesh':
            api_mesh.handle_mesh(self)
        elif path.startswith('/api/'):
            self.send_error(404)
        else:
            # Static file serving
            filepath, content_type = resolve_static(path)
            if filepath:
                self.send_file(filepath, content_type)
            else:
                # SPA fallback: serve index.html for unmatched routes
                filepath, content_type = resolve_static('/index.html')
                if filepath:
                    self.send_file(filepath, content_type)
                else:
                    self.send_error(404)

    def do_POST(self):
        path = self.path.split('?')[0]

        if path == '/api/control/interface':
            api_control.handle_interface(self)
        elif path == '/api/control/txpower':
            api_control.handle_txpower(self)
        elif path == '/api/control/halow_channel':
            api_control.handle_halow_channel(self)
        elif path == '/api/control/wifi_channel':
            api_control.handle_wifi_channel(self)
        elif path == '/api/control/hostname':
            api_control.handle_hostname(self)
        elif path == '/api/admin/save':
            api_admin.handle_save(self)
        elif path == '/api/admin/stage':
            api_admin.handle_stage(self)
        elif path == '/api/admin/activate':
            api_admin.handle_activate(self)
        elif path == '/api/admin/cancel':
            api_admin.handle_cancel(self)
        elif path == '/api/iperf/server/start':
            api_control.handle_iperf_server_start(self)
        elif path == '/api/iperf/server/stop':
            api_control.handle_iperf_server_stop(self)
        elif path == '/api/iperf/client/run':
            api_control.handle_iperf_client_run(self)
        elif path == '/api/iperf/client/stream':
            api_control.handle_iperf_client_stream(self)
        elif path == '/api/iperf/stop':
            api_control.handle_iperf_stop(self)
        elif path == '/api/ping/run':
            api_control.handle_ping_run(self)
        elif path == '/api/ping/stream':
            api_control.handle_ping_stream(self)
        elif path == '/api/ping/stop':
            api_control.handle_ping_stop(self)
        elif path.startswith('/api/services/') and path.count('/') == 3:
            service_id = path.split('/')[3]
            api_services.handle_action(self, service_id)
        elif path == '/api/terminal/exec':
            api_terminal.handle_exec(self)
        elif path == '/api/terminal/complete':
            api_terminal.handle_complete(self)
        elif path == '/api/terminal/reboot':
            api_terminal.handle_reboot(self)
        elif path == '/api/perf-auth':
            api_auth.handle_login(self)
        else:
            self.send_error(404)

    def do_DELETE(self):
        self.send_error(404)


class ThreadedServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    allow_reuse_address = True


def run(port=80):
    server = ThreadedServer(('0.0.0.0', port), MeshHandler)
    print(f'MANET Web Server listening on port {port}')
    print(f'  Serving frontend from: {WWW}')
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print('\nShutdown.')
        server.shutdown()
