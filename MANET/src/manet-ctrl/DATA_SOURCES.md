# Where the mesh/topology numbers come from

Several UI values that look interchangeable (a "speed", a "rate", a "quality")
actually come from different sources with different accuracy characteristics.
This is a map of which is which, so nobody has to re-derive it by reading
`iw`/`batctl` output side by side again.

## Topology graph — per-link speed

**Source:** batman-adv's own BATMAN_V routing throughput metric — the number
in parentheses in `batctl meshif bat0 o -n` / `batctl meshif bat0 n -n`.
batman-adv derives this itself (driver "expected throughput" when the driver
exposes it, otherwise nominal bitrate discounted by loss it measures via its
own periodic ELP probe packets), so it works uniformly for every radio type
without needing per-driver support.

**Code path:** `runBatctlOriginators()` / `runBatctlNeighbors()` in
`collect.go` parse the parenthesized value into `BatOriginator.RawTP` /
`BatNeighbor.RawTP`. `assembleStatusData()` in `topology.go` puts it on each
node's `bestLink.throughput`.

**Caveat (live-tested 2026-08-22):** on a WiFi-mesh (wlan0/wlan1) link this
number is a *conservative underestimate* — a real `iperf3` run measured
~433 Mbit/s TCP throughput against a reported ~237-251 Mbit/s, because the
underlying driver (`mt7915e`) doesn't expose "expected throughput" to
mac80211, so batman-adv falls back to its own loss-adjusted estimate off the
nominal bitrate table. On HaLow (`morse_usb`, which does expose driver
"expected throughput") the number should track much closer to reality. Either
way it's a trustworthy *relative* signal — it's literally what batman-adv
itself uses to pick the best path — just not a lab-grade absolute figure on
WiFi mesh.

## Mesh tab → Link Budget table

Built by `buildLinkBudget()` in `collect.go`, from two independent sources
merged per neighbor:

- **PHY Rate** (`phy_mbps`/`rx_mbps`): `iw dev <iface> station dump`'s
  `tx bitrate`/`rx bitrate`. This is the driver's *last-used* rate for the
  most recent frame, not an average — on an idle link it can read low even
  though a burst of traffic a minute ago negotiated a much higher MCS. For
  HaLow, divide by 20: the `morse_usb` driver reports S1G rates as a 5GHz VHT
  alias inflated 20x versus the real over-the-air rate (confirmed live
  against `iw` station dump — a reported "150 MBit/s" was a real 7.5 Mbit/s).

- **Real Rate** (`expected_mbps` + `expected_source`): two possible sources,
  in priority order —
  1. `expected_source: "driver"` — `iw`'s "expected throughput" field
     (`StationLink.ExpectedMbps`), populated only by drivers that implement
     it. `morse_usb` (HaLow) does; `mt7915e` (WiFi mesh, wlan0/wlan1) does
     not. This value is *not* subject to the HaLow 20x alias — use as-is.
  2. `expected_source: "batman"` — when the driver doesn't report it (always
     true for WiFi mesh in this fleet), falls back to that same neighbor's
     batman-adv throughput metric (`BatNeighbor.RawTP`, see Topology above).
     Same conservative-underestimate caveat applies. The UI tags this with an
     "est." badge specifically so it isn't read as a driver measurement.

- **Floor / Margin**: HaLow-only. `s1gFloor1MHz` + `s1gBWFloorOffset` in
  `collect.go` are approximate S1G receiver decode floors per MCS/channel
  width (from `MANET/docs/halow-range-calc.md` / MM6108 typicals), not measured —
  margin is signal headroom above that modeled floor.

- **Retry %**: `tx_retries / tx_packets` from the same station dump, live.

- **Signal / Signal avg**: `iw` station dump `signal`/`signal avg`, live.

## Mesh tab → Direct Neighbors / Reachable Nodes tables

- **TQ**: `normTQ()` in `collect.go` — batman's raw per-link throughput value
  (same source as Topology) normalized to a 0-255 scale against
  `meshRefThroughput()`, which resolves to the HaLow channel's realistic max
  (`HalowBWMaxMbps`, re-checked at most once/minute). WiFi mesh links, being
  far above that HaLow-scaled reference, saturate at TQ 255 almost
  immediately — TQ is only a meaningful gradient among HaLow-class links.

- **Radio**: kernel interface name (`wlan0`/`wlan1`/`wlan2`) mapped to a
  human label via `radioLabel()` in `js/common.js` — cosmetic only, no
  separate data source.

## Dashboard → Airtime widget

All from `airtimeLoop()` in `airtime.go`, a background goroutine ticking
every 5s independently of any HTTP request (reads are always free/instant —
nothing spawns a subprocess per page load):

- **Per-service breakdown** (Voice/CoT/Chat/WireGuard): a count-only nftables
  table (`manet_acct`) tallies bytes per service port; rates are counter
  deltas over the 5s tick.
- **wlan2 (HaLow) total**: `/sys/class/net/wlan2/statistics/{rx,tx}_bytes`
  delta. Source-of-truth totals (not the port-counter sum) because they
  include relay traffic and per-frame overhead the port counters can't see.
- **WiFi mesh (wlan0+wlan1) total**: same sysfs byte-counter approach, summed
  across both interfaces (`wifiMeshIfaces()`, `readIfacesBytes()`) — fails
  closed (omitted, not zero) if either interface can't be read.

## Caching

- `/api/data` (topology) and `/api/local`: 3s TTL in-process cache
  (`cachedStatusData()`/`cachedLocalData()` in `topology.go`).
- `/api/mesh`'s batman-adv reads (`batctl o/n/gwl`): share a separate 3s TTL
  cache (`cachedBatmanSnapshot()`, also `topology.go`) with the topology
  cache above — a mesh-tab refresh no longer re-triggers its own independent
  `batctl` calls on top of what topology is already polling.
- Everything else `/api/mesh` reads — `iw station dump` (signal/bitrate/
  retries/expected throughput), bat0 state, routing algo, gw_mode, DNS
  records — is live per request, uncached. These are either genuinely
  live RF diagnostics or single cheap reads not duplicated elsewhere, so
  there's nothing to gain by caching them and a real cost (staleness) to
  doing so.
