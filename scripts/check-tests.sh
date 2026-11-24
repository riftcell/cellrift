#!/usr/bin/env bash
# check-tests.sh — verify every internal package with Go source files has test files.
#
# Usage: ./scripts/check-tests.sh
# Exit 0 on success, 1 if any package is missing tests.
set -euo pipefail

cd "$(dirname "$0")/.."

failed=0

while IFS= read -r dir; do
  src=$(find "$dir" -maxdepth 1 -name "*.go" ! -name "*_test.go" 2>/dev/null | wc -l)
  tests=$(find "$dir" -maxdepth 1 -name "*_test.go" 2>/dev/null | wc -l)
  if [ "$src" -gt 0 ] && [ "$tests" -eq 0 ]; then
    echo "FAIL: $dir has $src source file(s) but no test files"
    failed=1
  fi
done < <(find internal -mindepth 1 -type d | sort)

if [ "$failed" -eq 0 ]; then
  echo "OK: all internal packages have test files"
fi

exit "$failed"
