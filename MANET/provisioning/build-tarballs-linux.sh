#!/usr/bin/env bash
#
# build-tarballs-linux.sh — prepare a Debian/Ubuntu machine to build the
# MANET install/update tarballs locally, then build them.
#
# Installs anything missing (Go, OpenJDK 17, Android SDK cmdline-tools +
# platform + build-tools — only if the Android app is present) and then
# invokes the packaging/build-*.sh scripts, writing output to
# install_packages/.
#
# Usage:
#   ./build-tarballs-linux.sh [cm4|rpi5|r3a|all]
#   (default: all — builds cm4 and rpi5; r3a is skipped unless requested,
#   since it requires an external kernel/firmware overlay — see
#   packaging/README.md)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PACKAGING_DIR="$REPO_ROOT/packaging"
INSTALL_DIR="$REPO_ROOT/install_packages"
ANDROID_PROJECT="$REPO_ROOT/src/mesh-ctrl-android"

GO_INSTALL_DIR="/usr/local/go"
PROFILE_SNIPPET="/etc/profile.d/manet-build-tools.sh"

log()  { echo "==> $*"; }
warn() { echo "WARNING: $*" >&2; }
die()  { echo "ERROR: $*" >&2; exit 1; }

# ---------------------------------------------------------------------------
#  0. Platform check — Debian/Ubuntu only, for now.
# ---------------------------------------------------------------------------
[ -f /etc/os-release ] || die "Cannot detect OS (/etc/os-release missing)."
# shellcheck disable=SC1091
. /etc/os-release
case " ${ID:-} ${ID_LIKE:-} " in
    *debian*|*ubuntu*) : ;;
    *) die "This script only supports Debian/Ubuntu hosts for now (detected: ${PRETTY_NAME:-unknown})." ;;
esac

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    command -v sudo >/dev/null 2>&1 || die "Not running as root and 'sudo' not found."
    SUDO="sudo"
fi

# ---------------------------------------------------------------------------
#  1. Base apt packages
# ---------------------------------------------------------------------------
BASE_PKGS_MISSING=()
for pair in git:git tar:tar curl:curl unzip:unzip; do
    bin="${pair%%:*}"; pkg="${pair##*:}"
    command -v "$bin" >/dev/null 2>&1 || BASE_PKGS_MISSING+=("$pkg")
done
if [ "${#BASE_PKGS_MISSING[@]}" -gt 0 ]; then
    log "Installing missing packages: ${BASE_PKGS_MISSING[*]}"
    $SUDO apt-get update -qq
    $SUDO apt-get install -y "${BASE_PKGS_MISSING[@]}"
fi

# ---------------------------------------------------------------------------
#  2. Go — cross-compiles the mesh services for linux/arm64.
#     apt's Go is usually too old, so we install upstream's official
#     tarball straight from go.dev when the installed version is missing
#     or too low.
# ---------------------------------------------------------------------------
REQUIRED_GO_VERSION=$(grep -rhoP '^go \K[0-9]+\.[0-9]+(\.[0-9]+)?' "$REPO_ROOT"/src/*/go.mod 2>/dev/null | sort -V | tail -1)
REQUIRED_GO_VERSION="${REQUIRED_GO_VERSION:-1.21}"

# Put a prior install on PATH *before* checking — the invoking shell may not
# have sourced the profile snippet from a previous run of this script.
export PATH="$GO_INSTALL_DIR/bin:$PATH"

installed_go_version() {
    command -v go >/dev/null 2>&1 || return 1
    go version | grep -oP 'go\K[0-9]+\.[0-9]+(\.[0-9]+)?'
}

need_go_install=1
if gv=$(installed_go_version); then
    highest=$(printf '%s\n%s\n' "$gv" "$REQUIRED_GO_VERSION" | sort -V | tail -1)
    [ "$highest" = "$gv" ] && need_go_install=0
fi

if [ "$need_go_install" -eq 1 ]; then
    log "Installing Go (need >= $REQUIRED_GO_VERSION)..."
    case "$(dpkg --print-architecture)" in
        amd64) GOARCH=amd64 ;;
        arm64) GOARCH=arm64 ;;
        *) die "Unsupported host architecture for Go download: $(dpkg --print-architecture)" ;;
    esac
    GO_LATEST=$(curl -fsSL "https://go.dev/VERSION?m=text" | head -1)
    [ -n "$GO_LATEST" ] || die "Could not determine the latest Go release from go.dev."
    TMP_TGZ=$(mktemp --suffix=.tar.gz)
    curl -fsSL -o "$TMP_TGZ" "https://go.dev/dl/${GO_LATEST}.linux-${GOARCH}.tar.gz"
    $SUDO rm -rf "$GO_INSTALL_DIR"
    $SUDO tar -C "$(dirname "$GO_INSTALL_DIR")" -xzf "$TMP_TGZ"
    rm -f "$TMP_TGZ"
    log "Installed $("$GO_INSTALL_DIR/bin/go" version)"
else
    log "Go $gv already satisfies >= $REQUIRED_GO_VERSION, skipping."
fi

# ---------------------------------------------------------------------------
#  3. Java + Android SDK — only needed if the Android app is present
#     (build-*-tarball.sh builds the APK when src/mesh-ctrl-android/gradlew
#     exists). Gradle itself is not installed here: the project's Gradle
#     wrapper downloads the pinned Gradle version automatically.
# ---------------------------------------------------------------------------
if [ -f "$ANDROID_PROJECT/gradlew" ]; then
    if ! command -v javac >/dev/null 2>&1 || ! javac -version 2>&1 | grep -q '"\?17\.'; then
        log "Installing OpenJDK 17..."
        $SUDO apt-get update -qq
        $SUDO apt-get install -y openjdk-17-jdk-headless
    fi
    export JAVA_HOME="$(dirname "$(dirname "$(readlink -f "$(command -v javac)")")")"

    if [ -n "${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}" ]; then
        ANDROID_HOME="${ANDROID_SDK_ROOT:-$ANDROID_HOME}"
    elif [ -d /opt/android-sdk ]; then
        ANDROID_HOME="/opt/android-sdk"
    else
        ANDROID_HOME="$HOME/Android/Sdk"
    fi
    export ANDROID_HOME
    export ANDROID_SDK_ROOT="$ANDROID_HOME"
    export PATH="$PATH:$ANDROID_HOME/cmdline-tools/latest/bin:$ANDROID_HOME/platform-tools"

    if ! command -v sdkmanager >/dev/null 2>&1; then
        log "Installing Android SDK cmdline-tools into $ANDROID_HOME..."
        $SUDO mkdir -p "$ANDROID_HOME"
        $SUDO chown -R "$(id -u):$(id -g)" "$ANDROID_HOME"
        CMDLINE_TOOLS_URL="https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip"
        TMP_ZIP=$(mktemp --suffix=.zip)
        TMP_EXTRACT=$(mktemp -d)
        curl -fsSL -o "$TMP_ZIP" "$CMDLINE_TOOLS_URL"
        unzip -q "$TMP_ZIP" -d "$TMP_EXTRACT"
        mkdir -p "$ANDROID_HOME/cmdline-tools"
        rm -rf "$ANDROID_HOME/cmdline-tools/latest"
        mv "$TMP_EXTRACT/cmdline-tools" "$ANDROID_HOME/cmdline-tools/latest"
        rm -f "$TMP_ZIP"
        rmdir "$TMP_EXTRACT" 2>/dev/null || true
        export PATH="$PATH:$ANDROID_HOME/cmdline-tools/latest/bin"
    fi

    COMPILE_SDK=$(grep -oP 'compileSdk\s+\K[0-9]+' "$ANDROID_PROJECT/app/build.gradle" 2>/dev/null || echo "34")
    BUILD_TOOLS_VERSION="${COMPILE_SDK}.0.0"

    if [ ! -d "$ANDROID_HOME/platforms/android-${COMPILE_SDK}" ] || \
       [ ! -d "$ANDROID_HOME/build-tools/${BUILD_TOOLS_VERSION}" ] || \
       [ ! -x "$ANDROID_HOME/platform-tools/adb" ]; then
        log "Accepting Android SDK licenses..."
        yes | sdkmanager --licenses --sdk_root="$ANDROID_HOME" >/dev/null 2>&1 || true
        log "Installing Android SDK platform-tools, platforms;android-${COMPILE_SDK}, build-tools;${BUILD_TOOLS_VERSION}..."
        sdkmanager --sdk_root="$ANDROID_HOME" \
            "platform-tools" "platforms;android-${COMPILE_SDK}" "build-tools;${BUILD_TOOLS_VERSION}"
    else
        log "Android SDK platform-${COMPILE_SDK} / build-tools;${BUILD_TOOLS_VERSION} already installed, skipping."
    fi

    if [ ! -f "$PROFILE_SNIPPET" ]; then
        log "Persisting PATH/env additions to $PROFILE_SNIPPET"
        $SUDO tee "$PROFILE_SNIPPET" >/dev/null <<EOF
export PATH="\$PATH:$GO_INSTALL_DIR/bin"
export ANDROID_HOME="$ANDROID_HOME"
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export PATH="\$PATH:$ANDROID_HOME/cmdline-tools/latest/bin:$ANDROID_HOME/platform-tools"
EOF
        $SUDO chmod +x "$PROFILE_SNIPPET"
    fi
else
    log "No Android project at $ANDROID_PROJECT — skipping Java/Android SDK setup (APK build step will be skipped)."
    if [ ! -f "$PROFILE_SNIPPET" ]; then
        $SUDO tee "$PROFILE_SNIPPET" >/dev/null <<EOF
export PATH="\$PATH:$GO_INSTALL_DIR/bin"
EOF
        $SUDO chmod +x "$PROFILE_SNIPPET"
    fi
fi

# ---------------------------------------------------------------------------
#  4. Build the requested tarball(s)
# ---------------------------------------------------------------------------
mkdir -p "$INSTALL_DIR"

build_one() {
    local out_name="$1" build_script="$2"
    log "Building $out_name..."
    bash "$PACKAGING_DIR/$build_script" "$INSTALL_DIR/$out_name"
}

PLATFORM="${1:-all}"
case "$PLATFORM" in
    cm4)  build_one "cm4-tools.tar.gz"  "build-cm4-tarball.sh" ;;
    rpi5) build_one "rpi5-tools.tar.gz" "build-rpi5-tarball.sh" ;;
    r3a)
        if [ -z "${SBC_OVERLAY_DIR:-}" ] && [ ! -d "$REPO_ROOT/../kernel-work/packages/r3a-sbc-overlay" ]; then
            die "r3a build requires an SBC overlay. Set SBC_OVERLAY_DIR or see packaging/README.md."
        fi
        build_one "r3a-tools.tar.gz" "build-r3a-tarball.sh"
        ;;
    all)
        build_one "cm4-tools.tar.gz"  "build-cm4-tarball.sh"
        build_one "rpi5-tools.tar.gz" "build-rpi5-tarball.sh"
        ;;
    *) die "Unknown platform '$PLATFORM'. Usage: $0 [cm4|rpi5|r3a|all]" ;;
esac

log "Done. Output in $INSTALL_DIR/"
