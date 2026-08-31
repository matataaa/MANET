#!/usr/bin/env bash
set -euo pipefail

# build-cm4-tarball.sh — assemble cm4-install.tar.gz from git repo content
# plus the CM4 SBC overlay (kernel, modules, firmware).
#
# Usage:
#   build-cm4-tarball.sh [output.tar.gz]
#   SBC_OVERLAY_DIR=/path/to/overlay build-cm4-tarball.sh [output.tar.gz]

OUT="${1:-cm4-install.tar.gz}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

DEFAULT_OVERLAY="$(cd "$SCRIPT_DIR/../.." && pwd)/kernel-work/packages/cm4-sbc-overlay"
# Presence of the directory is NOT enough: it is gitignored except for
# VENDORED_FROM.md, so a fresh clone leaves a directory that exists but holds
# no kernel layer. Testing -d alone silently produced tools-only tarballs.
if [ -z "${SBC_OVERLAY_DIR:-}" ]; then
    if [ -d "$DEFAULT_OVERLAY/usr/lib/modules" ]; then
        SBC_OVERLAY_DIR="$DEFAULT_OVERLAY"
        echo "Using default SBC overlay: $SBC_OVERLAY_DIR"
    elif [ "${ALLOW_TOOLS_ONLY:-0}" != "1" ]; then
        echo "ERROR: CM4 SBC overlay missing or empty: $DEFAULT_OVERLAY" >&2
        echo "       Expected $DEFAULT_OVERLAY/usr/lib/modules" >&2
        echo "" >&2
        echo "A CM4 tarball without the kernel layer has no mt7915e/morse/" >&2
        echo "dot11ah modules, so the node boots to stock Raspberry Pi OS" >&2
        echo "with no mesh radios. Vendor the overlay first:" >&2
        echo "  ./fetch-cm4-overlay.sh <path-to-cm4-install.tar.gz>" >&2
        echo "" >&2
        echo "Set ALLOW_TOOLS_ONLY=1 only if the base image already carries" >&2
        echo "the kernel, modules and firmware." >&2
        exit 1
    else
        echo "WARNING: SBC overlay not found — building tools-only tarball." >&2
        echo "         Base image must already have kernel/modules/firmware." >&2
    fi
fi

export SBC_OVERLAY_DIR="${SBC_OVERLAY_DIR:-}"
exec "$SCRIPT_DIR/build-rpi5-tarball.sh" "$OUT"
