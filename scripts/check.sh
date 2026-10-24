#!/usr/bin/env bash
# Run the a-stack quality checks. Stops at the first failure with a nonzero exit.
# Developer tools are pinned below and installed or cached outside source control
# (.tmp/bin, the npm cache and the uv cache); nothing is installed globally.
# Needs the Go 1.27.3 toolchain, a C compiler, Python 3, Node.js (npx), uv (uvx),
# findmnt, and network access for first installs and the Go vulnerability database.
set -euo pipefail
cd "$(dirname "$0")/.."

STATICCHECK=v0.8.2   # Staticcheck 2026.2.2
GOVULNCHECK=v1.9.0
OXLINT=1.89.0
RUFF=0.17.2
SHELLCHECK=0.11.0.1  # ShellCheck 0.11.0, from the shellcheck-py wheel
GITLEAKS=v8.30.1
ACTIONLINT=v1.7.12
ZIZMOR=1.31.1

export GOTOOLCHAIN=go1.27.3 CGO_ENABLED=1 GOBIN="$PWD/.tmp/bin"
export PYTHONPYCACHEPREFIX="$PWD/.tmp/pycache" npm_config_loglevel=error
mkdir -p .tmp

step() { printf '\n== %s\n' "$*"; }

step gofmt
unformatted="$(gofmt -l cmd internal)"
if [ -n "$unformatted" ]; then
  printf '%s\n' "$unformatted"
  exit 1
fi

step go vet
go vet ./...

step "staticcheck $STATICCHECK"
go install "honnef.co/go/tools/cmd/staticcheck@$STATICCHECK"
.tmp/bin/staticcheck ./...

step "govulncheck $GOVULNCHECK"
go install "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK"
.tmp/bin/govulncheck ./...

step "go test with coverage (.tmp/coverage.out)"
go test -count=1 -coverprofile=.tmp/coverage.out ./...
go tool cover -func=.tmp/coverage.out | tail -n 1

step go test -race
go test -race -count=1 ./...

step "go build (.tmp/a-stack)"
go build -o .tmp/a-stack ./cmd/a-stack

step "oxlint $OXLINT"
node --check internal/stack/browser/app.js
npx --yes "oxlint@$OXLINT" --deny-warnings internal/stack/browser/app.js

step "ruff $RUFF"
python3 -m py_compile scripts/validate-v1.py
uvx "ruff@$RUFF" check --no-cache scripts/validate-v1.py

step "shellcheck $SHELLCHECK"
uvx --from "shellcheck-py==$SHELLCHECK" shellcheck scripts/check.sh scripts/build-release.sh scripts/record-demo.sh install.sh

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
