# Cellrift Roadmap

This document summarises what is implemented today and what is planned for future releases.
Priorities shift based on community feedback — open an issue if a particular protocol or
feature would unblock your work.

---

## Phase 1 — Current (MVP)

SIP, SDP and Diameter are implemented, and each meets real third-party
software in CI on every push. SIP is answered by Asterisk, including digest
authentication; all six Diameter command pairs are answered by an Open5GS HSS
or a SigScale OCS. Diameter remains **experimental**: six command pairs is
not a full application (see "Diameter: what is missing" below). SIP is
client-side only and has no IMS-AKA, so no IMS core has answered it yet.
Read the per-protocol status below rather than assuming Phase 1 means
finished.

### Protocols

| Protocol | RFC / Spec | Status | What you can test |
| --- | --- | --- | --- |
| SIP | RFC 3261, RFC 7616 | **Experimental — client only; answered by a real Asterisk in CI** | REGISTER, INVITE, BYE, OPTIONS with dialog state tracking, client-side (UAC) only. Transport: UDP, TCP, TLS (§26.2.2). Digest authentication answers `401` and `407` challenges (MD5, SHA-256, `qop=auth`). The `sip-interop` job runs four scenarios against Asterisk 22.10.1 on every push, including a TLS call with the certificate verified and digest-authenticated REGISTERs. **No IMS-AKA** and no P-CSCF in CI, so IMS registration is unproven. |
| SDP | RFC 4566 | Experimental | Offer/answer in SIP INVITE and 200 OK bodies — codec capture, remote port extraction. Asterisk's SDP answer is parsed and its codec asserted in CI; one peer, one audio offer |
| Diameter | RFC 6733, RFC 4006 | **Experimental — six command pairs, all answered by real peers in CI** | UAR/UAA, CCR/CCA, AIR/AIA, ULR/ULA, MAR/MAA, and SAR/SAA message construction and parsing over TCP or real SCTP, multi-step shared state with SIP. CER/CEA, UAR, AIR, ULR, MAR and SAR are answered by a real Open5GS HSS over both TCP and SCTP; CCR by a real SigScale OCS over TCP. Quota management, RAR/RAA and an outbound watchdog timer are missing — see below. |

#### Diameter: what is missing

The RFC 6733 §5.3 capabilities exchange (CER/CEA), the Cx
`User-Authorization-Request`, S6a AIR/ULR and Cx MAR/SAR all interoperate
with a real `gradiant/open5gs:2.8.0` HSS, proven by an automated CI job on
every push over both TCP and real SCTP (`.github/workflows/interop.yml`,
evidence in `test/interop/EVIDENCE.md`). The UAR must be answered
`Experimental-Result-Code 2001 DIAMETER_FIRST_REGISTRATION`; AIR and ULR
`Result-Code 2001`. CCR/CCA is answered `2001` by a real SigScale OCS in its
own job. The §5.5 device watchdog (DWR/DWA) was verified by hand against the
same HSS image.

Reaching that took four real-peer runs, each surfacing exactly one more gap,
because a peer reports only the first rule it breaks: a missing request-body
`Vendor-Specific-Application-Id` (260), then no `Session-Id` (263) on any
request at all, then no `Origin-Host`/`Origin-Realm` (264/296) — which RFC
6733 §6.3 and §6.4 require in *every* message — and finally the discovery
that a Cx answer carries no base `Result-Code` whatsoever, reporting through
`Experimental-Result` instead. Cellrift had been reading only `Result-Code`,
finding nothing, and calling such a step **passed**.

What is still missing is breadth, not depth:

- **Quota management is unexercised.** RFC 4006 §3.1 marks
  `Auth-Application-Id` and `Service-Context-Id` mandatory in the CCR, and
  Cellrift sends both, which is what got the SigScale OCS (Apache-2.0,
  Erlang, serving Ro/Gy — Credit-Control Application-Id 4) to answer `2001`.
  But no `Requested-Service-Unit` is sent, so no credit is actually reserved
  or re-authorised.
- **Real SCTP is proven against the same peer.** The interop job runs a
  matrix over both transports: the TCP leg pins
  `CELLRIFT_TRANSPORT_FALLBACK=tcp` for a result that cannot vary with the
  runner's kernel, and the SCTP leg dials a genuine RFC 4960 association —
  `freeDiameter` logs `CONNECTED TO 'test.cellrift.local' (SCTP,soc#8)`. That
  leg fails if Cellrift substitutes TCP, so it can never be a TCP run wearing
  an SCTP label. SCTP itself is Linux-only, by the library's construction —
  see the platform note below; there is no untested macOS or Windows SCTP
  path to prove.
- **Each interface has met exactly one implementation.** Both peers are
  open-source and neither is a production node.
- **No real P-CSCF.** Every scenario mixing SIP with Diameter is still
  mock-only for its SIP half. Asterisk proves SIP and digest authentication,
  but it is not a P-CSCF, and an IMS P-CSCF normally challenges with IMS-AKA,
  which Cellrift does not implement yet.

Two further limits worth knowing before you plan around Diameter:

- An embedded `go:embed` dictionary (`internal/protocols/diameter/dictionary/*.yaml`)
  covers the RFC 6733 base set, RFC 4006 credit-control, the full 3GPP TS
  29.272 S6a set, and the full TS 29.229 Cx set (codes 600–655). An unknown
  AVP field name is now a hard error, both at `cellrift validate` time (with a
  did-you-mean suggestion) and at send time inside `buildAVPsFromFields`,
  rather than the silent drop it used to be.
- Six commands resolve through the table-driven `CommandRegistry` — UAR,
  CCR, AIR, ULR, MAR, and SAR. `RAR`/`RAA` (re-authorization) is the one
  Diameter command left unimplemented.
- **Real SCTP is used where the kernel provides it**, via
  `github.com/ishidawataru/sctp`, detected at runtime rather than by build tags.
  When it isn't available, `NewSCTPOrFallbackTransport` falls back to TCP and the
  substitution is observable: one `WARN: sctp unavailable (...), using tcp` line
  per process, plus an entry in `RunResult.Warnings` that all three reporters
  render. Two narrower facts worth knowing: a dial exceeding
  `CELLRIFT_SCTP_CONNECT_TIMEOUT_MS` returns to the caller on time but leaves its
  dial goroutine parked until the kernel gives up, since the library exposes no
  way to cancel an in-flight connect; and **SCTP does not exist at all off
  Linux**. `ishidawataru/sctp` ships a `sctp_unsupported.go` for every
  non-Linux target (and for `linux/386`) whose every entry point returns
  `ErrUnsupported`, so a macOS or Windows user asking for `transport: sctp`
  always gets TCP plus the `transport: requested sctp, used tcp` warning.
  Those platforms are verified by cross-compilation (`go build` + `go vet`)
  only, which is the right amount of verification for a path that is a stub.

`TestAdapter_FailsAgainstStrictPeer` runs the adapter against a mock HSS that
enforces the handshake and now asserts success (it asserted failure until
CER/CEA landed, then was inverted). What it proves is self-consistency against Cellrift's
own in-repo mock — the handshake is spec-shaped and the mock never tears the
connection down. What it does not prove is real-peer interoperability; that
evidence lives in `test/interop/EVIDENCE.md`, described above.

### Cryptography and export control

SIP/TLS ships in Phase 1. Optional fields on the `sip` protocol block
configure it. Server certificate verification is always on: with no `ca_file`
the system root pool is used, and only an explicit `insecure_skip_verify:
true` turns it off, which adds a warning to the run report (lab use only):

```yaml
protocols:
  sip:
    transport: tls
    cert_file: client.crt
    key_file:  client.key
    ca_file:   ca.crt
```

Cellrift uses Go's standard library `crypto/tls` and ships no compiled
cryptographic binaries beyond what Go's toolchain embeds by default. Public
distribution qualifies under EAR §740.13 (Technology and Software —
Unrestricted) OSS exemption: source is publicly available, and no BIS prior
review or licence exception filing is required.

The same `crypto/tls` configuration layer is intended to carry 5G SBI mTLS,
which 3GPP Release 15+ mandates for every NF-to-NF call (TS 33.501 §13.3).

### Built-in scenario library

[`scenarios/README.md`](../scenarios/README.md) lists every shipped scenario
with its kind (self-test, lab template, feature showcase or interop proof),
the exit code it promises, and whether a real peer answers it in CI. Eight
are proven against real third-party software on every push: four Diameter
files against Open5GS and SigScale, and four SIP files against Asterisk.
Seven carry a **mock-only** marker, each with its reason in its own header.

### CLI

```
cellrift run       — execute scenarios, output terminal / JUnit XML / JSON
cellrift validate  — schema-validate scenario YAML without running
cellrift list      — list the built-in scenario library
cellrift assert    — evaluate assertions against a JSON/YAML input (CI pipeline step)
cellrift replay    — replay a captured scenario from a recorded file (not yet implemented)
```

### Output formats

- Coloured terminal with per-step pass/fail and duration
- JUnit XML — consumed by GitHub Actions, GitLab CI, Jenkins
- Structured JSON — for downstream pipeline steps
- PII masking by default: E.164 numbers in error output replaced with `[REDACTED-N]`
  (stable tokens so the same number maps to the same token across the whole report)

### Network impairment testing

A scenario step can declare an `impair:` block to inject network-level
timing delay, message loss, or duplication, and two or more `send` steps can
be grouped under a new `action: reorder` to control the literal wire order
they are sent in, independent of the order they are declared in. All three
categories work on both SIP and Diameter:

- **Timing** — `latency_ms`/`jitter_ms` (send-side, via a shared transport
  decorator) and `delay_ms` (post-receive, applied by the adapter itself to
  exercise the peer's own timeout/retry behaviour).
- **Loss & duplication** — `impair: { drop: true }` builds a message in full
  and then deliberately never writes it to the wire; `impair: { duplicate:
  true }` writes the same already-built bytes twice.
- **Reordering** — `action: reorder` builds every sub-step's message up
  front and writes them in a declared `send_order`, independent of
  declaration order, with results returned in declaration order regardless
  of wire order. **SIP's reorder correlation is POSITIONAL only** — SIP
  performs no Call-ID/CSeq correlation anywhere in this codebase, so a
  reorder group's responses are attributed by arrival order and trust the
  peer to answer in receipt order. This is NOT equivalent to the Diameter
  implementation's Hop-by-Hop-ID correlation, which matches every answer to
  its own request regardless of arrival order. See `CHANGELOG.md` for the
  full disclosure.

**What was not proven:** every one of the above — timing, loss, duplication,
and reordering, on both protocols — is exercised against Cellrift's own
in-repo mock peers only. No real-peer run has been made for any of them.
Worked examples: `scenarios/ims/register_impaired.yaml` (latency/jitter/
delay) and `scenarios/ims/register_loss_reorder.yaml` (drop/duplicate/
reorder).

### Connection-level faults

The `impair:` block also carries four connection-level fault keys —
`reset`, `truncate`, `abrupt_close`, and `sctp_abort` — each performing a
real local socket operation (a genuine TCP RST, a short write, a plain
close, or a real RFC 4960 ABORT chunk) rather than a synthetic error, and
each reporting which one fired via `response.connection_state`
(`"reset"`/`"truncated"`/`"closed"`/`"aborted"`). A faulted step reports
`Status: failed`, distinct from a dropped step, which passes. `sctp_abort`
is rejected by `cellrift validate` on any step that will not run over SCTP.

**SIP's connection faults are scoped per-step, not mid-session** — this
client dials a fresh connection on every send, so a fault only ever affects
the step that inflicted it. Diameter's are genuinely mid-session, acting on
the one association the handshake established and later steps reuse. See
`CHANGELOG.md` for the full disclosure, including the Diameter
reconnection fix that keeps a later step from misattributing an
earlier step's self-inflicted fault to itself.

Worked example: `scenarios/ims/register_conn_fault.yaml`
(truncate/reset/abrupt_close over SIP; `sctp_abort` is proven separately,
entirely within `internal/protocols/diameter`'s own package tests, since
SIP has no SCTP transport to exercise it against). Running this file
deliberately exits non-zero — three of its four steps destroy their own
connection by design.

### Assertion vocabulary and the assert-liveness-after-fault pattern

`connection_state` and `timed_out` are the two cross-protocol keys a
scenario's `assert:`/`capture:` can read, meaning the same thing regardless
of whether the step ran over SIP or Diameter: `connection_state` records
which connection-level fault fired (`"reset"`/`"truncated"`/`"closed"`/
`"aborted"`); `timed_out` is `true` only when the peer never answered within
the step's own `timeout_ms`, `false` otherwise, and is always present. The
two are mutually exclusive by construction — a faulted step reports
`connection_state` and `timed_out == false`, never both.

`scenarios/ims/register_assert_liveness.yaml` is the worked example of the
assert-liveness-after-fault pattern this vocabulary exists for: a `reset`
fault followed by an ordinary, unimpaired step that asserts the peer is
still answering on the wire. **This proves wire-visible liveness only** — it
does not prove the peer's host is healthy, that no process crashed and
restarted underneath, or that any internal subsystem survived. Cellrift is a
client with no privileged access to the system under test.

### Malformed messages and cross-protocol injection

A `send` step can declare a `malform:` block, structurally parallel to
`impair:`, to corrupt exactly one already-correctly-encoded field:
`avp_length`/`msg_length` override a Diameter AVP's own Length field or the
message header's own Message Length field, and `sip_header` drops
(`missing`), appends a second copy of (`duplicate`), or replaces
(`overwrite`) a named SIP header line. Every corruption runs strictly AFTER
the real encoder (`BuildRequest`/`Serialise`) has already produced a
correct message — never a second, parallel encoder — so every byte except
the one named field is exactly what a correct message would carry.

A new `action: raw` sends literal bytes directly over a step's own
connection, bypassing the protocol encoder, the request builder, and
response parsing entirely — the mechanism behind "one protocol's wire bytes
sent over the other protocol's connection" that motivated this feature.
Two byte sources: an inline hex literal, or a `{{ }}` reference to an
earlier step's own captured `response.__raw` bytes, both resolved through
the same template machinery every other field uses with no byte lost.
Reports `response.raw_bytes_sent`; never attempts to parse whatever the
peer sends back. Landed as validator support, engine dispatch, and both
adapters' implementation in a single change, not split across a
"validates but doesn't dispatch" boundary the way `action: wait` originally
was — a dedicated dispatch-audit test requires both adapters to dispatch it.

**Enforced, with no opt-out:** any scenario file declaring `malform:`
or `action: raw` MUST carry the MOCK-ONLY marker. `scripts/check-scenario-
labels.sh`'s guard scans the whole scenario corpus by content
(`malform:`/`action: raw`) rather than a fixed file list, so a scenario
written after this feature shipped is caught exactly the same way a
long-standing one is. Malformed and cross-protocol payloads are
deliberately adversarial traffic, a materially different risk class from a
delayed or dropped packet.

**What was not proven:** no real peer — no real P-CSCF, no real HSS — has
ever been sent malformed or cross-protocol traffic. Every proof here is
against Cellrift's own in-process mocks, read at the pre-framing/pre-parse
byte level (`RawReads()`) specifically because a corrupted Message Length
or AVP Length can break a mock's own read-framing before the bytes ever
reach its higher-level request history. What a real peer does with
malformed or cross-protocol input is exactly the question this feature
exists to let a scenario author investigate against their own
infrastructure; Cellrift makes no claim about the answer.

Worked examples: `scenarios/diameter/malformed_diameter.yaml` (AVP-length
override, Message-Length override, and a raw SIP-over-Diameter injection)
and `scenarios/ims/malformed_sip_headers.yaml` (all three `sip_header` ops,
plus both `action: raw` byte sources).

### Tested against

**SIP and SDP:**

- Asterisk 22.10.1 (chan_pjsip), in CI on every push
  (`.github/workflows/interop.yml`, `sip-interop` job; evidence in
  `test/interop/EVIDENCE.md`, runs 12–14): calls over UDP and verified TLS,
  timing-impaired REGISTERs, and digest-authenticated REGISTERs. To run the
  same thing against your own Asterisk, see
  [Getting started with Asterisk](getting_started_asterisk.md).
- Mock UAS built into the test suite, plaintext and TLS (no external dependency
  for `go test`), which also pins the digest `Authorization` grammar that
  Asterisk does not check.

**Diameter:**

- The in-process mock HSS in `internal/protocols/diameter/mock_hss_test.go`,
  for unit-level coverage. The mock is written against the same encoder it is
  testing, so a green mock-only test says the codec is self-consistent — it is
  not evidence of interoperability with any third-party implementation on its
  own.
- A real `gradiant/open5gs:2.8.0` HSS, in CI on every push, over TCP and real
  SCTP: CER/CEA reaches `STATE_OPEN`, and UAR, AIR, ULR, MAR and SAR are all
  answered `2001`.
- A real SigScale OCS, in CI on every push, over TCP: CCR-I and CCR-U are
  answered `Result-Code 2001`.

Never point any of these scenarios at a production node.

---

## Phase 2 — Next

### Finishing SIP

SIP now has a real peer in CI and digest authentication. What comes next, in
order:

1. **Transaction timers and retransmission** — behave as an RFC 3261 §17
   client transaction over UDP (Timers A/B and E/F, T1/T2 backoff), so a
   scenario under packet loss tests the peer rather than Cellrift's inability
   to retransmit.
2. **IMS-AKA and a real P-CSCF** — register against a Kamailio IMS core with
   RFC 3310 `AKAv1-MD5`, which would make the IMS/VoLTE positioning
   demonstrable and retire the mock-only marker on the mixed SIP+Diameter
   scenarios.
3. **Server mode (UAS)** — let Cellrift be the peer, answering SIP requests
   from a declarative scenario, so someone else's CI can run against it.

Smaller SIP gaps, each with notes, are tracked in
[`docs/todo_next.md`](todo_next.md): no ACK for a `2xx` to an INVITE, a Via
sent-by of `[::]:<port>`, `local_port` being ignored, and `stale=true` not
being honoured on a digest challenge.

### Finishing Diameter

All six command pairs have real-peer evidence; that history lives in
`CHANGELOG.md` and `test/interop/EVIDENCE.md`. What's genuinely still next:

1. **Quota management** — send `Requested-Service-Unit` and exercise credit
   re-authorisation against the OCS.
2. **RAR/RAA** — the one Diameter command still unimplemented.
3. **Outbound device-watchdog timer (RFC 3539 Tw)** — Cellrift answers an
   inbound DWR but never sends one on an idle connection.
4. **DPR/DPA** — Cellrift never announces a Diameter-level disconnect, which
   is a plausible contributor to a peer refusing a reconnect under the same
   `Origin-Host` (see `docs/todo_next.md` item 8).

SCTP on macOS and Windows is not on this list: the library has no
implementation there, so it is a documentation job, not a coverage one.
There is no fixed release schedule for these, and the order is genuinely
open.

### New protocols

**PFCP — N4 interface (SMF ↔ UPF)**
Session establishment, modification, and deletion on the PFCP N4 interface. Lets you test
a UPF by running Cellrift as a mock SMF, or test an SMF by running Cellrift as a mock UPF.
Library: `go-pfcp`.

**5G SBI — Service-Based Interface**
HTTP/2 + JSON + OAuth2 + mTLS. Covers the RESTful API layer between 5GC Network Functions
(AMF, SMF, UDM, AUSF, NRF, NEF, PCF). Based on 3GPP Release 17 OpenAPI specs.

**SMPP — Short Message Peer-to-Peer**
Binary TLV protocol between SMS applications (ESMEs) and gateways (SMSCs). Lets CPaaS
and messaging platforms test their SMPP integration without a live SMSC.

**RTP — Real-time Transport Protocol (RFC 3550)**
Builds on Phase 1 SDP to add real media path verification: bind a local RTP port, include
it in the SDP offer, send noise packets after call setup, assert that media packets are
received back. No codec processing — payload is random bytes with a valid RTP header.
Confirms the media path is established end-to-end, not just the signalling.

### Features

**NF Mock / Stub mode**
Run Cellrift as a lightweight configurable stub NF. Point your software under development
at a mock HSS, SMF, or AMF instead of deploying a full Open5GS or free5GC stack.

```bash
cellrift mock --nf hss --port 3868 --config mock_hss.yaml
```

The mock YAML maps incoming requests to configurable responses, supports injecting delays
and errors, and records interactions as a replayable scenario file.

**Verbose run output — resolved variables in results**
`cellrift run --verbose` adds a `variables_used` field to the JSON result and prints the
resolved variable table in the terminal reporter before step execution begins. This makes
runs fully self-describing for audit and debugging without polluting the default output.

```json
{
  "scenario": "VoLTE basic call",
  "status": "passed",
  "variables_used": {
    "pcscf_host": "127.0.0.1",
    "callee_msisdn": "[REDACTED-1]"
  },
  "steps": [ ... ]
}
```

Variable values are passed through the PII masker before being written, so MSISDNs and SIP
identities are replaced with stable `[REDACTED-N]` tokens — the same masking applied to
error messages today. `CELLRIFT_*` runtime tunables (poll interval, timeouts) are
included in a separate `config_overrides` field when they differ from their defaults.

**Seagull scenario import**
`cellrift convert --from seagull` converts existing Seagull XML scenario files to Cellrift
YAML. Best-effort (~80% of real-world Diameter/H248 scenarios convert cleanly); edge cases
emit a `# TODO: manual review` comment.

**AI scenario generation**
Generate scenario YAML variants from an existing scenario plus RFC / 3GPP spec sections.
Uses a RAG architecture at inference time — spec chunks, substitution rules, and existing
YAML templates are provided to a local LLM (nothing leaves your network). Produces
boundary-value variations and spec-corner-case scenarios that manual authoring routinely
misses.

```bash
cellrift ai generate \
  --spec rfc3261-section10.txt \
  --template register_flow.yaml \
  --output scenarios/generated/
```

---

## Phase 3 — Planned

**RADIUS**
AVP-based AAA for Wi-Fi, broadband access, and enterprise network authentication.
Structurally similar to Diameter — reuses the AVP codec infrastructure.

**NGAP + NAS — RAN-core interface**
Simulate a gNB to test AMF implementations over the N1/N2 interface. High engineering
complexity (NAS security state machine, SCTP multihoming). Late Phase 3 or early Phase 4.

**SS7 / SIGTRAN / MAP**
Legacy 2G/3G roaming interconnect and SMS routing. Driven by community demand signals.

**Fault enrichment**
When a test fails, instead of just showing the assertion error, Cellrift cites the
relevant RFC or 3GPP TS section and explains what correct protocol behaviour looks like.
Builds on the same spec index as AI scenario generation.

```
Failure: Result-Code 5001 received; expected 2001

Spec: RFC 6733 §7.1.4, 3GPP TS 29.272 §6.3.1
Explanation: DIAMETER_ERROR_USER_UNKNOWN — the Public-Identity in the UAR
is not provisioned in the HSS. Provision the subscriber or use a known identity.
```

**Security / adversarial testing mode**
`cellrift fuzz` — stateful protocol fuzzing. Establishes valid protocol context first
(e.g., Diameter CER/CEA), then injects mutations at a configured step and monitors
for crashes, unexpected error codes, or protocol violations.

**Scenario marketplace**
Community library of shareable Cellrift scenarios, installable with `cellrift install`.

---

## Phase 4 — Exploratory

Items in this phase are directional; scope and sequencing depend on Phase 2–3 feedback.

- **SRTP / DTLS-SRTP** — encrypted media; RTCP-derived quality metrics (jitter, packet loss, MOS estimation)
- **SIP trunking** — PSTN interconnect, number portability, carrier-grade SIP profiles
- **CoAP / MQTT** — IoT application protocols for NB-IoT and LTE-M test flows
- **RTSP 2.0** — media streaming control (RFC 7826)

---

## Contributing

Priorities are adjusted based on GitHub issues and discussions. If a protocol or feature
would unblock your work, open an issue — it directly influences what gets scheduled next.

See [CONTRIBUTING.md](../CONTRIBUTING.md) for how to contribute scenarios, protocol
adapters, or bug fixes.
