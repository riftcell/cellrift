#!/usr/bin/env bash
# down.sh — tear down the Open5GS interop harness (Mongo + HSS containers).
#
# Safe to call unconditionally, including when nothing is running (e.g. as a
# CI `if: always()` cleanup step) — removal of an absent container is not
# treated as an error.
#
# Usage: bash test/interop/down.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

docker rm -f mongo hss >/dev/null 2>&1 || true
