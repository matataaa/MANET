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
if [ -z "${SBC_OVERLAY_DIR:-}" ]; then
    if [ -d "$DEFAULT_OVERLAY" ]; then
        SBC_OVERLAY_DIR="$DEFAULT_OVERLAY"
        echo "Using default SBC overlay: $SBC_OVERLAY_DIR"
    else
        echo "WARNING: SBC overlay not found — building tools-only tarball." >&2
        echo "         Base image must already have kernel/modules/firmware." >&2
    fi
fi

export SBC_OVERLAY_DIR="${SBC_OVERLAY_DIR:-}"
exec "$SCRIPT_DIR/build-rpi5-tarball.sh" "$OUT"
