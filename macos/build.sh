#!/bin/sh
# Builds "Hetzner Dashboard.app" into macos/build. Works with Xcode or only
# the Command Line Tools. Without Xcode, the newest SDK may lack SwiftUI's
# macro plugin; set SDKROOT to an older SDK in that case, for example:
#   SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX26.5.sdk ./build.sh
set -eu
cd "$(dirname "$0")"
swift build -c release --arch arm64 --arch x86_64 2>/dev/null || swift build -c release
BIN="$(swift build -c release --show-bin-path)/HetznerDashboard"
APP="build/Hetzner Dashboard.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN" "$APP/Contents/MacOS/HetznerDashboard"
cp Info.plist "$APP/Contents/Info.plist"
# Ad-hoc signature with the hardened runtime. Replace "-" with your
# Developer ID to distribute the app to other Macs.
codesign --force --options runtime --sign - "$APP"
echo "Built $APP"
