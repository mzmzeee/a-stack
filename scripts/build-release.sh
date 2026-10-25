#!/usr/bin/env bash
# Build the release archive for this machine's system and architecture, or
# cross-build a Windows archive on Linux x86_64.
# Usage: scripts/build-release.sh vX.Y.Z [windows amd64|arm64]
# Writes dist/a-stack_OS_ARCH.tar.gz, or dist/a-stack_windows_ARCH.zip.
# On Linux it needs Docker; the build runs in a pinned Alpine Go image so the
# binary links musl statically and runs on any Linux.
# On macOS it needs Go 1.27.3 and the Xcode command line tools. The binary
# links only the system libraries and runs on macOS 13 or later.
# For Windows it needs Go 1.27.3, curl, xz and zip; it downloads the pinned
# llvm-mingw C toolchain to .tmp once. The binary links its C runtime
# statically and needs only the DLLs of Windows 10 and later. It cannot run
# here, so the release workflow runs it on Windows.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE=golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c
# The C toolchain from Alpine 3.24, the image's release, pinned so that a
# build does not change silently. When Alpine replaces one of these versions,
# apk add fails; then pin the new version (apk policy gcc musl-dev binutils).
PACKAGES="gcc=15.2.0-r5 musl-dev=1.2.6-r3 binutils=2.45.1-r1"
# llvm-mingw (clang for Windows, UCRT) by its release tag and the SHA-256 of
# its Linux x86_64 archive.
LLVM_MINGW=20261006
LLVM_MINGW_SHA256=5f9c6ed95b2d4bdb2869a488c5fd5857fbdabcf288a0aa3eb1da43f6a08d8ab4

usage="usage: scripts/build-release.sh vX.Y.Z [windows amd64|arm64]"
version="${1:?$usage}"
if ! [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "version must look like v1.2.3 or v1.2.3-rc.1" >&2
  exit 2
fi

if [ $# -gt 1 ]; then
  if [ "$2" != windows ] || [ $# -ne 3 ]; then
    echo "$usage" >&2
    exit 2
  fi
  case "$3" in
    amd64) triple=x86_64-w64-mingw32 ;;
    arm64) triple=aarch64-w64-mingw32 ;;
    *) echo "$usage" >&2; exit 2 ;;
  esac
  if [ "$(uname -s)-$(uname -m)" != Linux-x86_64 ]; then
    echo "the Windows build runs on Linux x86_64" >&2
    exit 1
  fi
  toolchain="$PWD/.tmp/llvm-mingw-$LLVM_MINGW"
  if [ ! -x "$toolchain/bin/$triple-clang" ]; then
    tarball=".tmp/llvm-mingw-$LLVM_MINGW.tar.xz"
    mkdir -p .tmp
    curl -fsSL -o "$tarball" "https://github.com/mstorsjo/llvm-mingw/releases/download/$LLVM_MINGW/llvm-mingw-$LLVM_MINGW-ucrt-ubuntu-22.04-x86_64.tar.xz"
    echo "$LLVM_MINGW_SHA256  $tarball" | sha256sum -c --quiet -
    rm -rf "$toolchain"
    mkdir -p "$toolchain"
    tar -xJf "$tarball" -C "$toolchain" --strip-components=1
    rm "$tarball"
  fi
  out=".tmp/release-windows-$3"
  archive="dist/a-stack_windows_$3.zip"
  rm -rf "$out" "$archive"
  mkdir -p "$out" dist
  GOTOOLCHAIN=local GOFLAGS=-buildvcs=false CGO_ENABLED=1 GOOS=windows GOARCH="$3" CC="$toolchain/bin/$triple-clang" \
    go build -trimpath -ldflags "-s -w -X main.version=$version -linkmode external -extldflags -static" \
    -o "$out/a-stack.exe" ./cmd/a-stack
  # Only DLLs of Windows itself: no libc++, libunwind or winpthread.
  if "$toolchain/bin/llvm-objdump" -p "$out/a-stack.exe" | grep 'DLL Name:' | grep -Eiv 'DLL Name: (kernel32|advapi32|shell32|ws2_32|winmm|ntdll|bcryptprimitives|userenv|api-ms-win-[a-z0-9-]+)\.dll$'; then
    echo "the binary needs a DLL that Windows does not have" >&2
    exit 1
  fi
  zip -q -X -j "$archive" "$out/a-stack.exe"
  echo "$archive"
  exit 0
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
  # The Go 1.27 linker marks its binaries for macOS 13 or later; the C part of
  # SQLite targets the same.
  GOTOOLCHAIN=local GOFLAGS=-buildvcs=false CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=13.0 \
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$out/a-stack" ./cmd/a-stack
  # Only system libraries and frameworks, which every Mac has. The output is
  # read first, so that a failing otool stops the build.
  libraries="$(otool -L "$out/a-stack")"
  if printf '%s\n' "$libraries" | tail -n +2 | grep -Ev '^[[:space:]]+(/usr/lib/|/System/Library/)'; then
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
# The archive names owner 0, the time of the commit and no extended
# attributes, and gzip stores no name or time, so the same binary gives the
# same archive. COPYFILE_DISABLE keeps macOS tar from adding ._a-stack files.
epoch="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}"
if [ "$os" = darwin ]; then
  TZ=UTC touch -t "$(TZ=UTC date -r "$epoch" +%Y%m%d%H%M.%S)" "$out/a-stack"
  COPYFILE_DISABLE=1 tar -C "$out" --uid 0 --gid 0 --uname root --gname root --no-xattrs --no-mac-metadata -cf - a-stack | gzip -n >"$archive"
else
  tar -C "$out" --owner=0 --group=0 --numeric-owner --no-xattrs --mtime="@$epoch" -cf - a-stack | gzip -n >"$archive"
fi
echo "$archive"
