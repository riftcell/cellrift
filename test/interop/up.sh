#!/usr/bin/env bash
# up.sh — boot the two-container Open5GS interop harness (Mongo + HSS),
# provision a synthetic test subscriber, and wait for the HSS to be ready.
#
# Idempotent: always tears down any previous run first. Both containers run
# with --network host so Mongo and the HSS can reach each other (and the
# host can reach the HSS on 127.0.0.1:3868) without a docker network create
# step. See test/interop/README.md for the full topology and provenance.
#
# Usage: bash test/interop/up.sh
# Exit 0 once the HSS has logged "HSS initialize...done"; non-zero otherwise.
set -euo pipefail

cd "$(dirname "$0")/../.."

# Synthetic-only test subscriber (3GPP test PLMN 001/01; K/OPc are synthetic
# test keys). Never real subscriber data — see TERMS.md / scripts/check-synthetic-data.sh.
IMSI="001010000000001"
K="465B5CE8B199B49FAA5F0A2EE238A6BC"
OPC="E8ED289DEBA952E4283B54E88E6183CA"
# The MSISDN scenarios/diameter/uar_uaa.yaml authenticates with. Keep the two
# in sync: the Cx path looks the subscriber up by IMSI *or* MSISDN, and the
# scenario sends this one as User-Name.
MSISDN="447700900001"

MONGO_READY_RETRIES=30
HSS_READY_RETRIES=30

wait_for_mongo() {
  for _ in $(seq 1 "$MONGO_READY_RETRIES"); do
    if docker exec mongo mongosh --quiet --eval "db.adminCommand('ping')" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "mongo never became ready" >&2
  return 1
}

wait_for_hss() {
  for _ in $(seq 1 "$HSS_READY_RETRIES"); do
    if docker logs hss 2>&1 | grep -q "HSS initialize...done"; then
      return 0
    fi
    sleep 1
  done
  echo "HSS never logged ready" >&2
  docker logs hss 2>&1 || true
  return 1
}

start_mongo() {
  docker run -d --name mongo --network host mongo:7.0 >/dev/null
}

provision_subscriber() {
  docker cp test/interop/open5gs-dbctl mongo:/tmp/open5gs-dbctl
  docker exec mongo chmod +x /tmp/open5gs-dbctl
  docker exec mongo /tmp/open5gs-dbctl --db_uri=mongodb://localhost/open5gs add \
    "$IMSI" "$K" "$OPC"
}

# provision_msisdn adds the MSISDN to the subscriber dbctl just created.
#
# It is a separate step because the vendored open5gs-dbctl has no command
# that can do it: every one of its add* paths writes "msisdn": [] and it
# offers no addmsisdn equivalent. That empty array is not cosmetic — the
# HSS's Cx handler calls ogs_dbi_msisdn_data() before anything else, so a
# subscriber without an MSISDN fails every UAR with "Cannot get MSISDN-Data
# for IMSI or MSISDN", which is what run 4 hit after the last AVP gap was
# closed (test/interop/EVIDENCE.md). The subscriber exists and the request
# is valid; the fixture was simply never provisioned for the IMS path.
provision_msisdn() {
  docker exec mongo mongosh --quiet open5gs --eval \
    "db.subscribers.updateOne({imsi: \"$IMSI\"}, {\$set: {msisdn: [\"$MSISDN\"]}})" >/dev/null
}

# verify_msisdn fails the harness loudly if the update above did not land,
# rather than leaving the run to fail later as an apparent interop defect.
verify_msisdn() {
  local found
  found=$(docker exec mongo mongosh --quiet open5gs --eval \
    "db.subscribers.countDocuments({imsi: \"$IMSI\", msisdn: \"$MSISDN\"})")
  if [ "$found" != "1" ]; then
    echo "subscriber $IMSI has no MSISDN $MSISDN provisioned (countDocuments=$found)" >&2
    return 1
  fi
}

start_hss() {
  # -e DB_URI overrides the image's own baked-in DB_URI=mongodb://mongo/open5gs.
  # Open5GS's own config loader (lib/app/ogs-init.c, Stage 5) applies the
  # DB_URI env var AFTER parsing hss.yaml's db_uri key, so the env var always
  # wins if left unset here -- confirmed empirically in this harness; see
  # README.md's "DB_URI env var" note (it is easy to assume this variable has
  # no effect; it does).
  docker run -d --name hss --network host \
    -e DB_URI=mongodb://localhost/open5gs \
    -v "$PWD/test/interop/hss.yaml:/opt/open5gs/etc/open5gs/hss.yaml:ro" \
    -v "$PWD/test/interop/hss-freeDiameter.conf:/opt/open5gs/etc/freeDiameter/hss.conf:ro" \
    gradiant/open5gs:2.8.0 \
    open5gs-hssd -c /opt/open5gs/etc/open5gs/hss.yaml >/dev/null
}

bash "$(dirname "$0")/down.sh"

start_mongo
wait_for_mongo
provision_subscriber
provision_msisdn
verify_msisdn
start_hss
wait_for_hss

echo "interop harness ready: mongo + hss up, subscriber $IMSI provisioned with MSISDN $MSISDN"
