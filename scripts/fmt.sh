#!/usr/bin/env bash
# Keep versions and the check/fix file lists together in check.sh.
set -euo pipefail
exec "$(dirname "$0")/check.sh" --format write
