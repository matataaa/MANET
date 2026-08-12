#!/bin/bash
set -e

# Install tailscale if not present
if ! command -v tailscale &>/dev/null; then
    echo "Installing tailscale..."
    if command -v apt-get &>/dev/null; then
        curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.noarmor.gpg \
            -o /usr/share/keyrings/tailscale-archive-keyring.gpg 2>/dev/null
        curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.tailscale-keyring.list \
            -o /etc/apt/sources.list.d/tailscale.list 2>/dev/null
        apt-get update -qq
        apt-get install -y -qq tailscale
    else
        echo "No supported package manager found. Install tailscale manually."
        exit 1
    fi
fi

# Enable and start tailscaled
systemctl enable --now tailscaled 2>/dev/null || true
echo "Tailscale pre-install complete"
