#!/usr/bin/env bash
# check-changelog.sh — verify CHANGELOG.md was updated when Go source files changed.
#
# Usage:
#   ./scripts/check-changelog.sh [base-ref]
#
#   base-ref   Git ref to diff against (branch, tag, or SHA).
#              Defaults to origin/main for local use.
#              In CI pass the PR base SHA: ./scripts/check-changelog.sh "$BASE_SHA"
#
# Skip the check for internal-only changes (refactors, test-only edits):
#   SKIP_CHANGELOG=1 ./scripts/check-changelog.sh
#   Or set the 'skip-changelog' PR label in GitHub (CI handles that automatically).
#
# Exit 0 on success or skip, 1 if changelog is missing.
set -euo pipefail

cd "$(dirname "$0")/.."

# Honour skip flags — accept "1", "true", or "yes".
SKIP="${SKIP_CHANGELOG:-0}"
if [ "$SKIP" = "1" ] || [ "$SKIP" = "true" ] || [ "$SKIP" = "yes" ]; then
  echo "INFO: changelog check skipped (SKIP_CHANGELOG=$SKIP)"
  exit 0
fi

BASE="${1:-origin/main}"

if ! git rev-parse "$BASE" &>/dev/null; then
  echo "WARN: base ref '$BASE' not found — skipping changelog check"
  exit 0
fi

# Find the common ancestor so the diff covers only the PR/branch changes.
MERGE_BASE=$(git merge-base "$BASE" HEAD 2>/dev/null || git rev-parse "$BASE")

CHANGED=$(git diff --name-only "$MERGE_BASE" HEAD)

GO_CHANGED=$(echo "$CHANGED" | grep '\.go$' | grep -v '_test\.go' || true)
CHANGELOG_CHANGED=$(echo "$CHANGED" | grep '^CHANGELOG\.md$' || true)

if [ -n "$GO_CHANGED" ] && [ -z "$CHANGELOG_CHANGED" ]; then
  echo "FAIL: Go source files changed but CHANGELOG.md was not updated."
  echo ""
  echo "Changed source files:"
  echo "$GO_CHANGED" | sed 's/^/  /'
  echo ""
  echo "Add a CHANGELOG.md entry under 'Unreleased', or set SKIP_CHANGELOG=1"
  echo "for internal-only changes (refactors, tooling, test-only edits)."
  exit 1
fi

echo "OK: CHANGELOG.md is up to date"
