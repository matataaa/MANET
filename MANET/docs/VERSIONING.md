# Versioning

This project has two separate versioning schemes that intentionally don't mix.

## SBC overlay version — `rootfs/etc/manet_version.txt`

Tracks the version of the externally vendored board overlay (kernel, modules,
firmware) — not something built from this repo. Manually maintained; see the
"Bumping the SBC overlay version" section in `MANET/packaging/README.md` for
the exact process.

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
