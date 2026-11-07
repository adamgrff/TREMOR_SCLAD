#!/usr/bin/env bash
# All subprocesses and exit codes are managed by the standard-library controller.
# No Docker operation occurs in self-test/check modes of verify.py.
set -euo pipefail
umask 077
case "${1:-}" in
  run|collect-cleanup) ;;
  *) printf '%s\n' 'Usage: ci.sh {run|collect-cleanup} SOURCE' >&2; exit 2 ;;
esac
test "$#" -eq 2
test "$(uname -s)" = Linux
test "$(uname -m)" = x86_64
test "${GITHUB_ACTIONS:-}" = true
test "${GITHUB_EVENT_NAME:-}" = push
test "${GITHUB_REPOSITORY:-}" = adamgrff/TREMOR_SCLAD
test "${GITHUB_REF:-}" = refs/heads/ci/e1a-fixed-21fa6cd
export EVIDENCE_DIR="$RUNNER_TEMP/e1a-evidence"
export PRIVATE_DIR="$RUNNER_TEMP/e1a-private"
script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
exec python3 -B "$script_dir/verify.py" "$1" "$2"
