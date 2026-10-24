#!/bin/sh
# Install the a-stack binary from GitHub Releases.
#
#   curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | sh
#
# Environment:
#   A_STACK_VERSION      release tag to install, such as v0.1.0 (default: latest)
#   A_STACK_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#   A_STACK_BASE_URL     release download URL, for mirrors and tests
#                        (default: https://github.com/mzmzeee/a-stack/releases)
set -eu

main() {
	repo_url="${A_STACK_BASE_URL:-https://github.com/mzmzeee/a-stack/releases}"
	version="${A_STACK_VERSION:-latest}"
	install_dir="${A_STACK_INSTALL_DIR:-$HOME/.local/bin}"

	[ "$(uname -s)" = Linux ] || fail "a-stack supports Linux only"
	case "$(uname -m)" in
		x86_64 | amd64) arch=amd64 ;;
		aarch64 | arm64) arch=arm64 ;;
		*) fail "unsupported architecture: $(uname -m)" ;;
	esac
	for tool in curl tar sha256sum awk; do
		command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
	done

	if [ "$version" = latest ]; then
		url="$repo_url/latest/download"
	else
		url="$repo_url/download/$version"
	fi
	archive="a-stack_linux_$arch.tar.gz"
	# Refuse plain HTTP, also after redirects, unless a mirror is set.
	proto='=https'
	[ -z "${A_STACK_BASE_URL:-}" ] || proto='=https,http,file'

	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' INT TERM
	echo "Downloading $archive ($version)"
	curl -fsSL --proto "$proto" --tlsv1.2 -o "$tmp/$archive" "$url/$archive" ||
		fail "download failed: $url/$archive"
	curl -fsSL --proto "$proto" --tlsv1.2 -o "$tmp/SHA256SUMS" "$url/SHA256SUMS" ||
		fail "download failed: $url/SHA256SUMS"
	expected="$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/SHA256SUMS")"
	actual="$(sha256sum "$tmp/$archive" | cut -d ' ' -f 1)"
	[ -n "$expected" ] && [ "$expected" = "$actual" ] || fail "checksum mismatch for $archive"

	tar -xzf "$tmp/$archive" -C "$tmp" a-stack
	mkdir -p "$install_dir"
	install -m 755 "$tmp/a-stack" "$install_dir/a-stack"
	installed="$("$install_dir/a-stack" version | sed 's/.*"version":"\([^"]*\)".*/\1/')"
	echo "Installed a-stack $installed to $install_dir/a-stack"

	case ":$PATH:" in
		*":$install_dir:"*) ;;
		*) echo "Add $install_dir to your PATH, for example: export PATH=\"$install_dir:\$PATH\"" ;;
	esac
}

fail() {
	echo "a-stack install: $*" >&2
	exit 1
}

# The whole script is parsed before main runs, so a cut-off download does nothing.
main "$@"
