#Requires -RunAsAdministrator
<#
.SYNOPSIS
    A script to image new mesh radio nodes on Windows
.DESCRIPTION
    Equivalent to linux.sh - flashes Raspberry Pi devices with mesh network
    configurations using rpi-imager.
.NOTES
    Must be run as Administrator.
#>

# --- Configuration ---
$ScriptDir          = if ($PSScriptRoot) { $PSScriptRoot } else { Split-Path -Parent $MyInvocation.MyCommand.Path }
$TEMPLATE_FILE      = Join-Path $ScriptDir "firstrun.sh.template"
$CONFIG_DIR         = Join-Path $ScriptDir ".mesh-configs"

$OS_IMAGE_URL  = "https://downloads.raspberrypi.com/raspios_lite_arm64/images/raspios_lite_arm64-2025-10-02/2025-10-01-raspios-trixie-arm64-lite.img.xz"

# --- Global State ---
$Script:HARDWARE_MODEL    = ""
$Script:TARGET_DEVICE     = ""
$Script:EUD_CONNECTION    = ""
$Script:LAN_AP_SSID       = ""
$Script:LAN_AP_KEY        = ""
$Script:MAX_EUDS_PER_NODE = 0
$Script:MESH_SSID         = ""
$Script:MESH_SAE_KEY      = ""
$Script:LAN_CIDR_BLOCK    = ""
$Script:AUTO_CHANNEL      = ""
$Script:GPS_ENABLED       = "y"
$Script:RADIO_PW          = ""
$Script:ADMIN_PW          = ""
$Script:AUTO_UPDATE       = ""
$Script:REGULATORY_DOMAIN = ""
$Script:HALOW_REGULATORY_DOMAIN = ""
$Script:HALOW_BW          = ""
$Script:HALOW_CHANNEL     = ""
$Script:NODE_HOSTNAME     = ""
$Script:RPI_IMAGER_PATH   = $null


# ============================================================
# Helper Functions
# ============================================================

function Generate-Password {
    param([int]$length = 10)
    $chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
    $bytes = New-Object byte[] $length
    [Security.Cryptography.RNGCryptoServiceProvider]::Create().GetBytes($bytes)
    $password = ""
    foreach ($byte in $bytes) { $password += $chars[$byte % $chars.Length] }
    return $password
}

function Test-RegulatoryDomain {
    param([string]$domain)
    $validDomains = @(
        "US","CA","GB","DE","FR","IT","ES","NL","BE","AT","CH","SE","NO","DK","FI",
        "PL","CZ","HU","GR","PT","IE","RO","BG","HR","SI","SK","LT","LV","EE","CY",
        "MT","LU","AU","NZ","JP","KR","TW","SG","MY","TH","PH","ID","VN","IN","CN",
        "BR","AR","MX","CL","CO","PE","ZA","IL","AE","SA","RU","UA","TR","EG","MA"
    )
    $domain = $domain.ToUpper()
    if ($validDomains -contains $domain) { return $domain }
    return $null
}

function Test-EuHalowRegion {
    param([string]$domain)
    $euHalowDomains = @(
        "AT","BE","BG","HR","CY","CZ","DK","EE","FI","FR","DE","GR","HU","IE",
        "IT","LV","LT","LU","MT","NL","PL","PT","RO","SK","SI","ES","SE",
        "GB","CH","NO"
    )
    return $euHalowDomains -contains $domain.ToUpper()
}

function Get-HalowRegulatoryDomain {
    param([string]$wifiDomain)
    $wifiDomain = $wifiDomain.ToUpper()
    if (Test-EuHalowRegion -domain $wifiDomain) { return "EU" }
    return $wifiDomain
}

# Ground-truth legal HaLow channel list per regulatory domain + bandwidth,
# kept byte-consistent with radio-setup.sh's halow_channel_valid() (see
# MANET/rootfs/usr/local/bin/radio-setup.sh). Returns an empty array for
# any domain/bw not covered here -- that includes EU + anything but 1MHz,
# and every domain other than US/EU (those are out of scope for this
# feature and get no channel validation at all).
function Get-HalowChannelsForDomainBw {
    param([string]$domain, [string]$bw)
    switch ($domain) {
        "US" {
            switch ($bw) {
                "1MHz" { return @(1,3,5,7,9,11,13,15,17,19,21,23,25,27,29,31,33,35,37,39,41,43,45,47,49,51) }
                "2MHz" { return @(2,6,10,14,18,22,26,30,34,38,42,46,50) }
                "4MHz" { return @(8,16,24,32,40,48) }
                "8MHz" { return @(12,28,44) }
            }
        }
        "EU" {
            switch ($bw) {
                "1MHz" { return @(1,3,5,7,9) }
            }
        }
    }
    return @()
}

# Default channel for a given (in-scope) regulatory domain + bandwidth.
function Get-HalowDefaultChannelForDomainBw {
    param([string]$domain, [string]$bw)
    switch ($domain) {
        "US" {
            switch ($bw) {
                "1MHz" { return 11 }
                "2MHz" { return 10 }
                "4MHz" { return 24 }
                "8MHz" { return 12 }
            }
        }
        "EU" {
            switch ($bw) {
                "1MHz" { return 5 }
            }
        }
    }
    return $null
}

# freq_MHz = (start_kHz + channel*500) / 1000, start is 902000 for US
# (and every out-of-scope domain) or 863000 for EU.
function Get-HalowFreqMhzForChannel {
    param([string]$domain, [int]$channel)
    $start = if ($domain -eq "EU") { 863000 } else { 902000 }
    return [math]::Round(($start + $channel * 500) / 1000.0, 1)
}

function Calculate-Capacity {
    param([string]$cidr, [int]$maxEuds)

    if ($cidr -notmatch '^(\d+\.\d+\.\d+\.\d+)/(\d+)$') { return $null }
    $ip     = $Matches[1]
    $prefix = [int]$Matches[2]

    $hostBits   = 32 - $prefix
    $totalHosts = [math]::Pow(2, $hostBits) - 2   # subtract network and broadcast
    $reserved   = 5

    $available = $totalHosts - $reserved
    if ($maxEuds -gt 0) {
        $maxNodes = [math]::Floor($available / (1 + $maxEuds))
        $eudPool  = $maxNodes * $maxEuds
    } else {
        $maxNodes = $available
        $eudPool  = 0
    }

    return @{
        Total    = [int]$totalHosts
        Services = $reserved
        EudPool  = [int]$eudPool
        MaxNodes = [int]$maxNodes
    }
}

# Generates a Linux SHA-512 crypt hash for use in /etc/shadow.
# Tries openssl (Git for Windows) then WSL.
# Returns $null if no suitable tool is found.
function Get-LinuxPasswordHash {
    param([string]$password)

    # Try openssl in PATH (present when Git for Windows is installed)
    $openssl = Get-Command openssl -ErrorAction SilentlyContinue
    if ($openssl) {
        $hash = & openssl passwd -6 $password 2>$null
        if ($LASTEXITCODE -eq 0 -and $hash -and $hash.StartsWith('$6$')) {
            return $hash
        }
    }

    # Try WSL
    $wsl = Get-Command wsl -ErrorAction SilentlyContinue
    if ($wsl) {
        $hash = & wsl openssl passwd -6 $password 2>$null
        if ($LASTEXITCODE -eq 0 -and $hash -and $hash.StartsWith('$6$')) {
            return $hash
        }
    }

    return $null
}

# ============================================================
# Hardware and Device Selection
# ============================================================

function Select-HardwareAndTargetDevice {
    Write-Host ""
    Write-Host "--- 1. Select Hardware ---"
    Write-Host "Select hardware platform:"
    Write-Host "1. Raspberry Pi 5"
    Write-Host "2. Raspberry Pi 4B"
    Write-Host "3. Compute Module 4 (CM4)"

    do {
        $choice = Read-Host "Enter choice (1-3)"
        switch ($choice) {
            "1" { $Script:HARDWARE_MODEL = "rpi5"; break }
            "2" { $Script:HARDWARE_MODEL = "rpi4"; break }
            "3" { $Script:HARDWARE_MODEL = "rpi4"; break }
        }
    } while ($choice -notmatch "^[123]$")

    $rpiImagerPaths = @(
        (Get-Command rpi-imager-cli.cmd -ErrorAction SilentlyContinue).Source,
        "C:\Program Files\Raspberry Pi Ltd\Imager\rpi-imager-cli.cmd",
        "C:\Program Files\Raspberry Pi Ltd\Imager\rpi-imager.exe",
        "C:\Program Files (x86)\Raspberry Pi Imager\rpi-imager.exe",
        "C:\Program Files\Raspberry Pi Imager\rpi-imager.exe",
        (Get-Command rpi-imager -ErrorAction SilentlyContinue).Source
    )
    foreach ($p in $rpiImagerPaths) {
        if ($p -and (Test-Path $p)) { $Script:RPI_IMAGER_PATH = $p; break }
    }
    if (-not $Script:RPI_IMAGER_PATH) {
        Write-Host "ERROR: Raspberry Pi Imager not found!" -ForegroundColor Red
        Write-Host "Please install from: https://www.raspberrypi.com/software/"
        exit 1
    }
    Write-Host "Found Raspberry Pi Imager: $($Script:RPI_IMAGER_PATH)"

    if ($choice -eq "3") {
        Write-Host ""
        Write-Host "NOTE: For CM4 on Windows, you must run rpiboot manually before continuing." -ForegroundColor Yellow
        Write-Host "Once rpiboot has mounted the eMMC, press Enter to continue."
        Read-Host "Press Enter when the CM4 eMMC is mounted and ready"
    }

    Write-Host ""
    Write-Host "--- 2. Select Target Device ---"

    $bootDisk = (Get-Disk | Where-Object { $_.IsBoot -eq $true }).Number
    $disks = Get-Disk | Where-Object {
        $_.Number -ne $bootDisk -and
        $_.OperationalStatus -eq "Online" -and
        $_.Size -gt 0
    }

    if ($disks.Count -eq 0) {
        Write-Host "ERROR: No suitable target devices found." -ForegroundColor Red
        Write-Host "Please ensure your SD card reader, USB drive, or CM4 eMMC is connected."
        exit 1
    }

    Write-Host "Available devices:"
    $i = 1; $diskMap = @{}
    foreach ($disk in $disks) {
        $sizeGB = [math]::Round($disk.Size / 1GB, 2)
        Write-Host "$i. Disk $($disk.Number): $($disk.FriendlyName) - ${sizeGB}GB"
        $diskMap[$i] = $disk; $i++
    }
    Write-Host "$i. Quit"

    do {
        $c = Read-Host "Enter device number (1-$i)"
        $n = 0
        if ([int]::TryParse($c, [ref]$n)) {
            if ($n -eq $i) { Write-Host "Aborting."; exit 0 }
            if ($diskMap.ContainsKey($n)) {
                $Script:TARGET_DEVICE = $diskMap[$n].Number
                Write-Host "Selected: Disk $($Script:TARGET_DEVICE) - $($diskMap[$n].FriendlyName)"
                break
            }
        }
        Write-Host "Invalid selection." -ForegroundColor Red
    } while ($true)
}

function Confirm-Flash {
    param([int]$DiskNumber)
    $disk   = Get-Disk -Number $DiskNumber
    $sizeGB = [math]::Round($disk.Size / 1GB, 2)

    Write-Host ""
    Write-Host "==============================================" -ForegroundColor Yellow
    Write-Host "         *** FINAL CONFIRMATION ***"           -ForegroundColor Yellow
    Write-Host "==============================================" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "You are about to ERASE and FLASH:"
    Write-Host ""
    Write-Host "  Device: Disk $DiskNumber - $($disk.FriendlyName)"
    Write-Host "  Size:   ${sizeGB}GB"
    Write-Host ""
    Write-Host "  Hardware:  $($Script:HARDWARE_MODEL)"
    Write-Host "  Mesh SSID: $($Script:MESH_SSID)"
    Write-Host "  Network:   $($Script:LAN_CIDR_BLOCK)"
    Write-Host ""
    Write-Host "WARNING: ALL DATA ON DISK $DiskNumber WILL BE DESTROYED!" -ForegroundColor Red
    Write-Host ""
    Write-Host "==============================================" -ForegroundColor Yellow
    Write-Host ""

    $confirm = Read-Host "Type 'yes' to proceed, anything else to abort"
    if ($confirm -ne "yes") { Write-Host ""; Write-Host "Aborted by user."; exit 0 }
    Write-Host ""; Write-Host "Proceeding with flash..."
}

# ============================================================
# Configuration Questions / Save / Load
# ============================================================

function Ask-LanCidr {
    param([int]$maxEuds)

    $defaultCidr = "10.30.2.0/24"

    while ($true) {
        $confirm = Read-Host "Use default mesh network range ( $defaultCidr )? (Y/n)"
        if ([string]::IsNullOrWhiteSpace($confirm) -or $confirm -match "^[Yy]") {
            $Script:LAN_CIDR_BLOCK = $defaultCidr
        } else {
            while ($true) {
                $custom = Read-Host "Enter custom CIDR block for the mesh (e.g., 10.10.0.0/16)"
                if ($custom -notmatch '^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}/(\d{1,2})$') {
                    Write-Host "ERROR: Invalid format. Must be x.x.x.x/yy" -ForegroundColor Red
                    continue
                }
                $prefix = [int]$Matches[1]
                if ($prefix -lt 16 -or $prefix -gt 26) {
                    Write-Host "ERROR: Prefix /$prefix is invalid. Must be between /16 and /26." -ForegroundColor Red
                    continue
                }
                $ipPart = ($custom -split '/')[0]
                $octets = $ipPart -split '\.'
                $o1 = [int]$octets[0]; $o2 = [int]$octets[1]
                $isPrivate = ($o1 -eq 10) -or
                             ($o1 -eq 172 -and $o2 -ge 16 -and $o2 -le 31) -or
                             ($o1 -eq 192 -and $o2 -eq 168)
                if (-not $isPrivate) {
                    Write-Host "ERROR: $ipPart is not in a private range (10.x, 172.16-31.x, 192.168.x)." -ForegroundColor Red
                    continue
                }
                $Script:LAN_CIDR_BLOCK = $custom
                break
            }
        }

        if ($maxEuds -gt 0) {
            $cap = Calculate-Capacity -cidr $Script:LAN_CIDR_BLOCK -maxEuds $maxEuds
            if ($cap) {
                Write-Host ""
                Write-Host "=== Network Capacity Analysis ==="
                Write-Host "Network: $($Script:LAN_CIDR_BLOCK)"
                Write-Host "  Total usable IPs        : $($cap.Total)"
                Write-Host "  Reserved for services   : $($cap.Services)"
                Write-Host "  Reserved for EUD pool   : $($cap.EudPool) (${maxEuds} EUDs x $($cap.MaxNodes) nodes)"
                Write-Host "  Available for mesh nodes: $($cap.MaxNodes)"
                Write-Host "=================================="
                if ($cap.MaxNodes -lt 5) {
                    Write-Host "WARNING: Only $($cap.MaxNodes) mesh node addresses. Consider a larger network or fewer EUDs." -ForegroundColor Yellow
                }
                $accept = Read-Host "Accept this configuration? (Y/n)"
                if ([string]::IsNullOrWhiteSpace($accept) -or $accept -match "^[Yy]") { break }
                Write-Host "Let's reconfigure..."
            } else {
                Write-Host "Using network: $($Script:LAN_CIDR_BLOCK)"
                break
            }
        } else {
            Write-Host "Using network: $($Script:LAN_CIDR_BLOCK)"
            break
        }
    }
}

function Ask-Questions {
    Write-Host "--- Starting New Configuration ---"

    Write-Host "`nSelect EUD (client) connection type:"
    Write-Host "1. Wired"
    Write-Host "2. Wireless"
    Write-Host "3. Auto"
    do {
        $choice = Read-Host "Enter choice (1-3)"
        switch ($choice) {
            "1" { $Script:EUD_CONNECTION = "wired";    break }
            "2" { $Script:EUD_CONNECTION = "wireless"; break }
            "3" { $Script:EUD_CONNECTION = "auto";     break }
        }
    } while ($choice -notmatch "^[123]$")

    if ($Script:EUD_CONNECTION -eq "wireless" -or $Script:EUD_CONNECTION -eq "auto") {
        Write-Host "EUD wifi network name. This name will have the last 4 of the ethernet MAC address appended to it for node identification."
        $Script:LAN_AP_SSID = Read-Host "Enter EUD access point SSID name"
        while ($true) {
            $key = Read-Host "Enter LAN AP WPA2 Key (8-63 chars) [or press Enter to generate]"
            Write-Host ""
            if ([string]::IsNullOrWhiteSpace($key)) {
                $bytes = New-Object byte[] 33
                [Security.Cryptography.RNGCryptoServiceProvider]::Create().GetBytes($bytes)
                $Script:LAN_AP_KEY = [Convert]::ToBase64String($bytes)
                Write-Host "Generated LAN AP Key: $($Script:LAN_AP_KEY)"
                break
            }
            if ($key.Length -lt 8 -or $key.Length -gt 63) {
                Write-Host "ERROR: Key must be between 8 and 63 characters." -ForegroundColor Red
            } else { $Script:LAN_AP_KEY = $key; break }
        }
    } else {
        $Script:LAN_AP_SSID       = ""
        $Script:LAN_AP_KEY        = ""
        $Script:MAX_EUDS_PER_NODE = 0
    }

    $Script:MESH_SSID = Read-Host "Enter MESH SSID Name"

    while ($true) {
        $key = Read-Host "Enter MESH SAE Key (WPA3 password, 8-63 chars) [or press Enter to generate]"
        Write-Host ""
        if ([string]::IsNullOrWhiteSpace($key)) {
            $bytes = New-Object byte[] 33
            [Security.Cryptography.RNGCryptoServiceProvider]::Create().GetBytes($bytes)
            $Script:MESH_SAE_KEY = [Convert]::ToBase64String($bytes)
            Write-Host "Generated SAE Key: $($Script:MESH_SAE_KEY)"
            break
        }
        if ($key.Length -lt 8 -or $key.Length -gt 63) {
            Write-Host "ERROR: Key must be between 8 and 63 characters." -ForegroundColor Red
        } else { $Script:MESH_SAE_KEY = $key; break }
    }

    while ($true) {
        $domain = Read-Host "Enter WiFi regulatory domain (2-letter country code, default: US)"
        if ([string]::IsNullOrWhiteSpace($domain)) { $domain = "US" }
        $validated = Test-RegulatoryDomain -domain $domain
        if ($validated) {
            $Script:REGULATORY_DOMAIN       = $validated
            $Script:HALOW_REGULATORY_DOMAIN = Get-HalowRegulatoryDomain -wifiDomain $validated
            Write-Host "Using regulatory domain: $($Script:REGULATORY_DOMAIN)"
            if ($Script:HALOW_REGULATORY_DOMAIN -ne $Script:REGULATORY_DOMAIN) {
                Write-Host "Using HaLow regulatory region: $($Script:HALOW_REGULATORY_DOMAIN)"
            }
            break
        } else {
            Write-Host "ERROR: Invalid regulatory domain: $domain" -ForegroundColor Red
            Write-Host "Enter a valid 2-letter ISO country code (e.g., US, GB, DE, JP, AU)"
            Write-Host "NOTE: EU is not a country code, use your actual country"
        }
    }

    # HaLow Bandwidth
    if ($Script:HALOW_REGULATORY_DOMAIN -eq "EU") {
        $halowBwOptions = @("1MHz")
        $halowBwDefault = "1MHz"
    } else {
        $halowBwOptions = @("1MHz","2MHz","4MHz","8MHz")
        $halowBwDefault = "2MHz"
    }
    while ($true) {
        $bw = Read-Host "Enter HaLow bandwidth (options: $($halowBwOptions -join '/'), default: $halowBwDefault)"
        if ([string]::IsNullOrWhiteSpace($bw)) { $bw = $halowBwDefault }
        if ($halowBwOptions -contains $bw) {
            $Script:HALOW_BW = $bw
            Write-Host "Using HaLow bandwidth: $($Script:HALOW_BW)"
            break
        } else {
            Write-Host "ERROR: Invalid HaLow bandwidth: $bw" -ForegroundColor Red
            Write-Host "Valid options for $($Script:HALOW_REGULATORY_DOMAIN): $($halowBwOptions -join ', ')"
        }
    }

    # HaLow Channel
    $halowChannelList = Get-HalowChannelsForDomainBw -domain $Script:HALOW_REGULATORY_DOMAIN -bw $Script:HALOW_BW
    if ($halowChannelList.Count -gt 0) {
        $halowDefaultChannel = Get-HalowDefaultChannelForDomainBw -domain $Script:HALOW_REGULATORY_DOMAIN -bw $Script:HALOW_BW
        $halowDefaultFreq    = Get-HalowFreqMhzForChannel -domain $Script:HALOW_REGULATORY_DOMAIN -channel $halowDefaultChannel
        $halowChannelDesc = ($halowChannelList | ForEach-Object {
            $freq = Get-HalowFreqMhzForChannel -domain $Script:HALOW_REGULATORY_DOMAIN -channel $_
            "$_ ($freq MHz)"
        }) -join ", "
        Write-Host "Available channels for $($Script:HALOW_REGULATORY_DOMAIN)/$($Script:HALOW_BW): $halowChannelDesc"

        while ($true) {
            $ch = Read-Host "Enter HaLow channel [or press Enter for Auto (channel $halowDefaultChannel, $halowDefaultFreq MHz)]"
            if ([string]::IsNullOrWhiteSpace($ch)) { $Script:HALOW_CHANNEL = ""; break }
            if ($ch -match '^\d+$' -and ($halowChannelList -contains [int]$ch)) {
                $Script:HALOW_CHANNEL = $ch
                Write-Host "Using HaLow channel: $($Script:HALOW_CHANNEL)"
                break
            } else {
                Write-Host "ERROR: Invalid HaLow channel for $($Script:HALOW_REGULATORY_DOMAIN)/$($Script:HALOW_BW): $ch" -ForegroundColor Red
                Write-Host "Valid channels: $($halowChannelList -join ', ')"
            }
        }
    } else {
        # Out-of-scope domain (not US or EU) -- no ground-truth channel
        # table exists, so accept whatever's typed (or empty for Auto)
        # without validation.
        $Script:HALOW_CHANNEL = Read-Host "Enter HaLow channel [or press Enter for Auto]"
    }

    $hn = Read-Host "Enter node hostname [or press Enter for auto]"
    $Script:NODE_HOSTNAME = if ([string]::IsNullOrWhiteSpace($hn)) { "" } else { $hn }
    if ($Script:NODE_HOSTNAME) {
        Write-Host "Hostname will be: $($Script:NODE_HOSTNAME)-$($Script:MESH_SSID)-<mac>"
    } else {
        Write-Host "Hostname will be: $($Script:MESH_SSID)-<mac>"
    }

    Write-Host "The device will have a user called 'radio' for SSH access."
    $pw = Read-Host "Enter a password for the radio user [or press Enter to default to 'radio']"
    Write-Host ""
    $Script:RADIO_PW = if ([string]::IsNullOrWhiteSpace($pw)) { Write-Host "Setting default password"; "radio" } else { $pw }
    Write-Host "Radio password set to: $($Script:RADIO_PW)"

    Write-Host ""
    Write-Host "The network administrator password is used to access the mesh admin interface."
    $adminPw = Read-Host "Enter admin password [or press Enter to generate 10-char random]"
    Write-Host ""
    if ([string]::IsNullOrWhiteSpace($adminPw)) {
        $Script:ADMIN_PW = Generate-Password -length 10
        Write-Host "Generated admin password: $($Script:ADMIN_PW)"
    } else {
        $Script:ADMIN_PW = $adminPw
        Write-Host "Admin password set."
    }

    Write-Host ""
    $r = Read-Host "Enable automatic updates for MANET tools? (y/N)"
    if ($r -match "^[Yy]") {
        $Script:AUTO_UPDATE = "y"; Write-Host "Automatic updates enabled."
    } else {
        $Script:AUTO_UPDATE = "n"; Write-Host "Automatic updates disabled."
    }

    if ($Script:EUD_CONNECTION -eq "wireless" -or $Script:EUD_CONNECTION -eq "auto") {
        while ($true) {
            $input = Read-Host "Maximum EUDs per node's AP (1-20)"
            if ($input -match '^\d+$' -and [int]$input -ge 1 -and [int]$input -le 20) {
                $Script:MAX_EUDS_PER_NODE = [int]$input; break
            } else {
                Write-Host "ERROR: Please enter a number between 1 and 20." -ForegroundColor Red
            }
        }
    }

    Ask-LanCidr -maxEuds $Script:MAX_EUDS_PER_NODE

    if ($Script:EUD_CONNECTION -eq "wireless" -or $Script:EUD_CONNECTION -eq "auto") {
        $Script:AUTO_CHANNEL = "n"
        Write-Host "Automatic WiFi Channel Selection disabled (not compatible with Wireless/Auto EUD mode)"
    } else {
        $r = Read-Host "Use Automatic WiFi Channel Selection? (Y/n)"
        $Script:AUTO_CHANNEL = if ([string]::IsNullOrWhiteSpace($r) -or $r -match "^[Yy]") { "y" } else { "n" }
    }

    $gr = Read-Host "Does this node have a GPS module? (Y/n)"
    $Script:GPS_ENABLED = if ([string]::IsNullOrWhiteSpace($gr) -or $gr -match "^[Yy]") { "y" } else { "n" }

    Write-Host "----------------------------------"
}

function Save-Config {
    Write-Host ""
    $save_choice = Read-Host "Save this configuration? (Y/n)"
    if (-not ([string]::IsNullOrWhiteSpace($save_choice) -or $save_choice -match "^[Yy]")) { return }

    $config_name = Read-Host "Enter a name for this config"
    if ([string]::IsNullOrWhiteSpace($config_name)) { Write-Host "Invalid name, skipping save."; return }

    $CONFIG_FILE = Join-Path $CONFIG_DIR "$config_name.conf"
    $content = @"
# Mesh Config: $config_name
EUD_CONNECTION="$($Script:EUD_CONNECTION)"
LAN_AP_SSID="$($Script:LAN_AP_SSID)"
LAN_AP_KEY="$($Script:LAN_AP_KEY)"
MAX_EUDS_PER_NODE="$($Script:MAX_EUDS_PER_NODE)"
REGULATORY_DOMAIN="$($Script:REGULATORY_DOMAIN)"
HALOW_REGULATORY_DOMAIN="$($Script:HALOW_REGULATORY_DOMAIN)"
HALOW_BW="$($Script:HALOW_BW)"
HALOW_CHANNEL="$($Script:HALOW_CHANNEL)"
MESH_SSID="$($Script:MESH_SSID)"
MESH_SAE_KEY="$($Script:MESH_SAE_KEY)"
LAN_CIDR_BLOCK="$($Script:LAN_CIDR_BLOCK)"
AUTO_CHANNEL="$($Script:AUTO_CHANNEL)"
GPS_ENABLED="$($Script:GPS_ENABLED)"
RADIO_PW="$($Script:RADIO_PW)"
ADMIN_PW="$($Script:ADMIN_PW)"
AUTO_UPDATE="$($Script:AUTO_UPDATE)"
NODE_HOSTNAME="$($Script:NODE_HOSTNAME)"
"@
    [System.IO.File]::WriteAllText($CONFIG_FILE, $content.Replace("`r`n", "`n"))
    Write-Host "Configuration saved to $CONFIG_FILE"
}

function Load-Config {
    param([string]$ConfigFile)
    Write-Host "Loading config from $ConfigFile..."

    Get-Content $ConfigFile | ForEach-Object {
        if ($_ -match '^([^=]+)="([^"]*)"') {
            switch ($Matches[1]) {
                "EUD_CONNECTION"          { $Script:EUD_CONNECTION          = $Matches[2] }
                "LAN_AP_SSID"             { $Script:LAN_AP_SSID             = $Matches[2] }
                "LAN_AP_KEY"              { $Script:LAN_AP_KEY               = $Matches[2] }
                "MAX_EUDS_PER_NODE"       { $Script:MAX_EUDS_PER_NODE        = [int]$Matches[2] }
                "REGULATORY_DOMAIN"       { $Script:REGULATORY_DOMAIN        = $Matches[2] }
                "HALOW_REGULATORY_DOMAIN" { $Script:HALOW_REGULATORY_DOMAIN  = $Matches[2] }
                "HALOW_BW"                { $Script:HALOW_BW                  = $Matches[2] }
                "HALOW_CHANNEL"           { $Script:HALOW_CHANNEL             = $Matches[2] }
                "MESH_SSID"               { $Script:MESH_SSID                = $Matches[2] }
                "MESH_SAE_KEY"            { $Script:MESH_SAE_KEY              = $Matches[2] }
                "LAN_CIDR_BLOCK"          { $Script:LAN_CIDR_BLOCK            = $Matches[2] }
                "AUTO_CHANNEL"            { $Script:AUTO_CHANNEL              = $Matches[2] }
                "GPS_ENABLED"             { $Script:GPS_ENABLED               = $Matches[2] }
                "RADIO_PW"                { $Script:RADIO_PW                  = $Matches[2] }
                "ADMIN_PW"                { $Script:ADMIN_PW                  = $Matches[2] }
                "AUTO_UPDATE"             { $Script:AUTO_UPDATE               = $Matches[2] }
                "NODE_HOSTNAME"           { $Script:NODE_HOSTNAME             = $Matches[2] }
            }
        }
    }

    if (-not $Script:HALOW_REGULATORY_DOMAIN) {
        $Script:HALOW_REGULATORY_DOMAIN = Get-HalowRegulatoryDomain -wifiDomain $Script:REGULATORY_DOMAIN
    }
    if (-not $Script:HALOW_BW) {
        $Script:HALOW_BW = if ($Script:HALOW_REGULATORY_DOMAIN -eq "EU") { "1MHz" } else { "2MHz" }
    }

    Write-Host "--- Loaded Configuration ---"
    Write-Host "  EUD Connection: $($Script:EUD_CONNECTION)"
    if ($Script:EUD_CONNECTION -eq "wireless" -or $Script:EUD_CONNECTION -eq "auto") {
        Write-Host "  LAN AP SSID: $($Script:LAN_AP_SSID)"
        Write-Host "  LAN AP Key: $($Script:LAN_AP_KEY)"
        Write-Host "  Max EUDs per node: $($Script:MAX_EUDS_PER_NODE)"
    }
    Write-Host "  Regulatory Domain: $($Script:REGULATORY_DOMAIN)"
    Write-Host "  HaLow Regulatory Region: $($Script:HALOW_REGULATORY_DOMAIN)"
    Write-Host "  HaLow Bandwidth: $($Script:HALOW_BW)"
    Write-Host "  HaLow Channel: $(if ($Script:HALOW_CHANNEL) { $Script:HALOW_CHANNEL } else { 'Auto' })"
    Write-Host "  Mesh SSID: $($Script:MESH_SSID)"
    Write-Host "  Mesh SAE Key: $($Script:MESH_SAE_KEY)"
    Write-Host "  LAN CIDR Block: $($Script:LAN_CIDR_BLOCK)"
    Write-Host "  Auto Channel: $($Script:AUTO_CHANNEL)"
    Write-Host "  GPS Enabled: $($Script:GPS_ENABLED)"
    Write-Host "  User password: $($Script:RADIO_PW)"
    Write-Host "  Admin password: $(if ($Script:ADMIN_PW) { $Script:ADMIN_PW } else { '(not set)' })"
    Write-Host "  Auto Update: $(if ($Script:AUTO_UPDATE) { $Script:AUTO_UPDATE } else { 'n' })"
    Write-Host "  Node Hostname: $(if ($Script:NODE_HOSTNAME) { $Script:NODE_HOSTNAME } else { '(auto)' })"
    Write-Host "----------------------------"
}


# ============================================================
# Main Script
# ============================================================

$currentPrincipal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "ERROR: This script must be run as Administrator!" -ForegroundColor Red
    Write-Host "Right-click PowerShell and select 'Run as Administrator'"
    exit 1
}

Write-Host "Script directory: $ScriptDir"

Select-HardwareAndTargetDevice

if (-not (Test-Path $TEMPLATE_FILE)) {
    Write-Host "ERROR: Template file '$TEMPLATE_FILE' not found." -ForegroundColor Red
    exit 1
}
if (-not (Test-Path $CONFIG_DIR)) {
    New-Item -ItemType Directory -Path $CONFIG_DIR | Out-Null
}

$configFiles = Get-ChildItem -Path $CONFIG_DIR -Filter "*.conf" -ErrorAction SilentlyContinue

if ($configFiles.Count -gt 0) {
    Write-Host "Found $($configFiles.Count) saved configuration(s)."
    Write-Host "What would you like to do?"
    Write-Host "1. Load a saved configuration"
    Write-Host "2. Create a new configuration"

    do {
        $choice = Read-Host "Enter choice (1-2)"
        if ($choice -eq "1") {
            Write-Host "`nPlease select a configuration to load:"
            $i = 1; $configMap = @{}
            foreach ($file in $configFiles) {
                Write-Host "$i. $($file.BaseName)"
                $configMap[$i] = $file.FullName; $i++
            }
            Write-Host "$i. Cancel"
            do {
                $cc = Read-Host "Enter number (1-$i)"
                $cn = 0
                if ([int]::TryParse($cc, [ref]$cn)) {
                    if ($cn -eq $i) { Write-Host "Aborting."; exit 0 }
                    if ($configMap.ContainsKey($cn)) { Load-Config -ConfigFile $configMap[$cn]; break }
                }
                Write-Host "Invalid selection." -ForegroundColor Red
            } while ($true)
            break
        } elseif ($choice -eq "2") {
            Ask-Questions
            Save-Config
            break
        }
    } while ($choice -notmatch "^[12]$")
} else {
    Write-Host "No saved configs found. Starting new setup."
    Ask-Questions
    Save-Config
}

# ============================================================
# Raspberry Pi Flashing Path (all Pi models including CM4)
# ============================================================

Write-Host "Generating firstrun script from template..."
$templateContent = [System.IO.File]::ReadAllText($TEMPLATE_FILE)

$templateContent = $templateContent `
    -replace '__HARDWARE_MODEL__',          $Script:HARDWARE_MODEL `
    -replace '__EUD_CONNECTION__',          $Script:EUD_CONNECTION `
    -replace '__LAN_AP_SSID__',             $Script:LAN_AP_SSID `
    -replace '__LAN_AP_KEY__',              $Script:LAN_AP_KEY `
    -replace '__MAX_EUDS_PER_NODE__',       $Script:MAX_EUDS_PER_NODE `
    -replace '__MESH_SSID__',               $Script:MESH_SSID `
    -replace '__MESH_SAE_KEY__',            $Script:MESH_SAE_KEY `
    -replace '__LAN_CIDR_BLOCK__',          $Script:LAN_CIDR_BLOCK `
    -replace '__AUTO_CHANNEL__',            $Script:AUTO_CHANNEL `
    -replace '__GPS_ENABLED__',             $Script:GPS_ENABLED `
    -replace '__RADIO_PW__',                $Script:RADIO_PW `
    -replace '__REGULATORY_DOMAIN__',       $Script:REGULATORY_DOMAIN `
    -replace '__HALOW_REGULATORY_DOMAIN__', $Script:HALOW_REGULATORY_DOMAIN `
    -replace '__HALOW_BW__',                $Script:HALOW_BW `
    -replace '__HALOW_CHANNEL__',           $Script:HALOW_CHANNEL `
    -replace '__ADMIN_PW__',                $Script:ADMIN_PW `
    -replace '__AUTO_UPDATE__',             $Script:AUTO_UPDATE `
    -replace '__NODE_HOSTNAME__',           $Script:NODE_HOSTNAME

# First boot has no download fallback — the tools tarball must be embedded
# on the boot partition. Windows cannot rebuild it (needs bash + Go), so a
# prebuilt copy from install_packages\ is required before flashing.
$tarName = if ($Script:HARDWARE_MODEL -eq "rpi5") { "rpi5-tools.tar.gz" } else { "cm4-tools.tar.gz" }
$RepoRoot = Split-Path $ScriptDir -Parent
$ToolsTarball = Join-Path (Join-Path $RepoRoot "install_packages") $tarName
if (-not (Test-Path $ToolsTarball)) {
    Write-Host "ERROR: $ToolsTarball not found." -ForegroundColor Red
    Write-Host "Build it on macOS/Linux with packaging scripts, then copy" -ForegroundColor Red
    Write-Host "the install_packages folder to this machine. Without it the" -ForegroundColor Red
    Write-Host "node cannot provision on first boot." -ForegroundColor Red
    exit 1
}

$flashCount = 0
$keepFlashing = $true

while ($keepFlashing) {

    $tempScript = Join-Path $ScriptDir "firstrun.sh"
    Write-Host "Writing firstrun script to: $tempScript"
    [System.IO.File]::WriteAllText($tempScript, $templateContent.Replace("`r`n", "`n"))

    if (-not (Test-Path $tempScript)) {
        Write-Host "ERROR: Failed to write firstrun script!" -ForegroundColor Red
        exit 1
    }

    Confirm-Flash -DiskNumber $Script:TARGET_DEVICE

    Write-Host "Running Raspberry Pi Imager..."
    $targetDrive = "\\.\PhysicalDrive$($Script:TARGET_DEVICE)"
    & $Script:RPI_IMAGER_PATH --cli $OS_IMAGE_URL $targetDrive --first-run-script "$tempScript"

    Remove-Item $tempScript -Force -ErrorAction SilentlyContinue

    if ($LASTEXITCODE -eq 0) {
        $flashCount++

        # Embed the tools tarball. The imager ejects the card when done,
        # so ask for a re-insert and wait for the bootfs volume to mount.
        Write-Host ""
        Write-Host "Embedding tools tarball on the boot partition..."
        Write-Host "Remove and re-insert the SD card so Windows mounts it." -ForegroundColor Yellow
        $bootVol = $null
        for ($i = 0; $i -lt 24 -and -not $bootVol; $i++) {
            Start-Sleep -Seconds 5
            $bootVol = Get-Volume -ErrorAction SilentlyContinue |
                Where-Object { $_.FileSystemLabel -eq "bootfs" -and $_.DriveLetter } |
                Select-Object -First 1
        }
        if ($bootVol) {
            Copy-Item $ToolsTarball "$($bootVol.DriveLetter):\mesh-tools.tar.gz" -Force
            Write-Host "Embedded $tarName on $($bootVol.DriveLetter):\" -ForegroundColor Green
        } else {
            Write-Host "ERROR: bootfs volume never appeared." -ForegroundColor Red
            Write-Host "Manually copy the tarball to the boot partition before booting:" -ForegroundColor Red
            Write-Host "  $ToolsTarball -> <boot>\mesh-tools.tar.gz" -ForegroundColor Red
            Write-Host "The node CANNOT provision without it." -ForegroundColor Red
        }

        Write-Host ""
        Write-Host "==============================================" -ForegroundColor Green
        Write-Host "           DONE: Flash complete!" -ForegroundColor Green
        Write-Host "==============================================" -ForegroundColor Green
        Write-Host ""
        Write-Host " ONCE BOOTED, THE MESH NODE WILL AUTOMATICALLY START"
        Write-Host " SETTING ITSELF UP AND WILL REBOOT MULTIPLE TIMES"
        Write-Host " Just leave it alone, this process takes about ten minutes"
        Write-Host ""
    } else {
        Write-Host ""
        Write-Host "ERROR: rpi-imager exited with code $LASTEXITCODE" -ForegroundColor Red
        Write-Host ""
    }

    Write-Host "==============================================" -ForegroundColor Cyan
    $again = Read-Host "Flash another card with the same settings? (y/N)"
    if ($again -notmatch "^[Yy]") {
        $keepFlashing = $false
    } else {
        Write-Host ""
        Write-Host "Insert the next SD card, then select the target device."

        $bootDisk = (Get-Disk | Where-Object { $_.IsBoot -eq $true }).Number
        $disks = Get-Disk | Where-Object {
            $_.Number -ne $bootDisk -and
            $_.OperationalStatus -eq "Online" -and
            $_.Size -gt 0
        }

        if ($disks.Count -eq 0) {
            Write-Host "ERROR: No suitable target devices found." -ForegroundColor Red
            $keepFlashing = $false
        } else {
            Write-Host "Available devices:"
            $i = 1; $diskMap = @{}
            foreach ($disk in $disks) {
                $sizeGB = [math]::Round($disk.Size / 1GB, 2)
                Write-Host "$i. Disk $($disk.Number): $($disk.FriendlyName) - ${sizeGB}GB"
                $diskMap[$i] = $disk; $i++
            }
            Write-Host "$i. Done (stop flashing)"

            do {
                $c = Read-Host "Enter device number (1-$i)"
                $n = 0
                if ([int]::TryParse($c, [ref]$n)) {
                    if ($n -eq $i) { $keepFlashing = $false; break }
                    if ($diskMap.ContainsKey($n)) {
                        $Script:TARGET_DEVICE = $diskMap[$n].Number
                        Write-Host "Selected: Disk $($Script:TARGET_DEVICE) - $($diskMap[$n].FriendlyName)"
                        break
                    }
                }
                Write-Host "Invalid selection." -ForegroundColor Red
            } while ($true)
        }
    }
}

Write-Host ""
Write-Host "=============================================="
Write-Host "  Done. $flashCount card(s) flashed."
Write-Host "=============================================="
Write-Host ""
