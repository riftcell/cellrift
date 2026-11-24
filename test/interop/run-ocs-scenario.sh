#!/usr/bin/env bash
# run-ocs-scenario.sh — drive credit_control.yaml against the real SigScale
# OCS that up-ocs.sh boots, and grade the result honestly.
#
# Separate from run-scenario.sh because the peers are separate: that script
# grades a Cx/S6a HSS on 3868, this one a Credit-Control OCS on 3869. Folding
# them together would let one peer's absence mask the other's result.
#
# Usage: bash test/interop/run-ocs-scenario.sh
# Env:   REPORT — JSON report path (default /tmp/cellrift-ocs.json)
set -uo pipefail

cd "$(dirname "$0")/../.."

REPORT="${REPORT:-/tmp/cellrift-ocs.json}"
SCENARIO="scenarios/diameter/credit_control.yaml"
TARGET="127.0.0.1:3869"

# TCP only: this OCS is reached through a published container port, which
# does not carry SCTP. The SCTP path is proven against the HSS instead, so
# nothing is lost by pinning it here — and pinning it means the result never
# varies with the runner's kernel.
export CELLRIFT_TRANSPORT_FALLBACK=tcp

print_failure_diagnostics() {
  echo "--- report ($REPORT) ---" >&2
  cat "$REPORT" 2>/dev/null >&2
  echo "--- ocs logs (tail 60) ---" >&2
  docker logs ocs --tail 60 2>&1 >&2
}

rm -f "$REPORT"

go run ./cmd/cellrift run \
  --target-host "$TARGET" \
  --timeout 5000 \
  --format json \
  --output "$REPORT" \
  "$SCENARIO"
run_exit_code=$?

# Same stage-0 rule as run-scenario.sh: a run that produced no report says
# nothing about the peer, and must not be graded as though it did.
if [ ! -s "$REPORT" ]; then
  echo "INTEROP INCONCLUSIVE: cellrift wrote no report to $REPORT (exit $run_exit_code)." >&2
  echo "That is a build or startup failure, not an interop result." >&2
  print_failure_diagnostics
  exit 1
fi

if [ "$run_exit_code" -ne 0 ]; then
  echo "INTEROP FAILED: CCR/CCA was not answered 2001 by the real OCS" >&2
  print_failure_diagnostics
  exit 1
fi

echo "OK: CCR-I and CCR-U answered 2001 by a real SigScale OCS over TCP"
