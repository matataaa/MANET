# networkd-dispatcher hooks

Installed as-is under `/etc/networkd-dispatcher/` (image builds and the tools
tarball both ship this tree).

The built-in Ethernet port (`end0`) is owned by `ethernet-autodetect.sh`. It
decides once per physical connection between gateway, wired EUD and an
attached network, and records the role in `/run/manet-eth-role`. Gateway
setup itself is owned by `manet-uplink-dispatch.sh`, which also handles USB
uplinks and tethers and never touches a port bridged into br0.

| Hook | end0 | other interfaces |
|---|---|---|
| `carrier.d/50-ethernet-detect` | `ethernet-autodetect.sh --hotplug` | `manet-uplink-dispatch.sh carrier` |
| `off.d/50-gateway-disable`, `no-carrier.d/50-gateway-disable` | `ethernet-autodetect.sh --unplug` | `manet-uplink-dispatch.sh off/no-carrier` |
| `degraded.d/50-gateway-disable` | `manet-uplink-dispatch.sh degraded` (nothing torn down while carrier is present) | same |
| `routable.d/50-manet-uplink` | `manet-uplink-dispatch.sh routable` | same |
