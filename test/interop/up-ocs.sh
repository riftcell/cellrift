#!/usr/bin/env bash
# up-ocs.sh — boot SigScale OCS and provision a prepaid subscriber, so the
# CCR/CCA exchange can be proven against a real Credit-Control peer.
#
# Why a second harness rather than another container in up.sh: the Open5GS
# HSS runs with --network host and owns 3868 there, and this OCS wants the
# same port. Keeping them separate also keeps the failure domains separate —
# an OCS that will not boot must not be able to fail the five Diameter
# commands the HSS already proves.
#
# The OCS is SigScale's (Apache-2.0, Erlang), which serves the 3GPP OCS
# interfaces Ro/Gy — Credit-Control Application-Id 4, the application
# Cellrift sends CCR under. An earlier version of this repository claimed no
# such open-source peer existed; that was wrong, and this file is the
# correction.
#
# Usage: bash test/interop/up-ocs.sh
# Exit 0 once the OCS answers on Diameter and the subscriber is provisioned.
set -euo pipefail

cd "$(dirname "$0")/../.."

# Synthetic-only, matching scenarios/diameter/credit_control.yaml's
# subscriber_msisdn (Ofcom test range — never a real MSISDN).
MSISDN="447700900001"
DIAMETER_PORT=3869   # 3868 belongs to the HSS harness; keep them co-existable
REST_PORT=8080
OCS_HOST="ocs.lab.local"   # dotted: Erlang's -name needs a resolvable FQDN
READY_RETRIES=40
# The sigscale/ocs image's built-in default REST login. Not a secret: it only
# ever reaches the throwaway container this script starts on 127.0.0.1.
OCS_AUTH="admin:admin" # gitleaks:allow

api() {
  curl -fsS -u "$OCS_AUTH" -H 'Content-Type: application/json' \
    -H 'Accept: application/problem+json' "$@"
}

start_ocs() {
  docker volume create ocsdb >/dev/null
  # Erlang distribution needs a dotted hostname, and initialize must run as
  # its own entrypoint: the image's ENTRYPOINT is bin/start, so passing
  # bin/initialize as a command would append it rather than replace it.
  docker run --rm -h "$OCS_HOST" -v ocsdb:/home/otp/db \
    --entrypoint bin/initialize sigscale/ocs >/dev/null
  # -dit, not -d: the release runs an Erlang shell, and without a TTY it
  # reads EOF and terminates immediately.
  docker run -dit --name ocs -h "$OCS_HOST" -v ocsdb:/home/otp/db \
    -p "${REST_PORT}:8080/tcp" -p "${DIAMETER_PORT}:3868/tcp" \
    sigscale/ocs >/dev/null
}

wait_for_rest() {
  for _ in $(seq 1 "$READY_RETRIES"); do
    if curl -fsS -u "$OCS_AUTH" "http://127.0.0.1:${REST_PORT}/ocs/v1/client" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "OCS REST never became ready on ${REST_PORT}" >&2
  docker logs ocs 2>&1 | tail -30 >&2
  return 1
}

# provision_client tells the OCS which Diameter peers to accept. Without it
# the CER is answered 3010 DIAMETER_UNKNOWN_PEER, exactly as the HSS did
# before its ConnectPeer entry existed.
provision_client() {
  api -X POST "http://127.0.0.1:${REST_PORT}/ocs/v1/client" \
    -d '{"id":"127.0.0.1","protocol":"DIAMETER"}' >/dev/null
}

# provision_subscriber walks SigScale's TMF chain: an offering to subscribe
# to, a product instance of it, a service carrying the MSISDN the CCR names,
# and a balance for the rating engine to draw down. Miss the last two and a
# well-formed CCR is answered 5030 DIAMETER_USER_UNKNOWN.
provision_subscriber() {
  local now end pid
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  end=$(date -u -d '+10 years' +%Y-%m-%dT%H:%M:%SZ)

  api -X POST "http://127.0.0.1:${REST_PORT}/productCatalogManagement/v2/productOffering" -d @- >/dev/null <<JSON
{"name":"cellrift-interop-data","description":"Prepaid data offer for the Cellrift interop harness",
 "isBundle":false,"lifecycleStatus":"Active",
 "validFor":{"startDateTime":"$now","endDateTime":"$end"},
 "productSpecification":{"id":"8","href":"/productCatalogManagement/v2/productSpecification/8"},
 "productOfferingPrice":[{"name":"usage","description":"per megabyte","priceType":"usage",
   "validFor":{"startDateTime":"$now","endDateTime":"$end"},
   "price":{"taxIncludedAmount":"0.01","currencyCode":"USD"},"unitOfMeasure":"1000000b"}]}
JSON

  pid=$(api -X POST "http://127.0.0.1:${REST_PORT}/productInventoryManagement/v2/product" \
    -d '{"productOffering":{"id":"cellrift-interop-data","name":"cellrift-interop-data","href":"/productCatalogManagement/v2/productOffering/cellrift-interop-data"}}' \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')

  # `product` is a plain string id here, not an object — an object is
  # answered 400 "Exception occurred parsing request body".
  api -X POST "http://127.0.0.1:${REST_PORT}/serviceInventoryManagement/v2/service" \
    -d "{\"id\":\"${MSISDN}\",\"state\":\"active\",\"isServiceEnabled\":true,\"product\":\"${pid}\",
         \"serviceCharacteristic\":[{\"name\":\"serviceIdentity\",\"value\":\"${MSISDN}\"}]}" >/dev/null

  api -X POST "http://127.0.0.1:${REST_PORT}/balanceManagement/v1/balanceAdjustment" \
    -d "{\"amount\":{\"units\":\"cents\",\"amount\":100000},\"product\":{\"id\":\"${pid}\"}}" >/dev/null
}

verify_subscriber() {
  if ! api "http://127.0.0.1:${REST_PORT}/serviceInventoryManagement/v2/service/${MSISDN}" >/dev/null; then
    echo "subscriber ${MSISDN} was not provisioned" >&2
    return 1
  fi
}

bash "$(dirname "$0")/down-ocs.sh"
start_ocs
wait_for_rest
provision_client
provision_subscriber
verify_subscriber

echo "OCS harness ready: sigscale/ocs on Diameter ${DIAMETER_PORT}, subscriber ${MSISDN} provisioned with a balance"
