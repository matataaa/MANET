#!/bin/bash
#
#  A script to image new mesh radio nodes
set -e

# --- Configuration ---
TEMPLATE_FILE="firstrun.sh.template"
TEMP_SCRIPT_FILE=$(mktemp)
CONFIG_DIR=".mesh-configs"
# Hardcode the OS image URL. rpi-imager will download and cache this.
PI_OS_IMAGE_URL="https://downloads.raspberrypi.com/raspios_lite_arm64/images/raspios_lite_arm64-2025-10-02/2025-10-01-raspios-trixie-arm64-lite.img.xz"

# --- Helper Functions ---
# Function to validate regulatory domain
validate_regulatory_domain() {
    local domain=$1

    # List of valid regulatory domains (common ones)
    local valid_domains=(
        "US" "CA" "GB" "DE" "FR" "IT" "ES" "NL" "BE" "AT" "CH" "SE" "NO" "DK" "FI"
        "PL" "CZ" "HU" "GR" "PT" "IE" "RO" "BG" "HR" "SI" "SK" "LT" "LV" "EE" "CY"
        "MT" "LU" "AU" "NZ" "JP" "KR" "TW" "SG" "MY" "TH" "PH" "ID" "VN" "IN" "CN"
        "BR" "AR" "MX" "CL" "CO" "PE" "ZA" "IL" "AE" "SA" "RU" "UA" "TR" "EG"
    )

    # Convert to uppercase for comparison
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

# Function to calculate network capacity
calculate_capacity() {
        local cidr=$1
        local max_euds=$2

        # Calculate total usable IPs
        local CALC_OUTPUT=$(ipcalc "$cidr" 2>/dev/null)
        if [ -z "$CALC_OUTPUT" ]; then
                echo "0"
                return 1
        fi

        local HOST_MIN=$(echo "$CALC_OUTPUT" | awk '/HostMin/ {print $2}')
        local HOST_MAX=$(echo "$CALC_OUTPUT" | awk '/HostMax/ {print $2}')

        if [ -z "$HOST_MIN" ] || [ -z "$HOST_MAX" ]; then
                echo "0"
                return 1
        fi

        # Convert to integers for calculation
        local MIN_INT=$(echo $HOST_MIN | awk -F. '{print ($1 * 256^3) + ($2 * 256^2) + ($3 * 256) + $4}')
        local MAX_INT=$(echo $HOST_MAX | awk -F. '{print ($1 * 256^3) + ($2 * 256^2) + ($3 * 256) + $4}')

        local TOTAL_USABLE=$((MAX_INT - MIN_INT + 1))

        # Reserved IPs: 5 for services
        local RESERVED_SERVICES=5

        # Calculate based on max EUDs
        # We need to reserve enough for reasonable number of nodes
        # Start with assumption and iterate
        local AVAILABLE_FOR_NODES=$((TOTAL_USABLE - RESERVED_SERVICES))

        if [ "$max_euds" -gt 0 ]; then
                # Solve: nodes + (nodes * max_euds) = available
                # nodes * (1 + max_euds) = available
                # nodes = available / (1 + max_euds)
                local MAX_NODES=$((AVAILABLE_FOR_NODES / (1 + max_euds)))
                local EUD_POOL=$((MAX_NODES * max_euds))
                AVAILABLE_FOR_NODES=$((TOTAL_USABLE - RESERVED_SERVICES - EUD_POOL))
        else
                local MAX_NODES=$((AVAILABLE_FOR_NODES))
                local EUD_POOL=0
        fi

        echo "$TOTAL_USABLE $RESERVED_SERVICES $EUD_POOL $MAX_NODES"
}

# Function to ask for and validate the LAN CIDR block
ask_lan_cidr() {
        local max_euds=${1:-0}
        local DEFAULT_CIDR="10.30.2.0/24"
        local custom_cidr
        local confirm_default
        local ip_part
        local prefix_part

        while true; do
                read -p "Use default mesh network range ( $DEFAULT_CIDR )? (Y/n): " confirm_default
                confirm_default=${confirm_default:-y}

                if [ "$confirm_default" = "y" ] || [ "$confirm_default" = "Y" ]; then
                        LAN_CIDR_BLOCK="$DEFAULT_CIDR"
                else
                        # --- Custom CIDR Loop ---
                        while true; do
                               read -p "Enter custom CIDR block for the mesh (e.g., 10.10.0.0/16): " custom_cidr

                               # 1. Validate general format (IP/Prefix)
                               if ! [[ "$custom_cidr" =~ ^([0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3})\/([0-9]{1,2})$ ]]; then
                                echo "ERROR: Invalid format. Must be x.x.x.x/yy"
                                continue
                               fi

                               ip_part="${BASH_REMATCH[1]}"
                               prefix_part="${BASH_REMATCH[2]}"

                               # 2. Validate Prefix (16-28 is a reasonable range for a LAN)
                               if (( prefix_part < 16 || prefix_part > 26 )); then
                                echo "ERROR: Prefix /${prefix_part} is invalid. Must be between /16 and /26."
                                continue
                               fi

                               # 3. Validate IP as a private range
                               OIFS="$IFS"; IFS='.'; ip_octets=($ip_part); IFS="$OIFS"
                               local o1=${ip_octets[0]}
                               local o2=${ip_octets[1]}

                               local is_private=0
                               if [ "$o1" -eq 10 ]; then
                                is_private=1
                               elif [ "$o1" -eq 172 ] && [ "$o2" -ge 16 ] && [ "$o2" -le 31 ]; then
                                is_private=1
                               elif [ "$o1" -eq 192 ] && [ "$o2" -eq 168 ]; then
                                is_private=1
                               fi

                               if [ "$is_private" -eq 0 ]; then
                                echo "ERROR: IP $ip_part is not in a private range."
                                echo "Must be in 10.0.0.0/8, 172.16.0.0/12, or 192.168.0.0/16."
                                continue
                               fi

                               # 4. Check if it's a valid network address (e.g. not 192.168.1.1/24)
                               if [ "$prefix_part" -eq 24 ] && [ "${ip_octets[3]}" -ne 0 ]; then
                                echo "WARNING: For a /24 network, the IP should end in .0 (e.g., 192.168.1.0/24)."
                                echo "Your entry $custom_cidr may cause routing issues."
                                read -p "Use it anyway? (y/N): " use_anyway
                                use_anyway=${use_anyway:-n}
                                if [ "$use_anyway" != "y" ]; then
                                      continue
                                fi
                               fi

                               # All checks passed
                               LAN_CIDR_BLOCK="$custom_cidr"
                               break
                        done
                fi

                # Show capacity calculation if EUDs are configured
                if [ "$max_euds" -gt 0 ]; then
                        echo ""
                        echo "=== Network Capacity Analysis ==="
                        read TOTAL SERVICES EUD_POOL NODES <<< $(calculate_capacity "$LAN_CIDR_BLOCK" "$max_euds")

                        echo "Network: $LAN_CIDR_BLOCK"
                        echo "  Total usable IPs: $TOTAL"
                        echo "  Reserved for services: $SERVICES"
                        echo "  Reserved for EUD pool: $EUD_POOL (${max_euds} EUDs × ${NODES} nodes)"
                        echo "  Available for mesh nodes: $NODES"
                        echo "=================================="
                        echo ""

                        if [ "$NODES" -lt 3 ]; then
                               echo "WARNING: This configuration only supports $NODES mesh nodes."
                               echo "Consider using a larger network or reducing max EUDs per node."
                        fi

                        read -p "Accept this configuration? (Y/n): " accept
                        accept=${accept:-y}
                        if [ "$accept" = "y" ] || [ "$accept" = "Y" ]; then
                               break
                        fi
                        echo "Let's reconfigure..."
                else
                        echo "Using network: $LAN_CIDR_BLOCK"
                        break
                fi
        done
}


# This finds the top-level disk (e.g., nvme0n1) that hosts the / filesystem of the
# flashing computer
find_boot_disk() {
        local root_dev
        local physical_disk

        # Find the device hosting the root filesystem
        root_dev=$(findmnt -n -o SOURCE /)
        if [ -z "$root_dev" ]; then
                echo "ERROR: Could not find root filesystem." >&2
                return 1
        fi

        # Use lsblk with -s (inverse) to show all ancestor devices
        # Then filter for TYPE="disk" to get the physical disk
        physical_disk=$(lsblk -n -s -o NAME,TYPE "$root_dev" | awk '$2 == "disk" {print $1; exit}' | \
                sed 's/^[├└│─ ]*//')

        if [ -z "$physical_disk" ]; then
                echo "ERROR: Could not trace root device to physical disk." >&2
                return 1
        fi

        echo "$physical_disk"
}

# Function to generate a random alphanumeric password
generate_password() {
        local length=${1:-10}
        # Generate password with alphanumeric characters only (easier to type)
        openssl rand -base64 48 | tr -dc 'a-zA-Z0-9' | head -c "$length"
}

# Detect SD card devices: mmcblk (native readers) + USB-attached disks (external card readers).
# Excludes boot disk and eMMC internal storage.
# Returns array of "/dev/NAME (size)" strings in SD_DEVICES global.
detect_sd_cards() {
        local boot_disk="$1"
        SD_DEVICES=()

        while IFS= read -r line; do
                local NAME="" SIZE="" TYPE="" TRAN=""
                eval "$line"

                [ "$TYPE" = "disk" ] || continue
                [ "$NAME" = "$boot_disk" ] && continue

                if [[ "$NAME" =~ ^mmcblk[0-9]+$ ]]; then
                        # Native MMC slot: accept SD and MMC types, skip eMMC (empty sysfs type = internal eMMC on most SBCs)
                        local devtype
                        devtype=$(cat "/sys/block/$NAME/device/type" 2>/dev/null || echo "")
                        [ "$devtype" = "SD" ] || [ "$devtype" = "MMC" ] || continue
                elif [ "$TRAN" = "usb" ]; then
                        # USB-attached card reader — accept any non-zero-size disk
                        [ "$SIZE" = "0B" ] && continue
                else
                        continue
                fi

                SD_DEVICES+=("/dev/$NAME ($SIZE)")
        done < <(lsblk -d -n -P -o NAME,SIZE,TYPE,TRAN)
}

# Function to ask all setup questions
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

        # If Wireless or Auto, ask for LAN AP configuration
        if [ "$EUD_CONNECTION" = "wireless" ] || [ "$EUD_CONNECTION" = "auto" ]; then
                read -p "Enter LAN AP SSID Name: " LAN_AP_SSID

                while true; do
                        read -p "Enter LAN AP WPA2 Key (8-63 chars) [or press Enter to generate]: " LAN_AP_KEY
                        echo
                        if [ -z "$LAN_AP_KEY" ]; then
                               LAN_AP_KEY=$(openssl rand -base64 45  | tr -d '\n')
                               echo "Generated LAN AP Key: $LAN_AP_KEY"
                               break
                        fi

                        key_len=${#LAN_AP_KEY}
                        if (( key_len < 8 || key_len > 63 )); then
                               echo "ERROR: Key must be between 8 and 63 characters. You entered $key_len characters."
                        else
                               break # Valid key
                        fi
                done
        else
                LAN_AP_SSID=""
                LAN_AP_KEY=""
                MAX_EUDS_PER_NODE=0
        fi

        # Mesh Configuration
        read -p "Enter MESH SSID Name: " MESH_SSID

        while true; do
                read -p "Enter MESH SAE Key (WPA3 password, 8-63 chars) [or press Enter to generate]: " MESH_SAE_KEY
                echo
                if [ -z "$MESH_SAE_KEY" ]; then
                        MESH_SAE_KEY=$(openssl rand -base64 45  | tr -d '\n')
                        echo "Generated SAE Key: $MESH_SAE_KEY"
                        break
                fi

                key_len=${#MESH_SAE_KEY}
                if (( key_len < 8 || key_len > 63 )); then
                        echo "ERROR: Key must be between 8 and 63 characters. You entered $key_len characters."
                else
                        break # Valid key
                fi
        done

        # WiFi Regulatory Domain
    while true; do
        read -p "Enter WiFi regulatory domain (2-letter country code, default: US): " REGULATORY_DOMAIN
        REGULATORY_DOMAIN=${REGULATORY_DOMAIN:-US}

        if validated_domain=$(validate_regulatory_domain "$REGULATORY_DOMAIN"); then
            REGULATORY_DOMAIN="$validated_domain"
            echo "Using regulatory domain: $REGULATORY_DOMAIN"
            HALOW_REGULATORY_DOMAIN=$(halow_regulatory_domain_for_wifi_domain "$REGULATORY_DOMAIN")
            if [ "$HALOW_REGULATORY_DOMAIN" != "$REGULATORY_DOMAIN" ]; then
                echo "Using HaLow regulatory region: $HALOW_REGULATORY_DOMAIN"
            fi
            break
        else
            echo "ERROR: Invalid regulatory domain code: $REGULATORY_DOMAIN"
            echo "Please enter a valid 2-letter ISO country code (e.g., US, GB, DE, FR, JP)"
            echo "Common codes: US (United States), GB (UK), DE (Germany), FR (France), JP (Japan)"
            echo "              CA (Canada), AU (Australia), NZ (New Zealand), CN (China)"
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

        if [ -z "$RADIO_PW" ]; then
                RADIO_PW="radio"
                echo "Setting default password"
        fi
        echo "Setting radio password to be $RADIO_PW"

        # Network administrator password
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

        # Automatic updates for MANET tools
        echo ""
        read -p "Enable automatic updates for MANET tools? (Y/n): " AUTO_UPDATE
        AUTO_UPDATE=${AUTO_UPDATE:-y}
        if [ "$AUTO_UPDATE" = "y" ] || [ "$AUTO_UPDATE" = "Y" ]; then
                AUTO_UPDATE="y"
                echo "Automatic updates enabled."
        else
                AUTO_UPDATE="n"
                echo "Automatic updates disabled."
        fi

        # Ask for max EUDs before CIDR selection
        if [ "$EUD_CONNECTION" = "wireless" ] || [ "$EUD_CONNECTION" = "auto" ]; then
                while true; do
                        read -p "Maximum EUDs per node's AP (1-20): " MAX_EUDS_PER_NODE
                        if [[ "$MAX_EUDS_PER_NODE" =~ ^[0-9]+$ ]] && [ "$MAX_EUDS_PER_NODE" -ge 1 ] && [ "$MAX_EUDS_PER_NODE" -le 20 ]; then
                               break
                        else
                               echo "ERROR: Please enter a number between 1 and 20."
                        fi
                done
        fi

        # CIDR selection
        ask_lan_cidr "$MAX_EUDS_PER_NODE"

        # Auto Channel Selection (skip if wireless or auto)
        if [ "$EUD_CONNECTION" = "wireless" ] || [ "$EUD_CONNECTION" = "auto" ]; then
                AUTO_CHANNEL="n"
                echo "Automatic WiFi Channel Selection disabled (not compatible with Wireless/Auto EUD mode)"
        else
                read -p "Use Automatic WiFi Channel Selection? (Y/n): " AUTO_CHANNEL
                AUTO_CHANNEL=${AUTO_CHANNEL:-y}
                if [ "$AUTO_CHANNEL" = "y" ] || [ "$AUTO_CHANNEL" = "Y" ]; then AUTO_CHANNEL="y"; else AUTO_CHANNEL="n"; fi
        fi

        # GPS presence — some hardware in the fleet has no GPS module at all.
        read -p "Does this node have a GPS module? (Y/n): " GPS_ENABLED
        GPS_ENABLED=${GPS_ENABLED:-y}
        if [ "$GPS_ENABLED" = "y" ] || [ "$GPS_ENABLED" = "Y" ]; then GPS_ENABLED="y"; else GPS_ENABLED="n"; fi

        echo "----------------------------------"
}

# Function to save the current variables to a config file
save_config() {
        echo ""
        read -p "Save this configuration? (Y/n): " save_choice
        save_choice=${save_choice:-y}
        if [ "$save_choice" = "y" ] || [ "$save_choice" = "Y" ]; then
                read -p "Enter a name for this config: " config_name
                if [ -z "$config_name" ]; then
                        echo "Invalid name, skipping save."
                        return
                fi

                local CONFIG_FILE="$CONFIG_DIR/$config_name.conf"

                cat << EOF > "$CONFIG_FILE"
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
GPS_ENABLED="$GPS_ENABLED"
RADIO_PW="$RADIO_PW"
ADMIN_PW="$ADMIN_PW"
AUTO_UPDATE="$AUTO_UPDATE"
NODE_HOSTNAME="$NODE_HOSTNAME"
EOF

                echo "Configuration saved to $CONFIG_FILE"
        fi
}

# Function to load variables from a config file
load_config() {
        local CONFIG_FILE="$1"
        echo "Loading config from $CONFIG_FILE..."
        # Source the file to load the variables into this script
        source "$CONFIG_FILE"
        HALOW_REGULATORY_DOMAIN=${HALOW_REGULATORY_DOMAIN:-$(halow_regulatory_domain_for_wifi_domain "$REGULATORY_DOMAIN")}
        GPS_ENABLED=${GPS_ENABLED:-y}

        # Display the loaded settings
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
        echo "  GPS Enabled: $GPS_ENABLED"
        echo "  User password: $RADIO_PW"
        echo "  Admin password: ${ADMIN_PW:-(not set)}"
        echo "  Auto Update: ${AUTO_UPDATE:-n}"
        echo "  Node Hostname: ${NODE_HOSTNAME:-(auto)}"
        echo "----------------------------"
}

# Selects hardware model. Sets HARDWARE_MODEL global.
select_hardware() {
        echo ""
        echo "--- 1. Select Hardware ---"

        echo "Select hardware model:"
        select hw_choice in "Raspberry Pi 5" "Raspberry Pi 4B" "Compute Module 4 (CM4)"; do
                case $hw_choice in
                        "Raspberry Pi 5" )
                               HARDWARE_MODEL="rpi5"
                               break
                               ;;
                        "Raspberry Pi 4B" )
                               HARDWARE_MODEL="rpi4"
                               break
                               ;;
                        "Compute Module 4 (CM4)" )
                               echo "Compute Module 4 selected."
                               if ! command -v rpiboot &> /dev/null; then
                                echo "ERROR: 'rpiboot' command not found."
                                echo "Please install it (e.g., 'sudo apt install rpiboot') and re-run."
                                exit 1
                               fi
                               HARDWARE_MODEL="cm4"
                               break
                               ;;
                esac
        done
}

# Selects a single SD card target. Sets TARGET_DEVICE global.
# For CM4 uses rpiboot before/after detection.
select_target_device() {
        echo ""
        echo "--- Select Target SD Card ---"

        # CM4: use rpiboot before/after detection
        if [ "$HARDWARE_MODEL" = "cm4" ]; then
                local DISKS_BEFORE
                DISKS_BEFORE=$(lsblk -d -n -o NAME)
                echo "Please connect your CM4 to this computer in USB-boot mode."
                read -p "Press Enter to run 'sudo rpiboot' and mount the eMMC..."
                echo
                sudo rpiboot
                echo "'rpiboot' finished. Waiting up to 60s for device to appear..."
                local NEW_DISK=""
                local DISKS_AFTER
                for i in $(seq 1 60); do
                        sleep 1
                        DISKS_AFTER=$(lsblk -d -n -o NAME)
                        NEW_DISK=$(comm -13 <(echo "$DISKS_BEFORE" | sort) <(echo "$DISKS_AFTER" | sort))
                        if [ -n "$NEW_DISK" ]; then
                                echo "Device appeared after ${i}s."
                                sleep 2
                                break
                        fi
                        printf "."
                done
                echo

                if [ -z "$NEW_DISK" ]; then
                        echo "ERROR: No new disk detected after 60s."
                        echo "Please check connections and try again."
                        exit 1
                fi

                local NEW_DISK_SIZE
                NEW_DISK_SIZE=$(lsblk -d -n -o SIZE "/dev/$NEW_DISK")
                TARGET_DEVICE="/dev/$NEW_DISK"
                echo "Detected CM4 device: $TARGET_DEVICE ($NEW_DISK_SIZE)"
                HARDWARE_MODEL="rpi4"  # Use rpi4 template for CM4
                return
        fi

        echo "Detecting SD cards..."
        local BOOT_DISK
        BOOT_DISK=$(find_boot_disk)
        echo "(Excluding boot disk: $BOOT_DISK)"

        detect_sd_cards "$BOOT_DISK"

        if [ ${#SD_DEVICES[@]} -eq 0 ]; then
                echo "ERROR: No SD cards detected."
                echo "Please insert an SD card and try again."
                exit 1
        fi

        echo "Please select the target SD card:"
        PS3="Enter number (or 'q' to quit): "
        select device_choice in "${SD_DEVICES[@]}" "Quit"; do
                if [[ "$REPLY" =~ ^[Qq]$ ]] || [ "$device_choice" = "Quit" ]; then
                        echo "Aborting."
                        rm -f "$TEMP_SCRIPT_FILE"
                        exit 0
                fi
                if [ -n "$device_choice" ]; then
                        TARGET_DEVICE=$(echo "$device_choice" | awk '{print $1}')
                        echo "Selected: $TARGET_DEVICE"
                        break
                else
                        echo "Invalid selection."
                fi
        done
}

# Function to display final confirmation before flashing
confirm_flash() {
        local device="$1"
        local device_size=$(lsblk -d -n -o SIZE "$device" 2>/dev/null || echo "unknown")

        echo ""
        echo "=============================================="
        echo "         ⚠️  FINAL CONFIRMATION  ⚠️"
        echo "=============================================="
        echo ""
        echo "You are about to ERASE and FLASH:"
        echo ""
        echo "  Device: $device"
        echo "  Size:   $device_size"
        echo ""
        echo "  Hardware: $HARDWARE_MODEL"
        echo "  Mesh SSID: $MESH_SSID"
        echo "  Network: $LAN_CIDR_BLOCK"
        echo ""
        echo "⚠️  ALL DATA ON $device WILL BE DESTROYED! ⚠️"
        echo ""
        echo "=============================================="
        echo ""

        read -p "Type 'yes' to proceed, anything else to abort: " confirm
        if [ "$confirm" != "yes" ]; then
                echo ""
                echo "Aborted by user."
                exit 0
        fi

        echo ""
        echo "Proceeding with flash..."
}

# Resolve (rebuilding when possible) the tools tarball for a hardware model.
# Prints the path on stdout; returns 1 if unavailable. First boot has no
# download fallback — flashing without the tarball produces a node that
# cannot provision, so fail here at flash time instead.
resolve_tools_tarball() {
        local model="$1" tarball_name="" build_script=""
        case "$model" in
                cm4|rpi4) tarball_name="cm4-tools.tar.gz";   build_script="build-cm4-tarball.sh" ;;
                rpi5)     tarball_name="rpi5-tools.tar.gz";  build_script="build-rpi5-tarball.sh" ;;
                *)        return 1 ;;
        esac
        local repo_root
        repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
        local tarball_path="${repo_root}/install_packages/${tarball_name}"
        if command -v go >/dev/null 2>&1 && [ -f "${repo_root}/packaging/${build_script}" ]; then
                echo "Rebuilding ${tarball_name}..." >&2
                mkdir -p "${repo_root}/install_packages"
                if ! bash "${repo_root}/packaging/${build_script}" "$tarball_path" >&2; then
                        echo "WARNING: tarball rebuild failed, using existing copy if present" >&2
                fi
        fi
        if [ ! -f "$tarball_path" ]; then
                echo "ERROR: ${tarball_path} not found and could not be rebuilt." >&2
                echo "Build it with packaging/${build_script} (needs Go), or copy" >&2
                echo "install_packages/ from a machine that has built it." >&2
                return 1
        fi
        echo "$tarball_path"
}

# Copy the tools tarball onto the freshly flashed boot partition. rpi-imager
# can power the reader down when it finishes, so retry with a re-insert
# prompt before giving up.
embed_tools_tarball() {
        local target="$1" tarball_path="$2"
        local bootpart mnt attempt
        mnt=$(mktemp -d)
        for attempt in 1 2 3; do
                sudo partprobe "$target" 2>/dev/null || true
                sleep 2
                bootpart="${target}1"
                [ -b "${target}p1" ] && bootpart="${target}p1"
                if sudo mount "$bootpart" "$mnt" 2>/dev/null; then
                        sudo cp "$tarball_path" "$mnt/mesh-tools.tar.gz"
                        sudo sync
                        sudo umount "$mnt"
                        rmdir "$mnt"
                        echo "Embedded tools tarball: $(basename "$tarball_path")"
                        return 0
                fi
                echo "Boot partition not mountable (imager may have ejected the card)."
                read -p "Remove and re-insert the SD card, then press Enter to retry..."
        done
        rmdir "$mnt"
        echo "ERROR: Could not mount ${bootpart} to embed the tools tarball."
        echo "Mount the boot partition manually and copy:"
        echo "  ${tarball_path} -> <boot>/mesh-tools.tar.gz"
        echo "The node CANNOT provision without it."
        return 1
}

# Flash one SD card — Raspberry Pi path (rpi-imager)
flash_rpi() {
        local target="$1"

        local tools_tarball
        tools_tarball=$(resolve_tools_tarball "$HARDWARE_MODEL") || exit 1

        echo "Generating firstrun script from template..."
        sed -e "s|__HARDWARE_MODEL__|${HARDWARE_MODEL}|g" \
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
            -e "s|__ADMIN_PW__|${ADMIN_PW}|g" \
            -e "s|__AUTO_UPDATE__|${AUTO_UPDATE}|g" \
            -e "s|__NODE_HOSTNAME__|${NODE_HOSTNAME}|g" \
            "$TEMPLATE_FILE" > "$TEMP_SCRIPT_FILE"

        sudo rpi-imager --cli "$PI_OS_IMAGE_URL" "$target" --first-run-script "$TEMP_SCRIPT_FILE"

        embed_tools_tarball "$target" "$tools_tarball" || exit 1

        echo ""
        echo "=============================================="
        echo "           ✅ Flash complete: $target"
        echo "=============================================="
        echo ""
        echo " ONCE BOOTED, THE MESH NODE WILL AUTOMATICALLY START"
        echo " SETTING ITSELF UP AND WILL REBOOT MULTIPLE TIMES"
        echo " Just leave it alone, this process takes about ten"
        echo " minutes"
}


# --- Main Script ---

select_hardware

# --- 1. Check Dependencies ---
if ! command -v rpi-imager &> /dev/null; then
        echo "ERROR: 'rpi-imager' command not found. Please install it."
        exit 1
fi

if [ ! -f "$TEMPLATE_FILE" ]; then
        echo "ERROR: Template file '$TEMPLATE_FILE' not found."
        exit 1
fi
if ! command -v openssl &> /dev/null; then
        echo "ERROR: 'openssl' command not found. Needed for generating SAE key."
        exit 1
fi
if ! command -v bc &> /dev/null; then
        echo "ERROR: 'bc' command not found. Needed for network calculation."
        echo "Please install it (e.g., 'sudo apt install bc')."
        exit 1
fi
if ! command -v lsblk &> /dev/null; then
        echo "ERROR: 'lsblk' command not found. Needed for device detection."
        exit 1
fi
if ! command -v findmnt &> /dev/null; then
        echo "ERROR: 'findmnt' command not found. Needed for boot device detection."
        echo "Please install it (e.g., 'sudo apt install util-linux')."
        exit 1
fi
# Ensure config directory exists
mkdir -p "$CONFIG_DIR"

# --- 2. Load or Create Config ---
config_files=("$CONFIG_DIR"/*.conf)
num_configs=${#config_files[@]}

if [ ! -f "${config_files[0]}" ]; then
        num_configs=0
fi

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
                                if [ "$config_name" == "Cancel" ]; then
                                      echo "Aborting."
                                      exit 0
                                fi
                                if [ -n "$config_name" ]; then
                                      load_config "$CONFIG_DIR/$config_name.conf"
                                      break
                                else
                                      echo "Invalid selection."
                                fi
                               done
                               break
                               ;;
                        "Create a new configuration" )
                               ask_questions
                               save_config
                               break
                               ;;
                esac
        done
else
        echo "No saved configs found. Starting new setup."
        ask_questions
        save_config
fi


# --- 3. Multi-SD flash ---

# CM4 goes through its own single-device flow (rpiboot required)
if [ "$HARDWARE_MODEL" = "cm4" ]; then
        select_target_device
        confirm_flash "$TARGET_DEVICE"
        flash_rpi "$TARGET_DEVICE"
        rm -f "$TEMP_SCRIPT_FILE"
        exit 0
fi

# For all other hardware: detect all SD cards upfront, let user pick multiple

flash_multiple_cards() {
        local BOOT_DISK
        BOOT_DISK=$(find_boot_disk)

        while true; do
                echo ""
                echo "=============================================="
                echo "  Insert all SD cards you want to flash now,"
                echo "  then press Enter to detect them."
                echo "=============================================="
                read -p ""

                detect_sd_cards "$BOOT_DISK"

                if [ ${#SD_DEVICES[@]} -eq 0 ]; then
                        echo "No SD cards detected. Please insert cards and try again."
                        continue
                fi

                echo ""
                echo "Detected SD cards:"
                local i=1
                for dev in "${SD_DEVICES[@]}"; do
                        printf "  %d) %s\n" "$i" "$dev"
                        i=$((i + 1))
                done
                echo ""

                local selected_nums
                read -p "Enter card numbers to flash (e.g. 1 2 3), or 'r' to re-scan: " selected_nums

                [[ "$selected_nums" =~ ^[Rr]$ ]] && continue

                # Validate input
                local valid=true
                local nums=()
                for n in $selected_nums; do
                        if ! [[ "$n" =~ ^[0-9]+$ ]] || [ "$n" -lt 1 ] || [ "$n" -gt "${#SD_DEVICES[@]}" ]; then
                                echo "Invalid number: $n (valid range: 1-${#SD_DEVICES[@]})"
                                valid=false
                                break
                        fi
                        nums+=("$n")
                done
                $valid || continue

                [ ${#nums[@]} -eq 0 ] && echo "No cards selected." && continue

                echo ""
                echo "Will flash ${#nums[@]} card(s):"
                for n in "${nums[@]}"; do
                        echo "  - ${SD_DEVICES[$((n-1))]}"
                done
                echo ""
                read -p "Proceed? (Y/n): " proceed
                proceed=${proceed:-y}
                [[ "$proceed" =~ ^[Yy]$ ]] || continue

                local FLASH_COUNT=0
                for n in "${nums[@]}"; do
                        local dev_entry="${SD_DEVICES[$((n-1))]}"
                        TARGET_DEVICE=$(echo "$dev_entry" | awk '{print $1}')
                        echo ""
                        echo "=== Flashing card $((FLASH_COUNT+1)) of ${#nums[@]}: $TARGET_DEVICE ==="
                        flash_rpi "$TARGET_DEVICE"
                        FLASH_COUNT=$((FLASH_COUNT + 1))
                done

                echo ""
                echo "=============================================="
                echo "  Done. $FLASH_COUNT SD card(s) flashed."
                echo "=============================================="
                echo ""
                read -p "Flash another batch with the same settings? (y/N): " again
                again=${again:-n}
                [[ "$again" =~ ^[Yy]$ ]] || break
        done
}

flash_multiple_cards

rm -f "$TEMP_SCRIPT_FILE"
