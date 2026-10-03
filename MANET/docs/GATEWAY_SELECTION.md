# Gateway selection (design)

Status: **proposal**, not implemented. Covers how a mesh node picks the
internet gateway its default route points at, and what a gateway announces
about its uplink. Supersedes "follow batman-adv's pick" in
`src/gateway-manager`.

## Problem

`gateway-manager`'s `pollClient` routes `default via <gw> dev br0` to
whichever gateway batman-adv marks with `*` in `batctl gwl`. That pick only
exists to steer batman's DHCP handling, and as a route selector it has four
faults:

1. **Flaps.** BATMAN_V's `gw_sel_class` is an absolute margin (default
   5 Mbit/s), judged on one reading with no time component. On a ~6 Mbit/s
   HaLow path that margin is huge; on a 100+ Mbit/s Wi-Fi path it is noise.
   Every switch moves NAT to a different public address and breaks every
   open connection (SSH, TAK, video, voice).
2. **No failover.** If the selected gateway stops answering, `pollClient`
   just returns and keeps the dead route; it never tries another gateway.
3. **Blind to uplink speed.** Every gateway announces batman's default
   10/2 Mbit/s unless an operator sets `gateway_bandwidth`, so a 4G tether
   and a fibre uplink look the same.
4. **Restart moves the node.** A restart takes whatever batman currently
   prefers, not the gateway the existing route already uses.

Upstream (very-srs/MANET 0.559, `gateway-route-manager.sh` +
`manet-uplink-speed.sh`) fixed the same faults. This design ports that
behavior into our Go `gateway-manager` and keeps our existing structure.

## Client side: choosing a gateway

`pollClient` stops reading the `*` and decides itself, every poll (10 s).

**Inputs.** Every gateway in `batctl gwl -H -n`: originator MAC, path
throughput (BATMAN_V, Mbit/s) and announced download/upload bandwidth.
MAC → mesh IP via the registry, as today (`lookupRegistryIP`). Line format
(batctl 2025.3 / batman-adv 2025.4, captured on EUD3 2026-10-03):

```
* 0c:bf:74:00:28:d2 (      111.6) 00:0a:52:0f:1b:f7 [     wlan1]: 10.0/2.0 MBit
```

**Score.** `score = min(path_throughput, announced_download)` — the
bottleneck of mesh path and uplink, the same quantity batman's own
selection class approximates.

**Rules**, in order:

| Situation | Action |
|---|---|
| No current gateway (first run, none before) | Take the highest score. |
| Current gateway missing from `gwl` | Switch now to the best remaining. |
| Current gateway fails 2 consecutive pings (≈20 s at 10 s polls) | Switch now to the best remaining. |
| Another gateway scores ≥ 1.5× current **and** ≥ current + 2 Mbit/s, continuously for 60 s, and the last switch was ≥ 300 s ago | Switch to it. |
| Otherwise | Keep the current gateway. |

The ratio *and* absolute margin together mean the same thing at HaLow and at
Wi-Fi rates. The 60 s hold rides out metric jitter; the 300 s hold bounds how
often connections can be broken. The first choice counts as a switch, so a
node that picked from a partial list at boot waits out the hold before
upgrading. Failover (missing / unreachable) ignores both holds.

**Restart.** On start, the gateway the existing `default via X dev br0`
route points at becomes current (IP → MAC via the registry), so restarting
the service never moves the node.

**Unchanged:** withdrawing the route when no gateway is announced, the
`src` address (`br0IPv4`), and leaving a node's own local uplink route alone.

**Shape.** One pure function,
`decide(state, gateways []gw, now time.Time) (choice, state)`, with all
I/O (batctl, ping, ip route) outside it. That makes the rules table-driven
testable in `main_test.go` without a mesh.

## Gateway side: announcing bandwidth

Two options:

- **A. Measured (recommended).** When an Ethernet uplink is promoted,
  download at most 5 MB over HTTPS (Cloudflare's speed endpoint, an OVH test
  file as fallback), timed from the first byte so DNS/TCP/TLS setup doesn't
  count. Announce `batctl gw_mode server <down>/<down/5>` (upload isn't
  measured; a fifth of download is batman's usual ratio and nothing selects
  on it). Cache the result per (interface, address, router) and measure
  again only when one changes. Metered or wireless uplinks (phone tether,
  cellular modem — `rndis_host`, `ipheth`, `cdc_ether`, `cdc_ncm`,
  `qmi_wwan` — and Wi-Fi uplinks) are never tested and announce the default
  10/2.
- **B. Configured only.** Keep today's behavior: `gateway_bandwidth` if set,
  else batman's default. Simpler, no external traffic, but with several
  gateways the score is effectively path throughput only.

Either way an explicit `gateway_bandwidth` wins over a measurement, so an
operator can still pin it.

**Existing bug, fixed either way.** `gateway_bandwidth` has never applied:
the UI stores values like `10M/10M`, and `gateway-manager` passes them to
`batctl gw_mode server` verbatim, which this batctl rejects ("Invalid
throughput speed for download gateway speed: 10M"). batctl takes bare
numbers as **kbit** and accepts `kbit`/`mbit` suffixes (verified on EUD4
2026-10-03: `10000/2000` and `20mbit/4mbit` work, `10M` fails).
`gateway-manager` will convert `<n>M` to `<n>mbit` so values already stored
on nodes keep working, and compare against `batctl gw`'s parsed numbers
instead of the raw string (today's `strings.Contains` check never matches,
so it would re-run the failing command every poll). `gateway-manager` becomes the only writer of the
announced bandwidth: `manet-uplink-dispatch.sh` and `batman-if-setup.sh`
keep running `batctl gw_mode server` without a value, which keeps the
current bandwidth (verified on EUD4: 50.0/10.0 stayed 50.0/10.0).

A gateway announcing near-zero bandwidth disappears from clients' `gwl`
(seen on 2026-10-03: EUD4 at 0.1/0.0 Mbit → EUD3 withdrew its route within
2 s), so a measured value must never be announced below 1 Mbit/s down.

**Captive portals (optional, with A).** A captive portal passes our ICMP
probe but cannot complete HTTPS to the test host, so a failed download can
keep an Ethernet uplink from being promoted at all. That touches the
promote path in `manet-uplink-dispatch.sh`, not just `gateway-manager`, so it
is a separate, later step.

## Not changing

- batman-adv's own `gw_sel_class` and DHCP handling (EUD DHCP is local per
  node).
- NAT, MSS clamping, EUD shaping, demotion on carrier/probe loss.
- No new `mesh.conf` keys: the thresholds are constants.

## Testing

- Unit: table-driven tests of `decide` (first pick, hold timers, ratio and
  absolute margins, failover on missing/unreachable, restart adoption,
  no gateways) and of the `gwl` parser against captured output.
- Bench: today only EUD4 has an uplink. A second gateway (Ethernet or a
  phone tether on another node) is needed to see selection, failover
  (unplug the active uplink) and hysteresis (throttle one uplink).
