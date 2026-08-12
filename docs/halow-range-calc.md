# HaLow (802.11ah) Link Budget & Range Calculator

## System Parameters

| Parameter | CM4 Node (MM6108 SPI) | x86 Gateway (MM8108 USB) |
|-----------|----------------------|--------------------------|
| TX Power | 24 dBm (251 mW) | 21 dBm (126 mW) |
| Frequency | 908 MHz | 908 MHz |
| Channel | S1G ch12 (iw reports 5180 MHz) | S1G ch12 |
| Bandwidth | 1 MHz primary | 1 MHz primary |
| Antenna Gain | ~2 dBi (MMCX whip) | ~2 dBi (MMCX whip) |
| Antenna Connector | MMCX → SMA pigtail | USB dongle integrated |
| Cable Loss | ~1 dB (8" pigtail) | ~0 dB (integrated) |
| Driver | v1.16.4 (Morse SPI) | v2.0 (Morse USB) |
| Receiver Sensitivity | -97 dBm @ MCS0 1MHz | -97 dBm @ MCS0 1MHz |

## Link Budget

### Free Space Path Loss (FSPL)

    FSPL(dB) = 20·log10(d) + 20·log10(f) - 27.55

Where d = distance in meters, f = frequency in MHz.

At 908 MHz:

| Distance | FSPL (dB) | Margin above -97 dBm (24 dBm TX) |
|----------|-----------|-----------------------------------|
| 100 m | 51.6 | 45.4 dB |
| 500 m | 65.6 | 31.4 dB |
| 1 km | 71.6 | 25.4 dB |
| 5 km | 85.6 | 11.4 dB |
| 10 km | 91.6 | 5.4 dB |
| 15 km | 95.1 | 1.9 dB |

### EIRP

    EIRP = TX Power + Antenna Gain - Cable Loss

| Node Type | EIRP |
|-----------|------|
| CM4 | 24 + 2 - 1 = **25 dBm** |
| Gateway | 21 + 2 - 0 = **23 dBm** |

### Received Power

    P_rx = EIRP - FSPL + Rx_Antenna_Gain

For CM4-to-CM4 (25 dBm EIRP, 2 dBi Rx gain):

    P_rx = 25 - FSPL + 2 = 27 - FSPL

For Gateway-to-CM4 (23 dBm EIRP, 2 dBi Rx gain):

    P_rx = 23 - FSPL + 2 = 25 - FSPL

## Observed Signal Levels (Lab / Indoor)

From mesh station dumps during testing:

| Path | Signal | Distance (est.) |
|------|--------|-----------------|
| Gateway → RADIO1 | -16 to -25 dBm | ~3 m (same room) |
| Gateway → RADIO2 | -36 dBm | ~8 m (adjacent room) |
| RADIO1 → RADIO2 | -28 to -32 dBm | ~5 m |

## Practical Range Estimates

Sub-GHz (908 MHz) benefits from better propagation vs 2.4/5 GHz:
- Longer wavelength = better diffraction around obstacles
- Lower free-space loss per meter
- Better wall/foliage penetration

### Line of Sight

| MCS | Data Rate (1 MHz) | Sensitivity | Max Range (CM4↔CM4) | Max Range (GW↔CM4) |
|-----|-------------------|-------------|---------------------|---------------------|
| MCS0 | 300 kbps | -97 dBm | ~15 km | ~12 km |
| MCS2 | 600 kbps | -93 dBm | ~10 km | ~8 km |
| MCS4 | 1.2 Mbps | -88 dBm | ~6 km | ~5 km |
| MCS7 | 650 kbps (VHT) | -82 dBm | ~3 km | ~2.5 km |

### Obstructed (Urban / Forest)

Add 10-20 dB attenuation per obstruction layer. Typical deductions:

| Obstacle | Additional Loss |
|----------|----------------|
| Interior drywall | 3-5 dB |
| Exterior brick/concrete wall | 10-15 dB |
| Dense foliage (per 10 m) | 5-8 dB |
| Vehicle body | 8-12 dB |
| Building floor | 15-20 dB |

Practical urban range (MCS0): **500 m - 2 km** depending on obstructions.

## Fade Margin

For reliable links, reserve 10-20 dB fade margin above receiver sensitivity:

| Reliability | Fade Margin |
|-------------|-------------|
| 90% | 10 dB |
| 99% | 20 dB |
| 99.9% | 30 dB |

### Reliable Range (20 dB fade margin)

Effective sensitivity = -97 + 20 = **-77 dBm**

| Scenario | CM4↔CM4 | GW↔CM4 |
|----------|---------|--------|
| LOS | ~5 km | ~4 km |
| Light urban | ~1-2 km | ~800 m - 1.5 km |
| Dense urban / indoor | ~200-500 m | ~150-400 m |

## Mesh Hop Penalty

Each batman-adv mesh hop adds latency and halves throughput (half-duplex). For planning:

| Hops | Effective Throughput | Added Latency |
|------|---------------------|---------------|
| 1 (direct) | ~300 kbps (MCS0) | 5-15 ms |
| 2 | ~150 kbps | 15-30 ms |
| 3 | ~75 kbps | 30-60 ms |

## Quick Reference Formula

To estimate range for a given target RSSI:

    d(km) = 10^((EIRP + Rx_Gain - Target_RSSI - 20·log10(908) + 27.55) / 20) / 1000

Example: CM4 node, target -77 dBm (99% reliability):

    d = 10^((25 + 2 - (-77) - 59.16 + 27.55) / 20) / 1000
    d = 10^(72.39 / 20) / 1000
    d = 10^3.62 / 1000
    d ≈ 4.2 km

## Impact of Channel Bandwidth

802.11ah supports 1, 2, 4, and 8 MHz channel widths. Wider channels increase throughput but reduce range — the receiver must integrate noise across a wider band, raising the noise floor by 3 dB per bandwidth doubling.

### Noise Floor & Sensitivity by Bandwidth

Thermal noise floor at 25°C: **-174 dBm/Hz**

| Bandwidth | Noise BW | Noise Floor | MCS0 Sensitivity | Range vs 1 MHz |
|-----------|----------|-------------|-------------------|----------------|
| 1 MHz | 60 dBHz | -114 dBm | -97 dBm | baseline |
| 2 MHz | 63 dBHz | -111 dBm | -94 dBm | ~71% (−3 dB) |
| 4 MHz | 66 dBHz | -108 dBm | -91 dBm | ~50% (−6 dB) |
| 8 MHz | 69 dBHz | -105 dBm | -88 dBm | ~35% (−9 dB) |

Each 3 dB sensitivity loss halves the power margin, cutting range by ~30%.

### Throughput by Bandwidth (MCS0, single spatial stream)

| Bandwidth | PHY Rate | Typical UDP | Typical TCP |
|-----------|----------|-------------|-------------|
| 1 MHz | 300 kbps | ~250 kbps | ~200 kbps |
| 2 MHz | 650 kbps | ~550 kbps | ~450 kbps |
| 4 MHz | 1.35 Mbps | ~1.1 Mbps | ~900 kbps |
| 8 MHz | 2.93 Mbps | ~2.4 Mbps | ~1.9 Mbps |

Higher MCS rates scale proportionally with bandwidth.

### Max LOS Range by Bandwidth (CM4 node, 25 dBm EIRP)

| Bandwidth | MCS0 Max Range | MCS4 Max Range | 99% Reliable (20 dB margin) |
|-----------|----------------|----------------|----------------------------|
| 1 MHz | ~15 km | ~6 km | ~5 km |
| 2 MHz | ~10 km | ~4 km | ~3.5 km |
| 4 MHz | ~7 km | ~2.8 km | ~2.5 km |
| 8 MHz | ~5 km | ~2 km | ~1.7 km |

### Bandwidth Selection Guide

| Use Case | Bandwidth | Why |
|----------|-----------|-----|
| Max range / rural relay | 1 MHz | Lowest noise floor, best sensitivity |
| Balanced (our default) | 1 MHz | Range is the priority for mesh nodes |
| Moderate range + throughput | 2 MHz | Good compromise for semi-urban |
| Video / high-throughput short range | 4 MHz | Enough for low-bitrate video streams |
| LAN replacement / campus | 8 MHz | Max throughput, sub-km distances |

### Spectrum Considerations (US 902-928 MHz)

The US ISM band at 902-928 MHz provides 26 MHz of spectrum.

| Bandwidth | Channels Available | Co-channel Nodes | Frequency Reuse |
|-----------|--------------------|------------------|-----------------|
| 1 MHz | ~24 non-overlapping | Many | Excellent |
| 2 MHz | ~12 non-overlapping | Moderate | Good |
| 4 MHz | ~6 non-overlapping | Limited | Fair |
| 8 MHz | ~3 non-overlapping | Very limited | Poor |

Narrower channels also mean less interference from other ISM devices (LoRa, Zigbee, meters) sharing the band.

### Current Deployment Config

Our mesh uses **1 MHz** (S1G channel 12, 908 MHz center). This maximizes range at the cost of throughput — appropriate for CoT/SA data, voice (Codec2 at 1200-3200 bps), and mesh control traffic. Upgrading to 2 MHz would roughly double throughput but cut reliable range by ~30%.
