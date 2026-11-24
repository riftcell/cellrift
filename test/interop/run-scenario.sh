#!/usr/bin/env bash
# run-scenario.sh — run uar_uaa.yaml against the booted Open5GS HSS and make
# two SEPARATE, distinctly-named assertions: the CER/CEA handshake, and the
# UAR/UAA application exchange. Separating them is deliberate: a handshake
# failure is never reported as an application failure, or the reverse. Before
# 3GPP AVPs were vendor-scoped, stage 1 passed and stage 2 failed, which is
# how the two were shown to be independent. See test/interop/README.md.
#
# Usage: bash test/interop/run-scenario.sh
# Env:   REPORT — path to write the JSON report to (default /tmp/cellrift-interop.json)
set -uo pipefail

cd "$(dirname "$0")/../.."

REPORT="${REPORT:-/tmp/cellrift-interop.json}"
SCENARIO="scenarios/diameter/uar_uaa.yaml"

# Scenarios run after the staged UAR proof, each required to exit 0. ORDER IS
# LOAD-BEARING and not alphabetical:
#
#   - uar_uaa runs first, above, because its UAA asserts
#     DIAMETER_FIRST_REGISTRATION (2001). Once cx_mar_sar's SAR assigns an
#     S-CSCF the same UAR legitimately answers SUBSEQUENT_REGISTRATION
#     (2002), so running Cx first would fail the UAR for a correct reason.
#   - cx_mar_sar runs after uar_uaa for the opposite dependency: a UAR
#     registers the IMPI/IMPU pair in the HSS's context, and a MAR naming a
#     pair no UAR has registered is answered 5002 IDENTITIES_DONT_MATCH.
#     That is also why every Cx scenario must use one domain, derived from
#     the provisioned IMSI's PLMN.
EXTRA_SCENARIOS="scenarios/diameter/s6a_air_ulr.yaml scenarios/diameter/cx_mar_sar.yaml"

# Transport is chosen by the caller, not hardcoded here. The CI job runs this
# script twice: once with CELLRIFT_TRANSPORT_FALLBACK=tcp, which forces TCP
# explicitly rather than by accident of an erroring SCTP stub, and once with
# it unset, which dials a real RFC 4960 SCTP association. Nothing is exported
# here, so an unset variable means SCTP.
EXPECT_SCTP=0
if [ "${CELLRIFT_TRANSPORT_FALLBACK:-}" != "tcp" ]; then
  EXPECT_SCTP=1
fi

print_failure_diagnostics() {
  echo "--- report ($REPORT) ---" >&2
  cat "$REPORT" 2>/dev/null >&2
  echo "--- hss logs (tail 100) ---" >&2
  docker logs hss --tail 100 2>&1 >&2
}

# Stage 0 — did cellrift run at all? Without this, a build failure is graded
# as an interop result: `go run` exits non-zero having written no report,
# stage 1's grep finds no "diameter handshake:" in a file that does not
# exist and therefore PASSES, and stage 2 then announces "handshake reached
# I-Open, but the UAR did not return 2001" — a claim about a real peer that
# was never contacted. That is exactly the kind of false evidence this
# repository refuses to produce elsewhere, and it misled a CI run. A report
# that exists and is non-empty is the proof that the binary built, started
# and got far enough to write its own result; only then does grading it mean
# anything.
assert_cellrift_ran() {
  local run_exit_code="$1"
  if [ -s "$REPORT" ]; then
    return 0
  fi
  echo "INTEROP INCONCLUSIVE: cellrift wrote no report to $REPORT (exit $run_exit_code)." >&2
  echo "Nothing was proven about the peer — this is a build or startup failure," >&2
  echo "not an interop result. See the step log above for the compiler or runtime error." >&2
  print_failure_diagnostics
  return 1
}

# Stage 0.5 — was the transport the one we asked for? Cellrift substitutes
# TCP with a recorded warning when the kernel has no usable SCTP, which is
# the right runtime behaviour and the wrong thing to grade silently: an SCTP
# run that quietly became a TCP run would report "SCTP interop proven" while
# proving nothing of the sort. The substitution is visible in the report's
# own warnings, so the assertion reads Cellrift's output rather than probing
# the kernel separately.
assert_transport_not_substituted() {
  if [ "$EXPECT_SCTP" -ne 1 ]; then
    return 0
  fi
  if grep -q "used tcp" "$REPORT"; then
    echo "INTEROP FAILED: this run asked for SCTP and Cellrift substituted TCP." >&2
    echo "The kernel has no usable SCTP, so nothing here says anything about SCTP interop." >&2
    print_failure_diagnostics
    return 1
  fi
  return 0
}

# Stage 1 — CER/CEA. Every handshake failure (timeout, non-CEA, rejected CER)
# carries the literal prefix "diameter handshake:" in StepResult.Error, which
# the JSON reporter emits as each step's "error" field. A UAR-level failure
# never carries that prefix, so its absence proves the handshake reached
# I-Open regardless of what happens next.
assert_handshake_reached() {
  if grep -q "diameter handshake:" "$REPORT" 2>/dev/null; then
    echo "HANDSHAKE FAILED: CER/CEA against real Open5GS did not reach I-Open" >&2
    print_failure_diagnostics
    return 1
  fi
  return 0
}

# Stage 2 — UAR/UAA. Only meaningful once stage 1 has already passed.
assert_uar_answered() {
  local run_exit_code="$1"
  if [ "$run_exit_code" -ne 0 ]; then
    echo "UAR FAILED: handshake reached I-Open, but the UAR did not return 2001" >&2
    print_failure_diagnostics
    return 1
  fi
  return 0
}

# A stale report from an earlier run would be graded as if it were this
# run's; remove it so stage 0's "does it exist" test means what it says.
rm -f "$REPORT"

go run ./cmd/cellrift run \
  --target-host 127.0.0.1:3868 \
  --timeout 5000 \
  --format json \
  --output "$REPORT" \
  "$SCENARIO"
run_exit_code=$?

assert_cellrift_ran "$run_exit_code" || exit 1
assert_transport_not_substituted || exit 1
assert_handshake_reached || exit 1
assert_uar_answered "$run_exit_code" || exit 1

transport="TCP"
[ "$EXPECT_SCTP" -eq 1 ] && transport="SCTP"
echo "OK over $transport: stage 1 (CER/CEA reached I-Open) and stage 2 (UAR answered 2001) both passed"

# Stage 3 runs the other four commands. There is deliberately no settle and
# no retry here any more.
#
# Both were added when CI's SCTP leg failed on answers the HSS had already
# built correctly, on the theory that a brief peer-reaping window was
# swallowing them. The retry disproved that theory the next run by failing
# BOTH attempts. The real cause was that every scenario connected under the
# same Origin-Host, and freeDiameter keeps one peer object per name: three
# sequential processes fought over one object, and it routed answers to the
# entry it was reaping. Each scenario now uses its own identity (see the
# ConnectPeer block in hss-freeDiameter.conf), which removes the collision
# rather than waiting it out — both legs pass with zero settle and zero
# retries.
#
# A retry is not kept as insurance. It never once turned a red run green,
# and a bounded retry over a peer bug is exactly the kind of thing that
# converts a real failure into a passing build.
# Each scenario carries its own assertions, so a non-zero exit is a real-peer
# failure and needs no extra grading here.
for extra in $EXTRA_SCENARIOS; do
  echo "--- $extra over $transport"
  if ! go run ./cmd/cellrift run \
      --target-host 127.0.0.1:3868 \
      --timeout 5000 \
      --format json \
      --output "${REPORT%.json}-$(basename "$extra" .yaml).json" \
      "$extra"; then
    echo "INTEROP FAILED: $extra did not pass against the real peer" >&2
    print_failure_diagnostics
    exit 1
  fi
done

echo "OK over $transport: AIR/ULR (S6a) and MAR/SAR (Cx) also answered by the real peer"
