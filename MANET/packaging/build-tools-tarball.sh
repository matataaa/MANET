#!/usr/bin/env bash
set -euo pipefail

# build-tools-tarball.sh — assemble a tools-only update tarball.
#
# Contains scripts, Go binary, web frontend, systemd units, and etc files.
# Does NOT include the SBC overlay (kernel/modules/firmware) or pre-built
# binaries (alfred, batctl, wpa_supplicant_s1g).
#
# Usage:
#   build-tools-tarball.sh [output.tar.gz]
#   Defaults to: tools.tar.gz

OUT="${1:-tools.tar.gz}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
STAGE="$(mktemp -d)"

cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT

install_tree() {
    local src="$1" dst="$2"
    [ -d "$src" ] || return 0
    mkdir -p "$dst"
    cp -a "$src"/. "$dst"/
}

install_file() {
    local mode="$1" src="$2" dst="$3"
    if [ -f "$src" ]; then
        mkdir -p "$(dirname "$dst")"
        install -m "$mode" "$src" "$dst"
    fi
}

mkdir -p "$STAGE/usr/local/bin" "$STAGE/etc"

# Scripts — all subdirs flatten into /usr/local/bin on the node
for subdir in core elections radio network system; do
    install_tree "$REPO_ROOT/MANET/scripts/$subdir" "$STAGE/usr/local/bin"
done
chmod -R a+rX "$STAGE/usr/local/bin"
find "$STAGE/usr/local/bin" -type f \
    \( -name '*.sh' -o -name '*.py' -o -name 'morse_cli' -o -name 'chronyc' -o -name 'mesh' \) \
    -exec chmod 0755 {} +

# Go binary + service
install_file 0755 "$REPO_ROOT/MANET/cmd/manet-ctrl/manet-ctrl" "$STAGE/usr/local/bin/manet-ctrl"
install_file 0644 "$REPO_ROOT/MANET/cmd/manet-ctrl/manet-ctrl.service" "$STAGE/etc/systemd/system/manet-ctrl.service"

# Web frontend
install_tree "$REPO_ROOT/MANET/www" "$STAGE/usr/local/share/manet/www"

install_file 0644 "$REPO_ROOT/MANET/etc/manet_version.txt" "$STAGE/etc/manet_version.txt"

mkdir -p "$(dirname "$OUT")"
tar --owner=0 --group=0 --numeric-owner -czf "$OUT" -C "$STAGE" .
echo "Built: $OUT  ($(du -sh "$OUT" | cut -f1))"
