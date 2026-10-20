#!/usr/bin/env bash
# check-morse-bcfs.sh — fail when an SBC overlay carries modified Morse BCFs
# or lacks the Morse license. Morse Micro licenses the BCFs for distribution
# complete and unmodified, with the license alongside; the node applies the
# TX power policy at boot (manet-morse-bcf.py). An overlay with no Morse
# firmware passes. Re-vendor with fetch-cm4-overlay.sh to fix a failure.
#
# Usage: check-morse-bcfs.sh <overlay-dir>
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MORSE_DIR="${1:?usage: check-morse-bcfs.sh <overlay-dir>}/usr/lib/firmware/morse"
[ -d "$MORSE_DIR" ] || exit 0

if ! python3 -I "$SCRIPT_DIR/../rootfs/usr/local/bin/manet-morse-bcf.py" \
        --directory "$MORSE_DIR" --check-stock; then
    echo "ERROR: $MORSE_DIR has modified Morse BCFs; re-vendor it with fetch-cm4-overlay.sh" >&2
    exit 1
fi
if [ ! -f "$MORSE_DIR/LICENSE" ]; then
    echo "ERROR: $MORSE_DIR has no Morse LICENSE; re-vendor it with fetch-cm4-overlay.sh" >&2
    exit 1
fi
