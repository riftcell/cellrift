# Run 13: Asterisk with digest required, arithmetic only, local run

These are the raw artefacts behind run 13 in [`../../EVIDENCE.md`](../../EVIDENCE.md). They are
committed, rather than linked, because links do not last: a CI artifact expires, and the
run's logs follow it. Nothing here depends on a URL or on the
machine that produced it.

**The files at the top level of this directory are from a local run. The `ci/`
subdirectory holds the GitHub Actions run that followed it**, at commit
`5bccb73`, whose figures agree with the local ones: 3 challenges per pass,
exit 0, 6 requests. The green run's own page URL was not captured — the
artifact was supplied directly rather than from the run page — so the commit
and the artifact contents stand in for a link.

## What this is

Cellrift's SIP half, aimed at a real third-party SIP stack (Asterisk 22.10.1, `chan_pjsip`) that
requires digest authentication on three endpoints (evidence of the digest
arithmetic only), by `bash test/interop/up-sip.sh` and
`bash test/interop/run-sip-scenario.sh`. The scenario is `scenarios/ims/register_digest_auth.yaml`
over UDP (`127.0.0.1:15060`).

Asterisk validates the digest **arithmetic** (a wrong hash gets a 401) and does not enforce the
header's grammar; it returned 200 for several malformed headers when that was measured. These files
therefore support a claim about the arithmetic and no claim about the header's wire format, which is
pinned by in-process tests. Asterisk is a plain SIP server, not a P-CSCF.

## Provenance

| Item | Value |
| --- | --- |
| Run date | 2026-10-02 (peer log timestamps 15:51 UTC) |
| Commit the run was made at | `5bccb731cc5cf4923618c3d886268923e4a48455` (`5bccb73`) |
| Changes after that commit, before these files were committed | comment-only edits to `scenarios/ims/register_digest_auth.yaml` (header text) and `test/interop/run-sip-scenario.sh` (three comment blocks), made so the arithmetic-scope check passes; no code line changed |
| Pinned image | `andrius/asterisk:22.10.1_debian-trixie@sha256:212326bd3f973a597be9e4747be5d6653802e4950159e95aa7d728e394de2012` (the multi-arch index digest, which is what `docker pull` prints) |
| Version string | `Asterisk 22.10.1 built by root @ buildkitsandbox on a x86_64 running Linux on 2026-09-30 14:28:01 UTC` (`core show version`) |
| Docker | 29.8.1 |
| Network | `--network host`, ports 15060 (UDP) and 15061 (TLS) |

## Read this first: each scenario ran twice

The harness runs every scenario twice (pass 1 for the terminal rendering, pass 2 for the JSON report),
so `asterisk.log` holds two passes. Quote the per-pass figure for any statement about one scenario.

| Scenario | Requests the peer received **per pass** | 401s the peer sent per pass | Both passes |
| --- | --- | --- | --- |
| `register_digest_auth` | 6 (3 REGISTERs without credentials, 3 retries with them) | 3 | 12 requests, 6 401s |

`register_digest_auth.receipts` and `.receipts.pass2` hold `6` and `6`;
`register_digest_auth.challenges` holds `3` (pass 1, counted from the peer's own log). The
cumulative totals in `peer-counts.txt` (22 from UDP, 4 from TLS) are the sum over both passes of
**all four** scenarios and are informational only.

## Files

Copied from `/tmp/cellrift-sip/` exactly as the harness wrote them (`diff` between each committed
file and its original is empty). None of these is evidence of the header grammar, only of the digest
arithmetic and of what the peer logged:

| File | What it is |
| --- | --- |
| `asterisk.log` | The peer's complete `docker logs -t` output with the SIP logger on, colour-free. It covers all four scenarios; the digest scenario is the span from `asterisk.log:630` to `:938` (pass 1 first, pass 2 from `:793`). It keeps its CRLF line endings (918 of them) through a `-text` entry in `.gitattributes`. Boot-time `ERROR` lines (PostgreSQL, LDAP, CDR modules the image ships) are part of the image. It contains no password: the peer logs the digest `response=` hash, never the secret. |
| `register_digest_auth.stdout` | Cellrift's terminal output for pass 1. |
| `register_digest_auth.json` | Cellrift's JSON report for pass 2. |
| `register_digest_auth.receipts`, `.receipts.pass2` | Requests the peer received during pass 1 and pass 2, from its own log before and after. |
| `register_digest_auth.challenges` | 401 responses the peer's own log shows it sent during pass 1. |
| `register_digest_auth.exit`, `.json.exit` | Exit codes of pass 1 and pass 2 (both `0`). |
| `peer-counts.txt` | The cumulative totals described above. |

Produced in the same session, by hand-assembled commands (verbatim output, with the failure dumps
elided as each file says):

| File | What it is |
| --- | --- |
| `gate-proofs.txt` | The two refusal sites added for digest authentication (`wait_for_auths`, `gate_peer_challenged`) made to fire, with commands, verbatim output, exit codes and a green control after each. |
| `guard-d-mutations.txt` | Guard D of `scripts/check-scenario-labels.sh` against `register_digest_auth.yaml`: five mutations, one extra evasion, and a clean control. Also the Guards A, B and C comparison. |
| `vector-proofs.txt` | The RFC 7616 / RFC 2617 vector run that preceded any peer (section 1, from the implementing session's own capture), a re-run after the rig existed (section 2), and the git history that orders them (section 3). |

**Deliberately not committed:** the artefacts of `register_impaired`, `basic_call` and
`basic_call_tls`. They are unchanged by this run and produced the same per-pass figures as run 12
(3, 2 and 2 requests, all exits `0`, 0 challenges), so committing them again would add bytes and no
information. Their per-pass figures are quoted in run 13's entry.

## Verifying it yourself

```sh
# 401s the peer sent, both passes; any rejected credential would add a NOTICE
grep -c 'SIP/2.0 401 Unauthorized' asterisk.log       # 6
grep -c 'Failed to authenticate' asterisk.log         # 0

# The peer's WWW-Authenticate headers: pass 1 at :650, :704, :758 and :759
grep -n 'WWW-Authenticate' asterisk.log               # 8 lines

# The both-offered endpoint (447700900203), pass 1: the two headers, then the retry
sed -n '758p;759p;773p' asterisk.log

# Per pass: 6 requests, 3 challenges, both exits 0
cat register_digest_auth.receipts register_digest_auth.receipts.pass2 register_digest_auth.challenges
cat register_digest_auth.exit register_digest_auth.json.exit

# CRLF preserved
grep -c $'\r' asterisk.log                            # 918
sha256sum asterisk.log                                 # 832e4d59d1d039710c9d6163011b6fc1e6ea8c1704340aa0b356d04af9649e0f
```
