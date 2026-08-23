# Versioning

This project has four separate versioning schemes that intentionally don't
mix.

## Detect vs. apply, and the bandwidth gate

`node-update` always checks both channels below on its normal cadence (every
6h, plus immediately when Updates settings are saved), regardless of the
`auto_update`/`auto_update_overlay` flags — availability is recorded to
`/var/run/manet_update_status.json` either way, which `manet-ctrl` exposes at
`GET /api/admin/update-status` and the web UI shows as a persistent "Update
Available" banner. The flags only control what happens next:

- **Automatic** (`auto_update`/`auto_update_overlay` = `y`): applies only if
  also within `auto_update_min_mbps` (mesh.conf key, default `10`) of the
  node's current best uplink throughput — queried from `manet-ctrl`'s local
  status endpoint (`uplink_mbps`/`uplink_type`, itself derived from the same
  batman-adv throughput estimate behind the topology "Real Rate" column). A
  wired/gateway uplink always passes; an unreachable `manet-ctrl` fails
  closed (no automatic apply, not an assumed-fine default).
- **Manual**: `POST /api/admin/update-now` (per node, from the banner's
  "Update Now" button) or `POST /api/admin/force-update` (Fleet Control's
  "Force Update All Nodes," broadcast to every node via the same Alfred
  gossip mechanism config-push uses — see `fleet.go`) applies unconditionally
  if available, bypassing both the flag and the bandwidth gate. The UI shows
  a bandwidth/time warning before either action is confirmed.

Both paths funnel through the same `SIGUSR1`-triggered apply in
`node-update` — there's one download/extract/reboot code path per channel,
not a separate one for automatic vs. manual.

## SBC overlay version — `rootfs/etc/manet_version.txt`

Tracks the version of the externally vendored board overlay (kernel, modules,
firmware) — not something built from this repo. Manually maintained; see the
"Bumping the SBC overlay version" section in `MANET/packaging/README.md` for
the exact process. This file is purely informational — it is never read by
`node-update` to decide whether an OTA update is available (see "Release
version" below).

## Overlay OTA version — per-board `{board}-overlay-version.txt`, drives overlay auto-update

The SBC overlay (kernel, modules, firmware) is "not a version we invent" (see
above) — it mirrors whatever the vendor or CI stamps, using the existing
plain-decimal numbering (`0.538`, not `X.Y.Z`). Unlike `manet_version.txt`,
which is a single file shared by both boards, the overlay OTA channel is
**per board**, since RPi5 and CM4 overlays come from different sources and
genuinely different content:

- Remote, hosted at `update_url` alongside everything else: `rpi5-overlay-version.txt`
  / `cm4-overlay-version.txt` (plain decimal) and `rpi5-sbc-overlay.tar.gz` /
  `cm4-sbc-overlay.tar.gz` (root-relative, overlay content only — no
  universal rootfs, no Go binaries).
- Local, on the node: `/etc/manet_overlay_version.txt` — written by
  `node-update` itself immediately after a successful overlay extraction
  (not shipped inside the tarball). This is deliberately decoupled from the
  RPi5 asset contract and from `fetch-cm4-overlay.sh`'s vendoring logic: the
  daemon just trusts "I downloaded version X from the URL and extraction
  succeeded" as the new installed-version record.

See `MANET/packaging/README.md` → "Publishing an overlay OTA update" for the
concrete per-board publish steps, and `MANET/packaging/build-overlay-tarball.sh`
for the CM4 packaging step.

This channel is gated by a separate config key, `auto_update_overlay`,
default off — a kernel/module/firmware swap has no A/B boot slot or rollback
in this codebase, so it's opt-in per node rather than riding the same
`auto_update` toggle as the software stack.

## Release version — `manet_release_version.txt`, drives auto-update

A separate, deliberate "this is ready to ship to the fleet" marker, cut by a
human — not implied by ordinary commits landing on `main`. Tag lineage:
`release-vX.Y.Z`.

```sh
git tag release-v1.4.0
git push origin release-v1.4.0
```

`MANET/packaging/build-tools-tarball.sh` only writes
`etc/manet_release_version.txt` into the tarball when the build is run from
an exact `release-v*` tag (`git describe --exact-match`). An ad hoc or
in-between-releases build omits the file entirely, so it can never advance a
node's installed release version or trigger an auto-update.

The `node-update` service (`MANET/src/node-update/main.go`) compares its
local `/etc/manet_release_version.txt` against `{update_url}/manet_release_version.txt`
as plain `X.Y.Z` and only considers an update available when the remote
version is strictly greater — never on equal, and never on a remote version
that's lower (no flapping, no accidental downgrade). See "Detect vs. apply"
above for what happens once it's available.

## Software stack version — everything built from this repo

Every Go service built by `MANET/packaging/build-tools-tarball.sh`, plus the
Android app (`MANET/src/mesh-ctrl-android`), is versioned independently via
`git describe`, driven by per-component git tags — never hand-edited.

### Tag convention

`<component>-vX.Y.Z`, one tag lineage per component:

`manet-ctrl`, `node-manager`, `atak-overlay`, `battery-reader`, `cot-emitter`,
`gateway-manager`, `gps-reader`, `halow-mcs-summary`, `mesh-chat`,
`mesh-manager`, `mesh-radio-state`, `mesh-registry`, `android`.

(`mesh-voice` is excluded — it ships as a pre-built binary, not compiled from
this repo.)

### Cutting a release for a component

```sh
git tag manet-ctrl-v1.2.3
git push origin manet-ctrl-v1.2.3
```

Until a component has its first tag, its version falls back to a bare
abbreviated commit hash (`git describe --always`) — still traceable to an
exact commit, just not a round number.

### Where each version shows up

- **manet-ctrl**: `GET /api/version`, the web UI footer (which fetches that
  endpoint — the web UI has no separate version of its own), and
  `manet-ctrl -version` over SSH.
- **Every other Go service**: printed at startup in the systemd journal
  (`log.Printf("starting (version %s)", Version)`), and via `<binary>
  -version` for on-node diagnostics.
- **Android app**: APK `versionName` (from `git describe --match
  "android-v*"`) and `versionCode` (total commit count — monotonically
  increasing regardless of tagging discipline, satisfying Android's
  requirement).

### Why git-describe instead of a hand-maintained file

A hand-edited version file can drift silently — exactly what happened with
`MANET/version.txt` before it was removed (stale for months, out of sync with
the file actually in use). `git describe` always reflects the actual commit a
binary was built from, with zero manual bump step required for the version
string itself to change between releases.
