#!/usr/bin/env bash
# check-synthetic-data.sh — scan scenario YAML files for real subscriber identifiers.
#
# Allowed test ranges:
#   Phone numbers: Ofcom UK reserved range 447700900000 – 447700900999 (+447700900xxx)
#   PLMN:          3GPP test network MCC 001 / MNC 01 only
#
# Usage: ./scripts/check-synthetic-data.sh [scenarios-dir]
# Exit 0 on success, 1 if real-looking identifiers are found.
set -euo pipefail

cd "$(dirname "$0")/.."

SCENARIOS_DIR="${1:-scenarios}"
failed=0

if [ ! -d "$SCENARIOS_DIR" ]; then
  echo "WARN: directory '$SCENARIOS_DIR' not found — skipping synthetic data check"
  exit 0
fi

# ── E.164 format (+country-code number) ────────────────────────────────────
# Flag any +NNNN... number that is NOT in the Ofcom test range (+447700900xxx).
E164=$(grep -rE '\+[1-9][0-9]{9,14}' "$SCENARIOS_DIR" --include="*.yaml" || true)
REAL_E164=$(echo "$E164" | grep -vE '^\s*$|\+447700900' || true)
if [ -n "$REAL_E164" ]; then
  echo "FAIL: E.164 phone numbers outside the allowed test range found in $SCENARIOS_DIR"
  echo "      Allowed: +447700900xxx (Ofcom reserved range)"
  echo ""
  echo "$REAL_E164"
  failed=1
fi

# ── Bare digit strings in YAML values ──────────────────────────────────────
# Flag quoted strings that are purely 10–15 digits and NOT in the Ofcom range.
# This catches subscriber_msisdn: "441234567890" style entries.
BARE=$(grep -rE '"[1-9][0-9]{9,14}"' "$SCENARIOS_DIR" --include="*.yaml" || true)
REAL_BARE=$(echo "$BARE" | grep -vE '^\s*$|"447700900' || true)
if [ -n "$REAL_BARE" ]; then
  echo "FAIL: bare phone numbers outside the allowed test range found in $SCENARIOS_DIR"
  echo "      Allowed: 447700900xxx (Ofcom reserved range)"
  echo ""
  echo "$REAL_BARE"
  failed=1
fi

# ── PLMN identifiers (MCC/MNC) ─────────────────────────────────────────────
# Flag any 3GPP domain whose MCC is not 001, the test network reserved by
# ITU-T E.212 and used throughout 3GPP test specifications.
#
# This check exists because the repository shipped `ims.mnc001.mcc234...` in
# five scenarios for a long time. MCC 234 is the United Kingdom, so
# `mnc001.mcc234` reads as a real operator's IMS domain rather than an
# obviously-synthetic one — and the MSISDN checks above cannot catch it,
# because the MSISDN in those files was correctly inside the Ofcom range. A
# policy nobody enforces mechanically is a policy that drifts.
# Comment lines are excluded: a scenario may legitimately mention a real MCC
# in prose explaining why it must not use one (uar_uaa.yaml does exactly that,
# citing the run that forced the change). Comments never reach the wire.
MCC=$(grep -rE 'mcc[0-9]{3}' "$SCENARIOS_DIR" --include="*.yaml" || true)
REAL_MCC=$(echo "$MCC" | grep -vE '^\s*$|mcc001|:[[:space:]]*#' || true)
if [ -n "$REAL_MCC" ]; then
  echo "FAIL: PLMN identifiers outside the 3GPP test network found in $SCENARIOS_DIR"
  echo "      Allowed: mcc001 (with mnc001) — ITU-T E.212 reserved test MCC"
  echo ""
  echo "$REAL_MCC"
  failed=1
fi

# ── Cell identifiers (utran-cell-id-3gpp) ──────────────────────────────────
# The cell id in P-Access-Network-Info is MCC+MNC+cell, so the same rule
# applies: it must start with the test PLMN 00101.
CELL=$(grep -rE 'utran-cell-id-3gpp=[0-9]' "$SCENARIOS_DIR" --include="*.yaml" || true)
REAL_CELL=$(echo "$CELL" | grep -vE '^\s*$|utran-cell-id-3gpp=00101|:[[:space:]]*#' || true)
if [ -n "$REAL_CELL" ]; then
  echo "FAIL: cell identifiers outside the 3GPP test network found in $SCENARIOS_DIR"
  echo "      Allowed: utran-cell-id-3gpp=00101... (MCC 001, MNC 01)"
  echo ""
  echo "$REAL_CELL"
  failed=1
fi

if [ "$failed" -eq 0 ]; then
  echo "OK: no real phone numbers, PLMNs or cell ids found in $SCENARIOS_DIR"
fi

exit "$failed"
