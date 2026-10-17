#!/bin/sh
# Cross-builds the installer into dist/ (same targets and flags as the release workflow).
#   sh build.sh                 all targets
#   sh build.sh darwin/arm64    only these targets
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
