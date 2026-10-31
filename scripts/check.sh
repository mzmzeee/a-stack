#!/usr/bin/env bash
# Run the a-stack quality checks. Stops at the first failure with a nonzero exit.
# Developer tools are pinned below and installed or cached outside source control
# (.tmp/bin, the npm cache and the uv cache); nothing is installed globally.
# Needs the Go 1.27.3 toolchain, a C compiler, Python 3, Node.js (npx), uv (uvx),
# findmnt, and network access for first installs and the Go vulnerability database.
set -euo pipefail
cd "$(dirname "$0")/.."

format_mode=check
format_only=false
if [ "$#" -ne 0 ]; then
  if [ "$#" -ne 2 ] || [ "$1" != --format ] || [[ "$2" != check && "$2" != write ]]; then
    echo "usage: scripts/check.sh [--format check|write]" >&2
    exit 2
  fi
  format_mode="$2"
  format_only=true
fi

GOFUMPT=v0.12.0
GOLANGCI_LINT=v2.15.0
PRETTIER=3.9.10
SHFMT=v3.14.1
STATICCHECK=v0.8.2 # Staticcheck 2026.2.2
GOVULNCHECK=v1.9.0
OXLINT=1.89.0
RUFF=0.17.2
SHELLCHECK=0.11.0.1 # ShellCheck 0.11.0, from the shellcheck-py wheel
GITLEAKS=v8.30.1
ACTIONLINT=v1.7.12
ZIZMOR=1.31.1

export GOTOOLCHAIN=go1.27.3 CGO_ENABLED=1 GOBIN="$PWD/.tmp/bin"
export PYTHONPYCACHEPREFIX="$PWD/.tmp/pycache" npm_config_loglevel=error
mkdir -p .tmp

# Tests keep private state in TMPDIR, and a-stack rejects state below any .git.
# Use a fresh private TMPDIR so a stray .git above /tmp cannot break the suite.
TMPDIR="$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/a-stack-check.XXXXXX")"
export TMPDIR
trap 'chmod -R u+w "$TMPDIR" 2>/dev/null; rm -rf "$TMPDIR"' EXIT

step() { printf '\n== %s\n' "$*"; }

step "merge conflict markers"
if git grep -nE '^(<<<<<<<|>>>>>>>)( |$)|^=======$'; then
  echo "leftover merge conflict markers" >&2
  exit 1
fi

step "formatting ($format_mode): gofumpt, golangci-lint, Prettier, Ruff, shfmt"
# Both entry points use the same versions, file lists and rules.
trap 'echo "Formatting failed. Run scripts/fmt.sh, then scripts/check.sh." >&2' ERR
go install "mvdan.cc/gofumpt@$GOFUMPT"
go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$GOLANGCI_LINT"
go install "mvdan.cc/sh/v3/cmd/shfmt@$SHFMT"
web_files=(internal/stack/browser/*.{js,css,html} scripts/*.js)
python_files=(scripts/*.py)
shell_files=(install.sh scripts/*.sh)
if [ "$format_mode" = write ]; then
  .tmp/bin/gofumpt -w cmd internal
  .tmp/bin/golangci-lint fmt
  .tmp/bin/golangci-lint run --allow-serial-runners --build-tags bench --fix ./...
  # Added blank lines can push a short block over wsl's return threshold.
  .tmp/bin/golangci-lint run --allow-serial-runners --build-tags bench --fix ./...
  .tmp/bin/gofumpt -w cmd internal
  npx --yes "prettier@$PRETTIER" --write "${web_files[@]}"
  uvx "ruff@$RUFF" format "${python_files[@]}"
  .tmp/bin/shfmt -w -i 2 -ci "${shell_files[@]}"
else
  unformatted="$(.tmp/bin/gofumpt -l cmd internal)"
  if [ -n "$unformatted" ]; then
    printf '%s\nRun scripts/fmt.sh to format these files.\n' "$unformatted" >&2
    exit 1
  fi
  .tmp/bin/golangci-lint fmt --diff
  .tmp/bin/golangci-lint run --allow-serial-runners --build-tags bench ./...
  npx --yes "prettier@$PRETTIER" --check "${web_files[@]}"
  uvx "ruff@$RUFF" format --check "${python_files[@]}"
  .tmp/bin/shfmt -d -i 2 -ci "${shell_files[@]}"
fi
trap - ERR
if "$format_only"; then
  exit 0
fi

step "formatter regression test"
scripts/test-format.sh

step go vet
go vet ./...
go vet -tags bench ./internal/stack # the performance harness, see scripts/bench.py

step "staticcheck $STATICCHECK"
go install "honnef.co/go/tools/cmd/staticcheck@$STATICCHECK"
.tmp/bin/staticcheck ./...

step "govulncheck $GOVULNCHECK"
go install "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK"
.tmp/bin/govulncheck ./...

# Includes deterministic CLI scenarios, the short rapid model run and the
# production dependency guard. Long crash/mutation/fuzz runs stay in nightly.
step "go test with coverage (.tmp/coverage.out)"
go test -count=1 -coverprofile=.tmp/coverage.out ./...
go tool cover -func=.tmp/coverage.out | tail -n 1
CGO_ENABLED=0 go test -count=1 -run TestStartWithoutCgo ./internal/stack # sqlite_nocgo.go

step go test -race
go test -race -count=1 ./...

step "fuzz smoke (3 seconds per target)"
for target in FuzzSplitFlags FuzzExtractTarGz FuzzExtractZip; do
  go test ./cmd/a-stack -run '^$' -fuzz "^$target$" -fuzztime=3s -parallel=2
done
for target in FuzzMentionNames FuzzMailCursor FuzzMailCheckCursor; do
  go test ./internal/stack -run '^$' -fuzz "^$target$" -fuzztime=3s -parallel=2
done

step "go build (.tmp/a-stack)"
go build -o .tmp/a-stack ./cmd/a-stack

step "oxlint $OXLINT"
node --check internal/stack/browser/app.js
node --check scripts/demo-overlay.js
node --check contrib/opencode/a-stack.js
node --test contrib/opencode/a-stack.test.mjs
npx --yes "oxlint@$OXLINT" --deny-warnings internal/stack/browser/app.js scripts/demo-overlay.js contrib/opencode/a-stack.js contrib/opencode/a-stack.test.mjs

step "ruff $RUFF"
python3 -m py_compile "${python_files[@]}"
uvx "ruff@$RUFF" check --no-cache "${python_files[@]}"

step "validation runner regression tests"
python3 scripts/test-validate-v1.py

step "shellcheck $SHELLCHECK"
uvx --from "shellcheck-py==$SHELLCHECK" shellcheck "${shell_files[@]}"

step "actionlint $ACTIONLINT and zizmor $ZIZMOR"
go install "github.com/rhysd/actionlint/cmd/actionlint@$ACTIONLINT"
.tmp/bin/actionlint
uvx "zizmor@$ZIZMOR" --offline .github

step "gitleaks $GITLEAKS (Git history)"
go install "github.com/zricethezav/gitleaks/v8@$GITLEAKS"
.tmp/bin/gitleaks git --redact --no-banner .

step "validate-v1 --quick"
python3 scripts/validate-v1.py --quick --binary "$PWD/.tmp/a-stack" >.tmp/validate-quick.json
printf 'validate-v1 --quick passed; result in .tmp/validate-quick.json\n'

printf '\nAll checks passed.\n'
