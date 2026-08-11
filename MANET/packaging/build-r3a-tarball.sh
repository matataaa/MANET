#!/usr/bin/env bash
set -euo pipefail

# build-r3a-tarball.sh — assemble r3a-install.tar.gz from git repo content
# plus the Rock 3A SBC overlay (kernel, modules, firmware).
#
# Usage:
#   build-r3a-tarball.sh [output.tar.gz]
#   SBC_OVERLAY_DIR=/path/to/overlay build-r3a-tarball.sh [output.tar.gz]

OUT="${1:-r3a-install.tar.gz}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

DEFAULT_OVERLAY="$(cd "$SCRIPT_DIR/../.." && pwd)/kernel-work/packages/r3a-sbc-overlay"
if [ -z "${SBC_OVERLAY_DIR:-}" ]; then
    if [ -d "$DEFAULT_OVERLAY" ]; then
        SBC_OVERLAY_DIR="$DEFAULT_OVERLAY"
        echo "Using default SBC overlay: $SBC_OVERLAY_DIR"
    else
        echo "ERROR: SBC overlay not found." >&2
        echo "       Run kernel-work/real_work/build-r3a-sbc-overlay.sh first," >&2
        echo "       or set SBC_OVERLAY_DIR to an existing overlay directory." >&2
        exit 1
    fi
fi

export SBC_OVERLAY_DIR
exec "$SCRIPT_DIR/build-rpi5-tarball.sh" "$OUT"
