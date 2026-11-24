# What is still unproven

[`docs/pre-publication-audit.md`](pre-publication-audit.md) is finished — all
26 items closed. This file
is its successor, and it tracks one thing: **the gap between what Cellrift
does and what has been demonstrated against software Cellrift did not write.**

Every claim below is deliberately conservative. A mock that shares the
client's own encoder cannot prove interoperability with anything, and this
project has already been bitten three times by that exact self-consistency
trap — a missing AVP, a mock more forgiving than the real peer, and a status
gate that passed an answer it could not read.

## What IS proven today

The `diameter-interop` job runs on every push, against a genuine
`gradiant/open5gs:2.8.0` HSS, over **both** TCP and real SCTP:

| Exchange | Evidence |
| --- | --- |
| RFC 6733 §5.3 CER/CEA → `STATE_OPEN` | `test/interop/EVIDENCE.md`, runs 1–5 |
| §5.5 DWR/DWA | manual verification against the same image |
| Cx UAR/UAA → `Experimental-Result-Code 2001` | runs 4–5, both transports, in CI on every push |
| S6a AIR/AIA and ULR/ULA → `Result-Code 2001` | run 6, both transports |
| Cx MAR/MAA and SAR/SAA | run 6, both transports |
| An RFC 4960 ABORT reaching the peer | run 6 — peer logs `Connection reset by peer`, then `STATE_OPEN -> STATE_CLOSED` |
| CCR/CCA (Ro/Gy) → `Result-Code 2001` | run 10, against SigScale OCS, TCP |

All six Diameter commands now have real-peer evidence. Four SIP scenarios
(`register_impaired.yaml`, `basic_call.yaml`, `basic_call_tls.yaml`, and the
digest-authenticated `register_digest_auth.yaml`) are answered by Asterisk
22.10.1 on every push (runs 12–14), but Asterisk is a plain
SIP server, not a P-CSCF: every IMS-specific SIP flow is still mock-only, and
Cellrift sends no ACK for the 200 to an INVITE (item 9). Most of the
impairment set has never met third-party software.

---

## 1. ~~AIR/ULR (S6a)~~ — DONE (run 6)

Proven against the same Open5GS HSS, over both transports. The one thing in
the way was an identity rule: S6a's `User-Name` carries the IMSI, not an
MSISDN. Encoded in `scenarios/diameter/s6a_air_ulr.yaml`, which the interop
job now drives against the real peer.

## 2. ~~MAR/SAR (Cx)~~ — DONE (run 6)

Proven against the same peer, over both transports, after learning Cx's own
identity rule (bare MSISDN as IMPI; IMPU derived from the IMSI's PLMN) and
two ordering dependencies now documented in `run-scenario.sh`.

## 3. ~~CCR/CCA~~ — DONE

Proven against [SigScale OCS](https://github.com/sigscale/ocs) in its own CI
job. Three AVP defects the peer named in a single answer were fixed on the
way — a missing `Auth-Application-Id`, a missing `Service-Context-Id`, and a
`Rating-Group` sent outside `Multiple-Services-Credit-Control` — then a
`5030 DIAMETER_USER_UNKNOWN` until the harness provisioned a subscriber and a
balance. See `test/interop/EVIDENCE.md`, run 10.

Two earlier versions of this item were wrong and are worth remembering: the
first called it a missing container, the second asserted no RFC 4006 peer
existed anywhere and that Gx support was required. The peer was one
`docker pull` away, and no Gx work was needed.

Still open within CCR: TCP only (the OCS is reached through a published
container port), and no `Requested-Service-Unit`, so quota management and
mid-session credit re-authorisation are unexercised.

## 4. SCTP ABORT — half closed (run 6)

**The abort itself now works and the peer observes it.** It never did before:
the cmsg write returned `EINVAL` on every run and the test asserted only the
stamped `connection_state`, never the error, so a dead feature stayed green
all along. Fixed via `SO_LINGER{1,0}`; the peer logs `Connection
reset by peer` and tears the association down.

**What is left** is the second half of the scenario: the liveness step after
the abort. Cellrift re-dials and completes a fresh CER/CEA — the peer logs
the new association — but the following UAR's answer never arrives.

**Correction, 2026-09-29.** This item previously read:

> **Blocking that diagnosis is a defect of our own:** the receive loop reports
> its *last* error (`use of closed network connection`) rather than the first,
> so the actual cause is masked. Fix the reporting first; the gap cannot be
> honestly diagnosed until the first error survives.

**That was wrong.** No receive path reports a last error over a first one.
`nextMessage`, `receiveAnswer`, `receiveAnswers` and SIP's
`waitForFinalResponse` all return their first error immediately, and
`receiveFromHeldConn` does a single read with no loop.

The string came from `readFromSCTP`'s timeout error, which was deliberately built to
wrap **both** `net.ErrClosed` (so the dead association is dropped) and
`os.ErrDeadlineExceeded` (so the timeout is classifiable). Driving that path
prints:

```text
sctp read: timed out after 50ms: use of closed network connection: i/o timeout
```

A timeout, announced as one — with a phrase in the middle that reads like a
connection-state fault, which is how it was misread.

The mistake was explaining an observed string with a plausible mechanism and
never checking it against the code: an argument recorded as a finding, which
is precisely what this file's closing section exists to prevent.

**What is actually open:** two things, not one.

1. The error text is misleading and worth fixing — but it is small, and it is
   not blocking anything.
2. The liveness-after-abort failure itself is unexplained, and fixing the text
   will not make it pass. The UAR's answer still never arrives.

The next step reproduces (2) against the real peer before
proposing a remedy, rather than scoping a fix from a second unverified guess.

**Correction, 2026-09-30.** This item's name, and its "what is left" paragraph
above, describe a *liveness-after-abort* failure. They said:

> **What is left** is the second half of the scenario: the liveness step after
> the abort. Cellrift re-dials and completes a fresh CER/CEA — the peer logs
> the new association — but the following UAR's answer never arrives.

**The abort is not the cause.** Run 11 of `test/interop/EVIDENCE.md` captured
the association and the peer's own log. The HSS receives the UAR, builds an
answer, and drops it (`Unable to forward answer to deleted / closed peer`).
The same drop happens with no fault at all (two back-to-back runs), after a
graceful close, over TCP after a `reset`, and after a 35 s settle. A second
association under a *different* registered Origin-Host is answered. The
trigger is a second association under the **same Origin-Host**. It is not a
rule: one of three runs of a valid UAR was answered.

**How the mistake was made:** the failure was seen in the one scenario that
also injects an abort, so the abort got the blame and the name. Nothing
isolated the two. The no-fault control, which would have, was never run until
run 11. Same shape as the two earlier corrections in this item: a correlation
recorded as a mechanism.

**Still open:** why freeDiameter considers the name closed while the new
association is exchanging DWR, and what decides the outcome of the valid-UAR
runs. A fix was **not** in scope for that reproduction and none was attempted.

**Two further findings from run 11.**

- `scenarios/diameter/sctp_abort.yaml`'s UARs omit `Visited-Network-Identifier`,
  which this HSS requires, so its `== 2001` assertion can never pass against
  it: every UAR is answered 5005. Left unfixed deliberately, because fixing it
  would change what the baseline run measured.
- **Untested lead:** Cellrift has no DPR/DPA (command 282, RFC 6733 section 5.4)
  at all; it only tears down the transport. A DPR before close plausibly
  makes the *clean* reconnect case deterministic. It cannot help deliberate
  fault injection, since a fault cannot be announced. It has never been tried
  (the graceful-close run was a transport SHUTDOWN, not a DPR). It is a lead,
  not a fix, and would be its own plan.

## 5. A real SIP peer in CI — and IMS-AKA before a P-CSCF

**What is true:** a third-party SIP implementation, Asterisk 22.10.1, answers
Cellrift in CI on every push, including digest-authenticated REGISTERs (runs
12–14). What is missing is a P-CSCF: Asterisk is not one, and the interop HSS
has no SIP endpoint at all, which is why every mixed SIP+Diameter scenario
stays MOCK-ONLY even though its Diameter half is proven.

**Peers exist, and they are well-trodden.** Kamailio's `ims_*` modules
implement P-CSCF, I-CSCF and S-CSCF; OpenSIPS has an IMS build too; and
[`herlesupreeth/docker_open5gs`](https://github.com/herlesupreeth/docker_open5gs)
already wires Kamailio IMS to Open5GS with Bind9, RTPEngine and MySQL, as
does Open5GS's own
[Dockerized VoLTE tutorial](https://open5gs.org/open5gs/docs/tutorial/03-VoLTE-dockerized/).
Availability is not the blocker.

**The blocker is IMS-AKA, and it is ours.** Cellrift answers a `401` or
`407` with one in-step RFC 7616 digest retry (MD5 and SHA-256, `qop=auth`),
proven against Asterisk in CI (runs 13–14). Asterisk checks the digest
arithmetic but not the `Authorization` header's grammar, which in-process
tests pin instead. But a real P-CSCF normally challenges with IMS-AKA
(`AKAv1-MD5`, RFC 3310), which Cellrift does not compute, nor `-sess`,
SHA-512-256 or `auth-int`. Since a SIP step passes only on a 2xx, a step
against an AKA-challenging P-CSCF fails — correctly — and no amount of
container wiring changes it.

**Run 12 did not change the P-CSCF gap.** The paragraph above still holds
word for word: the interop HSS has no SIP endpoint at all, and Asterisk is not
a P-CSCF. Run 12 retires no `MOCK-ONLY SCENARIO` marker and says nothing about
IMS semantics; the IMS extension headers were tolerated by Asterisk, which
ignores headers it does not know. The hand verification recorded in
`docs/getting_started_asterisk.md` was against "Asterisk 18+"; the pinned peer
is 22.10.1, so run 12 is a new result and not a continuation of that one.

**So this is two items, and they are worth doing in order:**

1. ~~**An Asterisk container in CI**~~ — **DONE (run 12)**, answering
   `basic_call.yaml`, `register_impaired.yaml` and, with certificate
   verification on, `basic_call_tls.yaml`. Asterisk can be configured to accept
   unauthenticated registration, so this proves "a real third-party SIP
   stack answers Cellrift, automatically, on every push" without any new
   protocol work. It is the SIP equivalent of what the HSS job already does
   for Diameter. What it does not do is stated in item 9 and in run 12's
   "What this does not prove".
2. ~~**Digest**~~ — **DONE (runs 13–14)**: the retry is automatic and
   in-step, credentials come from the scenario, a credentials file or
   `CELLRIFT_SIP_PASSWORD`, and a scenario asserts on the challenge through
   `response.auth.*`. **IMS-AKA, and then a Kamailio P-CSCF**, remain.

Either way, most shipped SIP scenarios are not candidates: some fail by
design, and the malformed ones must never be aimed at a peer at all.

## 6. Diameter over SCTP is Linux-only — document it, do not test it

**Correction to an earlier claim.** "SCTP on macOS and Windows is verified by
cross-compilation only" implied the path might work there. It cannot: the
`ishidawataru/sctp` library carries a `sctp_unsupported.go` for every
non-Linux target (and for `linux/386`) whose every entry point returns
`ErrUnsupported`. A macOS or Windows user asking for `transport: sctp` will
always get TCP plus the warning `transport: requested sctp, used tcp (sctp
unavailable: ...)`.

**So the work here is honesty, not coverage:** state the platform limit where
a user chooses a transport — README's status table, `docs/cli-reference.md`'s
Diameter transport key, and the roadmap — rather than implying an untested
capability. A CI job on `macos-latest` would only re-prove the fallback.

## 7. Maintenance: the workflows pin Node 20 actions

Both Diameter interop legs emit, and so did the first `sip-interop` run
(run 12, where it named the same three actions):

```text
Node.js 20 is deprecated. The following actions target Node.js 20 but are
being forced to run on Node.js 24: actions/checkout@v4, actions/setup-go@v5,
actions/upload-artifact@v4
```

Not breaking — GitHub runs them on Node 24 regardless — but it is a
countdown, and it affects all three workflows plus
`.github/actions/run-scenarios`. Closing it means bumping to the current
major of each action and re-running every interop job to confirm nothing
depended on the old behaviour. Still open.

## 8. Reusing one Origin-Host across associations is a user-facing limitation

Not a harness quirk. Any user pointing Cellrift at freeDiameter-based
infrastructure and reconnecting under the same Origin-Host can have the
peer build and then drop their answer, with no fault injected: run 11's e3 is
two ordinary back-to-back runs, and the answer was dropped. A distinct
registered Origin-Host per association is answered, which is also what run 8's
CI fix does. It is not deterministic (one of three valid-UAR runs was
answered), so it will look like flakiness.

What a user needs is either a documented rule (one Origin-Host per
association against freeDiameter peers) or a Cellrift behaviour that avoids the
collision. The DPR lead in item 4 is the first thing to try for the second,
and it is untested.

## 9. Four findings the Asterisk CI work recorded and deliberately did not fix

Each was observed against Asterisk 22.10.1 in run 12
(`test/interop/EVIDENCE.md`; the peer's own log is committed under
`test/interop/evidence/run-12-asterisk/`). None is fixed here, because the
phase's deliverable is an evidence record and a fix would change what the
record measured.

### 9.1 Cellrift never ACKs a 2xx to an INVITE

**What is true.** RFC 3261 §13.2.1 requires a UAC to ACK a 2xx final response
to INVITE, and §13.3.1.4 has the peer resend the 200 until it arrives. The only
`ACK` handling in the SIP adapter is for an ACK a scenario author writes as an
explicit step; nothing sends one automatically.

**How it was observed.** The peer's log shows INVITE and BYE received and no
ACK request: `grep -c 'Received SIP request (.*ACK'` is 0. (A bare
`grep -c ACK` returns 14, all from `Allow:` headers, which is a trap for anyone
re-checking.) The same log shows the peer sending the 200 again to the same
source port at intervals of about 0.5 s, 1 s and 2 s, which is the behaviour
§13.3.1.4 describes for a 200 that is never acknowledged.
`basic_call.yaml` passes anyway, because its BYE is accepted, so the dialog is
never acknowledged from the peer's side.

**Why not fixed here.** It is dialog-layer production work needing its own
tests and mutation proof, adjacent to the transaction and timer work.

**What closing it takes.** The adapter sends the ACK itself on a 2xx to INVITE,
with a scenario-level opt-out, and a test that fails when the ACK is removed.

### 9.2 The Via sent-by is `[::]:<port>`

**What is true.** Cellrift's Via header carries `[::]:<port>` as the sent-by,
which is not an address a peer can route to. In the peer's log:
`Via: SIP/2.0/UDP [::]:53671;branch=...`.

**How it was observed.** Asterisk tolerates it by replying to the packet source
and adding `rport` and `received=127.0.0.1` to the Via of every response. A
stack that routed responses by the Via host would not reach Cellrift. This is
the first time a real third-party stack has seen it.

**Why not fixed here.** It changes bytes on the wire for every SIP scenario and
belongs with the transaction work, where the sent-by is chosen deliberately.

**What closing it takes.** Use the local address of the socket, and pin it with
a test.

### 9.3 `local_port` is ignored for SIP

**What is true.** `basic_call.yaml` declares `local_port: 5060`. The adapter
listens on `:0`.

**How it was observed.** The peer's log shows ephemeral source ports (53671,
55164, 35909 in run 12), never 5060.

**Why not fixed here.** Harmless for passing. Nothing may assert or document a
source port until it is honoured.

**What closing it takes.** Either honour the key or remove it from the schema,
the validator and the scenario, with the drift guard updated either way.

### 9.4 Cellrift has no `server_name` for TLS

**What is true.** `BuildTLSConfig` never sets `tls.Config.ServerName`, so Go
infers the verification name from the host portion of the dial address and does
not fall back to the certificate's CN.

**How it was observed.** The harness certificate carries
`subjectAltName = IP:127.0.0.1, DNS:localhost` precisely because a certificate
with only a DNS name fails a run aimed at 127.0.0.1 with
`x509: certificate is valid for localhost, not 127.0.0.1`. A user whose peer is
reached by IP but certified by a DNS name cannot verify the certificate today;
the only options are an IP SAN or `insecure_skip_verify`.

**Why not fixed here.** A `server_name` key is new scenario-schema surface and
needs the validator and schema drift guard, so it is its own change.

**What closing it takes.** Add the key to the validator, the schema and
`BuildTLSConfig`, with a test that verifies a DNS-certified peer reached by IP.

### Recorded alongside, not deferred work

**`impair_delay_ms` for `delay_ms` is a declaration echo, not a measurement.**
`PostReceiveDelay` sleeps for the configured duration and returns that
duration. Step 3 of `register_impaired.yaml` read exactly 200 in every run (four
runs on two machines with different timing), while step 1, which includes
random jitter, varied every time (628, 655, 672 locally and 691 in CI). So the
scenario's `>= 200` assertion shows the value was reported, not that a delay
occurred. The feature itself is guarded: four Go tests across three packages
measure real elapsed time and fail if the sleep is deleted. This is a note about
what a scenario assertion proves, not a missing guard.

**Two runner questions run 12 answered or left open.**

- *Container uid against key-file permissions.* Unknown since research, because
  the container runs as uid 1000, which matched the researcher's host uid. On
  the GitHub-hosted runner the TLS leg read the harness-generated key and
  passed with verification on, and ports 15060 and 15061 were free.
  Answered by evidence.
- *The fork gate is not exercised.* The `sip-interop` job is the only fork-gated
  job in the repository. A same-repo push exercises only the
  `github.event_name != 'pull_request'` half of its condition; only a fork pull
  request can test the `head.repo.full_name == github.repository` comparison,
  and no fork pull request exists. The expression was built from documented
  fields and has not been seen to refuse.

---

## 10. Three findings the digest-authentication work recorded and deliberately did not fix

**10.1 `reorder` members are not authenticated.** A `reorder` group writes all
members and then reads positionally, so a retry sent mid-read would break that
correlation. A challenged member is not retried and the result says so (the
behaviour is pinned by a test that checks the note). `parallel` sub-steps, by contrast, ARE
authenticated: `executeParallel` reaches `ExecuteStep` by the same call a
top-level step does, so a challenged sub-step retries normally. That was
checked by running a parallel scenario against a challenging peer; an earlier
note here claimed the opposite by analogy with reorder and was wrong. No test
pins it, which is the actual gap. Whether authenticated reorder is wanted is a product
decision nobody has made.

**10.2 A password supplied only by `credentials_file` or `CELLRIFT_SIP_PASSWORD`
is registered with the masker only when it is resolved, and resolution needs a
challenge.** In a scenario that is never challenged, such a password that someone
also writes into a report string (for example an assertion's right-hand side) is
not masked. Short secrets are masked and trigger a once-per-run warning; this
gap is the one the masking work stated as out of scope.

**10.3 The `--no-mask` sentence exists in three places** (`docs/cli-reference.md`,
the flag's help in `cmd/cellrift/main.go` and in `cmd/cellrift/selftest.go`) and
nothing mechanically keeps them equal. They agree today; the duplication is known
and was accepted.

---

## How to read a "proven" claim in this repository

- **Proven** means third-party software produced the observed result, the
  bytes are captured in `test/interop/EVIDENCE.md`, and a CI job re-checks
  it on every push.
- **Mock-only** means Cellrift's own mock produced it. That mock shares the
  client's encoder, so it can prove the code is self-consistent and nothing
  more. `scripts/check-scenario-labels.sh` enforces the marker in both
  directions.
- Anything between those two — "should work", "inherits the fix", "the same
  builder" — is an argument. Arguments belong in this file, not in a status
  table.

## 11. From the digest-authentication verification pass

These came out of a goal-backward verification of the digest work, after every
requirement had been closed. Each was reproduced, not reasoned about.

**11.1 `stale=true` is ignored.** RFC 7616 §3.3: a peer answering a stale nonce
sets `stale=true`, meaning "retry with the new nonce, your password was fine".
Cellrift does not read it, so the one-retry rule reports `credentials rejected
after one digest retry` for what is actually a nonce-lifetime problem. The
diagnosis names the wrong cause, and an author chasing a password that is
correct is the worst version of that. Handling it means allowing a second retry
when and only when `stale=true` is present, which needs a bound so a peer that
always says stale cannot drive a request loop.

**11.2 A broken `credentials_file` surfaces only on a challenge.** `validate`
does not open it, and the resolver runs only when a 401 arrives. An author with
a typo'd path gets a clean `validate` and a failure much later. `validate` could
read and decode the file without caring about its contents.

**11.3 `response.auth.*` drift is guarded in one direction only.** The docs test
proves each documented key is mentioned; nothing proves the adapter does not
emit an *undocumented* key. Adding one and running the suite leaves it green.
A set-equality assertion over the keys the adapter stamps would close it.

**11.4 The `--no-mask` sentence exists in four copies, not three.**
`cli-reference.md`'s main flag table, `main.go`, `selftest.go`, and
`cli-reference.md`'s selftest flags table — and the fourth already disagrees
with `selftest.go` about credentials. Nothing keeps them equal.

**11.5 Via sent-by is `[::]:<port>` and `local_port` binds nothing.** Together
an author cannot control the source port or the advertised address. Asterisk
tolerated it; whether a P-CSCF will is unknown until one is tested.
`local_port` is documented as inert with a tripwire test, but `validate` and
`run` still accept a value in silence — a run-level warning through the existing
`Warnings` channel would say so out loud.

**11.6 `parallel` sub-steps authenticate but nothing pins it.** They were
documented as unauthenticated by analogy with `reorder`; that was wrong, and
`executeParallel` reaches `ExecuteStep` by the same call a top-level step does.
Verified by running one against a challenging peer. The gap now is the missing
test, not the behaviour.

**11.7 Reorder's non-authentication is discoverable only after it fails.** The
error suffix is explicit and mutation-pinned, but neither `cli-reference.md`,
the schema, nor `validate` mentions it beforehand.
