# Vendored SBC overlay — CM4

Extracted from the externally-hosted `cm4-install.tar.gz`, which is not
published anywhere in the very-srs/MANET GitHub repo. Contains the
kernel, DTBs, and driver modules (mt7915e, morse, dot11ah) this fork
depends on for CM4 WiFi + HaLow, none of which ship in stock Raspberry
Pi OS.

- Source: https://www.colorado-governor.com/manet/cm4-install.tar.gz
- Bundled version (from ./etc/manet_version.txt inside that tarball): 0.530 (08/2026)
- HTTP Last-Modified: Sun, 16 Aug 2026 14:39:18 GMT
- Vendored: 2026-08-17 22:41 UTC

## Re-checking for updates

No lightweight version endpoint exists on that host (`manet_version.txt`
alone 404s — the version file only lives inside the tarball). Re-run this
script (`fetch-overlay.sh`) to pick up whatever is currently hosted; it
regenerates this file with the new version/timestamp automatically. To
check without downloading the full ~46MB first, compare the current
Last-Modified header (`curl -sI https://www.colorado-governor.com/manet/cm4-install.tar.gz`) against the value above.
