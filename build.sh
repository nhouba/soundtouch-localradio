#!/bin/sh
# Cross-builds the installer into dist/ (same targets and flags as the release workflow).
#   sh build.sh                 all targets
#   sh build.sh darwin/arm64    only these targets
# On macOS, building both darwin targets also makes the Mac downloads:
#   SoundTouch-Radio-Installer-macos.zip   app to double-click (Apple chip and Intel)
#   SoundTouch-Radio-Installer-macos       plain program for the Terminal one-liner
set -eu
cd "$(dirname "$0")"
GO=${GO:-go}
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
TARGETS=${*:-"darwin/arm64 darwin/amd64 windows/amd64 linux/amd64 linux/arm64"}
mkdir -p dist
for t in $TARGETS; do
  os=${t%/*}; arch=${t#*/}
  case $os in darwin) name=macos ;; *) name=$os ;; esac
  case $arch in amd64) a=x64 ;; *) a=$arch ;; esac
  ext=; [ "$os" = windows ] && ext=.exe
  out="dist/SoundTouch-Radio-Installer-$name-$a$ext"
  echo "building $out ($VERSION)"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch "$GO" build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$out" ./cmd/installer
done

mac=dist/SoundTouch-Radio-Installer-macos
if [ "$(uname)" = Darwin ] && [ -f "$mac-arm64" ] && [ -f "$mac-x64" ]; then
  echo "building $mac (universal) and $mac.zip"
  lipo -create -output "$mac" "$mac-arm64" "$mac-x64"
  rm "$mac-arm64" "$mac-x64"
  codesign --force -s - "$mac"
  app="dist/app/SoundTouch Radio Installer.app"
  rm -rf dist/app "$mac.zip"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  cp "$mac" "$app/Contents/MacOS/installer"
  cp packaging/macos/launcher "$app/Contents/MacOS/launcher"
  chmod 755 "$app/Contents/MacOS/launcher"
  sed "s/@VERSION@/${VERSION#v}/g" packaging/macos/Info.plist > "$app/Contents/Info.plist"
  codesign --force --deep -s - "$app"
  ditto -c -k --keepParent "$app" "$mac.zip"
  rm -rf dist/app
fi
