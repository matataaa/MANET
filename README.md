# MANET Project

This repository contains a complete software suite for provisioning, configuring, and orchestrating Mobile Ad-hoc Network (MANET) nodes on Single Board Computers (SBCs).

The project transforms hardware like a Rock3a or a Raspberry Pi CM4 (recommended) into self-forming, self-healing mesh nodes using **B.A.T.M.A.N. Advanced** (Layer 2 routing) and **802.11s / 802.11ah HaLow** (Layer 1/2). It features orchestration for automatic addressing and channel selection, partition healing, jamming detection, and decentralized service elections.

## Key Features

* **Mesh Networking**: batman-adv (BATMAN_V) L2 routing over 802.11ah HaLow (sub-GHz, long range) and standard 802.11ax/ac/n radios. Self-forming, self-healing mesh with SAE (WPA3) authentication.
* **Zero-Configuration**: Distributed IPv4 allocation, automatic gateway detection with NAT failover, `.mesh` DNS for all devices, mesh-wide hostname resolution.
* **Web Interface**: Single-page dashboard with topology visualization, node management, live terminal, performance testing, service controls, and built-in documentation. HTTPS with auto-generated TLS.
* **Push-to-Talk Voice**: Opus codec over multicast RTP with hardware PTT (OpenVLM USB, GPIO), web PTT, half-duplex detection, jitter buffer, and QoS (DSCP EF + WMM AC_VO + tc prio qdisc).
* **Applet System**: Installable mesh applications with DNS integration — applets declare `.mesh` hostnames that auto-redirect to their frontend.
* **EUD Support**: Connect phones/laptops via 5 GHz WiFi AP or Ethernet. DHCP, DNS, and applet hostnames all work transparently for connected devices.
* **Situational Awareness**: GPS integration with CoT/ATAK blue-force tracking, mesh-wide position sharing.
* **CLI Tool**: `mesh` command for status, node listing, config, radio info, services, and performance testing.

See [docs/features.md](docs/features.md) for the complete feature list.

## Repository Structure

* **`provisioning/`**: Scripts and templates for flashing the OS image.
* **`node_tools/`**: The runtime logic for the node. Contains the scripts that run the mesh, including:
    * `node-manager`: The core orchestrator for cooperative mesh functions.
    * `radio-setup.sh`: Initial provisioning tool.
    * `mesh-registry-builder.sh`: Decodes gossip data (via Alfred) to build a map of the network.
* **`binaries_arm64/`**: Pre-compiled custom binaries for ARM64, including `alfred`, `batctl`, and a modified `wpa_supplicant` for HaLow support.

## Supported Hardware

| Hardware | Support Level | Notes |
| :--- | :--- | :--- |
| **Compute Module 4 (CM4)** | Functional, primary dev target | Supports 802.11ax + HaLow. |
| **Raspberry Pi 4B** | Untested |  |
| **Raspberry Pi 5** | Functional | Supports 802.11ax + HaLow |
| **Radxa Rock 3A** | Functional | Supports 802.11ax + HaLow. |

## Getting Started

### 1. Prerequisites
You will need a supported SBC and a Linux or Windows machine to flash from, and ethernet Internet access for the SBC being flashed. 

See [/provisioning/README.md](MANET/provisioning/README.md) for detailed requirements and download links.

### 2. Provisioning a Node
1.  Navigate to the `provisioning` directory.
2.  Run the flashing script appropriate for your host OS (`linux.sh` or `windows.ps1`).
3.  Load a saved config or follow the interactive prompts to configure:
    * **EUD Connection**: Wired, Wireless (local AP), or Auto.
    * **Optional Services**: MediaMTX, (Mumble is untested).
    * **Mesh Security**: SSID and SAE Password.
    * **Network Settings**: CIDR blocks and addressing.

### 3. First Boot
Insert the storage media into the node and power it on. The `firstrun.sh` script will, over the course of a few reboots:
1.  Disable default setup wizards.
2.  Wait for internet connectivity (via Ethernet) to download the latest kernel and tools.
3.  Install necessary packages (`batctl`, `alfred`, `wpa_supplicant`, etc.).
4.  Configure the radio interfaces.
5.  Result in a fully functional mesh node

## Connectivity Modes

The nodes support connecting external devices (End User Devices) in three ways:

* **Wired**: Connect via Ethernet. The node acts as a bridge or gateway depending on upstream internet access.
* **Wireless**: The node broadcasts a local 5GHz AP (separate from the mesh backhaul) for clients to join.
* **Auto**: Default behavior. Acts as "Wireless" unless an Ethernet device is detected, then switches priority to "Wired".

## Credits

This project is a fork of [very-srs/MANET](https://github.com/very-srs/MANET) with additional contributions from [quietprotocol/MANET](https://github.com/quietprotocol/MANET).

## Documentation
* [Feature List](docs/features.md)
* [Node Architecture](docs/node-architecture.md)
* [Provisioning Guide](MANET/provisioning/README.md)
* [Binary Details](MANET/binaries_arm64/README.md)
