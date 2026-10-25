#!/bin/sh
# Install the a-stack binary from GitHub Releases.
#
#   curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | sh
#
# It runs on Linux and macOS. It checks the release signature with OpenSSL 3,
# if installed, and the SHA-256 checksum before it installs anything. macOS
# ships LibreSSL instead, which the script does not use.
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

	case "$(uname -s)" in
		Linux) os=linux ;;
		Darwin) os=darwin ;;
		*) fail "a-stack supports Linux and macOS only" ;;
	esac
	case "$(uname -m)" in
		x86_64 | amd64) arch=amd64 ;;
		aarch64 | arm64) arch=arm64 ;;
		*) fail "unsupported architecture: $(uname -m)" ;;
	esac
	for tool in curl tar awk; do
		command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
	done
	# macOS has shasum instead of sha256sum.
	if command -v sha256sum >/dev/null 2>&1; then
		sha256="sha256sum"
	elif command -v shasum >/dev/null 2>&1; then
		sha256="shasum -a 256"
	else
		fail "sha256sum or shasum is required"
	fi

	if [ "$version" = latest ]; then
		url="$repo_url/latest/download"
	else
		url="$repo_url/download/$version"
	fi
	archive="a-stack_${os}_$arch.tar.gz"
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
	check_signature
	expected="$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/SHA256SUMS")"
	actual="$($sha256 "$tmp/$archive" | cut -d ' ' -f 1)"
	[ -n "$expected" ] && [ "$expected" = "$actual" ] || fail "checksum mismatch for $archive"

	tar -xzf "$tmp/$archive" -C "$tmp" a-stack
	# An existing binary is always replaced, also when it has the same version.
	previous=""
	[ ! -x "$install_dir/a-stack" ] || previous="$(binary_version "$install_dir/a-stack")"
	mkdir -p "$install_dir"
	install -m 755 "$tmp/a-stack" "$install_dir/a-stack"
	installed="$(binary_version "$install_dir/a-stack")"
	[ -n "$installed" ] || fail "$install_dir/a-stack does not run"
	if [ -z "$previous" ]; then
		echo "Installed a-stack $installed to $install_dir/a-stack"
	elif [ "$previous" = "$installed" ]; then
		echo "a-stack $installed is already installed in $install_dir/a-stack"
	else
		echo "Updated a-stack $previous -> $installed in $install_dir/a-stack"
		if [ "$os" = linux ] && command -v systemctl >/dev/null 2>&1 && systemctl --user is-active --quiet a-stack 2>/dev/null; then
			echo "Restart the service: systemctl --user restart a-stack"
		elif [ "$os" = darwin ] && launchctl print "gui/$(id -u)/dev.a-stack" 2>/dev/null | grep -q 'state = running'; then
			echo "Restart the service: launchctl kickstart -k gui/$(id -u)/dev.a-stack"
		else
			echo "If a-stack serve is running, stop it with Ctrl-C and start it again."
		fi
		echo "Then run a-stack to open the view."
	fi

	case ":$PATH:" in
		*":$install_dir:"*) ;;
		*) echo "Add $install_dir to your PATH, for example: export PATH=\"$install_dir:\$PATH\"" ;;
	esac
	echo "To teach your coding agents a-stack, run: a-stack skill install"
	echo "To update a-stack next time, run: a-stack update"
}

# The release workflow signs SHA256SUMS with this Ed25519 key. SHA256SUMS.sig
# is the raw 64-byte signature. a-stack update checks it with the same key.
release_key() {
	cat <<'EOF'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEANkJiNRRt3iLovFX+EVrAxA1Z7a3diAw6F3PhyC9+D08=
-----END PUBLIC KEY-----
EOF
}

# Check SHA256SUMS.sig with OpenSSL 3 or later. Releases before v0.1.5 have
# no signature; every later release must have a valid one.
check_signature() {
	case "$(openssl version 2>/dev/null)" in
		"OpenSSL "[3-9].* | "OpenSSL "[1-9][0-9].*) ;;
		*)
			echo "OpenSSL 3 was not found, so the release signature was not checked, only the checksum."
			echo "To check it by hand, see https://github.com/mzmzeee/a-stack/blob/main/docs/install.md#how-releases-are-verified"
			return
			;;
	esac
	if curl -fsSL --proto "$proto" --tlsv1.2 -o "$tmp/SHA256SUMS.sig" "$url/SHA256SUMS.sig" 2>/dev/null; then
		release_key >"$tmp/release-key.pem"
		openssl pkeyutl -verify -pubin -inkey "$tmp/release-key.pem" -rawin \
			-in "$tmp/SHA256SUMS" -sigfile "$tmp/SHA256SUMS.sig" >/dev/null 2>&1 ||
			fail "the signature of SHA256SUMS is not valid; nothing was installed"
	elif [ "$version" != latest ] && unsigned_release "$version"; then
		echo "a-stack $version has no release signature, as every release before v0.1.5; only the checksum was checked."
	else
		fail "download failed: $url/SHA256SUMS.sig; every release since v0.1.5 is signed"
	fi
}

# Succeed for a version before v0.1.5, such as v0.1.4.
unsigned_release() {
	echo "$1" | awk -F '[.-]' '{ sub(/^v/, "", $1) } !($1 == 0 && ($2 < 1 || $2 == 1 && $3 < 5)) { exit 1 }'
}

# Print the version of an a-stack binary, or nothing if it fails or hangs.
binary_version() {
	if command -v timeout >/dev/null 2>&1; then
		timeout 5 "$1" version 2>/dev/null </dev/null
	else
		"$1" version 2>/dev/null </dev/null
	fi | sed -n 's/.*"version":"\([^"]*\)".*/\1/p' || true
}

fail() {
	echo "a-stack install: $*" >&2
	exit 1
}

# The whole script is parsed before main runs, so a cut-off download does nothing.
main "$@"
