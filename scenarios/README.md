# The scenario library

Nineteen YAML files, four jobs. Every file is one of the following, and its
header comment says which — read the header before running anything.

| Kind | What it is for | How to run it | What green means |
| --- | --- | --- | --- |
| **Self-test** | Proving this binary works | `cellrift selftest` | The install is fine. Nothing about your network. |
| **Lab template** | Copy, edit, point at your own SIP endpoint | `cellrift run --target-host <host:port> …` or `--var pcscf_host=…` | Your peer answered the way the file asserts. |
| **Feature showcase** | Reading, to learn one capability; and this repo's own regression proof | Against Cellrift's mocks only, via `go test ./cmd/cellrift/` | Nothing about any peer. Several **fail by design** — see the table. |
| **Interop proof** | Proving one exchange against real third-party software | The harness for its peer under `test/interop/` (e.g. `bash test/interop/up.sh && bash test/interop/run-scenario.sh`), and CI on every push | A genuine third-party peer — an Open5GS HSS, a SigScale OCS or Asterisk — answered it. |

Eight files are proven against real third-party software in CI on every
push. Four Diameter files are answered by real peers: `diameter/uar_uaa.yaml`,
`diameter/s6a_air_ulr.yaml` and `diameter/cx_mar_sar.yaml` by an Open5GS HSS,
and `diameter/credit_control.yaml` by a SigScale OCS. Four SIP files are
answered by Asterisk: `ims/basic_call.yaml`, `ims/basic_call_tls.yaml`,
`ims/register_impaired.yaml` and `ims/register_digest_auth.yaml`. Asterisk is
a plain SIP server, not a P-CSCF, so those four prove SIP, not IMS. No shipped
scenario except the self-test passes against nothing. If you run
any other file with no peer configured and it fails, that is the file doing
its job, not a broken install — run `cellrift selftest` to tell the two
apart.

Every file is exercised end to end by a test in `cmd/cellrift/*_e2e_test.go`
against the in-process mocks, so a file whose behaviour drifts from its own
header turns this repository's build red.

## The files

**Exit** is what `cellrift run` returns against a peer that behaves the way
the file expects. A non-zero exit that the file promises is not a failure
of the tool — it is how a scenario shows you a fault, a rejection, or a
deliberately destroyed connection in a report.

| File | Kind | Protocols | Exit | Shows |
| --- | --- | --- | --- | --- |
| `selftest/loopback.yaml` | Self-test | Diameter/TCP, SIP/UDP | 0 | UAR, CCR, REGISTER, OPTIONS, `assert_state: registered`; a capture flowing between protocols; every assertion operator |
| `ims/basic_call.yaml` | Lab template, **interop proof** | SIP/UDP | 0 | INVITE with an SDP offer → 180 → 200, SDP capture, BYE. Answered by Asterisk in CI on every push ([guide](../docs/getting_started_asterisk.md)) |
| `ims/basic_call_tls.yaml` | Lab template, **interop proof** | SIP/TLS | 0 | The same call over TLS with an explicit `insecure_skip_verify: true` (reported as a run warning). CI runs a derived copy that verifies Asterisk's certificate against a harness-generated CA |
| `ims/register_digest_auth.yaml` | Lab template, **interop proof** | SIP/UDP | 0 | Three REGISTERs, each answering a `401` digest challenge inside the step: MD5 only, SHA-256 only, and both offered (RFC 8760 §2.4 picks SHA-256). Answered by a digest-requiring Asterisk in CI |
| `ims/call_rejected.yaml` | Lab template | SIP/UDP | **1 by design** | INVITE → 486: a non-2xx final fails a SIP step even when its `== 486` assertion holds; then `assert_state: terminated`. Negative testing is an open design item |
| `ims/register_impaired.yaml` | Lab template / showcase, **interop proof** | SIP/UDP | 0 | `impair:` timing — `latency_ms` + `jitter_ms`, a clean control step, then `delay_ms`; asserted via `response.impair_delay_ms`. Answered by Asterisk in CI |
| `ims/register_loss_reorder.yaml` | Lab template / showcase | SIP/UDP | 0 | `drop:` (passes, `response.dropped`), `duplicate:`, an `action: reorder` pair, then a clean REGISTER |
| `ims/register_conn_fault.yaml` | Showcase | SIP/TCP | **1 by design** | `truncate`, `reset`, `abrupt_close` — three faulted steps reporting `response.connection_state`, then a clean REGISTER |
| `ims/register_assert_liveness.yaml` | Showcase (mock-only) | Diameter/TCP, SIP/TCP | **1 by design** | The assert-liveness-after-fault pattern on both protocols: a `reset` step, then an ordinary step that proves the peer still answers |
| `ims/register_basic.yaml` | Showcase (mock-only) | Diameter/SCTP, SIP/UDP | 0 | The canonical IMS attach: UAR → REGISTER carrying the UAA's `Server-Name` → `assert_state: registered` |
| `ims/register_deregister.yaml` | Showcase (mock-only) | Diameter/SCTP, SIP/UDP | 0 | The same attach, then a REGISTER with `Expires: 0` |
| `ims/register_parallel_wait.yaml` | Showcase (mock-only) | Diameter/TCP, SIP/UDP | 0 | `action: parallel` (cross-protocol — the only shape allowed), `action: wait`, then `assert_state` |
| `ims/malformed_sip_headers.yaml` | Showcase (mock-only, **no opt-out**) | SIP/TCP | **1 by design** | `malform: sip_header` missing/duplicate/overwrite, and both `action: raw` byte sources. Deliberately adversarial traffic — never aim it at a peer you do not own |
| `diameter/uar_uaa.yaml` | **Interop proof** | Diameter/SCTP (TCP in CI) | 0 | Cx UAR/UAA twice, answered `Experimental-Result-Code 2001 DIAMETER_FIRST_REGISTRATION` by a real `gradiant/open5gs:2.8.0` HSS on every push ([evidence](../test/interop/EVIDENCE.md)) |
| `diameter/credit_control.yaml` | **Interop proof** | Diameter/SCTP (TCP in CI) | 0 | RFC 4006 CCR-I then CCR-U on one `Session-Id`, answered `Result-Code 2001` by a real SigScale OCS on every push |
| `diameter/s6a_air_ulr.yaml` | **Interop proof** | Diameter/SCTP (TCP and SCTP in CI) | 0 | S6a AIR/AIA then ULR/ULA, with a nested Grouped AVP (`Terminal-Information`), both answered `Result-Code 2001` by the same Open5GS HSS on every push |
| `diameter/cx_mar_sar.yaml` | **Interop proof** | Diameter/SCTP (TCP and SCTP in CI) | 0 | Cx MAR/MAA then SAR/SAA, with a nested Grouped AVP (`SIP-Auth-Data-Item`), answered `2001` by the same Open5GS HSS on every push. Runs after `uar_uaa.yaml`, which registers the identity pair it names |
| `diameter/sctp_abort.yaml` | Showcase (mock-only) | Diameter/SCTP | **1 by design** | A real RFC 4960 ABORT after a UAR, then a liveness UAR over a fresh association and handshake. Needs kernel SCTP |
| `diameter/malformed_diameter.yaml` | Showcase (mock-only, **no opt-out**) | Diameter/TCP | **1 by design** | `malform: avp_length` / `msg_length` overrides and a raw cross-protocol injection. Deliberately adversarial — never aim it at a peer you do not own |

## What "mock-only" means

A `⚠️ MOCK-ONLY SCENARIO` header means the file's application-level exchange
has been proven only against Cellrift's own in-repo mock peers — which share
the client's encoder and therefore cannot prove interoperability with
anything.

Seven files carry it, and each one says in its own header *why*:

- **`sctp_abort.yaml`** — real SCTP now is proven against the interop peer,
  but a deliberate RFC 4960 ABORT is not: no real HSS has been asked to
  survive one, and the assertions here read Cellrift's own view of the
  association it tore down.
- **`register_basic.yaml`, `register_deregister.yaml`,
  `register_parallel_wait.yaml`, `register_assert_liveness.yaml`** — their
  Diameter half is the proven UAR, but no real P-CSCF has ever answered the
  SIP half. Cellrift answers a digest challenge, but an IMS P-CSCF normally
  challenges with IMS-AKA (`AKAv1-MD5`, RFC 3310), which it does not
  implement yet.
- **`malformed_*.yaml`** — deliberately adversarial; the marker has no
  opt-out and never will.

`scripts/check-scenario-labels.sh` enforces all of this with four guards:
files that must carry the marker; files declaring `malform:`/`action: raw`,
which must carry it with no exception; the four Diameter interop proofs,
which must *not* carry it because CI proves the opposite on every push; and
the four Asterisk-proven SIP files, which must carry a `PROVEN AGAINST`
header and be in the SIP harness's scenario list.

## Conventions every file follows

- **Synthetic identifiers only.** MSISDNs come from the Ofcom test range
  `447700900xxx`; domains are `.lab.local`, `.cellrift.local`, `.invalid` or
  the 3GPP example realm. Never commit a real subscriber identity.
- **Targets are variables.** A mixed-protocol file declares `hss_target` and
  `pcscf_target` as `host:port` so each protocol can be pointed at its own
  peer with `--var`; a single-protocol file can also take `--target-host`.
- **A file that fails by design says so** in its header, in this table and
  in the README's scenario table, with the exit code and the failing steps
  named. If you find one that does not, that is a bug — please report it.
- **Every step's assertions are falsifiable.** No assertion is present only
  for decoration; each one can fail against a peer that misbehaves.

## Writing your own

Start from `selftest/loopback.yaml` (the shortest complete tour) or the
lab template closest to your flow. The schema is
[`docs/scenario.schema.json`](../docs/scenario.schema.json); `cellrift
validate` applies the same rules and is strict about unknown keys. The
README's "How a step passes or fails" section is the one thing to read
before writing an `assert:` block.
