#!/bin/bash

# Disconnect tailscale
tailscale down 2>/dev/null || true

# Stop tailscaled service
systemctl stop tailscaled 2>/dev/null || true
systemctl disable tailscaled 2>/dev/null || true

# Remove config
rm -f /etc/mesh-tailscale.conf

echo "Tailscale post-remove complete"
