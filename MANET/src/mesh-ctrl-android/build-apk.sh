#!/bin/bash
# Build the Mesh Ctrl APK
# Requires: Android SDK (ANDROID_HOME set), Java 11+
set -e

cd "$(dirname "$0")"

if [ -z "$ANDROID_HOME" ]; then
    # Try common locations
    for d in ~/Android/Sdk ~/Library/Android/sdk /opt/android-sdk; do
        [ -d "$d" ] && export ANDROID_HOME="$d" && break
    done
fi

if [ -z "$ANDROID_HOME" ]; then
    echo "ERROR: ANDROID_HOME not set and SDK not found"
    echo "Install Android SDK or set ANDROID_HOME"
    exit 1
fi

echo "Using ANDROID_HOME=$ANDROID_HOME"

# Use gradle wrapper if available, otherwise system gradle
if [ -f ./gradlew ]; then
    GRADLE=./gradlew
else
    GRADLE=gradle
fi

$GRADLE assembleDebug

APK=$(find app/build/outputs/apk/debug -name '*.apk' | head -1)
if [ -n "$APK" ]; then
    cp "$APK" mesh-ctrl.apk
    echo ""
    echo "Built: mesh-ctrl.apk ($(du -h mesh-ctrl.apk | cut -f1))"
    echo ""
    echo "To install on device:  adb install mesh-ctrl.apk"
    echo "To deploy to radio:   scp mesh-ctrl.apk radio@<IP>:/usr/local/share/manet/www/downloads/"
fi
