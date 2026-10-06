#!/bin/sh
# Builds build/Rex.app: the SwiftUI app with rexd bundled in Contents/MacOS
# and the rex CLI in Contents/Helpers (not next to "Rex": the file system is
# case-insensitive). Ad-hoc signed so it runs on this machine.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=${OUT:-"$ROOT/build"}
APP="$OUT/Rex.app"

mkdir -p "$OUT"
echo "==> go build rex, rexd"
(cd "$ROOT" && go build -o "$OUT/rex" ./cmd/rex && go build -o "$OUT/rexd" ./cmd/rexd)

echo "==> swift build Rex"
swift build -c release --package-path "$ROOT/app"
BIN=$(swift build -c release --package-path "$ROOT/app" --show-bin-path)

echo "==> bundle $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Helpers" "$APP/Contents/Resources"
cp "$BIN/Rex" "$APP/Contents/MacOS/Rex"
cp "$OUT/rexd" "$APP/Contents/MacOS/rexd"
cp "$OUT/rex" "$APP/Contents/Helpers/rex"
cp "$ROOT/app/Resources/Info.plist" "$APP/Contents/Info.plist"

codesign --force --sign - "$APP/Contents/MacOS/rexd" "$APP/Contents/Helpers/rex"
codesign --force --sign - "$APP"
echo "==> done: $APP"
