#!/usr/bin/env bash
# down-ocs.sh — tear down the SigScale OCS harness. Idempotent: safe to run
# when nothing is up, which is why up-ocs.sh calls it first.
set -uo pipefail
docker rm -f ocs >/dev/null 2>&1 || true
docker volume rm ocsdb >/dev/null 2>&1 || true
exit 0
