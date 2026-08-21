# Syncing with upstream (very-srs/MANET)

This repo (`mattronix/MANET`) is a real GitHub fork of `very-srs/MANET`,
created 2026-08-11. On that same day the repo went through two structural
rewrites, both git-tracked as renames:

1. `a783d48` — "Restructure node_tools into semantic dirs" —
   `MANET/node_tools/*` → `MANET/scripts/{core,elections,network,radio,system}/*`
2. `5fa0be6` — "Restructure repo to mirror target filesystem layout" —
   `MANET/scripts/*`, `MANET/etc/*`, `MANET/systemd*`, `MANET/udev/*`,
   `MANET/networkd-dispatcher/*`, `MANET/root/*` → `MANET/rootfs/{etc,root,usr}/...`

Separately (not a rename — a rewrite, so git can't bridge it), several
Python/bash components were replaced with compiled Go services under
`MANET/src/`: the web server (`manet-ctrl`), `node-manager`, `node-update`,
`battery-reader`, `gps-reader`, `halow-mcs-summary`, `mesh-radio-state`,
`mesh-registry`, `gateway-manager`, `cot-emitter`, `mesh-voice`. Upstream
still has these as `.py`/`.sh` under `node_tools/` (upstream's voice PTT
daemon is `node_tools/mesh-voice.py`, Python/GStreamer/Lyra — architecturally
unrelated to the fork's compiled Go `mesh-voice`, so upstream voice fixes
need a manual read-and-translate, not a cherry-pick).

Because of this, `origin/main` and `upstream/main` can't be diffed directly
— paths (and for the Go pieces, languages) differ. But since both restructure
commits are proper git renames, **`git log --follow` on a current fork path
still bridges all the way back into the shared upstream history** for
anything that's still a script/config file.

## Checking a specific file

```
./check-upstream-sync.sh MANET/rootfs/usr/local/bin/radio-setup.sh
```

Walks the fork's rename history for that path, finds the newest hop that
still exists on `upstream/main`, and lists upstream commits on that path
since the fork date. Two outcomes:

- **Finds a matching upstream path** → still comparable. Read the listed
  commits, `git show <sha>` to see what changed, `git cherry-pick <sha>`
  to try applying it directly (expect normal merge conflicts where both
  sides evolved the file independently — resolve like any git conflict).
- **No historical path exists on upstream** → either introduced fork-side
  after 2026-08-11, or it's one of the Go rewrites above. No mechanical
  comparison possible — instead, manually check upstream's activity on the
  *old* path it replaced (e.g. `git log upstream/main -- MANET/node_tools/node-manager.sh`
  for `MANET/src/node-manager/`) and port relevant fixes by hand.

## Setup (once per clone)

```
git remote add upstream https://github.com/very-srs/MANET.git
git fetch upstream
```

## Last reviewed

Upstream commit reviewed up to: `695ca46` (2026-08-20) — "Back off pipeline
restarts and stop logging the same error for ever"

2026-08-21 pass found one actionable, mechanically-portable fix not yet in
the fork: `70dc3c6` "Fix mesh time sync and wire GPS in as a time source"
fixes real bugs also present in the fork's `provision-mesh.sh` /
`ethernet-autodetect.sh` / `one-shot-time-sync.sh` — invalid `offline`
directive in chrony-default.conf, chrony-default.conf written to the wrong
path (`/etc/chrony-default.conf` vs `/etc/chrony/chrony-default.conf`),
`one-shot-time-sync.service` never actually enabled, and
`one-shot-time-sync.sh` bursting a source chronyd was never told about. Not
yet ported — see conversation from that date. Everything else in the range
(the voice PTT/Lyra feature set, journald persistence — already fixed
independently in the fork's own `journald.conf.d/manet.conf`) needs no
action.

Update this line after each review pass so `git log upstream/main --oneline
<last-reviewed-sha>..upstream/main` shows only what's new.
