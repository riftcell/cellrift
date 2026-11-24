# Cellrift

[![CI](https://github.com/riftcell/cellrift/actions/workflows/ci.yml/badge.svg)](https://github.com/riftcell/cellrift/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-blue.svg)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**CI/CD-native Telecom protocol testing framework.**

Cellrift lets Telecom engineers describe SIP and Diameter test scenarios in a single YAML file and run them from a terminal or a CI pipeline — no GUI, no physical lab, no licence fee. SIP, including digest authentication, is answered by a real Asterisk in CI on every push. Diameter is experimental, but all six of its command pairs are answered by real peers in CI: an Open5GS HSS and a SigScale OCS ([details below](#status)). GTP and 5G SBI are planned.

> "Find where cellular systems crack under pressure."

```yaml
# A scenario is a file like this. Run it with: cellrift run basic_call.yaml
name: VoLTE basic call
version: "1.0"
protocols:
  sip: { transport: udp }
variables:
  pcscf_host: "pcscf.lab.local"
steps:
  - id: send_invite
    protocol: sip
    action: send
    target: "{{ pcscf_host }}:5060"
    message: INVITE
    body: { content_type: application/sdp, template: basic_audio_offer }
    capture:
      negotiated_codec: response.body.sdp.codec
    assert:
      - response.status_code == 200
      - negotiated_codec != null
```

`cellrift selftest` runs a scenario end to end against peers built into the
binary, so you can see a green run before you have any lab at all.

**Two things to read before you trust any of this:**
[`test/interop/EVIDENCE.md`](test/interop/EVIDENCE.md) — fourteen runs against real
third-party peers, recording what each one *rejected* and why — and
[Status](#status) below, which says plainly what is not proven.

---

## Why Cellrift?

**A signalling regression is a code change. It should fail in the pull request that caused it.**

In most telecom teams it doesn't. A registration flow breaks, CI stays green because CI only runs unit tests, and the failure surfaces days later — in a lab, on a schedule, found by someone who has to work backwards to the commit. The feedback loop that takes minutes for application code takes days for the protocol layer, and the cost of that gap is paid every release.

Cellrift closes it by making protocol tests behave like every other test in the repository:

- **Scenarios are code.** A test is a YAML file: readable in a diff, reviewable in a pull request, versioned with the implementation it covers. When someone changes a registration flow, the reviewer sees the scenario change next to it.
- **It runs where your tests already run.** One static binary, no runtime dependencies, no GUI, no lab allocation, no licence server. Install it on a CI runner with `go install`, or build the Docker image.
- **It reports in the format your CI already reads.** JUnit XML lands in the GitHub Actions / GitLab / Jenkins test UI as ordinary test results, so a SIP failure looks like — and is triaged like — a failing unit test.
- **State flows across protocols.** Capture a value in one step and use it in the next, so a scenario can follow a subscriber through a realistic sequence instead of testing each message in isolation.
- **It runs on a developer laptop.** The same scenario a pipeline runs, you can run locally against a local Asterisk before you push.

### What Cellrift is not

Being clear about this saves you evaluation time:

- **Not a load generator.** It verifies protocol correctness on a small number of sessions. It does not measure capacity, and it will not tell you how a node behaves at 10,000 registrations per second.
- **Not a conformance suite.** It does not certify a node against 3GPP test specifications, and passing Cellrift is not evidence of standards compliance.
- **Not a full protocol stack.** SIP is client-side (UAC) only, with digest authentication but no IMS-AKA and no RFC 3261 §17 retransmission timers yet. Diameter is experimental: the handshake and all six commands interoperate with real peers in CI, but that is request/answer shape rather than a full application — quota management, credit re-authorisation and RAR are unexercised — see [Status](#status) below. Everything else is on the roadmap, not in the binary.

If you need carrier-grade validation, capacity benchmarking, or formal conformance sign-off, you need a dedicated commercial suite and probably a lab. Cellrift is for the regression you want caught before the merge.

---

## Status

| Component | Status | What that rests on |
| --- | --- | --- |
| SIP | **Experimental — client only; answered by a real Asterisk in CI** | REGISTER / INVITE / BYE / OPTIONS as a client (UAC) only, over UDP, TCP and TLS. The `sip-interop` job runs four scenarios against Asterisk 22.10.1 on every push: a call over UDP, the same call over TLS with Asterisk's certificate verified against a harness-generated CA, timing-impaired REGISTERs, and REGISTERs that answer a `401` digest challenge with MD5 and with SHA-256 (`test/interop/EVIDENCE.md`, runs 12–14). Asterisk checks the digest arithmetic but tolerates malformed `Authorization` headers, so the header grammar is pinned by in-process tests instead; `407 Proxy-Authenticate` is proven in-process only. Asterisk is not a P-CSCF, and Cellrift has no IMS-AKA, so a real IMS core is still unproven. |
| SDP | Experimental | Offer/answer: the SDP answer from Asterisk's `200 OK` is parsed and its codec asserted in CI (`basic_call.yaml`). One peer, one audio offer |
| Diameter | **Interoperates with two real peers; six command pairs** | RFC 6733 §5.3 CER/CEA (and, per manual verification, §5.5 DWR/DWA) interoperate with a real `gradiant/open5gs:2.8.0` HSS in CI, and so does the Cx UAR/UAA exchange: the `diameter-interop` job drives `scenarios/diameter/uar_uaa.yaml` against that peer on every push and requires `Experimental-Result-Code 2001`, then drives S6a's AIR/ULR and Cx's MAR/SAR against the same peer (`test/interop/EVIDENCE.md`, runs 1–6). CCR/CCA is proven against a second peer, a SigScale OCS in its own job, over TCP (`credit-control-interop`). Six proven commands is not an interoperable stack: quota management, credit re-authorisation and RAR are unexercised. Real SCTP is used where the kernel provides it; when it isn't, the run falls back to TCP and reports the substitution in `RunResult.Warnings`. The interop job runs both transports against the peer, and its SCTP leg fails rather than passing if that substitution happens, so an SCTP result is never a disguised TCP one. |
| Python SDK | **Experimental** | An 83-line `subprocess` wrapper over the CLI exposing `run()` and `validate()` — see [the Python guide](docs/python_getting_started.md) |

> **⚠️ Read this before you evaluate Cellrift.**
>
> **Both protocols meet real third-party peers in CI on every push. Neither
> has met an IMS core.** That distinction is why the table above reads the
> way it does.
>
> **What Diameter has:** six command pairs — CER/CEA, DWR/DWA, Cx UAR/UAA,
> S6a AIR/ULR, Cx MAR/SAR, RFC 4006 CCR/CCA — exchanged with real peers by a
> CI job on every push. Five against a `gradiant/open5gs:2.8.0` HSS over
> **both TCP and a real RFC 4960 SCTP association**; Credit-Control against a
> SigScale OCS over TCP. The bytes, and what each peer rejected on the way,
> are in [`test/interop/EVIDENCE.md`](test/interop/EVIDENCE.md).
>
> **What that is not:** an interoperable Diameter stack. Six command pairs is
> not all of Diameter. Each interface has been tried against exactly one
> implementation, both open-source, neither a production node. RAR/RAA is
> unimplemented, there is no outbound watchdog timer, quota management and
> credit re-authorisation are unexercised, and there is no peer failover or
> `Origin-State-Id` restart detection (RFC 6733 §5.6).
>
> **What SIP has:** a real Asterisk in CI answering calls over UDP and
> verified TLS, and REGISTERs authenticated with an RFC 7616 digest (MD5 or
> SHA-256, `qop=auth`). The digest is answered transparently inside the
> challenged step, from a scenario password, a credentials file or
> `CELLRIFT_SIP_PASSWORD`, and the password is masked in every report.
>
> **What SIP is not:** an IMS client. There is no IMS-AKA (`AKAv1-MD5`, RFC
> 3310), so a P-CSCF that challenges with AKA rejects the REGISTER. Asterisk
> is a plain SIP server, not a P-CSCF. SIP is client-side only, does not
> retransmit over UDP (no RFC 3261 §17 timers), and sends no ACK for a `2xx`
> to an INVITE. **If you came here for IMS/VoLTE registration against real
> infrastructure, Cellrift cannot do it today.** Every mixed SIP+Diameter
> scenario is mock-only and says so in its own header.
>
> Full detail in [the roadmap](docs/roadmap.md#diameter-what-is-missing) and
> [`docs/todo_next.md`](docs/todo_next.md).

---

## Features (MVP)

- **SIP** — client-side (UAC) only: REGISTER / INVITE / BYE / OPTIONS, UDP + TCP + TLS, with RFC 7616 digest authentication answering `401` and `407` challenges (MD5, SHA-256). No server mode and no IMS-AKA — see [Status](#status)
- **SDP** — offer/answer in INVITE and 200 OK bodies, codec capture, remote port extraction
- **Diameter (experimental)** — AVP codec and six sendable commands (UAR/UAA, CCR/CCA, AIR/AIA, ULR/ULA, MAR/MAA, SAR/SAA) resolved through a table-driven command registry, over TCP or real SCTP. A Diameter Grouped AVP (e.g. Cx's `SIP-Auth-Data-Item`) is authored as a nested YAML map under a step's `fields:` and encoded with the V bit and its own Vendor-Id. All six command pairs interoperate with a real peer in CI — five against an Open5GS HSS, Credit-Control against a SigScale OCS — see [Status](#status)
- **Multi-protocol shared state** — capture a Diameter AVP and use it in a SIP header in the next step
- **YAML scenarios** — human-readable, version-controllable, reviewable in pull requests
- **JUnit XML output** — native for GitHub Actions, Jenkins, GitLab CI, TeamCity
- **Single static binary** — no runtime dependencies. Built from source today (`make build` or `go install`); no tagged release or published image exists yet, so there is nothing to `curl`
- **Docker image** — buildable from the included Dockerfile (Alpine, under 50 MB); not published to any registry yet

---

## Installation

**Requires Go 1.26.6 or newer.** The version is pinned deliberately: Go
1.26.0–1.26.5 carry advisories in `crypto/tls` and `crypto/x509` that
`govulncheck` reports as reachable from Cellrift's TLS transport. With a
default Go setup the toolchain is fetched automatically; if you run with
`GOTOOLCHAIN=local` on an older Go you will see
`go.mod requires go >= 1.26.6` — upgrade rather than lowering the pin.

### With `go install`

```sh
go install github.com/riftcell/cellrift/cmd/cellrift@latest
```

The example scenarios are embedded in the binary. `cellrift list` shows
them wherever you are, and `cellrift init` writes them to `./scenarios` so
the Quick Start paths below work without a clone.

### Build from source

```sh
git clone https://github.com/riftcell/cellrift.git
cd cellrift
make build
./cellrift --version
```

### Docker

No image is published to a registry yet. Build it from the included Dockerfile:

```sh
docker build -t cellrift .
docker run --rm cellrift --version
```

---

## Quick Start in 60 Seconds

**1. See a scenario pass, with nothing else running:**

```sh
cellrift selftest
```

This starts a mock SIP UAS and a mock Diameter HSS on loopback ports inside
the process, runs the bundled `scenarios/selftest/loopback.yaml` against
them, and prints the ordinary run report — `5/5 steps passed`, exit 0. It
is the one run in this repository that is *supposed* to go green against
nothing, and it is how you tell a working install from a broken one. It
proves nothing about your network; the peers are Cellrift's own mocks.

**2. Unpack the examples and validate one:**

```sh
cellrift init                    # writes the bundled library to ./scenarios (skip in a clone)
cellrift validate scenarios/ims/basic_call.yaml
```

**3. Run it (against a target):**

`ims.lab.local:5060` below is illustrative — swap it for a real SIP endpoint. The fastest way to see a scenario actually pass is against a local Asterisk instance; see the [Asterisk walkthrough](docs/getting_started_asterisk.md) for a copy-pasteable setup (`--target-host 127.0.0.1:5060` once it's running).

```sh
cellrift run --target-host ims.lab.local:5060 \
             scenarios/ims/basic_call.yaml
```

**4. Generate JUnit XML for CI:**

```sh
cellrift run --format junit --output results.xml \
             scenarios/ims/basic_call.yaml scenarios/ims/register_impaired.yaml
```

**5. List built-in scenarios:**

```sh
cellrift list
```

> **Synthetic test data:** The built-in scenarios use phone numbers from the Ofcom test range (`447700900xxx`) and `.lab.local` domains — identifiers reserved for fictional use that are never assigned to real subscribers. When writing your own scenarios, use synthetic identifiers and never commit real subscriber MSISDNs, IMSIs, or SIP identities to source control.

### Example Scenario

A full VoLTE call — INVITE with an SDP offer, then BYE. This one runs against a
real SIP target today; see the [Asterisk walkthrough](docs/getting_started_asterisk.md).

```yaml
# scenarios/ims/basic_call.yaml
name: VoLTE basic call
version: "1.0"
protocols:
  sip:
    transport: udp
    local_port: 5060
variables:
  caller_msisdn: "447700900001"
  callee_msisdn: "447700900002"
  pcscf_host: "pcscf.lab.local"
  icid: "cellrift-call-001"
steps:
  # INVITE with an SDP offer; capture the negotiated codec and remote RTP port.
  - id: send_invite
    protocol: sip
    action: send
    target: "{{ pcscf_host }}:5060"
    message: INVITE
    fields:
      From: "sip:{{ caller_msisdn }}@ims.local"
      To: "sip:{{ callee_msisdn }}@ims.local"
      P-Access-Network-Info: "3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=234010001000001"
      P-Charging-Vector: "icid-value={{ icid }}"
    body:
      content_type: application/sdp
      template: basic_audio_offer
    capture:
      remote_rtp_port: response.body.sdp.port
      negotiated_codec: response.body.sdp.codec
    assert:
      - response.status_code == 200
      - negotiated_codec != null

  # Tear the call down.
  - id: send_bye
    protocol: sip
    action: send
    target: "{{ pcscf_host }}:5060"
    message: BYE
    fields:
      From: "sip:{{ caller_msisdn }}@ims.local"
      To: "sip:{{ callee_msisdn }}@ims.local"
      P-Charging-Vector: "icid-value={{ icid }}"
    assert:
      - response.status_code == 200
```

### How a step passes or fails

A step's verdict has two layers, and the rules are the same on both
protocols:

1. **The protocol decides first.** A SIP step passes only on a 2xx final
   response; a Diameter step only on `Result-Code 2001`. Any other final
   answer — a 4xx, a 5xx, a redirect, a `5005 DIAMETER_MISSING_AVP` —
   fails the step with the code in its error, **even with no `assert:`
   block at all.** A peer that never answers within the step's
   `timeout_ms` (or the run's `--timeout`) fails with
   `response.timed_out == true`.
2. **Then your assertions run — on every step that declares them, passed
   or failed.** A failing assertion fails the step; on a step the protocol
   already failed, the assertion's failure is appended to the protocol
   error rather than replacing it, so the root cause is never lost. A
   passing assertion does **not** turn a failed step green: the protocol's
   verdict stands.

Three rules about writing assertions that will save you a confusing
afternoon:

- **An operand that does not exist is an error, not a pass.**
  `response.missing_field != 503` is rejected with "not present", rather
  than vacuously passing because nothing compares unequal to `503`. This
  also catches a typo'd namespace (`responce.status_code`) and a `capture:`
  that found nothing. To assert that a field is genuinely absent, write
  `== null`; `!= null` asserts it is present.
- **Quotes on the right-hand side are optional and equivalent.**
  `== "reset"` and `== reset` compare against the same five characters.
  (In YAML a plain scalar's quotes are literal, so without this rule the
  quoted form could never match.)
- **A step that is expected to fail cannot be made to pass.** Asserting
  `response.status_code == 403` on a step the peer answers 403 will pass
  the assertion, but the step is still failed by rule 1, and so is the run.
  Cellrift does not yet have a way to declare "this step is *supposed* to
  get an error" — if you need that, it is an open design item, not a
  configuration you have missed.

Any `send` step can also declare an `impair:` block to inject network-level
timing delay, message loss, duplication, or a connection-level fault —
useful for testing how a system under test behaves against a slow, jittery,
unreliable, or actively hostile peer:

```yaml
impair:
  latency_ms: 300      # fixed delay before send, and before the first inbound read
  jitter_ms: 50        # extra random delay in [0, jitter_ms], sampled per direction
  delay_ms: 0          # pause AFTER the response is parsed — exercises the peer's timeouts
  drop: false          # build the message but never put it on the wire; the step passes
  duplicate: false     # write the same bytes twice; the step reports response.duplicated
  reset: false         # after Send, hard-reset the connection (SO_LINGER 0 then close — a
                        # real TCP RST); response.connection_state == "reset"
  truncate: false      # write only the first few bytes of the already-encoded message —
                        # too few to frame or parse; response.connection_state == "truncated"
  abrupt_close: false  # after Send, close the connection with no clean shutdown (a plain
                        # FIN, not an RST); response.connection_state == "closed"
  sctp_abort: false    # after Send, write a real RFC 4960 ABORT chunk on the step's SCTP
                        # association; response.connection_state == "aborted"
```

At most ONE of `drop`, `duplicate`, `reset`, `truncate`, `abrupt_close`, and
`sctp_abort` may be declared on one step — `cellrift validate` rejects a step
declaring two of them. `impair:` itself is valid on `action: send` only;
`cellrift validate` rejects it anywhere else rather than silently ignoring
it.

`reset` and `abrupt_close` act on a connection, so they require a
connection-oriented transport: declaring either on a SIP step whose
transport is (or defaults to) UDP fails the step at send time with a
message saying so. `truncate` has no such requirement and works over every
transport including UDP, since a short datagram needs no connection
lifecycle at all — the datagram's own length is its frame boundary.
`sctp_abort` is narrower still: `cellrift validate` rejects it outright on
any step that will not run over SCTP, since a real RFC 4960 ABORT chunk has
no meaning on a TCP, TLS, or UDP socket.

A faulted step reports `Status: failed`, unlike a dropped step, which
passes — a dropped step's impairment did exactly what was declared and no
response was ever expected, while a faulted step genuinely sent a message
and then destroyed its own means of hearing the answer, so there is a real
absence of result to report.

**Asserting liveness after a fault.** Every `send` step also reports
`response.timed_out` — `true` when the peer never answered within that
step's own `timeout_ms`, `false` otherwise — and it is always present, on
both a passing and a faulted step, so `assert: - response.timed_out ==
false` works as a positive liveness check. `response.timed_out` and
`response.connection_state` are mutually exclusive by construction:
`connection_state` says HOW a connection died, `timed_out` says the peer
simply stayed silent. A faulted step reports `connection_state` and
`timed_out == false`, never both a connection state and `timed_out == true`.

This pair of keys is what makes the assert-liveness-after-fault pattern
expressible: a `reset` (or `truncate`/`abrupt_close`) step followed by an
ordinary, unimpaired step that asserts the peer is still answering:

```yaml
- id: diameter_reset
  protocol: diameter
  action: send
  target: "{{ hss_target }}"
  message: UAR
  impair:
    reset: true
  assert:
    - response.connection_state == "reset"

- id: diameter_liveness
  protocol: diameter
  action: send
  target: "{{ hss_target }}"
  message: UAR
  assert:
    - response.timed_out == false
```

`scenarios/ims/register_assert_liveness.yaml` is the runnable worked example
of this pattern, covering both protocols. Diameter's follow-up step
genuinely exercises reconnection — a fresh RFC 6733 §5.3 CER/CEA, since the
`reset` above tore the previous association down. SIP's follow-up step
proves only that a fresh dial succeeds, since this client dials per send and
holds no connection across steps for the earlier `reset` to have damaged.

**What this pattern actually proves, and what it does not.** A passing
liveness step proves the peer is answering on the wire: it accepted a new
connection, completed the protocol's own handshake where one exists, and
produced a well-formed answer. It does NOT prove the peer's host is
healthy, that no process crashed and restarted underneath, or that any
internal subsystem survived. Cellrift is a client with no privileged access
to the system under test — wire-visible liveness is the honest answer it
can give.

**SIP's connection-fault scope is narrower than Diameter's.** This client
dials a fresh connection on every send, so a SIP step's `reset`/`truncate`/
`abrupt_close` is scoped to that step's own dial-send-receive cycle, not a
mid-session fault on a connection held across steps. Diameter's connection
faults ARE genuinely mid-session, acting on the one association the
handshake established and later steps reuse. Do not read the two as
equivalent.

### Flow control: `parallel`, `wait`, `reorder`

Steps run in declaration order. Three actions change that:

```yaml
- id: attach
  action: parallel          # sub-steps run at the same moment, one goroutine each
  steps:
    - id: diameter_uar
      protocol: diameter
      action: send
      # ... an ordinary send step, with its own capture:/assert:
    - id: sip_register
      protocol: sip
      action: send
      # ...

- id: settle
  action: wait              # no protocol; the engine sleeps and reports passed
  duration_ms: 500
```

A `parallel` group's sub-steps must use **distinct protocols** — two SIP
sub-steps would share one adapter and one connection with no locking, so
`cellrift validate` rejects that shape until each sub-step gets its own
adapter. A value captured by a sub-step is visible to every step after the
group, not to the group's other sub-step. `wait` is for the gap a real
network needs between two events (a registration timer, a cache refresh)
where firing the next step immediately would measure Cellrift's speed, not
the peer's behaviour. See `scenarios/ims/register_parallel_wait.yaml`.

Two or more `send` steps can also be grouped under `action: reorder` to
control the literal wire order they are sent in, independent of the order
they are declared in:

```yaml
- id: two_registers_out_of_order
  action: reorder
  timeout_ms: 3000        # optional; governs the whole group's wait
  steps:
    - id: register_b
      protocol: sip
      action: send
      send_order: 1       # written to the wire FIRST, despite being listed second
      # ... target/message/fields/assert as an ordinary send step
    - id: register_a
      protocol: sip
      action: send
      send_order: 2
      # ...
```

Every sub-step's message is built before any of them is written, the writes
happen in `send_order` from one goroutine, and results come back in
declaration order. SIP has no request/response correlation anywhere in this
codebase, so its reorder support is POSITIONAL only — trusted against
Cellrift's own synchronous mocks, not equivalent to Diameter's Hop-by-Hop-ID
correlation. See the CHANGELOG for the full disclosure.

See `scenarios/ims/register_impaired.yaml` (latency/jitter/delay_ms),
`scenarios/ims/register_loss_reorder.yaml` (drop/duplicate/reorder),
`scenarios/ims/register_conn_fault.yaml` (truncate/reset/abrupt_close — this
one deliberately fails three of its four steps by design; see the file's own
header) and `scenarios/diameter/sctp_abort.yaml` (a real RFC 4960 ABORT on
a Diameter SCTP association, then a liveness UAR; fails one of two steps by
design) for full worked examples.

### Deliberate message corruption and cross-protocol injection

Any `send` step can also declare a `malform:` block, structurally parallel to
`impair:`, to corrupt one specific already-correctly-encoded field:

```yaml
# Diameter: override one AVP's own 3-byte Length field.
malform:
  avp_length:
    name: Session-Id
    value: 9999
```

```yaml
# Diameter: override the message header's own 3-byte Message Length field.
malform:
  msg_length: 9999
```

```yaml
# SIP: drop, duplicate, or overwrite a named header.
malform:
  sip_header:
    name: Via
    op: overwrite    # missing | duplicate | overwrite
    value: "SIP/2.0/TCP not-a-real-via"    # required for overwrite; optional for duplicate
```

`avp_length`/`msg_length` apply to Diameter steps only; `sip_header` applies
to SIP steps only — `cellrift validate` rejects the wrong protocol's key on a
step. In every case the corruption runs AFTER the real encoder (`BuildRequest`
for Diameter, `Serialise` for SIP) has already produced a fully correct
message — it is never a second, parallel encoder — so every byte except the
one field named above is exactly what a correct message would carry.
`duplicate`'s second header line is invisible to a re-parse (`ParseMessage`
coalesces two same-named headers into one comma-joined value), so its effect
is only observable in the raw wire bytes, never in a parsed response.
`malform:` is valid on `action: send` only.

A step can also declare `action: raw` to write literal bytes directly onto
its own connection, bypassing the protocol encoder, the request builder, and
response parsing entirely — this is how one protocol's wire bytes get sent
over the other protocol's connection:

```yaml
# An inline literal, decoded once and sent verbatim.
- id: inject_diameter_over_sip
  protocol: sip
  action: raw
  target: "{{ pcscf_host }}:5060"
  raw:
    hex: "0100001480000101000000001111111122222222"

# A prior step's own captured response.__raw, replayed byte-for-byte.
- id: replay_captured_wire
  protocol: sip
  action: raw
  target: "{{ pcscf_host }}:5060"
  raw:
    ref: "{{ register_clean.clean_wire }}"
```

`raw:` takes exactly one of `hex` (a literal hex string) or `ref` (a
`{{ }}` reference to an earlier step's captured `response.__raw` bytes) —
both are resolved through the same template machinery every other field
uses, so a captured `[]byte` survives the round trip with no byte lost and
no UTF-8 validation. A raw step reports `response.raw_bytes_sent`, the
number of bytes actually written; it never reads or parses whatever the
peer sends back, so `response.__raw`/parsed response fields are never
populated for a raw step's own receive side. It reuses its protocol's
already-open connection when one exists — for Diameter, handshaking first if
no earlier step already did.

**MOCK-ONLY, with no opt-out — but understand what that is.** Any scenario
file *in this repository* declaring `malform:` or `action: raw` must carry the
MOCK-ONLY marker, enforced by `scripts/check-scenario-labels.sh`, which scans
the whole scenario corpus and fails the build on any match missing it. A file
added after this feature shipped is caught the same way a long-standing one
is.
**It is a lint over this repo's own scenarios, not a runtime safety control.**
It does not inspect scenarios you write, and the binary will not stop you
pointing a malformed or raw step at any address you name. Nothing in Cellrift
can tell whether a target is yours. The marker is a convention this project
holds itself to and a signal to you — the actual control is your judgement
about what you are allowed to send traffic at. Malformed and cross-protocol payloads are deliberately
adversarial traffic, a different risk class from a delayed or dropped
packet; sending either at a real, unconsented peer is out of scope for this
tool. No real peer — no real P-CSCF, no real HSS — has ever been sent
malformed or cross-protocol traffic by this project; every proof here is
against Cellrift's own in-process mocks.

See `scenarios/diameter/malformed_diameter.yaml` (AVP-length override,
Message-Length override, and a raw SIP-over-Diameter injection) and
`scenarios/ims/malformed_sip_headers.yaml` (all three `sip_header` ops, plus
both `action: raw` byte sources) for full worked examples — both
deliberately fail some of their own steps by design; see each file's own
header.

---

## GitHub Actions

```yaml
# .github/workflows/telco-tests.yml
name: Protocol regression tests
# Manual, not on: [push]. This dials real infrastructure, so a human decides
# when — and a lab that answers every push is a lab nobody trusts.
on: workflow_dispatch
jobs:
  cellrift:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: ./.github/actions/run-scenarios
        with:
          # Name the scenarios you mean. A glob or a directory works too,
          # but many shipped scenarios must not be aimed at a real target:
          # seven are MOCK-ONLY (some send deliberately malformed traffic)
          # and the action refuses those unless you pass
          # allow-mock-only: true. See scenarios/README.md.
          scenarios: scenarios/ims/basic_call.yaml scenarios/ims/register_impaired.yaml
          target-host: ${{ secrets.TELCO_LAB_HOST }}
          format: junit
          output: results.xml
```

The action builds `cellrift` from the checked-out source, so it needs
`actions/checkout` first. Copying it into your own repository means copying
`.github/actions/run-scenarios/` with it and replacing its build step with a
download of a released binary. The full example, including artifact upload
and test-report publishing, is
[`.github/workflows/telco-tests.yml`](.github/workflows/telco-tests.yml).

---

## Built-in Scenarios

Eight files here are proven against real third-party software, and CI
re-proves each on every push. Four are Diameter: `uar_uaa.yaml`,
`s6a_air_ulr.yaml` and `cx_mar_sar.yaml` against an Open5GS HSS over both
TCP and real SCTP, and `credit_control.yaml` against a SigScale OCS. Four are
SIP, against Asterisk: `basic_call.yaml`, `basic_call_tls.yaml`,
`register_impaired.yaml` and `register_digest_auth.yaml`. What that leaves
unproven — and how hard each gap is to close — is tracked in
[`docs/todo_next.md`](docs/todo_next.md). Everything else runs against
Cellrift's own mocks.

Every shipped scenario is one of three kinds — a **self-test**, a **lab
template** to copy and point at your own endpoint, or a **feature showcase**
proven against Cellrift's mocks only — and several of the showcases **fail
by design** to show you what a fault or a rejection looks like in a report.
[`scenarios/README.md`](scenarios/README.md) states which is which, file by
file, with the exit code each one promises; the table below is the short
form.

Scenarios marked **mock-only** are proven against the in-repo mock peers
only, and each says in its own header why it is still one:

- **the mixed SIP+Diameter `register_*` files** — their Diameter half is
  proven, but no real P-CSCF has answered the SIP half, and an IMS P-CSCF
  normally challenges with IMS-AKA, which Cellrift does not implement yet.
- **`sctp_abort.yaml`** — real SCTP is proven, but no real peer has been
  asked to survive a deliberate RFC 4960 ABORT.
- **`malformed_*.yaml`** — deliberately adversarial; never aim one at a peer
  you do not own.

See [Status](#status), and [`docs/todo_next.md`](docs/todo_next.md) for what
would close each.

| Scenario | Protocols | Runs against a real target? | Description |
| --- | --- | --- | --- |
| `scenarios/selftest/loopback.yaml` | SIP, Diameter | **Self-test** — run it with `cellrift selftest`, which supplies the peers | UAR/UAA, CCR/CCA, REGISTER, OPTIONS and an `assert_state: registered`, against the built-in mocks; the only scenario that passes out of the box |
| `scenarios/ims/basic_call.yaml` | SIP | **Yes — proven in CI against Asterisk** | INVITE → 180 → 200 OK → BYE |
| `scenarios/ims/basic_call_tls.yaml` | SIP/TLS | **Yes — proven in CI against Asterisk, certificate verified** | Same flow over TLS (port 5061) |
| `scenarios/ims/register_digest_auth.yaml` | SIP | **Yes — proven in CI against Asterisk** | Three REGISTERs answering a `401` digest challenge: MD5 only, SHA-256 only, and both offered (SHA-256 chosen, RFC 8760 §2.4) |
| `scenarios/ims/call_rejected.yaml` | SIP | Yes | INVITE → 486 Busy Here, then `assert_state: terminated` — **deliberately fails 1 of 2 steps: a non-2xx final fails a SIP step even when its `== 486` assertion holds; `cellrift run` exits non-zero on this file by design** |
| `scenarios/ims/register_basic.yaml` | SIP, Diameter | **Mock-only** | IMS registration with Diameter UAR/UAA |
| `scenarios/ims/register_deregister.yaml` | SIP, Diameter | **Mock-only** | Registration followed by de-registration |
| `scenarios/diameter/uar_uaa.yaml` | Diameter | **Yes — proven in CI against a real Open5GS HSS** | Standalone Cx UAR/UAA exchange, answered `Experimental-Result-Code 2001 DIAMETER_FIRST_REGISTRATION` |
| `scenarios/diameter/credit_control.yaml` | Diameter | **Yes — proven in CI against a real SigScale OCS** | CCR-I then CCR-U on one `Session-Id`, answered `Result-Code 2001` |
| `scenarios/diameter/s6a_air_ulr.yaml` | Diameter | **Yes — proven in CI against a real Open5GS HSS** | S6a AIR/AIA then ULR/ULA, with a nested `Terminal-Information` Grouped AVP, answered `Result-Code 2001` |
| `scenarios/diameter/cx_mar_sar.yaml` | Diameter | **Yes — proven in CI against a real Open5GS HSS** | Cx MAR/MAA then SAR/SAA, with a nested `SIP-Auth-Data-Item` Grouped AVP, answered `2001` |
| `scenarios/ims/register_impaired.yaml` | SIP | **Yes — proven in CI against Asterisk** | Three REGISTERs; the first injects 300ms latency + 50ms jitter via `impair:`, the second does not, the third holds its received answer for 200ms via `delay_ms` |
| `scenarios/ims/register_loss_reorder.yaml` | SIP | Yes | A dropped REGISTER, a duplicated REGISTER, a reordered pair sent in reverse of their declared order, then a normal REGISTER |
| `scenarios/ims/register_conn_fault.yaml` | SIP | Yes | A truncated REGISTER, a reset REGISTER, an abruptly-closed REGISTER, then a normal REGISTER — **deliberately fails 3 of 4 steps; `cellrift run` exits non-zero on this file by design** |
| `scenarios/diameter/sctp_abort.yaml` | Diameter/SCTP | **Mock-only** | A UAR followed by a real RFC 4960 ABORT on its SCTP association, then a liveness UAR over a fresh association and handshake — **deliberately fails 1 of 2 steps; `cellrift run` exits non-zero on this file by design**; needs kernel SCTP |
| `scenarios/ims/register_parallel_wait.yaml` | SIP, Diameter | **Mock-only** | `action: parallel` with a Diameter UAR and a SIP REGISTER in flight together, `action: wait` for 500ms, then `assert_state: registered` |
| `scenarios/ims/register_assert_liveness.yaml` | SIP, Diameter | **Mock-only** | The assert-liveness-after-fault pattern on both protocols: a reset Diameter UAR then a liveness UAR, a reset SIP REGISTER then a liveness REGISTER — **deliberately fails 2 of 4 steps; `cellrift run` exits non-zero on this file by design** |
| `scenarios/diameter/malformed_diameter.yaml` | Diameter | **Mock-only** (deliberately adversarial traffic, no opt-out) | An AVP-length override, a Message-Length header override, and a raw SIP-over-Diameter injection via `action: raw` — **deliberately fails all 3 steps; `cellrift run` exits non-zero on this file by design** |
| `scenarios/ims/malformed_sip_headers.yaml` | SIP | **Mock-only** (deliberately adversarial traffic, no opt-out) | All three `sip_header` malform ops (missing/duplicate/overwrite) against a REGISTER's Via header, a normal REGISTER capturing `response.__raw`, then both `action: raw` byte sources (inline hex, captured replay) — **deliberately fails 4 of 6 steps; `cellrift run` exits non-zero on this file by design** |

---

## What's next

There is no fixed release schedule. The work in progress is bringing SIP up
to the evidence standard Diameter already meets. Done so far: a real Asterisk
in CI and digest authentication. Next, in order:

1. **SIP transaction timers** — behave as an RFC 3261 §17 client transaction
   over UDP (Timers A/B and E/F, T1/T2 backoff), so a scenario under packet
   loss tests the peer rather than Cellrift's inability to retransmit.
2. **IMS-AKA and a real P-CSCF** — register against a Kamailio IMS core with
   RFC 3310 `AKAv1-MD5`, which would retire the mock-only marker on the mixed
   SIP+Diameter scenarios.
3. **Server mode (UAS)** — let Cellrift be the peer, answering SIP requests
   from a declarative scenario, so someone else's CI can run against it.

On the Diameter side, all six command pairs already have real-peer evidence.
What remains is quota management (no `Requested-Service-Unit` is sent),
RAR/RAA, and an outbound watchdog timer.

After that, the order is genuinely open. Candidates sketched in
[the roadmap](docs/roadmap.md) include GTP-C, 5G SBI, PFCP, SMPP, RADIUS, RTP
media-path checks, and a mock-NF server mode — but none of it is committed,
and the sequence is not settled.

**This is where feedback changes things.** If one of those would unblock your
work — or if the thing you need isn't listed at all — open an issue and say
which protocol you'd point Cellrift at and what you're trying to catch. A
concrete use case moves an item up the list faster than anything else, and it's
far more useful than a star.

---

## Documentation

- [CLI Reference](docs/cli-reference.md)
- [Architecture](docs/architecture.md)
- [Roadmap](docs/roadmap.md)
- [Sample Output](docs/examples/) — terminal, JUnit XML, and JSON examples
- [Changelog](CHANGELOG.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). In short: fork, branch, PR.

## Licence

MIT — see [LICENSE](LICENSE).

See [TERMS.md](TERMS.md) for responsible-use guidance and a Data Processing Agreement template.

Found a security vulnerability? See [SECURITY.md](SECURITY.md) for how to report it privately.
