# Vendored SBC overlay — CM4

Extracted from the externally-hosted `cm4-install.tar.gz`, which is not
published anywhere in the very-srs/MANET GitHub repo. Contains the
kernel, DTBs, and driver modules (mt7915e, morse, dot11ah) this fork
depends on for CM4 WiFi + HaLow, none of which ship in stock Raspberry
Pi OS.

- Source: https://www.colorado-governor.com/manet/cm4-install.tar.gz
- Bundled version (from ./etc/manet_version.txt inside that tarball): 0.538 (08/2026)
- HTTP Last-Modified: Sat, 22 Aug 2026 03:01:56 GMT
- Vendored: 2026-08-17 22:44 UTC
- Last checked: 2026-08-22 — downloaded the full tarball and diffed it
  against everything vendored here (kernel8.img, all DTBs, mm610x-spi.dtbo,
  usr/lib/modules, usr/lib/firmware/morse): every file was byte-identical,
  so no re-vendor was needed. The tarball's own bundled version had moved
  0.530 -> 0.538 upstream, but that bump lives entirely in files this
  overlay deliberately doesn't take (the vendor's own userspace/services,
  which this fork has rewritten independently) — updated the version/
  Last-Modified fields above to match what's now hosted, without touching
  the vendored files themselves.

## Re-checking for updates

No lightweight version endpoint exists on that host (`manet_version.txt`
alone 404s — the version file only lives inside the tarball). Re-run
`MANET/packaging/fetch-cm4-overlay.sh` to pick up whatever is currently
hosted; it regenerates this file with the new version/timestamp
automatically (and overwrites the `Last checked` line above). To check
without downloading the full ~50MB first, compare the current
Last-Modified header
(`curl -sI https://www.colorado-governor.com/manet/cm4-install.tar.gz`) against the value above.
