> **Everything you need is in the provisioning directory**
>
> The files in this repository are under active development. They frequently contain breaking bugs, untested changes, or active debugging.
>
> When you flash a device it will pull down the most recent code automatically.

---

### Feature Roadmap

#### Working
- [x] Wireless EUD
- [x] Wired EUD
- [x] Auto EUD
- [x] EUD multicast over mesh
- [x] Host lookup without DNS
- [x] Automatic gateway selection and election
- [x] Zero-conf IP addressing
- [x] Tri-band mesh (802.11ah, 802.11ax 2.4/5)
- [x] Unified Go web UI + REST API (manet-ctrl)
- [x] D3.js topology visualization
- [x] Push-to-talk voice with Opus/RTP
- [x] Hardware PTT (OpenVLM USB HID, GPIO)
- [x] Web PTT via WebSocket
- [x] Voice QoS (DSCP EF, WMM AC_VO, tc prio)
- [x] Multi-channel voice with per-channel ports
- [x] Applet system with DNS integration
- [x] Mesh Chat applet
- [x] Tailscale VPN applet
- [x] WireGuard VPN applet
- [x] Fleet configuration management
- [x] CoT/ATAK blue-force tracking
- [x] CoT relay to EUD devices
- [x] Syncthing file sync
- [x] `mesh` CLI tool
- [x] Android KDU companion app
- [x] OTA update service (node-update)
- [x] Morse SPI radio watchdog with GPIO reset
- [x] x86 HaLow gateway support

#### In Testing
- [ ] Automatic channel selection
- [ ] In-mesh NTP (chrony)

#### Future Work
- [ ] Further reduction in network traffic
- [ ] Enclosure selection
- [ ] Physical interaction (buttons, knobs)
- [ ] Display or status indication
