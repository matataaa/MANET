#!/usr/bin/env bash
set -euo pipefail

# deploy-x86.sh — deploy MANET stack to an x86_64 Linux node.
#
# Usage:
#   deploy-x86.sh <tarball> <host> [ssh-user]
#
# Examples:
#   deploy-x86.sh x86-install.tar.gz root@192.168.70.46
#   deploy-x86.sh x86-install.tar.gz 192.168.70.46 root
#
# Prerequisites on the target:
#   - Debian/Ubuntu with systemd-networkd
#   - batman-adv kernel module (apt install batctl)
#   - wpa_supplicant_s1g (Morse Micro) in /usr/sbin/
#   - alfred (apt install alfred)

TARBALL="${1:?Usage: deploy-x86.sh <tarball> <host> [ssh-user]}"
HOST="${2:?Usage: deploy-x86.sh <tarball> <host> [ssh-user]}"
SSH_USER="${3:-root}"

if [ ! -f "$TARBALL" ]; then
    echo "ERROR: Tarball not found: $TARBALL" >&2
    exit 1
fi

SSH_TARGET="${SSH_USER}@${HOST}"
# Strip user@ if already in HOST
[[ "$HOST" == *@* ]] && SSH_TARGET="$HOST"

echo "=== Deploying MANET x86 tarball to $SSH_TARGET ==="

echo "Uploading tarball..."
scp -o StrictHostKeyChecking=no "$TARBALL" "${SSH_TARGET}:/tmp/manet-x86-install.tar.gz"

echo "Installing on target..."
ssh -o StrictHostKeyChecking=no "$SSH_TARGET" bash -s <<'REMOTE'
set -euo pipefail

echo "Extracting tarball to /..."
tar -xzf /tmp/manet-x86-install.tar.gz -C /
rm -f /tmp/manet-x86-install.tar.gz

# Fix ownership for networkd-dispatcher (refuses scripts not owned by root)
for d in /etc/networkd-dispatcher /etc/networkd-dispatcher/*/; do
    [ -d "$d" ] && chown root:root "$d" && chmod 755 "$d"
done
for f in /etc/networkd-dispatcher/*/* ; do
    [ -f "$f" ] && chown root:root "$f"
done

# Fix sudoers ownership
[ -d /etc/sudoers.d ] && chown root:root /etc/sudoers.d /etc/sudoers.d/* 2>/dev/null || true
chmod 750 /etc/sudoers.d 2>/dev/null || true
chmod 440 /etc/sudoers.d/* 2>/dev/null || true

# Make scripts executable
chmod +x /usr/local/bin/*.sh /usr/local/bin/*.py 2>/dev/null || true
chmod +x /usr/local/bin/manet-ctrl /usr/local/bin/mesh-voice 2>/dev/null || true

# Ensure batman-adv loads at boot
echo "batman_adv" > /etc/modules-load.d/batman.conf
modprobe batman_adv 2>/dev/null || true

# Enable systemd-networkd if not already
systemctl enable systemd-networkd 2>/dev/null || true

# Apply networkd configs
networkctl reload 2>/dev/null || systemctl restart systemd-networkd

# Set bat0/br0 MTU immediately if interfaces exist
ip link show bat0 &>/dev/null && ip link set bat0 mtu 1400
ip link show br0 &>/dev/null && ip link set br0 mtu 1400

# Reload systemd and enable services
systemctl daemon-reload

for unit in \
    gateway-route-manager.service \
    sae-watchdog.service \
    manet-ctrl.service
do
    if [ -f "/etc/systemd/system/$unit" ]; then
        systemctl enable "$unit" 2>/dev/null || true
        systemctl restart "$unit" 2>/dev/null || true
        echo "  Enabled + restarted: $unit"
    fi
done

echo ""
echo "=== Deploy complete ==="
echo "  bat0 MTU: $(ip link show bat0 2>/dev/null | grep -oP 'mtu \K\d+' || echo 'N/A')"
echo "  br0  MTU: $(ip link show br0 2>/dev/null | grep -oP 'mtu \K\d+' || echo 'N/A')"
echo "  manet-ctrl: $(systemctl is-active manet-ctrl 2>/dev/null)"
echo "  sae-watchdog: $(systemctl is-active sae-watchdog 2>/dev/null)"
REMOTE

echo "=== Done ==="
