#!/bin/bash
set -e

# Install wireguard-tools if not present
if ! command -v wg &>/dev/null; then
    echo "Installing wireguard-tools..."
    if command -v apt-get &>/dev/null; then
        apt-get update -qq
        apt-get install -y -qq wireguard-tools
    else
        echo "No supported package manager found. Install wireguard-tools manually."
        exit 1
    fi
fi

# Ensure wireguard kernel module is available
modprobe wireguard 2>/dev/null || true
echo "WireGuard pre-install complete"
