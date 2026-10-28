#!/usr/bin/env bash
# Exercise the real format/check entry points in a disposable miniature repo.
set -euo pipefail
cd "$(dirname "$0")/.."
root="$PWD"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/a-stack-format.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture"/{scripts,cmd/example,internal/stack/browser,.tmp}
cp scripts/{check,fmt}.sh "$fixture/scripts/"
cp .golangci.yml .prettierrc.json .editorconfig ruff.toml "$fixture/"
ln -s "$root/.tmp/bin" "$fixture/.tmp/bin"
cd "$fixture"
git init -q
printf 'module example.com/format\n\ngo 1.27.3\n' >go.mod
printf 'package main\n\nfunc main() {}\n' >cmd/example/main.go
printf 'const x = 1;\nconsole.log(x);\n' >internal/stack/browser/app.js
printf 'body {\n  color: red;\n}\n' >internal/stack/browser/app.css
printf '<p>Hello</p>\n' >internal/stack/browser/index.html
printf 'console.log(1);\n' >scripts/example.js
printf 'x = 1\n' >scripts/example.py
printf '#!/bin/sh\necho hello\n' >install.sh
scripts/fmt.sh >format.log 2>&1
scripts/check.sh --format check >check.log 2>&1
for file in cmd/example/main.go internal/stack/browser/app.js internal/stack/browser/app.css internal/stack/browser/index.html scripts/example.py install.sh; do
  case "$file" in
    *.go) printf 'package main\n\nfunc main() {\n\nx := 1\nprintln(x)\nx = 2\nreturn\n}\n' >"$file" ;;
    *.js) printf 'console.log(  1 )\n' >"$file" ;;
    *.css) printf 'body{color:red}\n' >"$file" ;;
    *.html) printf '<p    class="hello">Hello</p>\n' >"$file" ;;
    *.py) printf 'x=  1\n' >"$file" ;;
    *.sh) printf '#!/bin/sh\nif true; then\necho hello\nfi\n' >"$file" ;;
  esac
  before="$(sha256sum "$file")"
  if scripts/check.sh --format check >check.log 2>&1; then
    echo "format gate accepted unformatted $file" >&2
    exit 1
  fi
  grep -q 'scripts/fmt.sh' check.log
  test "$before" = "$(sha256sum "$file")"
  scripts/fmt.sh >format.log 2>&1
  scripts/check.sh --format check >check.log 2>&1
  after="$(sha256sum "$file")"
  test "$before" != "$after"
  scripts/fmt.sh >format.log 2>&1
  test "$after" = "$(sha256sum "$file")"
done
printf 'Format gate rejects each language, never rewrites on check, and fixes idempotently.\n'
