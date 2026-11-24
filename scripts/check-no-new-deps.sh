#!/usr/bin/env bash
# check-no-new-deps.sh — fail if new direct (non-indirect) dependencies are added to go.mod.
#
# New runtime dependencies must be discussed before merging (see CONTRIBUTING.md).
# Indirect dependencies (transitive) and go.sum changes are not checked.
#
# Maintainers and owners may bypass this check by:
#   - Applying the 'allow-new-deps' label to the PR in GitHub (CI handles this automatically).
#   - Setting ALLOW_NEW_DEPS=1 when running locally.
#
# Usage:
#   ./scripts/check-no-new-deps.sh [base-ref]
#   ALLOW_NEW_DEPS=1 ./scripts/check-no-new-deps.sh [base-ref]
#
#   base-ref   Git ref to diff against. Defaults to origin/main.
#
# Exit 0 if no new direct deps or check is bypassed, 1 if new direct deps are detected.
set -euo pipefail

cd "$(dirname "$0")/.."

# Honour bypass flag — accept "1", "true", or "yes".
ALLOW="${ALLOW_NEW_DEPS:-0}"
if [ "$ALLOW" = "1" ] || [ "$ALLOW" = "true" ] || [ "$ALLOW" = "yes" ]; then
  echo "INFO: dependency check bypassed (ALLOW_NEW_DEPS=$ALLOW)"
  exit 0
fi

BASE="${1:-origin/main}"

if ! git rev-parse "$BASE" &>/dev/null; then
  echo "WARN: base ref '$BASE' not found — skipping dependency check"
  exit 0
fi

MERGE_BASE=$(git merge-base "$BASE" HEAD 2>/dev/null || git rev-parse "$BASE")

# Lines added to go.mod that look like a direct dependency entry:
#   +\tgithub.com/foo/bar v1.2.3
# Indirect deps carry a "// indirect" comment — exclude them.
NEW_DEPS=$(git diff "$MERGE_BASE" HEAD -- go.mod \
  | grep '^+' \
  | grep -v '^+++' \
  | grep -E '^\+\s+[a-zA-Z]' \
  | grep -v '// indirect' \
  || true)

if [ -n "$NEW_DEPS" ]; then
  echo "FAIL: new direct dependencies added to go.mod."
  echo "      Runtime dependencies must be discussed before merging (see CONTRIBUTING.md)."
  echo ""
  echo "$NEW_DEPS" | sed 's/^+/  +/'
  exit 1
fi

echo "OK: no new direct dependencies in go.mod"
