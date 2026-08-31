#!/bin/bash
#
#  macOS provisioning script for MANET mesh radio nodes
#  Builds a ready-to-flash .img file with mesh config baked in.
#  Flash the output with Balena Etcher, rpi-imager, or dd.
#
set -e

# --- Configuration ---
TEMPLATE_FILE="firstrun.sh.template"
CONFIG_DIR=".mesh-configs"
CACHE_DIR=".image-cache"

PI_OS_IMAGE_URL="https://downloads.raspberrypi.com/raspios_lite_arm64/images/raspios_lite_arm64-2025-10-02/2025-10-01-raspios-trixie-arm64-lite.img.xz"
PI_OS_IMAGE_XZ="raspios-trixie-arm64-lite.img.xz"
PI_OS_IMAGE="raspios-trixie-arm64-lite.img"

# --- Helper Functions ---

validate_regulatory_domain() {
    local domain=$1
    local valid_domains=(
        "US" "CA" "GB" "DE" "FR" "IT" "ES" "NL" "BE" "AT" "CH" "SE" "NO" "DK" "FI"
        "PL" "CZ" "HU" "GR" "PT" "IE" "RO" "BG" "HR" "SI" "SK" "LT" "LV" "EE" "CY"
        "MT" "LU" "AU" "NZ" "JP" "KR" "TW" "SG" "MY" "TH" "PH" "ID" "VN" "IN" "CN"
        "BR" "AR" "MX" "CL" "CO" "PE" "ZA" "IL" "AE" "SA" "RU" "UA" "TR" "EG"
    )
    domain=$(echo "$domain" | tr '[:lower:]' '[:upper:]')
    for valid in "${valid_domains[@]}"; do
        if [ "$domain" == "$valid" ]; then
            echo "$domain"
            return 0
        fi
    done
    return 1
}

uses_eu_halow_region() {
    local domain
    domain=$(echo "$1" | tr '[:lower:]' '[:upper:]')
    local eu_halow_domains=(
        "AT" "BE" "BG" "HR" "CY" "CZ" "DK" "EE" "FI" "FR" "DE" "GR" "HU" "IE"
        "IT" "LV" "LT" "LU" "MT" "NL" "PL" "PT" "RO" "SK" "SI" "ES" "SE"
        "GB" "CH" "NO"
    )
    for eu_halow_domain in "${eu_halow_domains[@]}"; do
        if [ "$domain" == "$eu_halow_domain" ]; then
            return 0
        fi
    done
    return 1
}

halow_regulatory_domain_for_wifi_domain() {
    local domain
    domain=$(echo "$1" | tr '[:lower:]' '[:upper:]')
    if uses_eu_halow_region "$domain"; then
        echo "EU"
    else
        echo "$domain"
    fi
}

# Ground-truth legal HaLow channel list per regulatory domain + bandwidth,
# kept byte-consistent with radio-setup.sh's halow_channel_valid() (see
# MANET/rootfs/usr/local/bin/radio-setup.sh). Prints nothing (empty) for
# any domain/bw not covered here — that includes EU + anything but 1MHz,
# and every domain other than US/EU (those are out of scope for this
# feature and get no channel validation at all).
halow_channels_for_domain_bw() {
    local domain="$1" bw="$2"
    case "$domain" in
        US)
            case "$bw" in
                1MHz) echo "1 3 5 7 9 11 13 15 17 19 21 23 25 27 29 31 33 35 37 39 41 43 45 47 49 51" ;;
                2MHz) echo "2 6 10 14 18 22 26 30 34 38 42 46 50" ;;
                4MHz) echo "8 16 24 32 40 48" ;;
                8MHz) echo "12 28 44" ;;
            esac
            ;;
        EU)
            case "$bw" in
                1MHz) echo "1 3 5 7 9" ;;
            esac
            ;;
    esac
}

# Default channel for a given (in-scope) regulatory domain + bandwidth.
halow_default_channel_for_domain_bw() {
    local domain="$1" bw="$2"
    case "$domain" in
        US)
            case "$bw" in
                1MHz) echo "11" ;;
                2MHz) echo "10" ;;
                4MHz) echo "24" ;;
                8MHz) echo "12" ;;
            esac
            ;;
        EU)
            case "$bw" in
                1MHz) echo "5" ;;
            esac
            ;;
    esac
}

# freq_MHz = (start_kHz + channel*500) / 1000, start is 902000 for US
# (and every out-of-scope domain) or 863000 for EU.
halow_freq_mhz_for_channel() {
    local domain="$1" ch="$2" start
    case "$domain" in
        EU) start=863000 ;;
        *) start=902000 ;;
    esac
    awk -v start="$start" -v ch="$ch" 'BEGIN { printf "%.1f", (start + ch * 500) / 1000 }'
}

calculate_capacity() {
    local cidr=$1
    local max_euds=$2
    local prefix
    prefix=$(echo "$cidr" | cut -d/ -f2)
    local host_bits=$((32 - prefix))
    local total_ips=$(( (1 << host_bits) ))
    local total_usable=$((total_ips - 2))
    local reserved_services=5
    local available=$((total_usable - reserved_services))

    if [ "$max_euds" -gt 0 ]; then
        local max_nodes=$((available / (1 + max_euds)))
        local eud_pool=$((max_nodes * max_euds))
    else
        local max_nodes=$available
        local eud_pool=0
    fi
    echo "$total_usable $reserved_services $eud_pool $max_nodes"
}

generate_password() {
    local length=${1:-10}
    openssl rand -base64 48 | tr -dc 'a-zA-Z0-9' | head -c "$length"
}

# --- LAN CIDR ---
ask_lan_cidr() {
    local max_euds=${1:-0}
    local DEFAULT_CIDR="10.30.2.0/24"

    while true; do
        read -p "Use default mesh network range ( $DEFAULT_CIDR )? (Y/n): " confirm_default
        confirm_default=${confirm_default:-y}

        if [ "$confirm_default" = "y" ] || [ "$confirm_default" = "Y" ]; then
            LAN_CIDR_BLOCK="$DEFAULT_CIDR"
        else
            while true; do
                read -p "Enter custom CIDR block for the mesh (e.g., 10.10.0.0/16): " custom_cidr
                if ! [[ "$custom_cidr" =~ ^([0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3})\/([0-9]{1,2})$ ]]; then
                    echo "ERROR: Invalid format. Must be x.x.x.x/yy"
                    continue
                fi
                local ip_part="${BASH_REMATCH[1]}"
                local prefix_part="${BASH_REMATCH[2]}"
                if (( prefix_part < 16 || prefix_part > 26 )); then
                    echo "ERROR: Prefix /${prefix_part} is invalid. Must be between /16 and /26."
                    continue
                fi
                IFS='.' read -ra ip_octets <<< "$ip_part"
                local o1=${ip_octets[0]} o2=${ip_octets[1]}
                local is_private=0
                if [ "$o1" -eq 10 ]; then is_private=1
                elif [ "$o1" -eq 172 ] && [ "$o2" -ge 16 ] && [ "$o2" -le 31 ]; then is_private=1
                elif [ "$o1" -eq 192 ] && [ "$o2" -eq 168 ]; then is_private=1; fi
                if [ "$is_private" -eq 0 ]; then
                    echo "ERROR: IP $ip_part is not in a private range."
                    continue
                fi
                if [ "$prefix_part" -eq 24 ] && [ "${ip_octets[3]}" -ne 0 ]; then
                    echo "WARNING: For a /24 network, the IP should end in .0."
                    read -p "Use it anyway? (y/N): " use_anyway
                    [ "${use_anyway:-n}" != "y" ] && continue
                fi
                LAN_CIDR_BLOCK="$custom_cidr"
                break
            done
        fi

        if [ "$max_euds" -gt 0 ]; then
            echo ""
            echo "=== Network Capacity Analysis ==="
            read TOTAL SERVICES EUD_POOL NODES <<< $(calculate_capacity "$LAN_CIDR_BLOCK" "$max_euds")
            echo "Network: $LAN_CIDR_BLOCK"
            echo "  Total usable IPs: $TOTAL"
            echo "  Reserved for services: $SERVICES"
            echo "  Reserved for EUD pool: $EUD_POOL (${max_euds} EUDs x ${NODES} nodes)"
            echo "  Available for mesh nodes: $NODES"
            echo "=================================="
            [ "$NODES" -lt 5 ] && echo "WARNING: This configuration only supports $NODES mesh nodes."
            echo ""
            read -p "Accept this configuration? (Y/n): " accept
            if [ "${accept:-y}" = "y" ] || [ "$accept" = "Y" ]; then break; fi
            echo "Let's reconfigure..."
        else
            echo "Using network: $LAN_CIDR_BLOCK"
            break
        fi
    done
}

# --- Configuration questions ---
ask_questions() {
    echo "--- Starting New Configuration ---"

    echo "Select EUD (client) connection type:"
    select eud_choice in "Wired" "Wireless" "Auto"; do
        case $eud_choice in
            "Wired" ) EUD_CONNECTION="wired"; break;;
            "Wireless" ) EUD_CONNECTION="wireless"; break;;
            "Auto" ) EUD_CONNECTION="auto"; break;;
        esac
    done

    if [ "$EUD_CONNECTION" = "wireless" ] || [ "$EUD_CONNECTION" = "auto" ]; then
        echo "EUD wifi network name. This name will have the last 4 of the ethernet MAC address appended."
        read -p "Enter EUD access point SSID name: " LAN_AP_SSID
        while true; do
            read -p "Enter EUD AP WPA2 Key (8-63 chars) [or press Enter to generate]: " LAN_AP_KEY
            echo
            if [ -z "$LAN_AP_KEY" ]; then
                LAN_AP_KEY=$(openssl rand -base64 45 | tr -d '\n')
                echo "Generated LAN AP Key: $LAN_AP_KEY"
                break
            fi
            key_len=${#LAN_AP_KEY}
            if (( key_len < 8 || key_len > 63 )); then
                echo "ERROR: Key must be between 8 and 63 characters."
            else
                break
            fi
        done
    else
        LAN_AP_SSID=""
        LAN_AP_KEY=""
        MAX_EUDS_PER_NODE=0
    fi

    read -p "Enter global MESH SSID Name: " MESH_SSID

    while true; do
        read -p "Enter MESH SAE Key (WPA3 password, 8-63 chars) [or press Enter to generate]: " MESH_SAE_KEY
        echo
        if [ -z "$MESH_SAE_KEY" ]; then
            MESH_SAE_KEY=$(openssl rand -base64 45 | tr -d '\n')
            echo "Generated SAE Key: $MESH_SAE_KEY"
            break
        fi
        key_len=${#MESH_SAE_KEY}
        if (( key_len < 8 || key_len > 63 )); then
            echo "ERROR: Key must be between 8 and 63 characters."
        else
            break
        fi
    done

    while true; do
        read -p "Enter WiFi regulatory domain (2-letter country code, default: US): " REGULATORY_DOMAIN
        REGULATORY_DOMAIN=${REGULATORY_DOMAIN:-US}
        if validated_domain=$(validate_regulatory_domain "$REGULATORY_DOMAIN"); then
            REGULATORY_DOMAIN="$validated_domain"
            echo "Using regulatory domain: $REGULATORY_DOMAIN"
            HALOW_REGULATORY_DOMAIN=$(halow_regulatory_domain_for_wifi_domain "$REGULATORY_DOMAIN")
            [ "$HALOW_REGULATORY_DOMAIN" != "$REGULATORY_DOMAIN" ] && echo "Using HaLow regulatory region: $HALOW_REGULATORY_DOMAIN"
            break
        else
            echo "ERROR: Invalid regulatory domain code: $REGULATORY_DOMAIN"
            echo "Please enter a valid 2-letter ISO country code (e.g., US, GB, DE, FR, JP)"
            echo "NOTE: EU is not a country code, use your actual country"
        fi
    done

    # HaLow Bandwidth
    if [ "$HALOW_REGULATORY_DOMAIN" = "EU" ]; then
        halow_bw_options="1MHz"
        halow_bw_default="1MHz"
    else
        halow_bw_options="1MHz 2MHz 4MHz 8MHz"
        halow_bw_default="2MHz"
    fi
    while true; do
        read -p "Enter HaLow bandwidth (options: ${halow_bw_options// //}, default: $halow_bw_default): " HALOW_BW
        HALOW_BW=${HALOW_BW:-$halow_bw_default}

        halow_bw_valid=0
        for opt in $halow_bw_options; do
            [ "$HALOW_BW" = "$opt" ] && halow_bw_valid=1 && break
        done

        if [ "$halow_bw_valid" -eq 1 ]; then
            echo "Using HaLow bandwidth: $HALOW_BW"
            break
        else
            echo "ERROR: Invalid HaLow bandwidth: $HALOW_BW"
            echo "Valid options for $HALOW_REGULATORY_DOMAIN: $halow_bw_options"
        fi
    done

    # HaLow Channel
    halow_channel_list=$(halow_channels_for_domain_bw "$HALOW_REGULATORY_DOMAIN" "$HALOW_BW")
    if [ -n "$halow_channel_list" ]; then
        halow_default_channel=$(halow_default_channel_for_domain_bw "$HALOW_REGULATORY_DOMAIN" "$HALOW_BW")
        halow_default_freq=$(halow_freq_mhz_for_channel "$HALOW_REGULATORY_DOMAIN" "$halow_default_channel")

        halow_channel_desc=""
        for ch in $halow_channel_list; do
            ch_freq=$(halow_freq_mhz_for_channel "$HALOW_REGULATORY_DOMAIN" "$ch")
            halow_channel_desc="${halow_channel_desc}${ch} (${ch_freq} MHz), "
        done
        halow_channel_desc="${halow_channel_desc%, }"
        echo "Available channels for $HALOW_REGULATORY_DOMAIN/$HALOW_BW: $halow_channel_desc"

        while true; do
            read -p "Enter HaLow channel [or press Enter for Auto (channel $halow_default_channel, ${halow_default_freq} MHz)]: " HALOW_CHANNEL

            if [ -z "$HALOW_CHANNEL" ]; then
                break
            fi

            halow_channel_valid=0
            for ch in $halow_channel_list; do
                [ "$HALOW_CHANNEL" = "$ch" ] && halow_channel_valid=1 && break
            done

            if [ "$halow_channel_valid" -eq 1 ]; then
                echo "Using HaLow channel: $HALOW_CHANNEL"
                break
            else
                echo "ERROR: Invalid HaLow channel for $HALOW_REGULATORY_DOMAIN/$HALOW_BW: $HALOW_CHANNEL"
                echo "Valid channels: $halow_channel_list"
            fi
        done
    else
        # Out-of-scope domain (not US or EU) — no ground-truth channel
        # table exists, so accept whatever's typed (or empty for Auto)
        # without validation.
        read -p "Enter HaLow channel [or press Enter for Auto]: " HALOW_CHANNEL
    fi

    read -p "Enter node hostname [or press Enter for auto]: " NODE_HOSTNAME
    NODE_HOSTNAME=${NODE_HOSTNAME:-}
    if [ -n "$NODE_HOSTNAME" ]; then
        echo "Hostname will be: ${NODE_HOSTNAME}-${MESH_SSID}-<mac>"
    else
        echo "Hostname will be: ${MESH_SSID}-<mac>"
    fi

    echo "The device will have a user called radio, for ssh access."
    read -p "Enter a password for the radio user [or press Enter to default to 'radio']: " RADIO_PW
    echo
    RADIO_PW=${RADIO_PW:-radio}
    echo "Setting radio password to be $RADIO_PW"

    echo ""
    echo "The network administrator password is used to access the mesh admin interface."
    read -p "Enter admin password [or press Enter to generate 10-char random]: " ADMIN_PW
    echo
    if [ -z "$ADMIN_PW" ]; then
        ADMIN_PW=$(generate_password 10)
        echo "Generated admin password: $ADMIN_PW"
    else
        echo "Admin password set."
    fi

    echo ""
    read -p "Enable automatic updates for MANET tools? (y/N): " AUTO_UPDATE
    AUTO_UPDATE=${AUTO_UPDATE:-n}
    if [ "$AUTO_UPDATE" = "y" ] || [ "$AUTO_UPDATE" = "Y" ]; then AUTO_UPDATE="y"; else AUTO_UPDATE="n"; fi

    if [ "$EUD_CONNECTION" = "wireless" ] || [ "$EUD_CONNECTION" = "auto" ]; then
        while true; do
            read -p "Maximum EUDs per radio (via wifi) (1-20): " MAX_EUDS_PER_NODE
            if [[ "$MAX_EUDS_PER_NODE" =~ ^[0-9]+$ ]] && [ "$MAX_EUDS_PER_NODE" -ge 1 ] && [ "$MAX_EUDS_PER_NODE" -le 20 ]; then
                break
            else
                echo "ERROR: Please enter a number between 1 and 20."
            fi
        done
    fi

    ask_lan_cidr "$MAX_EUDS_PER_NODE"

    if [ "$EUD_CONNECTION" = "wireless" ] || [ "$EUD_CONNECTION" = "auto" ]; then
        AUTO_CHANNEL="n"
        echo "Automatic WiFi Channel Selection disabled (not compatible with Wireless/Auto EUD mode)"
    else
        read -p "Use Automatic WiFi Channel Selection? (Y/n): " AUTO_CHANNEL
        AUTO_CHANNEL=${AUTO_CHANNEL:-y}
        if [ "$AUTO_CHANNEL" = "y" ] || [ "$AUTO_CHANNEL" = "Y" ]; then AUTO_CHANNEL="y"; else AUTO_CHANNEL="n"; fi
    fi

    read -p "Does this node have a GPS module? (Y/n): " GPS_ENABLED
    GPS_ENABLED=${GPS_ENABLED:-y}
    if [ "$GPS_ENABLED" = "y" ] || [ "$GPS_ENABLED" = "Y" ]; then GPS_ENABLED="y"; else GPS_ENABLED="n"; fi

    echo "----------------------------------"
}

save_config() {
    echo ""
    read -p "Save this configuration? (Y/n): " save_choice
    save_choice=${save_choice:-y}
    if [ "$save_choice" = "y" ] || [ "$save_choice" = "Y" ]; then
        read -p "Enter a name for this config: " config_name
        [ -z "$config_name" ] && echo "Invalid name, skipping save." && return
        cat << EOF > "$CONFIG_DIR/$config_name.conf"
# Mesh Config: $config_name
EUD_CONNECTION="$EUD_CONNECTION"
LAN_AP_SSID="$LAN_AP_SSID"
LAN_AP_KEY="$LAN_AP_KEY"
MAX_EUDS_PER_NODE="$MAX_EUDS_PER_NODE"
REGULATORY_DOMAIN="$REGULATORY_DOMAIN"
HALOW_REGULATORY_DOMAIN="$HALOW_REGULATORY_DOMAIN"
HALOW_BW="$HALOW_BW"
HALOW_CHANNEL="$HALOW_CHANNEL"
MESH_SSID="$MESH_SSID"
MESH_SAE_KEY="$MESH_SAE_KEY"
LAN_CIDR_BLOCK="$LAN_CIDR_BLOCK"
AUTO_CHANNEL="$AUTO_CHANNEL"
GPS_ENABLED="$GPS_ENABLED"
RADIO_PW="$RADIO_PW"
ADMIN_PW="$ADMIN_PW"
AUTO_UPDATE="$AUTO_UPDATE"
NODE_HOSTNAME="$NODE_HOSTNAME"
EOF
        echo "Configuration saved to $CONFIG_DIR/$config_name.conf"
    fi
}

load_config() {
    local CONFIG_FILE="$1"
    echo "Loading config from $CONFIG_FILE..."
    source "$CONFIG_FILE"
    HALOW_REGULATORY_DOMAIN=${HALOW_REGULATORY_DOMAIN:-$(halow_regulatory_domain_for_wifi_domain "$REGULATORY_DOMAIN")}
    if [ "$HALOW_REGULATORY_DOMAIN" = "EU" ]; then
        HALOW_BW=${HALOW_BW:-1MHz}
    else
        HALOW_BW=${HALOW_BW:-2MHz}
    fi
    HALOW_CHANNEL=${HALOW_CHANNEL:-}
    GPS_ENABLED=${GPS_ENABLED:-y}
    echo "--- Loaded Configuration ---"
    head -n 1 "$CONFIG_FILE" | sed 's/\#//'
    echo "  EUD Connection: $EUD_CONNECTION"
    if [ "$EUD_CONNECTION" = "wireless" ] || [ "$EUD_CONNECTION" = "auto" ]; then
        echo "  LAN AP SSID: $LAN_AP_SSID"
        echo "  LAN AP Key: $LAN_AP_KEY"
        echo "  Max EUDs per node: $MAX_EUDS_PER_NODE"
    fi
    echo "  Regulatory Domain: $REGULATORY_DOMAIN"
    echo "  HaLow Regulatory Region: $HALOW_REGULATORY_DOMAIN"
    echo "  HaLow Bandwidth: $HALOW_BW"
    echo "  HaLow Channel: ${HALOW_CHANNEL:-Auto}"
    echo "  Mesh SSID: $MESH_SSID"
    echo "  Mesh SAE Key: $MESH_SAE_KEY"
    echo "  LAN CIDR Block: $LAN_CIDR_BLOCK"
    echo "  Auto Channel: $AUTO_CHANNEL"
    echo "  GPS Enabled: $GPS_ENABLED"
    echo "  User password: $RADIO_PW"
    echo "  Admin password: ${ADMIN_PW:-(not set)}"
    echo "  Auto Update: ${AUTO_UPDATE:-n}"
    echo "  Node Hostname: ${NODE_HOSTNAME:-(auto)}"
    echo "----------------------------"
}

# --- Hardware selection ---
select_hardware() {
    echo ""
    echo "--- 1. Select Hardware ---"
    echo ""
    echo "Select hardware model:"
    select hw_choice in "Raspberry Pi 5" "Raspberry Pi 4B" "Compute Module 4 (CM4)"; do
        case $hw_choice in
            "Raspberry Pi 5" ) HARDWARE_MODEL="rpi5"; break;;
            "Raspberry Pi 4B" ) HARDWARE_MODEL="rpi4"; break;;
            "Compute Module 4 (CM4)" ) HARDWARE_MODEL="cm4"; break;;
        esac
    done
}

# --- Acquire base image ---
acquire_base_image() {
    mkdir -p "$CACHE_DIR"

    if [ -f "$CACHE_DIR/$PI_OS_IMAGE" ]; then
        echo "Using cached image: $CACHE_DIR/$PI_OS_IMAGE"
        return 0
    fi

    if [ -f "$CACHE_DIR/$PI_OS_IMAGE_XZ" ]; then
        echo "Found compressed image, decompressing..."
        xz -dk "$CACHE_DIR/$PI_OS_IMAGE_XZ"
        echo "Done."
        return 0
    fi

    echo "Downloading Raspberry Pi OS Trixie Lite..."
    echo "Source: $PI_OS_IMAGE_URL"
    echo ""
    curl -L --progress-bar -o "$CACHE_DIR/$PI_OS_IMAGE_XZ" "$PI_OS_IMAGE_URL"
    echo ""
    echo "Decompressing..."
    xz -dk "$CACHE_DIR/$PI_OS_IMAGE_XZ"
    echo "Done."
}

# --- Build the image ---
build_image() {
    local hw_model="$1"

    # Stop macOS writing AppleDouble sidecars (._foo) onto the FAT32 boot
    # partition — FAT32 has no xattr support, so cp/tar would otherwise leave
    # a 4 KB ._file next to every file we write. linux.sh produces none.
    export COPYFILE_DISABLE=1

    # CM4 uses rpi4 template
    [ "$hw_model" = "cm4" ] && hw_model="rpi4"

    local output_img="manet-${HARDWARE_MODEL}-${RADIO_NAME}-$(date +%Y%m%d).img"

    echo ""
    echo "=== Building image: $output_img ==="
    echo ""

    # Copy base image
    echo "Copying base image..."
    cp "$CACHE_DIR/$PI_OS_IMAGE" "$output_img"

    # Attach the image — macOS will mount the FAT32 boot partition
    echo "Mounting image..."
    local attach_output
    attach_output=$(hdiutil attach -imagekey diskimage-class=CRawDiskImage -nomount "$output_img" 2>&1)
    local loop_dev
    loop_dev=$(echo "$attach_output" | grep -oE '/dev/disk[0-9]+' | head -1)

    if [ -z "$loop_dev" ]; then
        echo "ERROR: Failed to attach image."
        echo "$attach_output"
        rm -f "$output_img"
        exit 1
    fi
    echo "Attached as: $loop_dev"

    # Find and mount the FAT32 boot partition (partition 1)
    local boot_part="${loop_dev}s1"
    local boot_mount
    boot_mount=$(mktemp -d)

    # Give macOS a moment to create the partition device
    sleep 1

    if ! mount -t msdos "$boot_part" "$boot_mount" 2>/dev/null; then
        sudo mount -t msdos "$boot_part" "$boot_mount"
    fi
    echo "Boot partition mounted at: $boot_mount"

    # Generate the actual firstrun script from template
    echo "Generating firstrun.sh..."
    local firstrun_body
    firstrun_body=$(sed -e "s|__HARDWARE_MODEL__|${hw_model}|g" \
        -e "s|__EUD_CONNECTION__|${EUD_CONNECTION}|g" \
        -e "s|__LAN_AP_SSID__|${LAN_AP_SSID}|g" \
        -e "s|__LAN_AP_KEY__|${LAN_AP_KEY}|g" \
        -e "s|__MAX_EUDS_PER_NODE__|${MAX_EUDS_PER_NODE}|g" \
        -e "s|__MESH_SSID__|${MESH_SSID}|g" \
        -e "s|__MESH_SAE_KEY__|${MESH_SAE_KEY}|g" \
        -e "s|__LAN_CIDR_BLOCK__|${LAN_CIDR_BLOCK}|g" \
        -e "s|__AUTO_CHANNEL__|${AUTO_CHANNEL}|g" \
        -e "s|__GPS_ENABLED__|${GPS_ENABLED}|g" \
        -e "s|__RADIO_PW__|${RADIO_PW}|g" \
        -e "s|__REGULATORY_DOMAIN__|${REGULATORY_DOMAIN}|g" \
        -e "s|__HALOW_REGULATORY_DOMAIN__|${HALOW_REGULATORY_DOMAIN}|g" \
        -e "s|__HALOW_BW__|${HALOW_BW}|g" \
        -e "s|__HALOW_CHANNEL__|${HALOW_CHANNEL}|g" \
        -e "s|__ADMIN_PW__|${ADMIN_PW}|g" \
        -e "s|__AUTO_UPDATE__|${AUTO_UPDATE}|g" \
        -e "s|__NODE_HOSTNAME__|${NODE_HOSTNAME}|g" \
        "$TEMPLATE_FILE" | tr -d '\r')

    # Write a wrapper that runs firstrun.sh then removes itself from
    # cmdline.txt so it doesn't loop on every boot.
    # This is what rpi-imager --first-run-script does internally.
    cat > "$boot_mount/firstrun.sh" << 'WRAPPER_HEAD'
#!/bin/bash
set -e

# Remove ourselves from cmdline.txt FIRST to prevent reboot loops
if [ -f /boot/firmware/cmdline.txt ]; then
    sed -i 's| systemd.run=[^ ]*||g' /boot/firmware/cmdline.txt
    sed -i 's| systemd.run_success_action=[^ ]*||g' /boot/firmware/cmdline.txt
    sed -i 's| systemd.unit=kernel-command-line.target||g' /boot/firmware/cmdline.txt
fi

WRAPPER_HEAD
    echo "$firstrun_body" | tail -n +2 >> "$boot_mount/firstrun.sh"

    # Embed the tools tarball so firstrun.sh doesn't need internet on first
    # boot. The prebuilt copy in install_packages/ is authoritative and is
    # NEVER overwritten here — rebuilding it in place used to silently
    # replace a full tarball with a tools-only one on any machine lacking the
    # SBC overlay, producing an image that boots to stock Raspberry Pi OS with
    # no radio drivers. windows.ps1 has always required a prebuilt copy; this
    # matches that. Set REBUILD_TARBALL=1 to deliberately rebuild.
    local tarball_name="" build_script=""
    case "$HARDWARE_MODEL" in
        cm4|rpi4) tarball_name="cm4-tools.tar.gz"; build_script="build-cm4-tarball.sh" ;;
        rpi5)     tarball_name="rpi5-tools.tar.gz"; build_script="build-rpi5-tarball.sh" ;;
    esac
    local repo_root
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
    local tarball_path="${repo_root}/install_packages/${tarball_name}"
    local packaging_dir="${repo_root}/packaging"

    if [ -z "$tarball_name" ]; then
        echo "ERROR: No tools tarball defined for hardware '$HARDWARE_MODEL'."
        diskutil unmount "$boot_mount" 2>/dev/null || true
        exit 1
    fi

    if [ ! -f "$tarball_path" ] || [ "${REBUILD_TARBALL:-0}" = "1" ]; then
        if [ ! -f "${packaging_dir}/${build_script}" ]; then
            echo "ERROR: ${tarball_path} not found and ${build_script} is missing."
            diskutil unmount "$boot_mount" 2>/dev/null || true
            exit 1
        fi
        echo "Building ${tarball_name}..."
        mkdir -p "${repo_root}/install_packages"
        if ! bash "${packaging_dir}/${build_script}" "$tarball_path"; then
            echo "ERROR: ${build_script} failed."
            diskutil unmount "$boot_mount" 2>/dev/null || true
            exit 1
        fi
        echo "Tarball built: $(du -h "$tarball_path" | cut -f1)"
    else
        echo "Using prebuilt tarball: $tarball_path ($(du -h "$tarball_path" | cut -f1))"
    fi

    if [ ! -f "$tarball_path" ]; then
        echo "ERROR: Tools tarball not found at $tarball_path"
        echo "First boot has no download fallback — the node cannot provision without it."
        diskutil unmount "$boot_mount" 2>/dev/null || true
        exit 1
    fi

    # CM4/rpi4 need the kernel layer (kernel8.img, DTBs, mt7915e/morse/dot11ah
    # modules, HaLow firmware) — none of it ships in stock Raspberry Pi OS, and
    # a tarball without it yields a node with no mesh radios at all. Catch that
    # here rather than after a ten-minute provision on hardware.
    case "$HARDWARE_MODEL" in
        cm4|rpi4)
            if ! tar -tzf "$tarball_path" 2>/dev/null \
                 | grep -qE '(^|/)usr/lib/modules/|kernel8\.img'; then
                echo ""
                echo "ERROR: $tarball_name contains no kernel/driver layer."
                echo "It has the MANET userspace but no kernel8.img, DTBs or"
                echo "mt7915e/morse/dot11ah modules, so the node would boot to"
                echo "stock Raspberry Pi OS with no mesh radios."
                echo ""
                echo "Fix: vendor the CM4 SBC overlay, then rebuild:"
                echo "  MANET/packaging/fetch-cm4-overlay.sh <path-to-cm4-install.tar.gz>"
                echo "  REBUILD_TARBALL=1 ./mac.sh"
                echo ""
                echo "A known-good cm4-tools.tar.gz copied into install_packages/"
                echo "from another machine works too."
                diskutil unmount "$boot_mount" 2>/dev/null || true
                exit 1
            fi
            ;;
    esac

    cp "$tarball_path" "$boot_mount/mesh-tools.tar.gz"
    echo "Embedded tools tarball: $tarball_name ($(du -h "$tarball_path" | cut -f1))"

    # Modify cmdline.txt to run firstrun.sh on first boot
    if [ -f "$boot_mount/cmdline.txt" ]; then
        local existing
        existing=$(tr -d '\n' < "$boot_mount/cmdline.txt")
        echo "${existing} systemd.run=/boot/firmware/firstrun.sh systemd.run_success_action=reboot systemd.unit=kernel-command-line.target" > "$boot_mount/cmdline.txt"
        echo "Modified cmdline.txt for first-boot provisioning."
    else
        echo "WARNING: cmdline.txt not found on boot partition!"
    fi

    # Unmount and detach
    # Sweep any AppleDouble leftovers before unmounting (belt and braces —
    # COPYFILE_DISABLE covers cp/tar, dot_clean catches anything else).
    dot_clean -m "$boot_mount" 2>/dev/null || true
    rm -f "$boot_mount"/._* 2>/dev/null || true
    sync
    umount "$boot_mount" 2>/dev/null || sudo umount "$boot_mount"
    rmdir "$boot_mount"
    hdiutil detach "$loop_dev" -quiet 2>/dev/null || sudo hdiutil detach "$loop_dev" -force -quiet
    echo ""

    echo "=============================================="
    echo "  Image ready: $output_img"
    echo "=============================================="
    echo ""
    echo "  Hardware: $HARDWARE_MODEL"
    echo "  Mesh SSID: $MESH_SSID"
    echo "  Network: $LAN_CIDR_BLOCK"
    echo "  Size: $(du -h "$output_img" | cut -f1)"
    echo ""
    echo "  Flash this image to your device with:"
    echo "    - Balena Etcher (recommended)"
    echo "    - Raspberry Pi Imager"
    echo "    - dd: sudo dd if=$output_img of=/dev/rdiskN bs=4m"
    echo ""
    if [ "$HARDWARE_MODEL" = "cm4" ]; then
        echo "  For CM4: run 'sudo rpiboot' first to expose eMMC,"
        echo "  then flash with Balena Etcher."
        echo ""
    fi
    echo "  ONCE BOOTED, THE MESH NODE WILL AUTOMATICALLY START"
    echo "  SETTING ITSELF UP AND WILL REBOOT MULTIPLE TIMES."
    echo "  Just leave it alone — takes about ten minutes."
}


# ============================================================
#  Main
# ============================================================

if [ "$(uname)" != "Darwin" ]; then
    echo "ERROR: This script is for macOS. Use linux.sh on Linux."
    exit 1
fi

select_hardware

# --- Check dependencies ---
if [ ! -f "$TEMPLATE_FILE" ]; then
    echo "ERROR: Template file '$TEMPLATE_FILE' not found."
    echo "Run this script from the MANET/provisioning/ directory."
    exit 1
fi
if ! command -v openssl &>/dev/null; then
    echo "ERROR: 'openssl' command not found."
    exit 1
fi
if ! command -v xz &>/dev/null; then
    echo "ERROR: 'xz' command not found."
    echo "Install with: brew install xz"
    exit 1
fi

mkdir -p "$CONFIG_DIR"

# --- Load or create config ---
config_files=("$CONFIG_DIR"/*.conf)
num_configs=${#config_files[@]}
[ ! -f "${config_files[0]}" ] && num_configs=0

if [ "$num_configs" -gt 0 ]; then
    echo "Found $num_configs saved configuration(s)."
    echo "What would you like to do?"
    select choice in "Load a saved configuration" "Create a new configuration"; do
        case $choice in
            "Load a saved configuration" )
                echo "Please select a configuration to load:"
                config_names=()
                for f in "${config_files[@]}"; do
                    config_names+=("$(basename "$f" .conf)")
                done
                config_names+=("Cancel")
                PS3="Select config (or 'Cancel'): "
                select config_name in "${config_names[@]}"; do
                    [ "$config_name" == "Cancel" ] && echo "Aborting." && exit 0
                    if [ -n "$config_name" ]; then
                        RADIO_NAME="$config_name"
                        load_config "$CONFIG_DIR/$config_name.conf"
                        break
                    fi
                    echo "Invalid selection."
                done
                break;;
            "Create a new configuration" )
                ask_questions
                save_config
                RADIO_NAME="${config_name:-node}"
                break;;
        esac
    done
else
    echo "No saved configs found. Starting new setup."
    ask_questions
    save_config
    RADIO_NAME="${config_name:-node}"
fi

# --- Download base image and build ---
acquire_base_image
build_image "$HARDWARE_MODEL"
