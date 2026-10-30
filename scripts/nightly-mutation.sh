#!/usr/bin/env bash
# Targeted pilot, not a score gate. Keep lived/uncovered/timeout results for review.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=go1.27.3 CGO_ENABLED=1
mkdir -p .tmp/bin .tmp/nightly
export GOBIN="$PWD/.tmp/bin"
go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0

# A cached baseline can be under a second; allow time to compile each mutant.
# Gremlins mutates a private copy. Explicitly exclude every other source file:
# lifecycle/tasks cover authority and revisions; mail_reads covers scope/cursors.
excludes=()
while IFS= read -r file; do
  case "$file" in
    internal/stack/lifecycle.go | internal/stack/tasks.go | internal/stack/mail_reads.go) ;;
    *) excludes+=(--exclude-files "(^|/)$(basename "$file" .go)\\.go$") ;;
  esac
done < <(find internal/stack -maxdepth 1 -name '*.go' -type f | sort)
.tmp/bin/gremlins unleash ./internal/stack "${excludes[@]}" \
  --workers 2 --test-cpu 2 --timeout-coefficient 100 \
  --arithmetic-base=false --increment-decrement=false --invert-negatives=false \
  --output .tmp/nightly/mutations.json "$@" 2>&1 | tee .tmp/nightly/mutations.log
