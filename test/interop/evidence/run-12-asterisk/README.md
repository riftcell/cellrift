# Run 12 — the Asterisk SIP peer, captured on a GitHub-hosted runner

These are the raw artefacts behind run 12 in [`../../EVIDENCE.md`](../../EVIDENCE.md).
They are committed, rather than linked, because the links do not last: the CI
artifact expires on GitHub's retention window, and the run's logs follow it. An
evidence file whose proof lives behind a dead link proves nothing.

## What this is

Cellrift's SIP half, aimed at a real third-party SIP stack (Asterisk 22.10.1,
`chan_pjsip`, pinned image
`andrius/asterisk:22.10.1_debian-trixie@sha256:212326bd3f973a597be9e4747be5d6653802e4950159e95aa7d728e394de2012`)
by `bash test/interop/up-sip.sh` and `bash test/interop/run-sip-scenario.sh`.
Three scenarios: `register_impaired.yaml` and `basic_call.yaml` over UDP
(`127.0.0.1:15060`), and a derived copy of `basic_call_tls.yaml` over TLS
(`127.0.0.1:15061`) with certificate verification ON.

Asterisk is a plain SIP server, **not a P-CSCF**. Cellrift sent **no ACK** for
the 200 to either INVITE, so no dialog was acknowledged from the peer's side.
The 180 is not asserted by any scenario. See run 12's "What this does not
prove" before drawing any conclusion from these files.

## Read this first: each scenario ran twice

`asterisk.log` holds **more requests than any one scenario sends**, and a reader
who counts it without this paragraph will reasonably distrust the rest.

The harness runs every scenario **twice** against the peer: pass 1 produces the
terminal rendering, pass 2 produces the JSON report. They are separate runs
because `--output` replaces stdout and one reporter is selected per run, so no
single invocation yields both.

| Scenario | Transport | Requests the peer received **per pass** | Both passes |
| --- | --- | --- | --- |
| `register_impaired` | UDP | 3 REGISTER | 6 |
| `basic_call` | UDP | 2 (INVITE, BYE) | 4 |
| `basic_call_tls` | TLS | 2 (INVITE, BYE) | 4 |

The per-pass figures are in `register_impaired.receipts`, `basic_call.receipts`
and `basic_call_tls.receipts` (pass 1) and the matching `.receipts.pass2` files
(pass 2); each holds one number and all six hold the figure above. The
**cumulative** totals in `peer-counts.txt` (10 from UDP, 4 from TLS; 14 in all)
are the sum over both passes of every scenario: 6 + 4 over UDP, 4 over TLS. Quote
the per-pass figure for any statement about one scenario.

All six exit files (`*.exit`, `*.json.exit`) hold `0`.

## Provenance

- Run: <https://github.com/riftcell/cellrift/actions/runs/36902813507> (run id 36902813507)
- Artifact `sip-interop-evidence`: <https://github.com/riftcell/cellrift/actions/runs/36902813507/artifacts/11182372217> (artifact id 11182372217, 25 files, 33,496 bytes, zip sha256 `ed88395967f3de706563e1be448716dce67e71dacd7c5630d4f168a565b6ffc8`)
- Commit checked out: `1b694734805f06105e624243c86fe0f7e4a0f60d`
- Runner: GitHub-hosted `ubuntu-24.04`, image version `20260927.320.1`, runner 2.337.0
- Timing: `up-sip.sh` 17:54:24 to ready 17:54:33 UTC (a cold image pull is included); scenarios 17:54:33 to 17:55:06 UTC

Both links are expected to stop resolving. A 404 is not evidence the run never
happened; the content is here.

## Files

From the CI artifact, unedited:

| File | What it is |
| --- | --- |
| `asterisk.log` | The peer's own complete `docker logs -t` output with the SIP logger on, colour-free (`ASTERISK_TERMINAL_OPTS=-n`). Boot-time `ERROR`/`WARNING` lines (LDAP, PostgreSQL, CDR/CEL modules the image ships but this rig does not configure) and a `cdr_csv` write error after each call are part of the image and unrelated to SIP. The artifact held this file twice, byte-identical; it is committed once. It keeps its CRLF line endings (the SIP messages carry them) through a `-text` entry in the repository's `.gitattributes`; its sha256 is `27cd6d9e7ea99a6d7a148d006971288f80e878186438fc3809dacbeea6d809d7`, the same as the file inside the CI artifact. |
| `register_impaired.stdout`, `basic_call.stdout`, `basic_call_tls.stdout` | Cellrift's terminal output for pass 1 of each scenario. |
| `register_impaired.json`, `basic_call.json`, `basic_call_tls.json` | Cellrift's JSON reports for pass 2. |
| `*.receipts`, `*.receipts.pass2` | Requests the peer received during pass 1 and pass 2 of that scenario, counted from the peer's own log before and after. |
| `*.exit`, `*.json.exit` | Exit codes of pass 1 and pass 2. |
| `peer-counts.txt` | The cumulative totals described above. |
| `basic_call_tls.verified.yaml` | The derived scenario the TLS leg actually ran. `ca_file: /tmp/cellrift-sip-certs/ca.crt` is a live key; `insecure_skip_verify` appears only in comments and prose, never as a key. |

The artifact also held three empty `*.json.stdout` files (pass 2 writes its
report to the file, so stdout is empty); they are not committed.

Produced locally, not by CI:

| File | What it is |
| --- | --- |
| `gate-proofs.txt` | Every one of the 20 refusal call sites in `up-sip.sh` and `run-sip-scenario.sh`, made to fire, with the verbatim output and exit code. Run on a developer machine; post-refusal diagnostic dumps and Asterisk's per-poll `Remote UNIX connection` lines are elided and replaced by counts, as its header says. |
| `guard-d-mutations.txt` | Guard D of `scripts/check-scenario-labels.sh`: five mutations (each red) and a clean-tree control (green), verbatim. |
| `wrongca-terminal.txt`, `wrongca-report.json`, `wrongca-report.xml` | The wrong-CA probe: the TLS scenario aimed at the real peer with a CA that did not sign its certificate. All three formats carry the `x509: certificate signed by unknown authority` cause. |

## Reading the log without being misled

Three traps, all real:

1. **The ACK grep.** The correct check is
   `grep -c 'Received SIP request (.*ACK' asterisk.log`, which returns **0**: no
   ACK request ever reached the peer. A bare `grep -c ACK asterisk.log` returns
   **14**, and every one of those is the word `ACK` inside an `Allow:` header
   (`Allow: OPTIONS, REGISTER, ..., INVITE, ACK, BYE, ...`) on a response the
   peer sent. Anyone re-verifying will hit this.
2. **The peer sends the 200 to an INVITE more than once.** Because no ACK
   arrives, the log shows the same 200 (same `To` tag, same Call-ID) transmitted
   again with roughly doubling gaps between consecutive sends — 0.5 s, then 1 s,
   then 2 s (timestamps 02.799, 03.299, 04.299, 06.298), i.e. +0.5 s, +1.5 s and
   +3.5 s from the first 200. The log stops when the
   harness removes the container, so how many repeats appear depends on how long
   after the INVITE the log ends. They say something about the missing ACK and
   nothing about `register_impaired.yaml`: the REGISTER 200s are each sent once.
3. **Source ports change between steps over TLS.** The INVITE and the BYE of one
   TLS scenario arrive from different ephemeral ports (47790 and 47796 in pass
   1), which is consistent with each step dialling its own connection.

## Verifying it yourself

```sh
# Requests the peer received, by transport (cumulative, both passes)
grep -c 'Received SIP request.*UDP' asterisk.log      # 10
grep -c 'Received SIP request.*TLS' asterisk.log      # 4

# No ACK request reached the peer  (NOT a bare grep -c ACK, which gives 14)
grep -c 'Received SIP request (.*ACK' asterisk.log    # 0

# 100, 180 and 200 for each INVITE: four INVITEs across two passes and two transports
grep -c 'SIP/2.0 100 Trying' asterisk.log             # 4
grep -c 'SIP/2.0 180 Ringing' asterisk.log            # 4

# The peer's version, from its own startup banner and its own Server header
grep -m1 'Asterisk 22.10.1' asterisk.log
grep -m1 'Server: Asterisk PBX 22.10.1' asterisk.log

# Per pass receipt counts: 3, 2, 2 in all six files
cat *.receipts *.receipts.pass2

# The TLS scenario verified: a live ca_file, no live insecure_skip_verify key
grep -E '^[[:space:]]*(ca_file|insecure_skip_verify):' basic_call_tls.verified.yaml
```
