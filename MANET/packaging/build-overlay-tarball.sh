#!/usr/bin/env bash
set -euo pipefail

# build-overlay-tarball.sh — package a directory of SBC overlay content
# (kernel, modules, firmware, DTBs) into a root-relative tarball for the
# node-update overlay OTA channel. See MANET/docs/VERSIONING.md.
#
# The input directory must already be root-relative (e.g.
# kernel-work/packages/cm4-sbc-overlay, as produced by fetch-cm4-overlay.sh).
# Not needed for RPi5 — the existing rpi5-sbc-overlay-current GitHub Release
# asset is already in this shape and can be hosted as-is.
#
# Usage:
#   build-overlay-tarball.sh <overlay-dir> <output.tar.gz>

OVERLAY_DIR="${1:?usage: build-overlay-tarball.sh <overlay-dir> <output.tar.gz>}"
OUT="${2:?usage: build-overlay-tarball.sh <overlay-dir> <output.tar.gz>}"

[ -d "$OVERLAY_DIR" ] || { echo "Missing overlay dir: $OVERLAY_DIR" >&2; exit 1; }

STAGE="$(mktemp -d)"
cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT

cp -a "$OVERLAY_DIR"/. "$STAGE"/

# macOS: strip extended attributes and AppleDouble files before packing —
# see build-tools-tarball.sh for why.
export COPYFILE_DISABLE=1
if command -v xattr >/dev/null 2>&1; then
    xattr -rc "$STAGE" 2>/dev/null || true
fi
find "$STAGE" -name '._*' -delete 2>/dev/null || true

# Same directory-mode rules as build-tools-tarball.sh: never emit an entry
# for the stage root itself (its mode would land on / on the node), and
# strip group/other write inherited from the build machine's umask.
find "$STAGE" -type d -exec chmod go-w {} +

mkdir -p "$(dirname "$OUT")"
OUT_ABS="$(cd "$(dirname "$OUT")" && pwd)/$(basename "$OUT")"
( cd "$STAGE" && find . -mindepth 1 -maxdepth 1 -printf './%P\0' \
    | tar --owner=0 --group=0 --numeric-owner --null -T - -czf "$OUT_ABS" )
echo "Built: $OUT  ($(du -sh "$OUT" | cut -f1))"
