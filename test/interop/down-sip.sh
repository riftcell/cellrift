#!/usr/bin/env bash
# down-sip.sh — tear down the Asterisk SIP interop peer. Idempotent: safe to
# run when nothing is up, which is why up-sip.sh calls it first and why CI can
# call it as an `if: always()` step.
#
# The per-run certificate directory is deliberately NOT removed here. A
# refused run's artefacts must survive inspection; up-sip.sh clears the
# directory at the start of the next run instead.
set -uo pipefail
docker rm -f asterisk >/dev/null 2>&1 || true
exit 0
