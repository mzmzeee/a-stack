#!/usr/bin/env bash
# Build the static release archive for this machine's architecture.
# Usage: scripts/build-release.sh vX.Y.Z
# Writes dist/a-stack_linux_ARCH.tar.gz. Needs Docker; the build runs in a
# pinned Alpine Go image so the binary links musl statically and runs on any Linux.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE=golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c

version="${1:?usage: scripts/build-release.sh vX.Y.Z}"
if ! [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "version must look like v1.2.3 or v1.2.3-rc.1" >&2
  exit 2
fi
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

out=".tmp/release-$arch"
rm -rf "$out"
mkdir -p "$out" dist
docker run --rm -v "$PWD:/src:ro" -v "$PWD/$out:/out" -w /src \
  -e GOTOOLCHAIN=local -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=1 -e VERSION="$version" \
  "$IMAGE" sh -euc '
    apk add --no-cache gcc musl-dev >/dev/null
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION -linkmode external -extldflags -static" \
      -o /out/a-stack ./cmd/a-stack
    chown '"$(id -u):$(id -g)"' /out/a-stack'

if [ "$("$out/a-stack" version)" != "{\"data\":{\"version\":\"$version\"}}" ]; then
  echo "built binary reports the wrong version" >&2
  exit 1
fi
tar -C "$out" -czf "dist/a-stack_linux_$arch.tar.gz" a-stack
echo "dist/a-stack_linux_$arch.tar.gz"
