#!/bin/bash

# Bring down any active WireGuard interfaces
for iface in $(wg show interfaces 2>/dev/null); do
    wg-quick down "$iface" 2>/dev/null || true
done

# Remove configs
rm -f /etc/mesh-wireguard.conf
rm -f /etc/wireguard/wg*.conf

echo "WireGuard post-remove complete"
