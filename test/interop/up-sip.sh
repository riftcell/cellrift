#!/usr/bin/env bash
# up-sip.sh — boot the Asterisk SIP interop peer, generate its per-run TLS
# material, and refuse to report readiness that has not been verified.
#
# Idempotent: always tears down any previous run first. The container runs with
# --network host, so the peer is reachable on 127.0.0.1:15060 (UDP) and
# 127.0.0.1:15061 (TLS). Ports are NOT 5060/5061: a developer machine often
# runs its own Asterisk there and a host-networked container then silently
# loses its bind.
#
# Usage: bash test/interop/up-sip.sh
# Env:   CERTS_DIR         - where the per-run CA and server certificate are
#                            written (default /tmp/cellrift-sip-certs). The
#                            scenario runner reads the CA from here.
#        PJSIP_CONF        - pjsip configuration to mount. Point it at
#                            test/interop/asterisk/pjsip-reject.conf for the
#                            rejecting-peer probe.
#        EXPECT_ENDPOINTS  - 1 (default) requires the scenario endpoints to
#                            exist; 0 skips that gate, which the reject config
#                            has no endpoints to satisfy by design.
# Exit:  0  both transports are bound on the intended ports and the peer is
#           logging every SIP request it receives
#        1  a gate refused, with a "SIP RIG REFUSED:" line naming the reason
#
# `set -e` is kept here (unlike capture-liveness.sh): nothing in this script is
# expected to fail and carry on, so an unchecked failure should stop the run.
set -euo pipefail

cd "$(dirname "$0")/../.."

ASTERISK_IMAGE="andrius/asterisk:22.10.1_debian-trixie@sha256:212326bd3f973a597be9e4747be5d6653802e4950159e95aa7d728e394de2012"
SIP_UDP_PORT=15060
SIP_TLS_PORT=15061
CERTS_DIR="${CERTS_DIR:-/tmp/cellrift-sip-certs}"
PJSIP_CONF="${PJSIP_CONF:-test/interop/asterisk/pjsip.conf}"
EXPECT_ENDPOINTS="${EXPECT_ENDPOINTS:-1}"
READY_RETRIES=30

# refuse is the single exit path for every gate: one loud line on stdout,
# exit 1, no teardown (the container and certs stay for inspection).
refuse() {
  echo "SIP RIG REFUSED: $1"
  exit 1
}

# generate_certs writes a per-run CA and a server certificate signed by it.
#
# The SAN must contain the HOST PORTION of the address the scenario targets.
# Cellrift has no server_name field and never sets tls.Config.ServerName, so
# Go infers the verification name from the dial address and does not fall
# back to CN: a cert with only DNS:localhost fails a run targeting
# 127.0.0.1 with "x509: certificate is valid for localhost, not 127.0.0.1".
# IP:127.0.0.1 covers --target-host 127.0.0.1:PORT; DNS:localhost covers
# --target-host localhost:PORT. Both were verified to pass verification.
#
# chmod 644, not 600: the container's asterisk user is uid 1000 and a CI
# runner's uid may differ. These are synthetic, per-run and discarded.
generate_certs() {
  rm -rf "$CERTS_DIR"
  mkdir -p "$CERTS_DIR"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
    -keyout "$CERTS_DIR/ca.key" -out "$CERTS_DIR/ca.crt" -days 2 \
    -subj "/CN=cellrift-interop-ca" \
    -addext "basicConstraints=critical,CA:TRUE" 2>/dev/null
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
    -keyout "$CERTS_DIR/asterisk.key" -out "$CERTS_DIR/asterisk.csr" \
    -subj "/CN=asterisk.interop" 2>/dev/null
  printf 'subjectAltName = IP:127.0.0.1, DNS:localhost\nextendedKeyUsage = serverAuth\nbasicConstraints = CA:FALSE\n' \
    > "$CERTS_DIR/ext.cnf"
  openssl x509 -req -in "$CERTS_DIR/asterisk.csr" -CA "$CERTS_DIR/ca.crt" \
    -CAkey "$CERTS_DIR/ca.key" -CAcreateserial \
    -out "$CERTS_DIR/asterisk.crt" -days 2 -extfile "$CERTS_DIR/ext.cnf" 2>/dev/null
  chmod 644 "$CERTS_DIR/ca.crt" "$CERTS_DIR/asterisk.crt" "$CERTS_DIR/asterisk.key"
  openssl x509 -in "$CERTS_DIR/asterisk.crt" -noout -text | grep -q "IP Address:127.0.0.1" ||
    refuse "the server certificate has no IP:127.0.0.1 SAN, so it would not verify against a 127.0.0.1 target"
}

# ASTERISK_TERMINAL_OPTS=-n suppresses ANSI colour so `docker logs` is
# committable as text. The image's entrypoint runs `chown -R` on /etc/asterisk,
# which fails harmlessly on the :ro binds.
start_asterisk() {
  docker run -d --name asterisk --network host \
    -e ASTERISK_TERMINAL_OPTS="-n" \
    -v "$PWD/$PJSIP_CONF:/etc/asterisk/pjsip.conf:ro" \
    -v "$PWD/test/interop/asterisk/extensions.conf:/etc/asterisk/extensions.conf:ro" \
    -v "$CERTS_DIR/asterisk.crt:/etc/asterisk/keys/asterisk.crt:ro" \
    -v "$CERTS_DIR/asterisk.key:/etc/asterisk/keys/asterisk.key:ro" \
    "$ASTERISK_IMAGE" >/dev/null
}

# assert_no_bind_failure refuses if the peer's log records a failed bind.
# It must run AFTER wait_for_transports: `docker run -d` returns before
# Asterisk has attempted its binds, so an earlier call reads a log that cannot
# yet contain the failure and passes vacuously. The log is captured to a
# variable first so grep -q cannot SIGPIPE docker under pipefail.
#
# The pattern is the transport line, not bare "Address already in use": on a
# machine that runs its own Asterisk, the unrelated pbx_dundi module also logs
# "Unable to bind to 0.0.0.0:4520 : Address already in use" at boot, which is
# harmless to SIP and would make a bare match refuse every local run. The
# line that matters reads:
#   Transport 'transport-udp' could not be started: Address already in use
assert_no_bind_failure() {
  local log pattern="Transport '.*' could not be started"
  log=$(docker logs asterisk 2>&1 || true)
  if grep -qE "$pattern" <<<"$log"; then
    grep -E "$pattern" <<<"$log" || true
    refuse "a transport failed to bind (port collision on $SIP_UDP_PORT/$SIP_TLS_PORT?). The peer still logs readiness in this state, so its startup message is not a readiness signal, and scenarios would hit whatever else holds the port."
  fi
}

# wait_for_transports polls the container's own CLI, not its log. The startup
# line "Asterisk Ready." appears at ~0.7 s even when a transport failed to
# bind, so it is NOT used as a gate. Both bind ADDRESSES are required, not
# just the transport names, because a name can be configured without being
# bound. Observed: a transport that failed to bind or to load its certificate
# is ABSENT from this list while Asterisk still logs "Asterisk Ready.", so this
# gate alone refuses for a missing required transport. assert_no_bind_failure
# stays a separate gate because it is the one that catches any OTHER transport
# failing, which leaves both required addresses listed.
wait_for_transports() {
  local out
  for _ in $(seq 1 "$READY_RETRIES"); do
    out=$(docker exec asterisk asterisk -rx "pjsip show transports" 2>&1 || true)
    if grep -q "0.0.0.0:$SIP_UDP_PORT" <<<"$out" && grep -q "0.0.0.0:$SIP_TLS_PORT" <<<"$out"; then
      return 0
    fi
    sleep 1
  done
  docker logs asterisk 2>&1 || true
  refuse "transports never showed 0.0.0.0:$SIP_UDP_PORT and 0.0.0.0:$SIP_TLS_PORT in 'pjsip show transports'"
}

wait_for_endpoints() {
  [ "$EXPECT_ENDPOINTS" = "1" ] || return 0
  local out
  for _ in $(seq 1 "$READY_RETRIES"); do
    out=$(docker exec asterisk asterisk -rx "pjsip show endpoints" 2>&1 || true)
    if all_present "$out" 447700900123 447700900001 447700900201 447700900202 447700900203; then
      return 0
    fi
    sleep 1
  done
  refuse "endpoints 447700900123, 447700900001, 447700900201, 447700900202 and 447700900203 are not all present in 'pjsip show endpoints'"
}

# all_present <text> <needle>...: true when every needle occurs in the text.
all_present() {
  local text="$1" n
  shift
  for n in "$@"; do
    grep -q "$n" <<<"$text" || return 1
  done
}

# auths_loaded <'pjsip show auths' output>: true when each of the three auth
# objects is listed as its own object. The match is on the whole id followed by
# the "/username" the CLI prints, NOT a substring: a section mistyped to
# [auth-md5-TYPO] still contains the text "auth-md5", and a bare substring
# match accepted exactly that.
auths_loaded() {
  local id
  for id in auth-md5 auth-sha256 auth-both; do
    grep -qE "Auth: +$id/" <<<"$1" || return 1
  done
}

# endpoint_auth_is <endpoint> <auth-id>: true when the container's own CLI
# reports that endpoint's auth= as exactly that id. Listing the auth objects is
# not enough on its own: an endpoint whose auth= line is mistyped still loads,
# is listed, and silently accepts an unauthenticated REGISTER.
endpoint_auth_is() {
  docker exec asterisk asterisk -rx "pjsip show endpoint $1" 2>&1 |
    grep -qE "^ *auth +: $2\$"
}

# auth_wiring_ok: the three auth objects are loaded AND each digest endpoint
# names the object the scenario expects it to.
auth_wiring_ok() {
  auths_loaded "$(docker exec asterisk asterisk -rx 'pjsip show auths' 2>&1 || true)" &&
    endpoint_auth_is 447700900201 auth-md5 &&
    endpoint_auth_is 447700900202 auth-sha256 &&
    endpoint_auth_is 447700900203 auth-both
}

# wait_for_auths refuses unless the container's own CLI lists the three auth
# objects AND reports each digest endpoint's auth= as the object it should be.
#
# Without it, a typo in an endpoint's auth= line is INVISIBLE at boot: Asterisk
# loads the endpoint, answers the REGISTER 200 with no challenge at all, the
# scenario passes, and the job reports a green digest result for a run in which
# no digest was computed. That is not a weaker proof, it is a different one
# wearing the same label.
#
# Gated on EXPECT_ENDPOINTS for the same reason wait_for_endpoints is:
# pjsip-reject.conf deliberately defines neither endpoints nor auth objects.
wait_for_auths() {
  [ "$EXPECT_ENDPOINTS" = "1" ] || return 0
  for _ in $(seq 1 "$READY_RETRIES"); do
    auth_wiring_ok && return 0
    sleep 1
  done
  refuse "'pjsip show auths' does not list auth-md5, auth-sha256 and auth-both as objects, or endpoints 447700900201/202/203 do not report auth= auth-md5/auth-sha256/auth-both, so a REGISTER would be answered 200 with no challenge"
}

# enable_sip_logger makes the container log carry lines such as
# `<--- Received SIP request (742 bytes) from TLS:127.0.0.1:52380 --->`.
# Those lines are the only proof the bytes reached the intended peer rather
# than something else on the port.
enable_sip_logger() {
  local out
  out=$(docker exec asterisk asterisk -rx "pjsip set logger on" 2>&1 || true)
  grep -qi "logging enabled" <<<"$out" ||
    refuse "the peer did not confirm its SIP logger is on (got: $out)"
}

bash "$(dirname "$0")/down-sip.sh"

generate_certs
start_asterisk
wait_for_transports
assert_no_bind_failure
wait_for_endpoints
wait_for_auths
enable_sip_logger

AUTH_NOTE="digest auth wiring verified (auth-md5, auth-sha256, auth-both)"
[ "$EXPECT_ENDPOINTS" = "1" ] || AUTH_NOTE="endpoint and auth gates skipped (EXPECT_ENDPOINTS=0)"
echo "sip interop harness ready: asterisk on udp/$SIP_UDP_PORT and tls/$SIP_TLS_PORT, certs in $CERTS_DIR, $AUTH_NOTE, sip logger on"
