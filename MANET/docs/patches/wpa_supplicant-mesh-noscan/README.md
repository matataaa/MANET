# Vendored copies — OpenWrt `noscan`/mesh-noscan patches

Archived here 2026-08-27 so this repo doesn't depend on GitHub staying up or
`openwrt/openwrt` keeping these files at this path. Source of truth is still
upstream OpenWrt; re-fetch from there if a newer revision is ever needed:

- `300-noscan.patch` — https://github.com/openwrt/openwrt/blob/master/package/network/services/hostapd/patches/300-noscan.patch
  (Felix Fietkau, 2010-01-20, "Add noscan, no_ht_coex config options")
- `301-mesh-noscan.patch` — https://github.com/openwrt/openwrt/blob/master/package/network/services/hostapd/patches/301-mesh-noscan.patch
  (Daniel Golle, 2018-04-20, "Allow HT40 also on 2.4GHz if noscan option is
  set, which also skips secondary channel scan just like noscan works in AP
  mode")

Both fetched and archived verbatim (not summarized) on 2026-08-27. See
`../../wpa-supplicant-mesh-noscan.md` for what these do, why they're the fix
for the 5GHz mesh channel-width/ACS problem documented in `../../ACS.md`,
and the build plan for actually using them.

**Apply order matters — 301 depends on 300.** `301`'s `mesh.c` hunk reads
`conf->noscan`, a field on `struct hostapd_config` that only exists because
`300` adds it (`src/ap/ap_config.h`). Apply `300` first, then `301`, against
the same source tree.
