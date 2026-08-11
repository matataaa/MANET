# Mesh Chat Applet

Text chat across the MANET mesh using UDP multicast. Messages are sent to a
multicast group on br0 — any node running this applet will receive them.
Your node's hostname is used as your identity.

## Build

Cross-compile for the target architecture:

```bash
# ARM64 (Raspberry Pi, Rock 3A)
cd MANET/applets/mesh-chat
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o mesh-chat .

# x86_64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o mesh-chat .
```

## Package

Create a tarball with the applet contents:

```bash
tar czf mesh-chat-applet.tar.gz \
  applet.json mesh-chat mesh-chat.service mesh-chat.conf.default \
  index.html config.html
```

## Install (manual)

```bash
scp mesh-chat-applet.tar.gz radio@<node-ip>:/tmp/

ssh radio@<node-ip>
sudo mkdir -p /usr/local/share/manet/applets/mesh-chat
sudo tar xzf /tmp/mesh-chat-applet.tar.gz -C /usr/local/share/manet/applets/mesh-chat
sudo cp /usr/local/share/manet/applets/mesh-chat/mesh-chat.service /etc/systemd/system/applet-mesh-chat.service
sudo cp /usr/local/share/manet/applets/mesh-chat/mesh-chat.conf.default /etc/mesh-chat.conf
sudo systemctl daemon-reload
sudo systemctl enable --now applet-mesh-chat.service
```

## Install (web UI)

Upload the tarball via the Applets page in the MANET web interface.
The system will extract, install the service, and start it automatically.

## Configuration

Edit `/etc/mesh-chat.conf` on the node, or use the config page in the web UI:

```
MULTICAST_ADDR=239.255.50.50:9800
INTERFACE=br0
PORT=9800
```

All nodes must use the same `MULTICAST_ADDR` to communicate.

## Applet Manifest Format

Every applet needs an `applet.json`:

```json
{
  "name": "my-applet",
  "version": "1.0.0",
  "description": "What it does",
  "author": "Your Name",
  "type": "go",
  "backend": {
    "binary": "my-applet",
    "port": 9801,
    "args": ["-port", "9801"],
    "service": "applet-my-applet.service"
  },
  "frontend": {
    "entrypoint": "index.html"
  },
  "config": {
    "page": "config.html",
    "file": "/etc/my-applet.conf"
  }
}
```

Fields:
- **name**: unique identifier (lowercase, hyphens)
- **version**: semver string
- **description**: shown in the applets list
- **type**: `go`, `python`, or `static` (static = frontend only, no backend)
- **backend.binary**: compiled binary filename in the applet directory
- **backend.port**: local port the backend listens on (proxied by the web server)
- **backend.service**: systemd unit name (convention: `applet-<name>.service`)
- **frontend.entrypoint**: HTML file served as the applet UI
- **config.page**: optional config page HTML
- **config.file**: optional config file path on disk
