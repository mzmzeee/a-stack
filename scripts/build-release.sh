#!/usr/bin/env bash
# Build the release archive for this machine's system and architecture.
# Usage: scripts/build-release.sh vX.Y.Z
# Writes dist/a-stack_OS_ARCH.tar.gz.
# On Linux it needs Docker; the build runs in a pinned Alpine Go image so the
# binary links musl statically and runs on any Linux.
# On macOS it needs Go 1.27.3 and the Xcode command line tools. The binary
# links only the system libraries and runs on macOS 12 or later.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE=golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c
# The C toolchain from Alpine 3.24, the image's release, pinned so that a
# build does not change silently. When Alpine replaces one of these versions,
# apk add fails; then pin the new version (apk policy gcc musl-dev binutils).
PACKAGES="gcc=15.2.0-r5 musl-dev=1.2.6-r3 binutils=2.45.1-r1"

version="${1:?usage: scripts/build-release.sh vX.Y.Z}"
if ! [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "version must look like v1.2.3 or v1.2.3-rc.1" >&2
  exit 2
fi
case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "unsupported system: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

out=".tmp/release-$os-$arch"
archive="dist/a-stack_${os}_$arch.tar.gz"
rm -rf "$out"
mkdir -p "$out" dist
if [ "$os" = darwin ]; then
  # Go 1.27 runs on macOS 12 or later; the C part of SQLite targets the same.
  GOTOOLCHAIN=local GOFLAGS=-buildvcs=false CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=12.0 \
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$out/a-stack" ./cmd/a-stack
  # Only system libraries and frameworks, which every Mac has.
  if otool -L "$out/a-stack" | tail -n +2 | grep -Ev '^[[:space:]]+(/usr/lib/|/System/Library/)'; then
    echo "the binary links a library outside the system" >&2
    exit 1
  fi
else
  docker run --rm -v "$PWD:/src:ro" -v "$PWD/$out:/out" -w /src \
    -e GOTOOLCHAIN=local -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=1 -e VERSION="$version" -e PACKAGES="$PACKAGES" \
    "$IMAGE" sh -euc '
      apk add --no-cache $PACKAGES >/dev/null
      go build -trimpath -ldflags "-s -w -X main.version=$VERSION -linkmode external -extldflags -static" \
        -o /out/a-stack ./cmd/a-stack
      chown '"$(id -u):$(id -g)"' /out/a-stack'
fi

if [ "$("$out/a-stack" version)" != "{\"data\":{\"version\":\"$version\"}}" ]; then
  echo "built binary reports the wrong version" >&2
  exit 1
fi
# COPYFILE_DISABLE keeps macOS tar from adding ._a-stack metadata files.
COPYFILE_DISABLE=1 tar -C "$out" -czf "$archive" a-stack
echo "$archive"
