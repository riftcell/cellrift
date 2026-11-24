#!/usr/bin/env bash
# check-scenario-labels.sh — enforce four independent scenario-label guards.
#
# Guard A (check_enumerated) is the original guard: a fixed list of the
# Diameter-touching scenarios whose application-level exchange is not proven
# against a real peer (see each file's own header for its specific reason).
# An enumerated list is exactly expressive enough for a small, permanently
# fixed, historical set — this guard's concern is unrelated to Guard B and
# must not be folded into it.
#
# Guard B (check_malformed) closes a different requirement: any scenario
# file that declares a malform: block or an action: raw step is deliberately
# adversarial traffic — a corrupted AVP/Message-Length header or a raw
# cross-protocol byte injection is a different risk class than a timing
# delay or even a torn-down connection, and must never be aimed at a real,
# unconsented peer. That requirement has no opt-out and must hold for a file
# that does not exist yet, so it cannot be expressed as a list of today's
# filenames — the corpus's actual content has to be scanned every run. This
# is why the mechanism here is grep -rlE across scenarios/**/*.yaml rather
# than a second enumerated array.
#
# Guard C (check_proven) is Guard A's mirror: four Diameter scenarios a real
# peer answers on every push must NOT claim to be mock-only.
#
# Guard D (check_proven_sip) is the positive counterpart for SIP: the four
# scenarios a real Asterisk answers in CI must carry a PROVEN AGAINST header.
# Asterisk is a plain SIP server and not a P-CSCF, so Guard D adds a positive
# claim; it retires no MOCK-ONLY marker and says nothing about IMS semantics.
#
# Usage: ./scripts/check-scenario-labels.sh
# Exit 0 if all four guards pass, 1 if any finds a missing, extra or false
# marker.
set -euo pipefail

cd "$(dirname "$0")/.."

# MARKER is the canonical header a mock-only scenario carries. Both guards
# match this, not a bare "MOCK-ONLY", because prose mentions the phrase:
# register_loss_reorder.yaml explains why it is NOT one, and uar_uaa.yaml now
# says every OTHER Diameter scenario still is. Matching the bare string let a
# file satisfy these guards by merely discussing the marker — uar_uaa.yaml
# did exactly that the moment its own marker was removed, and Guard A
# reported "keeps MOCK-ONLY" about a file that no longer had it.
MARKER="MOCK-ONLY SCENARIO"

# PROVEN_SIP_MARKER is the POSITIVE counterpart: the header a scenario carries
# once a real third-party SIP stack answers it in CI on every push. Asterisk is
# a plain SIP server and not a P-CSCF, which is why this is an addition and not
# a reason to retire any MOCK-ONLY marker — the register_* scenarios still have
# no P-CSCF to answer their SIP halves, and that claim is unchanged by this one.
PROVEN_SIP_MARKER="PROVEN AGAINST: Asterisk (non-IMS SIP)"

# ADVERSARIAL_PATTERN is the content scan for deliberately hostile traffic: a
# malform: block or an action: raw step. Guard B requires such a file to carry
# the MOCK-ONLY marker; Guard D (below) rejects such a step appearing in a file
# a real container answers. Those are two halves of one requirement, so they
# read one pattern — two copies would drift, and the drift would be silent in
# exactly the direction that matters.
#
# Both halves are anchored to a line start (leading whitespace allowed) so a
# prose mention inside a # comment does not trip the guard. The action: raw
# half's trailing (#.*)? tolerates a same-line YAML comment (e.g.
# "action: raw  # cross-protocol inject") without weakening that anchor —
# without it, an author's own explanatory comment on that line would silently
# escape the guard. The ['\''"]? around raw tolerates either quote style YAML
# allows (action: raw, action: "raw", action: 'raw' are all equivalent).
#
# The optional (-[[:space:]]+)? is the fix for a real evasion. YAML lets a list
# item put any key first, so a step written
#
#   - action: raw
#     protocol: sip
#
# is semantically identical to one whose action: line stands alone, but the
# earlier pattern anchored action: directly after leading whitespace and so
# matched only the second form. Appending such a step to a scenario carrying a
# PROVEN AGAINST header passed every check — Guard B, Guard D and the harness's
# own gate_no_adversarial_scenario all read this one variable — which meant
# the adversarial no-opt-out rule turned on which key an author happened to write first.
# Verified by appending exactly that step to basic_call.yaml and watching the
# script exit 0.
ADVERSARIAL_PATTERN='^[[:space:]]*(-[[:space:]]+)?malform:|^[[:space:]]*(-[[:space:]]+)?action:[[:space:]]*['\''"]?raw['\''"]?[[:space:]]*(#.*)?$'

# check_enumerated is Guard A: the Diameter-touching scenarios whose
# application-level exchange is NOT proven against a real peer.
#
# Four files have left this list, each when a real peer started answering it
# on every push: uar_uaa.yaml, s6a_air_ulr.yaml and cx_mar_sar.yaml (Open5GS
# HSS, driven by run-scenario.sh) and credit_control.yaml (SigScale OCS,
# Ro/Gy CCR/CCA). Requiring any of them to claim it is mock-only would be
# requiring a false statement, which is why Guard C below asserts the
# opposite for them.
#
# What remains here keeps the marker for a reason of its own, named in each
# file's own header: the register_* scenarios have no real P-CSCF to answer
# their SIP halves.
check_enumerated() {
  local scenarios=(
    "scenarios/ims/register_basic.yaml"
    "scenarios/ims/register_deregister.yaml"
    "scenarios/ims/register_assert_liveness.yaml"
  )
  local rc=0
  for f in "${scenarios[@]}"; do
    if grep -q "$MARKER" "$f"; then
      echo "OK: $f keeps MOCK-ONLY"
    else
      echo "FAIL: $f is missing the $MARKER header — its application-level exchange is not proven against a real peer"
      rc=1
    fi
  done
  return "$rc"
}

# check_malformed is Guard B: a content scan for ADVERSARIAL_PATTERN across the
# whole scenario corpus, not a list of known offenders — the requirement must
# hold for a file that does not exist yet, so the corpus's actual content is
# scanned every run.
check_malformed() {
  local risky
  mapfile -t risky < <(grep -rlE "$ADVERSARIAL_PATTERN" scenarios --include='*.yaml' || true)
  local rc=0
  for f in "${risky[@]}"; do
    if grep -q "$MARKER" "$f"; then
      echo "OK: $f declares malform:/action: raw and keeps MOCK-ONLY"
    else
      echo "FAIL: $f declares malform: or action: raw but is missing the $MARKER header — malformed and cross-protocol traffic must never be pointed at a real peer"
      rc=1
    fi
  done
  return "$rc"
}

# check_proven is Guard C, the mirror of Guard A: a scenario proven against a
# real peer must NOT carry the marker. Without it, re-adding the header to
# uar_uaa.yaml would pass every check here while telling readers the opposite
# of what the interop job proves on every push — the same drift Guard A
# exists to stop, in the other direction.
check_proven() {
  local proven=(
    "scenarios/diameter/uar_uaa.yaml"
    "scenarios/diameter/s6a_air_ulr.yaml"
    "scenarios/diameter/cx_mar_sar.yaml"
    "scenarios/diameter/credit_control.yaml"
  )
  local rc=0
  for f in "${proven[@]}"; do
    if grep -q "^# *⚠️ *$MARKER" "$f"; then
      echo "FAIL: $f carries the $MARKER header, but the diameter-interop job proves it against a real peer — remove the marker or, if the proof no longer holds, move this file back to Guard A"
      rc=1
    else
      echo "OK: $f is proven against a real peer and carries no mock-only marker"
    fi
  done
  return "$rc"
}

# fail_check prints this script's single FAIL shape. Four conditions share it
# rather than each carrying its own copy of the echo.
fail_check() {
  echo "FAIL: $1"
}

# check_proven_sip is Guard D: the SIP scenarios a real Asterisk answers in CI
# carry a POSITIVE marker naming what answered them, and four things must hold
# for that marker to be true.
#
# Why a guard rather than nothing: an unenforced label drifts. mcc234 survived
# in nine scenario files for a long time while check-synthetic-data.sh
# reported OK on every run, because nothing checked the thing the label
# claimed.
#
# The list is exactly the four SIP-only scenarios the sip-interop job runs.
# register_basic.yaml is deliberately absent: it is a mixed scenario with a
# Diameter step and an hss_target, so it needs an HSS alongside Asterisk and
# is not proven by this job.
check_proven_sip() {
  local proven=(
    "scenarios/ims/register_impaired.yaml"
    "scenarios/ims/basic_call.yaml"
    "scenarios/ims/basic_call_tls.yaml"
    "scenarios/ims/register_digest_auth.yaml"
  )
  local rc=0 f
  for f in "${proven[@]}"; do
    proven_sip_file_ok "$f" || rc=1
  done
  return "$rc"
}

# proven_sip_file_ok checks one file. The header grep is ANCHORED to a comment
# line start for the same reason the MOCK-ONLY greps above are: a file that
# merely mentions the phrase in its description would otherwise satisfy the
# guard while claiming nothing.
#
# The drift check is anchored to the scenario-list assignments, not the file as
# a whole: a bare match would be satisfied by a comment, a refuse message or a
# commented-out line naming the file. Trailing comments are stripped from those
# assignment lines before matching, because a comment ON the assignment line was
# the remaining way to satisfy this check while the variable pointed somewhere
# else entirely. All three variables (UDP_SCENARIOS, TLS_SCENARIO,
# AUTH_SCENARIO) are single-line assignments today; if any ever becomes a
# multi-line construct, this check must be updated with it.
proven_sip_file_ok() {
  local f="$1" b harness="test/interop/run-sip-scenario.sh"
  b=$(basename "$f")
  if ! grep -q "^# *$PROVEN_SIP_MARKER" "$f"; then
    fail_check "$f is missing the '$PROVEN_SIP_MARKER' header — the sip-interop job runs it against a real Asterisk container on every push, and an unenforced label drifts"
    return 1
  fi
  if grep -q "^# *⚠️ *$MARKER" "$f"; then
    fail_check "$f carries both '$MARKER' and '$PROVEN_SIP_MARKER' — a file cannot be both, and the mock-only marker would satisfy Guard B while a real peer answers the file"
    return 1
  fi
  if grep -qE "$ADVERSARIAL_PATTERN" "$f"; then
    fail_check "$f declares malform: or action: raw AND is aimed at a real container by the sip-interop job — malformed and cross-protocol traffic must never be pointed at a real, unconsented peer, and that requirement has no opt-out"
    return 1
  fi
  if ! grep -E "^(UDP_SCENARIOS|TLS_SCENARIO|AUTH_SCENARIO)=" "$harness" |
    sed 's/[[:space:]]*#.*$//' | grep -qF "$b"; then
    fail_check "$f claims '$PROVEN_SIP_MARKER' but $b is not in UDP_SCENARIOS, TLS_SCENARIO or AUTH_SCENARIO in $harness — the marker and the harness's scenario list are two copies of one fact, and this is the check that catches them diverging"
    return 1
  fi
  echo "OK: $f carries $PROVEN_SIP_MARKER, carries no mock-only marker, declares no adversarial step, and is run by the sip-interop harness"
}

failed=0
check_enumerated || failed=1
check_malformed || failed=1
check_proven || failed=1
check_proven_sip || failed=1

exit "$failed"
