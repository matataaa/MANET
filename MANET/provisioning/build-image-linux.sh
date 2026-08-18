#!/bin/bash
#
#  Linux image-builder for MANET mesh radio nodes.
#  Builds a ready-to-flash .img file with mesh config baked in, instead of
#  flashing a device directly (that's what linux.sh does). Copy the output
#  to any machine — Windows included — and flash it with Raspberry Pi
#  Imager ("Use custom") or Balena Etcher.
#
#  This mirrors mac.sh's approach (same question flow, same output). The
#  boot (FAT32) partition is edited in place with mtools (mcopy/mformat),
#  addressed by byte offset inside the .img — not loop-mounted. That means
#  no root/sudo and no loop-device/mount access is required at all, which
#  matters in containers/CI where /dev/loop-control isn't available.
#
set -e

# Pick up the Go/Android SDK toolchain build-tarballs-linux.sh installs,
# even if this shell was started before that profile snippet existed —
# /etc/profile.d is only sourced when a shell *starts*, so a terminal left
# open from before the toolchain was installed won't see it otherwise, and
# the tarball rebuild below would fail with "go: command not found".
[ -f /etc/profile.d/manet-build-tools.sh ] && . /etc/profile.d/manet-build-tools.sh

# --- Configuration ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE_FILE="$SCRIPT_DIR/firstrun.sh.template"
CONFIG_DIR="$SCRIPT_DIR/.mesh-configs"
CACHE_DIR="$SCRIPT_DIR/.image-cache"

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
MESH_SSID="$MESH_SSID"
MESH_SAE_KEY="$MESH_SAE_KEY"
LAN_CIDR_BLOCK="$LAN_CIDR_BLOCK"
AUTO_CHANNEL="$AUTO_CHANNEL"
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
    echo "  Mesh SSID: $MESH_SSID"
    echo "  Mesh SAE Key: $MESH_SAE_KEY"
    echo "  LAN CIDR Block: $LAN_CIDR_BLOCK"
    echo "  Auto Channel: $AUTO_CHANNEL"
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
    echo "(Rock 3A not supported by this script — use linux.sh to flash a Rock 3A directly)"
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

    # CM4 uses rpi4 template
    [ "$hw_model" = "cm4" ] && hw_model="rpi4"

    local output_img="$SCRIPT_DIR/manet-${HARDWARE_MODEL}-${RADIO_NAME}-$(date +%Y%m%d).img"

    echo ""
    echo "=== Building image: $(basename "$output_img") ==="
    echo ""

    # Copy base image
    echo "Copying base image..."
    cp "$CACHE_DIR/$PI_OS_IMAGE" "$output_img"

    # Locate the FAT32 boot partition's byte offset inside the image, so it
    # can be edited in place with mtools (mcopy) — no loop device or mount
    # needed, so no root/sudo either. This also works in containers/CI where
    # /dev/loop-control isn't exposed.
    echo "Locating boot partition..."
    local part_offset
    part_offset=$(parted -ms "$output_img" unit B print 2>/dev/null | awk -F: '$1=="1"{gsub("B","",$2); print $2}')
    if [ -z "$part_offset" ]; then
        echo "ERROR: Could not locate partition 1 in $(basename "$output_img")."
        rm -f "$output_img"
        exit 1
    fi
    local mimg="${output_img}@@${part_offset}"
    echo "Boot partition found at byte offset $part_offset"

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
        -e "s|__RADIO_PW__|${RADIO_PW}|g" \
        -e "s|__REGULATORY_DOMAIN__|${REGULATORY_DOMAIN}|g" \
        -e "s|__HALOW_REGULATORY_DOMAIN__|${HALOW_REGULATORY_DOMAIN}|g" \
        -e "s|__ADMIN_PW__|${ADMIN_PW}|g" \
        -e "s|__AUTO_UPDATE__|${AUTO_UPDATE}|g" \
        -e "s|__NODE_HOSTNAME__|${NODE_HOSTNAME}|g" \
        "$TEMPLATE_FILE" | tr -d '\r')

    # Write a wrapper that runs firstrun.sh then removes itself from
    # cmdline.txt so it doesn't loop on every boot.
    # This is what rpi-imager --first-run-script does internally.
    local firstrun_local
    firstrun_local=$(mktemp)
    cat > "$firstrun_local" << 'WRAPPER_HEAD'
#!/bin/bash
set -e

# Remove ourselves from cmdline.txt FIRST to prevent reboot loops
if [ -f /boot/firmware/cmdline.txt ]; then
    sed -i 's| systemd.run=[^ ]*||g' /boot/firmware/cmdline.txt
    sed -i 's| systemd.run_success_action=[^ ]*||g' /boot/firmware/cmdline.txt
    sed -i 's| systemd.unit=kernel-command-line.target||g' /boot/firmware/cmdline.txt
fi

WRAPPER_HEAD
    echo "$firstrun_body" | tail -n +2 >> "$firstrun_local"
    mcopy -o -i "$mimg" "$firstrun_local" ::firstrun.sh
    rm -f "$firstrun_local"

    # Build and embed the tools tarball so firstrun.sh doesn't need internet.
    local tarball_name="" build_script=""
    case "$HARDWARE_MODEL" in
        cm4|rpi4) tarball_name="cm4-tools.tar.gz"; build_script="build-cm4-tarball.sh" ;;
        rpi5)     tarball_name="rpi5-tools.tar.gz"; build_script="build-rpi5-tarball.sh" ;;
    esac
    local repo_root="$(cd "$SCRIPT_DIR/.." && pwd)"
    local tarball_path="${repo_root}/install_packages/${tarball_name}"
    local packaging_dir="${repo_root}/packaging"
    if [ -n "$build_script" ] && [ -f "${packaging_dir}/${build_script}" ]; then
        echo "Rebuilding ${tarball_name}..."
        mkdir -p "${repo_root}/install_packages"
        bash "${packaging_dir}/${build_script}" "$tarball_path"
        echo "Tarball rebuilt: $(du -h "$tarball_path" | cut -f1)"
    fi
    if [ -n "$tarball_name" ] && [ -f "$tarball_path" ]; then
        mcopy -o -i "$mimg" "$tarball_path" ::mesh-tools.tar.gz
        echo "Embedded tools tarball: $tarball_name ($(du -h "$tarball_path" | cut -f1))"
    else
        echo "WARNING: Tools tarball not found at $tarball_path — node will download at first boot"
        echo "         Run provisioning/build-tarballs-linux.sh first to build it locally."
    fi

    # Modify cmdline.txt to run firstrun.sh on first boot
    local cmdline_local
    cmdline_local=$(mktemp)
    if mcopy -n -i "$mimg" ::cmdline.txt "$cmdline_local" 2>/dev/null; then
        local existing
        existing=$(tr -d '\n' < "$cmdline_local")
        echo "${existing} systemd.run=/boot/firmware/firstrun.sh systemd.run_success_action=reboot systemd.unit=kernel-command-line.target" > "$cmdline_local"
        mcopy -o -i "$mimg" "$cmdline_local" ::cmdline.txt
        echo "Modified cmdline.txt for first-boot provisioning."
    else
        echo "WARNING: cmdline.txt not found on boot partition!"
    fi
    rm -f "$cmdline_local"
    echo ""

    echo "=============================================="
    echo "  Image ready: $(basename "$output_img")"
    echo "=============================================="
    echo ""
    echo "  Hardware: $HARDWARE_MODEL"
    echo "  Mesh SSID: $MESH_SSID"
    echo "  Network: $LAN_CIDR_BLOCK"
    echo "  Size: $(du -h "$output_img" | cut -f1)"
    echo "  Path: $output_img"
    echo ""
    echo "  Copy this .img to the machine you'll flash from, then use:"
    echo "    - Raspberry Pi Imager -> 'Choose OS' -> 'Use custom' -> select this .img"
    echo "    - Balena Etcher -> 'Flash from file' -> select this .img"
    echo ""
    if [ "$HARDWARE_MODEL" = "cm4" ]; then
        echo "  For CM4: put the module in USB-boot mode and run 'rpiboot' on the"
        echo "  flashing machine first to expose the eMMC as a drive, then flash"
        echo "  this image with Raspberry Pi Imager or Etcher."
        echo ""
    fi
    echo "  ONCE BOOTED, THE MESH NODE WILL AUTOMATICALLY START"
    echo "  SETTING ITSELF UP AND WILL REBOOT MULTIPLE TIMES."
    echo "  Just leave it alone — takes about ten minutes."
}


# ============================================================
#  Main
# ============================================================

select_hardware

# --- Check dependencies ---
if [ ! -f "$TEMPLATE_FILE" ]; then
    echo "ERROR: Template file '$TEMPLATE_FILE' not found."
    exit 1
fi
if ! command -v openssl &>/dev/null; then
    echo "ERROR: 'openssl' command not found. Install with: sudo apt install openssl"
    exit 1
fi
if ! command -v xz &>/dev/null; then
    echo "ERROR: 'xz' command not found. Install with: sudo apt install xz-utils"
    exit 1
fi
if ! command -v curl &>/dev/null; then
    echo "ERROR: 'curl' command not found. Install with: sudo apt install curl"
    exit 1
fi
if ! command -v parted &>/dev/null; then
    echo "ERROR: 'parted' command not found. Install with: sudo apt install parted"
    exit 1
fi
if ! command -v mcopy &>/dev/null; then
    echo "ERROR: 'mcopy' (mtools) command not found. Install with: sudo apt install mtools"
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
