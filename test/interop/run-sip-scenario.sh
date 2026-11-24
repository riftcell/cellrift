#!/usr/bin/env bash
# run-sip-scenario.sh — run the four proven SIP scenarios against the real
# Asterisk that up-sip.sh boots, and grade them honestly.
#
# What it runs: register_impaired and basic_call over UDP (15060), and a
# DERIVED copy of basic_call_tls over TLS (15061) with certificate
# verification ON against the CA up-sip.sh generated.
#
# Each scenario runs TWICE (two passes). `--output` writes a report to a file
# instead of stdout and one reporter is selected per run, so one invocation
# cannot yield both a terminal rendering and a JSON report. Pass 1 is the
# terminal capture (graded; the TLS refusal gate reads it, and it does not
# depend on the JSON reporter's error precedence). Pass 2 is the JSON report
# (structured artefact; carries the verification warning, if any). The peer
# therefore receives each scenario's requests twice. The per-pass counts are
# recorded as deltas in $OUT/<name>.receipts (pass 1) and
# $OUT/<name>.receipts.pass2, so a per-scenario claim quotes an observed
# number rather than a cumulative total divided by two.
#
# Exit:  0  every scenario passed against the real peer AND every
#           rig-integrity gate held
#        1  either a genuine peer failure (the scenario did not pass) or a
#           refusal ("SIP RIG REFUSED:" line): the run would have measured
#           something other than what it claims. Read the message.
#
# Env (every one overridable, deliberately: a gate that cannot be fired
# without editing the script is a gate nobody re-fires, so these exist so
# each gate can be proven):
#   OUT             run artefact directory (default /tmp/cellrift-sip)
#   CERTS_DIR       where up-sip.sh wrote its CA (default /tmp/cellrift-sip-certs)
#   CA_CRT          CA the TLS leg trusts (default $CERTS_DIR/ca.crt)
#   SIP_UDP_TARGET  UDP target (default 127.0.0.1:15060)
#   SIP_TLS_TARGET  TLS target (default 127.0.0.1:15061)
#   STEP_TIMEOUT    per-step timeout in ms (default 5000)
#   UDP_SCENARIOS   space-separated UDP scenario files
#   TLS_SCENARIO    the TLS scenario file the verifying copy is derived from
#   AUTH_SCENARIO   the digest scenario (UDP); a pass proves the digest ARITHMETIC only
#   CHALLENGE_WAIT_MS  upper bound on how long to wait for the peer's log to
#                   show its 401s after the scenario returns (default 5000)
#
# `set -e` is deliberately absent: a failing scenario must still be captured
# and graded rather than killing the script before the evidence is written.
set -uo pipefail

cd "$(dirname "$0")/../.."

OUT="${OUT:-/tmp/cellrift-sip}"
CERTS_DIR="${CERTS_DIR:-/tmp/cellrift-sip-certs}"
CA_CRT="${CA_CRT:-$CERTS_DIR/ca.crt}"
# Duplicated from up-sip.sh on purpose: gate_peer_is_ours asks whether the
# container matches what this harness intends. Reading the answer from the
# variable the boot used would make the check circular.
ASTERISK_IMAGE="andrius/asterisk:22.10.1_debian-trixie@sha256:212326bd3f973a597be9e4747be5d6653802e4950159e95aa7d728e394de2012"
SIP_UDP_TARGET="${SIP_UDP_TARGET:-127.0.0.1:15060}"
SIP_TLS_TARGET="${SIP_TLS_TARGET:-127.0.0.1:15061}"
STEP_TIMEOUT="${STEP_TIMEOUT:-5000}"
UDP_SCENARIOS="${UDP_SCENARIOS:-scenarios/ims/register_impaired.yaml scenarios/ims/basic_call.yaml}"
TLS_SCENARIO="${TLS_SCENARIO:-scenarios/ims/basic_call_tls.yaml}"
# AUTH_SCENARIO is run separately from UDP_SCENARIOS because it needs one gate
# the others must NOT have: gate_peer_challenged. The other three scenarios
# register against endpoints that require no authentication and are never
# challenged, so a challenge gate applied to them would refuse every correct
# run. Asterisk validates only the digest ARITHMETIC, so a pass of the digest
# scenario says nothing about the Authorization header's grammar.
AUTH_SCENARIO="${AUTH_SCENARIO:-scenarios/ims/register_digest_auth.yaml}"
# The peer writes its log asynchronously, so a 401 it has sent can be missing
# from `docker logs` at the instant Cellrift's step returns. The challenge
# count is therefore POLLED up to this bound rather than read once or after a
# fixed sleep: it returns the moment the log shows a challenge, and only a rig
# that never challenges pays the full wait.
CHALLENGE_WAIT_MS="${CHALLENGE_WAIT_MS:-5000}"
# GATE_TLS_WARNING_SOURCE - TEST SEAM ONLY. Extra file gate_tls_verified scans
# for the insecure_skip_verify warning, alongside the run's own outputs. It
# exists because the warning can only arise from a run that skipped
# verification, and derive_verified_tls_scenario refuses before such a run can
# start - so without a seam this branch could never be shown to fire, and a
# gate nobody has seen fire is untested. Setting it in a real run defeats the
# check it exists to make.
GATE_TLS_WARNING_SOURCE="${GATE_TLS_WARNING_SOURCE:-}"

print_failure_diagnostics() {
  local f
  for f in "$OUT"/*.stdout "$OUT"/*.json; do
    [ -e "$f" ] || continue
    echo "--- $f ---" >&2
    cat "$f" >&2
  done
  echo "--- asterisk logs (tail 120) ---" >&2
  docker logs asterisk --tail 120 >&2 2>&1
}

# refuse is the single exit path for every gate: one line on stderr naming
# what would otherwise have been measured wrongly, diagnostics, exit 1.
refuse() {
  echo "SIP RIG REFUSED: $1" >&2
  print_failure_diagnostics
  exit 1
}

# Checks the reference the container was STARTED with, not the registry
# content. It proves the running container is the one this harness booted
# rather than something else that happens to be named asterisk.
gate_peer_is_ours() {
  local img
  img=$(docker inspect asterisk --format '{{.Config.Image}}' 2>&1)
  [ "$img" = "$ASTERISK_IMAGE" ] && return 0
  refuse "the running 'asterisk' container is not the pinned image this harness booted (got: $img), so nothing graded here would be about the intended peer"
}

# up-sip.sh already gates on this at boot. It is repeated immediately before
# grading because a transport can be lost between boot and run, and because a
# run graded against an unbound transport is the failure this rig exists to
# prevent.
gate_transports_bound() {
  local out
  out=$(docker exec asterisk asterisk -rx "pjsip show transports" 2>&1)
  grep -q '0.0.0.0:15060' <<<"$out" && grep -q '0.0.0.0:15061' <<<"$out" && return 0
  refuse "'pjsip show transports' does not list both 0.0.0.0:15060 and 0.0.0.0:15061; a run now would be graded against an unbound transport"
}

# True when the file carries a malform: block or an action: raw step.
#
# The (-[[:space:]]+)? is load-bearing, not defensive. YAML lets a list item
# put any key first, so a step written "  - action: raw" is identical in
# meaning to one whose action: line stands alone, but an anchor that expects
# action: directly after leading whitespace matches only the latter. Appending
# the list-item form to a scenario carrying a PROVEN AGAINST header once passed
# every check here and in the static guard, which made the adversarial no-opt-out rule
# depend on which key an author wrote first.
#
# This regex is a hand copy of ADVERSARIAL_PATTERN in
# scripts/check-scenario-labels.sh. They are two copies of one rule and nothing
# mechanically keeps them equal -- if you change one, change the other, and
# check both forms still fail.
is_adversarial() {
  grep -qE "^[[:space:]]*(-[[:space:]]+)?malform:|^[[:space:]]*(-[[:space:]]+)?action:[[:space:]]*['\"]?raw['\"]?[[:space:]]*(#.*)?\$" "$1"
}

has_proven_marker() {
  grep -qE '^# *PROVEN AGAINST: Asterisk \(non-IMS SIP\)' "$1"
}

# Malformed and raw cross-protocol traffic must never be aimed at a real
# peer, and that requirement has no opt-out. scripts/check-scenario-labels.sh
# enforces the same rule statically; this copy exists so the harness does not
# depend on CI running those checks first or in any particular order.
#
# ORDER IS LOAD-BEARING: the adversarial condition is checked FIRST. Most
# files with a malform: block also lack the PROVEN AGAINST marker, so both
# conditions are true at once and the first check decides what the operator is
# told. "Must never be aimed at a real peer" is a materially more serious
# statement than "not one of the proven three", and an operator who reads the
# milder one may simply add a marker. Pinning the order also makes the two
# refusals separately provable.
gate_no_adversarial_scenario() {
  local s
  for s in $UDP_SCENARIOS $TLS_SCENARIO $AUTH_SCENARIO; do
    [ -r "$s" ] || refuse "scenario file $s is not readable; there is nothing to aim at the peer"
    is_adversarial "$s" &&
      refuse "$s contains a malform: block or an action: raw step; adversarial traffic must never be aimed at a real peer"
    has_proven_marker "$s" ||
      refuse "$s carries no '# PROVEN AGAINST: Asterisk (non-IMS SIP)' marker, so it is not one of the scenarios proven against this peer"
  done
}

check_ca_present() {
  [ -s "$CA_CRT" ] && return 0
  refuse "CA file $CA_CRT is missing or empty; up-sip.sh was not run or wrote elsewhere, and the TLS leg cannot verify anything"
}

# The shipped basic_call_tls.yaml keeps insecure_skip_verify: it is a lab
# template pointed at a user's own gear. Running it unchanged here would
# produce a green TLS job that does not verify the certificate — proving
# "TLS transport works", not "TLS works". So the verifying copy is DERIVED
# into the output directory and the shipped file is never edited.
#
# Cellrift verifies by default: ca_file present and insecure_skip_verify
# absent means verification is on. There is no CLI override for ca_file, so
# a substitution is the only way to point it at a per-run generated CA.
#
# The anchored grep matters. This file's own description prose and comments
# contain the words insecure_skip_verify, so a bare-word grep would fire on
# every run. The substitution is anchored the same way and keeps the indent.
derive_verified_tls_scenario() {
  local live derived="$OUT/basic_call_tls.verified.yaml"
  check_ca_present
  live=$(grep -cE '^[[:space:]]*insecure_skip_verify:[[:space:]]*true' "$TLS_SCENARIO")
  [ "$live" = "1" ] || refuse "expected exactly one live insecure_skip_verify: true line in $TLS_SCENARIO, found $live; the derivation below would change the wrong number of lines"
  sed -E "s|^([[:space:]]*)insecure_skip_verify:[[:space:]]*true.*|\1ca_file: $CA_CRT|" \
    "$TLS_SCENARIO" > "$derived"
  grep -qE '^[[:space:]]*insecure_skip_verify:' "$derived" &&
    refuse "the derived TLS scenario still carries a live insecure_skip_verify key"
  grep -qF "ca_file: $CA_CRT" "$derived" ||
    refuse "the derived TLS scenario does not contain ca_file: $CA_CRT"
}

# peer_request_count <transport>: requests the peer's own log shows it
# received over that transport so far (cumulative).
peer_request_count() {
  local log
  log=$(docker logs asterisk 2>&1)
  grep -cE "Received SIP request .* from ${1}:" <<<"$log" || true
}

# peer_challenge_count: 401 responses the peer's own log shows it TRANSMITTING
# so far (cumulative), counted the same way peer_request_count counts requests.
#
# The peer's log is the source, not Cellrift's report: Cellrift reporting
# "challenged" is Cellrift's own account of the run, and this gate exists for the
# case where the two disagree.
peer_challenge_count() {
  local log
  log=$(docker logs asterisk 2>&1)
  grep -c 'SIP/2.0 401 Unauthorized' <<<"$log" || true
}

# wait_for_challenges <count-before>: print how many 401s the peer logged since
# <count-before>, waiting up to CHALLENGE_WAIT_MS for the first one to appear.
# See CHALLENGE_WAIT_MS for why this is a bounded poll.
wait_for_challenges() {
  local before="$1" delta=0 waited=0
  while :; do
    delta=$(($(peer_challenge_count) - before))
    [ "$delta" -ge 1 ] && break
    [ "$waited" -ge "$CHALLENGE_WAIT_MS" ] && break
    sleep 0.1
    waited=$((waited + 100))
  done
  echo "$delta"
}

# run_cellrift_pass <scenario> <target> <outfile-args...>
run_cellrift_pass() {
  local scenario="$1" target="$2"
  shift 2
  go run ./cmd/cellrift run --target-host "$target" --timeout "$STEP_TIMEOUT" \
    "$@" "$scenario"
}

# run_scenario <name> <scenario-path> <target> <transport>: two passes, each
# with its own exit code and its own peer-receipt delta (see the header).
run_scenario() {
  local name="$1" scenario="$2" target="$3" transport="$4" before after cbefore
  before=$(peer_request_count "$transport")
  cbefore=$(peer_challenge_count)
  run_cellrift_pass "$scenario" "$target" > "$OUT/$name.stdout" 2>&1
  echo $? > "$OUT/$name.exit"
  after=$(peer_request_count "$transport")
  echo "$((after - before))" > "$OUT/$name.receipts"
  # Pass 1 only, like .receipts: a per-pass figure, never a cumulative total
  # presented as a per-scenario number. Every scenario records it; only the
  # authenticated one is gated on it. Scenarios that are never challenged
  # would each wait the full bound here, so only wait when asked to.
  echo "$(challenge_delta "$name" "$cbefore")" > "$OUT/$name.challenges"
  run_cellrift_pass "$scenario" "$target" --format json \
    --output "$OUT/$name.json" > "$OUT/$name.json.stdout" 2>&1
  echo $? > "$OUT/$name.json.exit"
  echo "$(($(peer_request_count "$transport") - after))" > "$OUT/$name.receipts.pass2"
}

# challenge_delta <name> <count-before>: the digest scenario waits for the
# peer's log to catch up; the others read the count once, because a wait would
# only make every run that is never challenged slower.
challenge_delta() {
  if [ "$1" = "$(basename "$AUTH_SCENARIO" .yaml)" ]; then
    wait_for_challenges "$2"
  else
    echo $(($(peer_challenge_count) - $2))
  fi
}

# Without this, a build or startup failure is graded as an interop result:
# the later greps find nothing in a file that does not exist, pass, and the
# run announces a conclusion about a peer that was never contacted.
gate_cellrift_ran() {
  [ -s "$OUT/$1.stdout" ] && [ -s "$OUT/$1.json" ] && return 0
  refuse "cellrift produced no terminal output or no JSON report for $1; that is a build or startup failure, not a result about the peer"
}

# THE gate. Everything else in this file grades Cellrift's own account of
# the run; this one reads the peer's. A host-networked container that lost
# its bind still logs its ready line, and Cellrift then talks to whatever
# else is on the port — a result about a peer nobody configured. A TLS leg
# that quietly became a UDP leg is the same class of lie. If this count is
# zero, the bytes did not reach the intended peer over the intended
# transport, and nothing graded below means anything.
#
# Per scenario and BEFORE grade, both deliberately. Before grade, because a
# scenario aimed at the wrong port fails its assertions too, and if grade ran
# first it would refuse with "the peer did not pass it" — a claim about a peer
# that was never reached, making this gate unreachable and unprovable.
#
# This gate CANNOT distinguish "the bytes went to the wrong place" from "the
# transport never came up": a failed TLS handshake also produces zero
# received requests, because the peer logs a SIP request only once it can
# read one. So the message names every cause and points at the run's own
# output for which one it was.
#
# Minimum 1, not an expected count. Zero means nothing arrived; anything above
# is a peer that is answering, and how many requests a scenario sends is the
# scenario's business. A rejecting peer answers the first REGISTER and the
# scenario stops, so a higher minimum would report a rig problem where there
# is a real peer rejection.
gate_peer_received() {
  local n
  n=$(cat "$OUT/$1.receipts" 2>/dev/null || echo 0)
  [ "$n" -ge 1 ] && return 0
  refuse "the peer's own log shows no request received over $2 during $1. The target was wrong, the peer was not listening, OR the transport never completed (for TLS, the handshake, e.g. a rejected certificate). See $OUT/$1.stdout for which."
}

# THE gate for digest authentication, and the counterpart of gate_peer_received.
#
# gate_peer_received proves the bytes reached the intended peer. This proves the
# peer REQUIRED authentication. A mistyped auth= produces an immediate 200, a
# passing scenario, and a job that announces a digest result for a run with no
# digest in it. Minimum 1 rather than an exact count: how many times a scenario
# is challenged is the scenario's business, and the peer answers each REGISTER's
# first copy with its own challenge.
#
# It runs after gate_peer_received and BEFORE grade for the same reason that
# one does: a scenario that was never challenged still passes its own
# assertions against a peer that answers 200, so grading first would report a
# green result and this gate would be unreachable.
#
# Asterisk is a weak oracle (see the scenario's header), so this gate asserts
# only that the peer challenged. It says nothing about the header grammar.
gate_peer_challenged() {
  local n
  n=$(cat "$OUT/$1.challenges" 2>/dev/null || echo 0)
  [ "$n" -ge 1 ] && return 0
  refuse "the peer's own log shows it never sent a 401 during $1 (waited up to ${CHALLENGE_WAIT_MS} ms for it), so the rig required no authentication and this run exercised no digest. Check that pjsip.conf's auth= lines name loaded [auth] objects ('pjsip show auths')."
}

# Cumulative totals are informational only: never gated on, because two
# passes per scenario make a total unusable as a per-scenario figure.
log_peer_counts() {
  {
    echo "cumulative (both passes of every scenario, informational only):"
    echo "from UDP: $(peer_request_count UDP)"
    echo "from TLS: $(peer_request_count TLS)"
  } | tee "$OUT/peer-counts.txt"
}

gate_tls_verified() {
  local f
  for f in "$OUT/basic_call_tls.json" "$OUT/basic_call_tls.stdout" ${GATE_TLS_WARNING_SOURCE:+"$GATE_TLS_WARNING_SOURCE"}; do
    grep -q 'NOT verified' "$f" &&
      refuse "the TLS leg ran without certificate verification ($f carries the warning); it proves only that the transport works"
  done
  grep -qE '^[[:space:]]*insecure_skip_verify:' "$OUT/basic_call_tls.verified.yaml" &&
    refuse "the derived TLS scenario has a live insecure_skip_verify after the run"
  return 0
}

grade() {
  local t j
  t=$(cat "$OUT/$1.exit")
  j=$(cat "$OUT/$1.json.exit")
  [ "$t" = "0" ] ||
    refuse "the real peer did not pass $1 (exit $t); this is a result about the peer, not the rig"
  [ "$t" = "$j" ] ||
    refuse "the terminal pass of $1 exited $t but the JSON pass exited $j; the two passes disagree"
}

grade_udp_scenarios() {
  local s name
  for s in $UDP_SCENARIOS; do
    name=$(basename "$s" .yaml)
    run_scenario "$name" "$s" "$SIP_UDP_TARGET" UDP
    gate_cellrift_ran "$name"
    gate_peer_received "$name" UDP
    grade "$name"
  done
}

# Per scenario the order is: did we run at all, did the peer get it, did the
# peer pass it. Each answers a question the next one presupposes.
grade_tls_scenario() {
  run_scenario basic_call_tls "$OUT/basic_call_tls.verified.yaml" "$SIP_TLS_TARGET" TLS
  gate_cellrift_ran basic_call_tls
  gate_peer_received basic_call_tls TLS
  gate_tls_verified
  grade basic_call_tls
}

# Per scenario: did we run, did the peer get it, did the peer challenge, did
# the peer pass it.
grade_auth_scenario() {
  local name
  name=$(basename "$AUTH_SCENARIO" .yaml)
  run_scenario "$name" "$AUTH_SCENARIO" "$SIP_UDP_TARGET" UDP
  gate_cellrift_ran "$name"
  gate_peer_received "$name" UDP
  gate_peer_challenged "$name"
  grade "$name"
}

main() {
  rm -rf "$OUT"
  mkdir -p "$OUT"
  gate_peer_is_ours
  gate_transports_bound
  gate_no_adversarial_scenario
  derive_verified_tls_scenario
  grade_udp_scenarios
  grade_auth_scenario
  grade_tls_scenario
  log_peer_counts
  docker logs -t asterisk > "$OUT/asterisk.log" 2>&1
  echo "OK: asterisk answered register_impaired, basic_call, register_digest_auth (udp/15060) and basic_call_tls with certificate verification ON (tls/15061); the peer's own log confirms it received each scenario's requests and that it challenged the authenticated one. Asterisk accepts several malformed Authorization headers, so this is a proof of the digest ARITHMETIC; the header grammar is pinned by unit tests."
}

main
