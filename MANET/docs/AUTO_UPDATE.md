# Auto-Update

How OTA updates work on a node, what every setting does, how the
manual/fleet-wide update actions behave, and the exact steps to cut and
publish a release. For the tag/version-file formats themselves, see
[`VERSIONING.md`](VERSIONING.md); for the overlay-vendoring scripts'
internals (`fetch-cm4-overlay.sh`, etc.), see
[`../packaging/README.md`](../packaging/README.md).

## Two independent channels

`node-update` (`MANET/src/node-update/main.go`) runs on every node and
handles two separate update channels:

- **Software** — the Go services, scripts, web UI, and pre-built binaries
  (`build-tools-tarball.sh`). Versioned via `release-vX.Y.Z` git tags.
- **Overlay** — the SBC's kernel, modules, and firmware. Versioned with the
  existing plain-decimal overlay numbering, per board (RPi5/CM4 come from
  different sources — see `VERSIONING.md`).

They're kept separate because the risk is completely different: a bad
software update is a service restart away from fixed; a bad overlay update
has no rollback and can require a physical re-flash. That's why the overlay
channel has its own opt-in flag, off by default, independent of the
software flag.

## How it works: detect always, apply conditionally

Every node checks **both** channels every 6 hours, and immediately whenever
Settings are saved — this happens regardless of whether `auto_update` or
`auto_update_overlay` is enabled. Availability is always recorded to
`/var/run/manet_update_status.json` and surfaced in the UI as an "Update
Available" banner, whether or not anything is allowed to apply
automatically.

The flags only control what happens *after* something is detected as
available:

- **Automatic apply** requires the relevant flag to be on **and** the
  bandwidth gate (below) to pass. If the flag is on but the gate fails, the
  update stays flagged as available and the node just waits for the next
  check.
- **Manual apply** — the per-node update buttons (Config tab, or "Update
  Selected" in Fleet Control), the fleet-wide "Force Update" buttons, or
  `mesh update now` — applies unconditionally if available, bypassing both
  the flag and the gate. The UI shows a bandwidth/time warning before any
  of these is confirmed.

Both paths funnel through the same download/extract/reboot code per
channel — there's no separate "automatic" vs. "manual" apply logic, only a
different gate in front of the same mechanism.

Software and overlay are evaluated **independently** each cycle — if both
are available and both are eligible to apply (automatic with the gate
passing, or manually/fleet-triggered), both apply in the same cycle rather
than one silently waiting for the other. Applying schedules a reboot; if
both channels apply in the same cycle that's still a single reboot, not
two (scheduling a second one just reschedules the pending shutdown to the
new jittered time).

## Settings reference

All four keys live in `/etc/mesh.conf`, editable per node in Settings, or
fleet-wide via Fleet Control's Network Config.

| Key | Default | Effect |
|---|---|---|
| `update_url` | *(empty)* | Base URL both channels fetch from. Empty disables OTA entirely — no checks happen at all, not even detection. |
| `auto_update` | `n` | Software channel: apply automatically once detected, if the bandwidth gate passes. Off by default on every provisioning platform. |
| `auto_update_overlay` | `n` | Overlay channel: apply automatically once detected, if the gate passes. Independent of `auto_update` — a kernel/firmware swap has no rollback, so this stays opt-in even if you trust software auto-update fleet-wide. |
| `auto_update_min_mbps` | `10` | Minimum current uplink throughput required for *automatic* apply on either channel. Manual and fleet-wide updates ignore this (with a warning shown first). |
| `update_allow_unsigned` | `n` | Install a package even when its signature (see [Package signing](#package-signing)) is missing or invalid, with a warning in the log. Lab use only. |

Saving any of these triggers an immediate re-check on that node
(`systemctl reload node-update`), rather than waiting for the next
scheduled cycle.

## The bandwidth gate

`node-update` asks `manet-ctrl`'s local status endpoint
(`GET /api/local`) for the node's current best uplink:

- `uplink_type: "wired"` — this node is itself the mesh gateway (no mesh
  hop involved). Always passes the gate, no throughput check.
- `uplink_type: "halow-mesh"` / `"wifi-mesh"` — reached via a mesh hop.
  `uplink_mbps` is the same batman-adv throughput estimate already used for
  the topology "Real Rate" column, for the route toward the selected
  gateway. Compared against `auto_update_min_mbps`.
- `uplink_type: "unknown"` — no gateway route found, or `manet-ctrl` didn't
  respond. Fails **closed**: no automatic apply, not an assumed-fine
  default.

This only gates automatic apply. It has no effect on detection (which
always runs) or on manual/fleet-triggered apply (which always bypasses it,
after showing a warning).

## Manual update on one node

Shown as a persistent banner on the node's Config tab whenever the status
file reports an available update on either channel — it stays until acted
on, not a toast that disappears. It offers **Update MANET** (software),
**Update Kernel/Drivers** (overlay) and, when both are available, **Update
Both**. Clicking one:

1. Shows the current `uplink_mbps`/`uplink_type` against
   `auto_update_min_mbps`, with a warning about expected time and a
   suggestion to use a higher-bandwidth connection if the link is below
   threshold.
2. On confirm, calls `POST /api/admin/update-now` with
   `{"channel": "software" | "overlay" | "both"}`, which writes the channel
   to node-update's trigger file and signals it (`SIGUSR1`) to apply that
   channel, ignoring the `auto_update`/`auto_update_overlay` flags and the
   gate. With `both`, the node reboots once, after both are installed.

`mesh update now <software|overlay|both>` does the same from the CLI.

## Fleet-wide updates

Fleet Control aggregates every node's update status
(`GET /api/admin/update-summary`, which fetches each node's
`/api/admin/update-status` in parallel) into a banner: "N of M nodes have
an update available", with **Force Update MANET**, **Force Update
Kernel/Drivers** and **Force Update Both**. Clicking one:

1. Shows one aggregate warning if any nodes are below the bandwidth
   threshold ("3 of 4 nodes are below the recommended bandwidth...").
2. On confirm, calls `POST /api/admin/force-update` with the channel, which
   broadcasts a trigger to every node via the same Alfred mesh-gossip
   mechanism the fleet config-push already uses (a separate slot, so the
   two package schemas never collide — see `fleet.go`'s
   `broadcastUpdatePackage`). Each node picks it up within one poll cycle
   (~10s) and applies locally via the same mechanism as a manual update —
   same bypass semantics, same warning-before-confirm. A node with nothing
   to install for that channel ignores it.

Below the banner, a per-node version table shows each reachable node's
installed and available versions:

- **Update Selected — MANET / Kernel/Drivers / Both** applies to the ticked
  nodes only, by calling each node's `update-now` (through
  `/api/peer/<ip>` for every node but this one).
- **Check for Updates** broadcasts the `check` channel: every node
  re-checks the update server now, skipping the cooldown, and never
  applies anything — so the table shows fresh versions.

## Interrupted updates

An update unpacks straight onto `/`. A power cut part-way through would
leave a mix of old and new files, and the node might need exactly those
files to get back on the network and download the release again. So
`node-update` keeps the verified package in `/var/lib/manet-update/` with a
`<channel>.pending` marker until the unpacked files have reached the disk.
If the node boots with a marker still there, the early-boot unit
`manet-update-recover.service` unpacks the kept package again, offline,
records the version and reboots once so every service starts on complete
files. A package that cannot be unpacked is left as `<channel>.failed` and
not retried on every boot. The recovery script and unit are written by
`node-update` itself, not shipped in the tarball, so the unpack they
protect can't break them.

## Package signing

`node-update` extracts tarballs as root onto `/`, so it only installs one
that comes with a valid signature: next to every `<name>.tar.gz` on the
update server sits `<name>.tar.gz.sig`, an Ed25519 signature over the file
name, the version from the matching version file, and the file's SHA-256.
Nodes check it against the public keys in
`/usr/local/share/manet/ota-keys/*.pub`, shipped in the tools tarball.

Because the package itself is verified, the transport doesn't matter:
`update_url` can be plain `http://`, e.g. a gateway or laptop on the mesh
serving the files off-grid. Binding the name and version means an old
signed release can't be re-served as a newer one, and an overlay can't be
swapped in for a tools tarball.

- **Keys.** The private key lives on the release machine
  (`/root/manet-releases/keys/ota-signing.key`, mode 0600, never in git).
  Back it up: without it, nodes only take new releases with
  `update_allow_unsigned=y` or a manual install. To rotate, add the new
  `.pub` to `rootfs/usr/local/share/manet/ota-keys/`, ship a release signed
  with the old key, then switch keys and drop the old `.pub`.
- **Signing** (`release.sh` does this automatically):
  `node-update ota-sign <private-key> <version> <tarball>` writes
  `<tarball>.sig`. `<version>` is exactly what goes in the version file
  (`0.4.7` for software, `0.549` for an overlay). Sign each file under its
  final published name: the name is part of what's signed.
- **New key pair:** `node-update ota-keygen <private-key> <public-key>`
  (refuses to overwrite an existing key).
- **First rollout.** Nodes still running a node-update without signature
  checks install the first signed release like any other; from then on
  they verify every package.

## Publishing a software release: step by step

Nothing publishes itself — there's no CI for this today. You build the
artifacts locally and host them yourself at whatever URL nodes' `update_url`
points to. Versions are entirely your call: pick the next number yourself,
`node-update` only checks "is the remote version strictly greater than
what's locally installed."

**1. Pick a version and tag the exact commit you want to ship:**

```sh
git tag release-v0.2.0
git push origin release-v0.2.0
```

**2. Check out that tag before building** — the build script stamps the
release version by running `git describe --exact-match` against whatever
commit is currently checked out, so it has to actually be sitting on the
tag, not just have it somewhere in history:

```sh
git checkout release-v0.2.0
```

**3. Build the tools tarball:**

```sh
bash MANET/packaging/build-tools-tarball.sh tools.tar.gz
```

This cross-compiles every Go service for arm64, bundles scripts/web-UI/
systemd units/the seven pre-built radio binaries, and — because you're
exactly on the tag — writes `etc/manet_release_version.txt = 0.2.0` inside
the tarball. (If you weren't on an exact `release-v*` tag, that file would
be silently omitted and the tarball couldn't advance any node's version —
worth checking `tar -tzf tools.tar.gz | grep manet_release_version` if
something seems off.)

**4. Produce the two board-named copies.** The tools tarball is
board-agnostic (no kernel/firmware in it, just arm64 userspace) but
`node-update` requests it by board-prefixed name, so ship the same file
twice:

```sh
cp tools.tar.gz rpi5-tools.tar.gz
cp tools.tar.gz cm4-tools.tar.gz
```

**5. Write the plain-text version file** — same value the build just
stamped into the tarball, as its own standalone file:

```sh
echo -n "0.2.0" > manet_release_version.txt
```

**5b. Sign each tarball** (see [Package signing](#package-signing)):

```sh
node-update ota-sign /root/manet-releases/keys/ota-signing.key 0.2.0 rpi5-tools.tar.gz
node-update ota-sign /root/manet-releases/keys/ota-signing.key 0.2.0 cm4-tools.tar.gz
```

**6. Copy these files to your webhost**, all in the same directory —
this directory *is* what `update_url` should point to:

```
manet_release_version.txt
rpi5-tools.tar.gz  rpi5-tools.tar.gz.sig
cm4-tools.tar.gz   cm4-tools.tar.gz.sig
```

Any static file host works — `node-update` does a plain HTTP(S) GET, no
special server logic required. A couple of concrete ways to get them there:

```sh
# Plain web server over SSH
scp manet_release_version.txt rpi5-tools.tar.gz cm4-tools.tar.gz \
    you@yourhost:/var/www/manet-updates/

# Or rsync (only sends what changed)
rsync -avz manet_release_version.txt rpi5-tools.tar.gz cm4-tools.tar.gz \
    you@yourhost:/var/www/manet-updates/

# Or an S3-compatible bucket
aws s3 cp manet_release_version.txt s3://your-bucket/manet-updates/
aws s3 cp rpi5-tools.tar.gz         s3://your-bucket/manet-updates/
aws s3 cp cm4-tools.tar.gz          s3://your-bucket/manet-updates/
```

**7. Point nodes at it** — set `update_url` to that directory's URL (e.g.
`https://updates.example.com/manet-updates`) in Settings or fleet-wide via
Fleet Control, with `auto_update=y` if you want it to apply automatically
(subject to the bandwidth gate), or leave it `n` and update manually or
fleet-wide from the UI once you've confirmed the release is good.

## Publishing an overlay update: step by step

Same idea, but per board, and there's no build step for RPi5 since the
CI-published asset is already in the right shape. See the "Publishing an
overlay OTA update" section of
[`../packaging/README.md`](../packaging/README.md) for the exact commands —
summarized:

- **CM4**: `packaging/fetch-cm4-overlay.sh` (vendors the latest overlay),
  then `packaging/build-overlay-tarball.sh kernel-work/packages/cm4-sbc-overlay
  cm4-sbc-overlay.tar.gz`. Take the `Bundled version` value from the
  generated `VENDORED_FROM.md` and write it to a plain-text
  `cm4-overlay-version.txt`. Copy both files to the same webhost directory
  as above.
- **RPi5**: `gh release download rpi5-sbc-overlay-current --pattern
  rpi5-sbc-overlay.tar.gz` — no rebuild needed. Hand-write its known version
  into `rpi5-overlay-version.txt`. Copy both to the same directory.

Only bump the version file once you're satisfied the paired tarball is
good — any node with `auto_update_overlay=y` applies it with no rollback if
it's wrong.

## Risk notes

- **Overlay updates have no rollback.** A bad kernel/module/firmware swap
  can leave a node needing a physical re-flash. Test on one node with
  `auto_update_overlay=y` before ever enabling it fleet-wide, and prefer
  a manual per-node update over blanket automatic enablement until you
  trust a given release.
- **Reboots are jittered** (1–15 minutes) after any apply, specifically so
  a fleet-wide update doesn't reboot every node — and drop the whole mesh,
  including any gateway — within moments of each other.
- **The signing key is what's trusted, not `update_url`.** Packages are
  verified before anything is unpacked (see [Package signing](#package-signing)),
  so a hostile update host can withhold updates but not install its own.
  Whoever holds the private signing key controls what every node runs as
  root — and so does anyone who can set `update_allow_unsigned=y` on a node.
