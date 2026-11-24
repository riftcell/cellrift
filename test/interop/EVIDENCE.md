# EVIDENCE.md — Real-Peer Diameter Interop (Open5GS)

> **About the commit hashes in this file and under `evidence/`.** Each run
> records the commit it ran against, in the project's pre-publication
> history. That history was squashed into this repository's single initial
> commit when it was published, so those hashes can no longer be checked
> out. They are left exactly as recorded; every run's inputs and outputs are
> committed alongside it, so no result depends on finding its commit.

This file records a real run of `test/interop/up.sh` + `test/interop/run-scenario.sh`
against a genuine, third-party Diameter peer. **Nothing in this file was produced by
`MockHSS`** — MockHSS is Cellrift's own in-repo test double and shares the client's
own encoder/decoder, which is exactly the self-consistency weakness this evidence
retires (see `adapter_test.go`'s `TestAdapter_FailsAgainstStrictPeer` comment: a
mock that shares the client's encoder proves self-consistency, not interoperability).
Every AVP, Result-Code, and log line below was copied from the actual output of the
commands shown, on the date shown, not assumed or reconstructed from an earlier
exploratory spike against the same image.

## Run parameters

| Property | Value |
|---|---|
| Peer software | `gradiant/open5gs:2.8.0` (Open5GS HSS, `open5gs-hssd`) |
| Sidecar | `mongo:7.0` (subscriber database; the image ships none) |
| Transport | TCP (`No_SCTP;` in the vendored `hss-freeDiameter.conf`; `CELLRIFT_TRANSPORT_FALLBACK=tcp` set by `run-scenario.sh`) |
| Provisioned subscriber | IMSI `001010000000001` (synthetic, 3GPP test PLMN 001/01 — see `up.sh`) |
| Scenario | `scenarios/diameter/uar_uaa.yaml` (unmodified for this run) |
| Date of run | 2026-09-10 |
| Commands | `bash test/interop/up.sh && bash test/interop/run-scenario.sh` |

## Result: NOT YET 2001

The UAR now reaches the HSS's own Cx handler — it is no longer rejected at the
routing layer — but the HSS answers `Result-Code 5005 DIAMETER_MISSING_AVP`, not
`2001`. Both defects this run set out to check (the vendor-scoped
`Public-Identity` and the `Destination-Realm`) are confirmed present and
correct on the wire, by this run, against the real peer. **A third, previously
undetected gap is what blocks `2001` now** — see "Not yet 2001" below.

### CER/CEA — reached, succeeded

`run-scenario.sh`'s stage-1 assertion (`assert_handshake_reached`) passed: the
JSON report below carries no `"diameter handshake:"`-prefixed error, which is the
literal string Cellrift's own `doHandshake`/`validateCEA` prepend to every
handshake-stage failure (timeout, non-CEA, rejected CER). The HSS's own log
confirms the same thing from its side — the peer connection reached transport
level and the freeDiameter peer state machine advanced to `STATE_OPEN` (visible in
the state-transition line captured below; freeDiameter's log only dumps *failed*
messages at ERROR level, so a successful CER/CEA produces no dump of its own,
consistent with what's below: no CER/CEA parsing error appears anywhere in this
run's log, and the very next thing to fail is the UAR).

```
[32m09/10 06:29:09.787[0m: [app] INFO: HSS initialize...done (../src/hss/app-init.c:31)
[32m09/10 06:29:16.024[0m: [diam] INFO: CONNECTED TO 'test.cellrift.local' (TCP,soc#9): (../lib/diameter/common/logger.c:81)
```

Applications advertised in the CEA: confirmed in an earlier, independently
captured run (Cx/Sh/S6a, each via a correctly-formed
`Vendor-Specific-Application-Id` AVP) — this run's HSS log does not re-dump that
content because a *successful* CEA is never ERROR-logged by freeDiameter; the
absence of any CEA-parsing error here, combined with `STATE_OPEN` being reached,
is consistent with (not a re-derivation of) that prior capture.

### DWR/DWA

None occurred during this run — the connection was open only briefly (from
`CONNECTED` to the peer tearing it down after the UAR's parse failure), too short
a window for Open5GS's watchdog interval to fire. DWR/DWA interoperation with this
same image was previously captured in an exploratory spike before this harness
existed (three unsolicited DWRs answered correctly through `receiveAnswer`'s demux
loop). That spike's output is not in the tree, so treat DWR/DWA as hand-verified,
not as evidence of this run's standard.

### UAR as the HSS logged it — both fixes confirmed on the wire

```
AVP: 'User-Name'(1) l=20 f=-M val="447700900001"
AVP: 'Public-Identity'(601) vend='3GPP'(10415) l=62 f=VM val="sip:447700900001@ims.mnc001.mcc234.3gppnetwork.org"
AVP: 'Destination-Realm'(283) l=16 f=-M val="gradiant"
AVP: 'Route-Record'(282) l=8 f=-M val="test.cellrift.local"
```

- `Public-Identity`(601) carries `vend='3GPP'(10415)` and flags `f=VM` — the
  vendor-scoping fix, confirmed again here against the real peer.
- `Destination-Realm`(283) is present, flags `f=-M` (no V bit — base AVP, exactly as
  Cellrift encodes it), valued `"gradiant"` — the exact `Realm` the
  vendored `hss.yaml`/`hss-freeDiameter.conf` configure, and the exact value
  Cellrift derived from the peer's own CEA `Origin-Realm` (RFC 6733 §6.6), not
  hard-coded anywhere in `uar_uaa.yaml`. The `Destination-Realm` gap is closed.
- `Route-Record`(282) is freeDiameter's own addition (it stamps the record as the
  message transits); not something Cellrift sends.

### UAA — Result-Code 5005, not 2001

```
AVP: 'Result-Code'(268) l=12 f=-M val='DIAMETER_MISSING_AVP' (5005 (0x138d))
AVP: 'Failed-AVP'(279) l=8 f=-M val=(grouped)
   AVP: 'Vendor-Specific-Application-Id'(260) l=8 f=-M val=(grouped)
AVP: 'Error-Message'(281) l=8 f=-- val="DIAMETER_MISSING_AVP"
```

Cellrift's own JSON report (`--format json`) for this run:

```json
{
  "scenario": "Diameter UAR/UAA",
  "status": "failed",
  "duration_ms": 1,
  "steps": [
    {
      "id": "send_uar",
      "protocol": "diameter",
      "status": "failed",
      "duration_ms": 1,
      "error": "Result-Code 5005 (expected 2001)"
    },
    {
      "id": "uar_repeat",
      "protocol": "diameter",
      "status": "failed",
      "duration_ms": 0,
      "error": "Result-Code 5005 (expected 2001)"
    }
  ]
}
```

### `docker logs hss` (relevant excerpt, this run, boot through connection close)

```
Open5GS daemon v2.8.0

09/10 06:29:09.762: [app] INFO: Configuration: '/opt/open5gs/etc/open5gs/hss.yaml' (../lib/app/ogs-init.c:144)
09/10 06:29:09.764: [dbi] INFO: MongoDB URI: 'mongodb://localhost/open5gs' (../lib/dbi/ogs-mongoc.c:128)
09/10 06:29:09.787: [app] INFO: HSS initialize...done (../src/hss/app-init.c:31)
09/10 06:29:16.024: [diam] INFO: CONNECTED TO 'test.cellrift.local' (TCP,soc#9): (../lib/diameter/common/logger.c:81)
09/10 06:29:16.024: [diam] ERROR: Conflicting rule: the number of occurences (0) is < the rule min (1) for 'Vendor-Specific-Application-Id'.
09/10 06:29:16.024: [diam] ERROR: pid:Dispatch (0x5641ea203ad8) in parserules_do@messages.c:2650: ERROR: in '((fd_dict_iterate_rules ( model, &data, parserules_check_one_rule )))' :	Bad message
09/10 06:29:16.024: [diam] ERROR: pid:Dispatch (0x5641ea203ad8) in md_hook_cb_tree@dbg_msg_dumps.c:99: PARSING ERROR: 'DIAMETER_MISSING_AVP' from 'test.cellrift.local':
09/10 06:29:16.024: [diam] ERROR:      '3GPP/User-Authorization-Request'
09/10 06:29:16.024: [diam] ERROR:         AVP: 'Public-Identity'(601) vend='3GPP'(10415) l=62 f=VM val="sip:447700900001@ims.mnc001.mcc234.3gppnetwork.org"
09/10 06:29:16.024: [diam] ERROR:         AVP: 'User-Name'(1) l=20 f=-M val="447700900001"
09/10 06:29:16.024: [diam] ERROR:         AVP: 'Destination-Realm'(283) l=16 f=-M val="gradiant"
09/10 06:29:16.025: [diam] ERROR:         AVP: 'Route-Record'(282) l=8 f=-M val="test.cellrift.local"
09/10 06:29:16.025: [diam] ERROR: pid:Dispatch (0x5641ea203ad8) in md_hook_cb_tree@dbg_msg_dumps.c:109:         AVP: 'Result-Code'(268) l=12 f=-M val='DIAMETER_MISSING_AVP' (5005 (0x138d))
09/10 06:29:16.025: [diam] ERROR:         AVP: 'Failed-AVP'(279) l=8 f=-M val=(grouped)
09/10 06:29:16.025: [diam] ERROR:            AVP: 'Vendor-Specific-Application-Id'(260) l=8 f=-M val=(grouped)
09/10 06:29:16.025: [diam] ERROR:         AVP: 'Error-Message'(281) l=8 f=-- val="DIAMETER_MISSING_AVP"
09/10 06:29:16.025: [diam] ERROR: pid:PSM/test.cellrift.local in fd_psm_change_state@p_psm.c:284: 'STATE_OPEN'	-> 'STATE_CLOSED'	'test.cellrift.local'
```

(The identical sequence repeats a second time in the full log for `uar_repeat`,
the scenario's second step, with a different Hop-by-Hop Identifier — both steps
hit the same 5005.)

## Not yet 2001

**Result-Code observed: `5005 DIAMETER_MISSING_AVP`.** freeDiameter's own
`Failed-AVP` names the missing AVP: `Vendor-Specific-Application-Id` (260), with
its own error text: `"Conflicting rule: the number of occurences (0) is < the
rule min (1) for 'Vendor-Specific-Application-Id'."` freeDiameter's compiled-in
3GPP Cx dictionary requires every `User-Authorization-Request` to carry a
`Vendor-Specific-Application-Id` grouped AVP (naming which vendor application —
Cx — the UAR is for); Cellrift's UAR builder (`buildAVPsFromFields`,
`adapter.go`) does not emit one. This is a different AVP than the one just
added (283, base) or the one just fixed (601, vendor-scoped) — it is
a structural, message-level requirement of the Cx UAR itself, not a per-field
encoding defect.

**What kind of failure this is:**

- *Not* "still 3001" — Destination-Realm is confirmed present and correctly
  encoded (see above); the routing-layer rejection the `Destination-Realm` fix
  targeted is gone. The P-bit contingency does not apply.
- *Not* user-unknown-shaped — the Result-Code is `5005` (`DIAMETER_MISSING_AVP`),
  not `5001`/`5421`, and `Failed-AVP` names an AVP, not a subscriber identity.
  The provisioned IMSI is not implicated.
- *This is* an AVP-completeness/dictionary-shaped gap: the missing AVP is
  recognized by freeDiameter (it names it precisely), but Cellrift's UAR
  construction does not include a required structural element of the Cx
  User-Authorization-Request. This is the exact class of unresolved fact
  pre-run research flagged as an open, low-confidence question ("will the
  vendor-AVP fix alone make UAR pass" — it does not, for this reason).

**No AVP-dictionary change was attempted in this run**: adding a
`Vendor-Specific-Application-Id` AVP to the UAR message shape is
dictionary/message-completeness work, not a one-line encoding fix like the
`Destination-Realm` one. This run captured the bytes and stopped, rather than
guessing at a wider change.

**Blocked state:** a real `2001` UAA is **not yet achieved**. The
vendor-scoped `Public-Identity` and the `Destination-Realm` are both
independently confirmed fixed against the real peer by this run.
`scenarios/diameter/uar_uaa.yaml` must **not** be relabelled away from
`MOCK-ONLY` — it still does not reach `2001` against a real peer.

## What the next step needs to know

- Do not remove `uar_uaa.yaml`'s `MOCK-ONLY` header. The scenario still fails
  against the real Open5GS peer with `Result-Code 5005`, not `2001`.
- The new, precisely-named gap is: Cellrift's UAR does not carry a
  `Vendor-Specific-Application-Id` (AVP 260) grouped AVP identifying the Cx
  application, which freeDiameter's compiled-in Cx dictionary requires as a
  minimum-one-occurrence rule on every UAR. This is not the same defect as
  601 not being vendor-scoped or 283 being absent — both of those
  are now confirmed fixed on the wire by this same run.
- This is dictionary/message-completeness work, out of this run's scope; it
  should be tracked as its own item before attempting a fix.

---

# Run 2 — 2026-09-22 (CI, `diameter-interop` job)

A second real run against the same peer image, after the
`Vendor-Specific-Application-Id` fix landed. Same provenance rule as above:
every line below is copied from the job's own output, not reconstructed.

## Result: still 5005, for a DIFFERENT missing AVP

**AVP 260 is fixed, and this run proves it on the wire.** The HSS's own dump of
the UAR it received now contains exactly the grouped AVP whose absence blocked
run 1:

```text
'3GPP/User-Authorization-Request'
  AVP: 'User-Name'(1) l=20 f=-M val="447700900001"
  AVP: 'Public-Identity'(601) vend='3GPP'(10415) l=62 f=VM val="sip:447700900001@ims.mnc001.mcc234.3gppnetwork.org"
  AVP: 'Destination-Realm'(283) l=16 f=-M val="gradiant"
  AVP: 'Vendor-Specific-Application-Id'(260) l=32 f=-M val=(grouped)
     AVP: 'Vendor-Id'(266) l=12 f=-M val=10415 (0x28af)
     AVP: 'Auth-Application-Id'(258) l=12 f=-M val=16777216 (0x1000000)
  AVP: 'Route-Record'(282) l=8 f=-M val="test.cellrift.local"
```

Run 1's three confirmed-correct AVPs (601 vendor-scoped, 283 present, and now
260 with the right Vendor-Id and Auth-Application-Id) are all visible in one
dump, from the peer's own parser. The `Public-Identity` and `Destination-Realm`
fixes and run 1's newly found AVP-260 gap are each closed, by real-peer evidence.

**The answer is still `5005 DIAMETER_MISSING_AVP`, and freeDiameter names the
new offender precisely:**

```text
AVP: 'Result-Code'(268) l=12 f=-M val='DIAMETER_MISSING_AVP' (5005 (0x138d))
AVP: 'Failed-AVP'(279) l=8 f=-M val=(grouped)
   AVP: 'Session-Id'(263) l=9 f=-M val=""
AVP: 'Error-Message'(281) l=8 f=-- val="DIAMETER_MISSING_AVP"
ERROR: Conflicting rule: the number of occurences (0) is < the rule min (1) for 'Session-Id'.
```

Cellrift sent **no `Session-Id` (AVP 263) at all**, on any application request.
RFC 6733 §8.8 makes it mandatory and requires it to appear immediately after the
header, and every ABNF this client can send marks it so: Cx (TS 29.229 §6.1),
S6a (TS 29.272 §7.2) and Credit-Control (RFC 4006 §3).

The damage is not one-sided. The HSS could not build a well-formed answer
either — its own UAA has no `Session-Id` to echo, so it failed its own parse
rules on the way out (`PARSING ERROR, returning: '3GPP/User-Authorization-Answer'`)
and then dropped the peer: `'STATE_OPEN' -> 'STATE_CLOSED' 'test.cellrift.local'`.

## Why the in-repo suite did not catch this

`MockHSS` echoed whatever `Session-Id` it found in the request, including
nothing, and answered `2001` regardless. A mock more forgiving than any real
peer is the self-consistency weakness this file exists to retire, and here it
hid a mandatory AVP's total absence behind a green suite.

Fixed on both sides in the same change as this entry:

- `ensureSessionID` (`adapter.go`) generates an RFC 6733 §8.8 identifier for
  every application request that does not declare one, and hoists a declared
  one to the front. A declared `Session-Id` is never replaced — CCR-I/U/T
  belong to one session and must share one identifier across three steps.
- `NewStrictMockHSS` now answers `5005 DIAMETER_MISSING_AVP` to an application
  request with no `Session-Id`, modelling the real peer's rule rather than only
  its handshake.

## Status

A real `2001` UAA is **still not achieved**, and no scenario's
`MOCK-ONLY` marker may be removed on the strength of this run. What changed is
the reason: run 1 blamed AVP 260, which is now confirmed correct on the wire;
run 2 blames AVP 263, which is now fixed in code but **not yet re-verified
against the real peer**. Only a third run can close it.

---

# Run 3 — 2026-09-22 (CI, `diameter-interop` job)

## Result: still 5005, now for Origin-Host

**Session-Id is fixed, and this run proves it.** The HSS's dump of the UAR
shows it present, well-formed and first in the message:

```text
AVP: 'Session-Id'(263) l=49 f=-M val="test.cellrift.local;1790098980;2;48552bfc"
AVP: 'User-Name'(1) l=20 f=-M val="447700900001"
AVP: 'Public-Identity'(601) vend='3GPP'(10415) l=62 f=VM
AVP: 'Destination-Realm'(283) l=16 f=-M val="gradiant"
AVP: 'Vendor-Specific-Application-Id'(260) l=32 f=-M val=(grouped)
AVP: 'Route-Record'(282) l=8 f=-M val="test.cellrift.local"
```

The answer names the next one:

```text
AVP: 'Result-Code'(268) val='DIAMETER_MISSING_AVP' (5005)
AVP: 'Failed-AVP'(279) val=(grouped)
   AVP: 'Origin-Host'(264) l=9 f=-M val=""
ERROR: Conflicting rule: the number of occurences (0) is < the rule min (1) for 'Origin-Host'.
```

Cellrift put `Origin-Host` (264) and `Origin-Realm` (296) in the CER only.
RFC 6733 §6.3 and §6.4 require both in **every** Diameter message. The
adapter's own DWA already did this correctly; only application requests did
not.

## The pattern, and what was done about it

Three runs, three separate mandatory AVPs, one CI round trip each: 260, then
263, then 264/296. The peer reports only the first rule violation it hits, so
this walk could have continued indefinitely — and each step of it cost a
full Docker-in-CI cycle to learn.

Rather than fix only what run 3 named, this change closed every remaining
mandatory AVP in the Cx UAR's ABNF (TS 29.229 §6.1.1):

| AVP | Status before | Now |
|---|---|---|
| `Session-Id` (263) | fixed in run 2's follow-up | confirmed on the wire by this run |
| `Vendor-Specific-Application-Id` (260) | fixed in run 1's follow-up | confirmed on the wire by run 2 |
| `Origin-Host` (264) / `Origin-Realm` (296) | CER only | injected into every application request |
| `Auth-Session-State` (277) | excluded from UAR by the command registry | injected; the ABNF marks it mandatory |
| `Visited-Network-Identifier` (600) | absent | declared by `uar_uaa.yaml`, since it is roaming data Cellrift cannot invent |
| `Destination-Realm` (283), `User-Name` (1), `Public-Identity` (601) | present | unchanged |

`Auth-Session-State` was excluded deliberately when the command registry was
written, on the grounds that injecting it would change bytes a regression
test pinned exactly. The ABNF marks it mandatory, so that pin was pinning a
defect; the pin has been updated and the test that asserted "UAR must NOT get
Auth-Session-State injected" now asserts the opposite, with the reason.

The strict mock now rejects a request missing `Session-Id`, `Origin-Host` or
`Origin-Realm` with 5005, table-driven, so the next omission of a
base-protocol mandatory AVP fails in `go test` rather than costing a CI
round trip.

## Status

A real `2001` UAA is **still not achieved** and no `MOCK-ONLY` marker moves. Runs
1-3 each confirmed the previous fix on the wire and named exactly one more
missing AVP; this change is the first that fixes more than the peer has yet
complained about, so how much of it is right remains to be seen. What run 4
can still find: a Cx-specific rule not in the base protocol, an AVP whose
*value* the HSS rejects (rather than its absence), or an unprovisioned
subscriber — the IMSI in `up.sh` is synthetic and has never been exercised
past the AVP-completeness gate.

## Known, unverified gaps elsewhere

RFC 4006 §3.1 marks `Auth-Application-Id` and `Service-Context-Id` mandatory
in the CCR, and Cellrift sends neither. No real OCS has ever been stood up —
the Open5GS image used here ships none — so this is reasoning from the spec,
not evidence, and it is deliberately left alone rather than guessed at.
`scenarios/diameter/credit_control.yaml` stays MOCK-ONLY for this reason
among others.

---

# Run 4 — 2026-09-22 (local, `gradiant/open5gs:2.8.0`, same harness as CI)

Run 4 is the first run of this harness executed on a developer machine
rather than in CI. Same `up.sh`, same image, same `run-scenario.sh`. It is
recorded here because it is also the first run that reached **`2001`**.

## Result: PASSED — `OK: stage 1 (CER/CEA reached I-Open) and stage 2 (UAR answered 2001) both passed`

```text
run status: passed
  send_uar   -> passed
  uar_repeat -> passed
```

Reproduced from a full teardown and rebuild of the harness, not just a
re-run against a warm HSS.

## Three separate findings, only one of them a protocol gap

**1. A fixture gap, not a Cellrift defect.** With the AVP-completeness gates
finally closed, the UAR reached the HSS's own Cx handler and was rejected
there:

```text
[dbi] ERROR: [447700900001] Cannot find IMSI or MSISDN in DB (../lib/dbi/ims.c:54)
[hss] ERROR: Cannot get MSISDN-Data for IMSI or MSISDN: 447700900001 (../src/hss/hss-cx-path.c:223)
```

`up.sh` provisions IMSI `001010000000001`, and the vendored `open5gs-dbctl`
has no command that can add an MSISDN — every one of its `add*` paths writes
`"msisdn": []`. The HSS's Cx handler calls `ogs_dbi_msisdn_data()` before
anything else, so a subscriber with an empty `msisdn` array fails every UAR
no matter how correct the request is. `up.sh` now sets the MSISDN directly
via `mongosh` and verifies it landed, failing the harness loudly if not.

**2. A real Cx answer carries no `Result-Code` at all.** Decoded from the
wire, the UAA's only verdict was:

```text
AVP-297 raw=00 00 01 0a 40 00 00 0c 00 00 28 af 00 00 01 2a 40 00 00 0c 00 00 07 d1
         └ Vendor-Id (266) = 10415      └ Experimental-Result-Code (298) = 0x7d1 = 2001
```

`Experimental-Result-Code 2001` is 3GPP's `DIAMETER_FIRST_REGISTRATION` —
a success, in a different namespace from the base protocol's 2001
`DIAMETER_SUCCESS`. RFC 6733 §7.6/§7.7 define this as how an application
reports its own results, and TS 29.229 uses it exclusively for the UAA.

**3. The defect that finding exposed: Cellrift reported the step PASSED
while understanding none of it.** `buildStepResult` read only the base
`Result-Code`, found nothing, and fell through to `StatusPassed`. A peer
answering `5001 DIAMETER_ERROR_USER_UNKNOWN` in that same field would have
produced a green step and exit 0 — a false pass in the protocol family this
tool exists to test. Fixed: the verdict now prefers `Result-Code` and falls
back to `Experimental-Result-Code`, judging by RFC 6733 §7.1.3's 2xxx
success **class** so that both namespaces' successes pass and every other
code fails with the field it came from named.

## What changed as a result

| Change | Why |
|---|---|
| `Experimental-Result` (297) / `Experimental-Result-Code` (298) added to the dictionary | the answer's only verdict decoded as an unnamed `AVP-297` that no scenario could assert on |
| Grouped AVP members flattened into the response namespace | `response.Experimental-Result-Code` now resolves, with no new assertion grammar |
| Status gate honours the experimental verdict | closes the false pass above |
| `MockHSS.buildUAA` answers with `Experimental-Result` in **both** modes | the mock answered a shape no real Cx peer sends, so `response.Result-Code == 2001` passed here and could never pass against a real HSS |
| `NewSCTPMockHSS` is strict | every mock a shipped scenario runs against now models the peer's rules |
| UAR assertions across seven scenarios | they asserted a base `Result-Code` no Cx peer returns |
| `uar_uaa.yaml` no longer asserts `server_name != null` | a real HSS returns Server-Capabilities on a first registration and no Server-Name at all, so that assertion could only ever pass against a mock |

## Status: a real `2001` UAA achieved

`scenarios/diameter/uar_uaa.yaml` passes end to end against a genuine
third-party Diameter peer: CER/CEA to `STATE_OPEN`, then two UARs answered
`DIAMETER_FIRST_REGISTRATION`. Every AVP gap runs 1-3 found is confirmed
closed on the wire by this run.

**Confirmed in CI, and the marker has moved.** The `diameter-interop` job
went green on the pushed commit, which is the authority every previous entry
in this file deferred to — run 4 was a developer machine, and that was never
enough on its own. `scenarios/diameter/uar_uaa.yaml`'s `MOCK-ONLY` header is
removed, it is out of Guard A's list in
`scripts/check-scenario-labels.sh`, and a new Guard C fails the build if that
marker is ever put back while CI keeps proving the opposite. The job runs on
every push, so this is a standing proof rather than a one-off result.

What this still does not prove: AIR, ULR, MAR and SAR have never been sent
to a real peer; CCR has no real OCS to talk to (and RFC 4006 §3.1 marks two
AVPs mandatory that Cellrift does not send); no real P-CSCF has ever seen
Cellrift's SIP.

---

# Run 5 — 2026-09-24 (local, then confirmed in CI): real SCTP

The first four runs all used TCP, because the vendored
`hss-freeDiameter.conf` carried `No_SCTP;` and the runner set
`CELLRIFT_TRANSPORT_FALLBACK=tcp`. Both were deliberate — a result that
cannot vary with the kernel is the right thing to have while chasing AVP
gaps — but they left "does Cellrift's SCTP interoperate with third-party
software" unanswered, and this repository had been saying so in six places.

## Result: PASSED over a genuine RFC 4960 association

```text
OK over SCTP: stage 1 (CER/CEA reached I-Open) and stage 2 (UAR answered 2001) both passed
```

The peer's own log names the transport, which is the part that matters:

```text
09/24 08:50:54.839: [diam] INFO: CONNECTED TO 'test.cellrift.local' (SCTP,soc#8): (../lib/diameter/common/logger.c:81)
```

`ss -lnp` on the host confirms the HSS was listening on both, so the client
chose SCTP rather than being left with no alternative:

```text
tcp    LISTEN 0  5  0.0.0.0:3868  users:(("open5gs-hssd",...,fd=11))
sctp   LISTEN 0  5        *:3868  users:(("open5gs-hssd",...,fd=7))
```

Two config lines were blocking it, both now removed: the global `No_SCTP;`
and a per-peer `ConnectPeer = "test.cellrift.local" { No_TLS; No_SCTP; }`.
The second is easy to miss — the global setting alone is not enough.

## How this cannot become a lie

Cellrift substitutes TCP with a recorded warning when the kernel has no
usable SCTP. That is right at runtime and wrong to grade silently: an SCTP
run that quietly became a TCP run would report "SCTP interop proven" while
proving nothing. `run-scenario.sh` now fails the SCTP leg on that warning,
reading Cellrift's own report rather than probing the kernel separately.
Mutation-tested by forcing the substitution with the SCTP expectation left
in place:

```text
INTEROP FAILED: this run asked for SCTP and Cellrift substituted TCP.
The kernel has no usable SCTP, so nothing here says anything about SCTP interop.
```

## In CI

`.github/workflows/interop.yml` runs a matrix over both transports against
the same peer. GitHub's `ubuntu-latest` runners do support SCTP — known here
not by assumption but because `TestAdapter_SCTPAbort_AttributedToTheFaultingStep`
once **failed** on a runner over a scheduling race rather than skipping, and
that test skips itself twice over when SCTP is unavailable or substituted.

## Confirmed in CI, 2026-09-24

The same leg on a GitHub-hosted runner, which is the authority every entry in
this file defers to:

Both legs of the matrix, same commit, different runners:

```text
Image: ubuntu-24.04 / 20260920.314.1
Commit: a9e839c273f327945a2e95a6600b9ac69d78d618

(sctp, Azure eastus2)  CELLRIFT_TRANSPORT_FALLBACK:        # empty — EXPECT_SCTP=1
  OK over SCTP: stage 1 (CER/CEA reached I-Open) and stage 2 (UAR answered 2001) both passed

(tcp,  Azure westus3)  CELLRIFT_TRANSPORT_FALLBACK: tcp
  OK over TCP:  stage 1 (CER/CEA reached I-Open) and stage 2 (UAR answered 2001) both passed
```

Two things that matter more than the green tick:

- **The transport assertion was armed and stayed silent.** An empty
  `CELLRIFT_TRANSPORT_FALLBACK` puts `run-scenario.sh` in `EXPECT_SCTP=1`, so
  the run would have failed had Cellrift substituted TCP. It did not, which
  is what makes this an SCTP result rather than a TCP one with an SCTP label.
- **No `modprobe` was needed.** The runner's kernel provided SCTP on demand,
  through a container on `--network host`, with no privileged setup step in
  the workflow. `ubuntu-24.04` image `20260920.314.1` is the version this was
  observed on; the assertion above is what will catch it if a later image
  drops the module.

## What run 5 does not prove

The SCTP *fault* path. No real HSS has been asked to survive a deliberate
RFC 4960 ABORT — `scenarios/diameter/sctp_abort.yaml` is still mock-only for
that reason. SCTP on macOS and Windows remains verified by cross-compilation
only. And this is still one Diameter command of six.

---

# Run 6 — 2026-09-24 (local): four more commands, and a fault that never fired

Runs 1–5 proved one command. This run set out to close four gaps at once and
closed three of them, each after the peer named exactly what was wrong.

## AIR/ULR (S6a) — PASSED

First attempt: `Experimental-Result-Code 5001 DIAMETER_ERROR_USER_UNKNOWN`.
The scenario sent the MSISDN as `User-Name`; **S6a's `User-Name` carries the
IMSI** (TS 29.272 §7.3.1). With the provisioned IMSI `001010000000001`, both
AIR and ULR are answered `Result-Code 2001` — note the *base* Result-Code,
unlike Cx, whose UAA reports through `Experimental-Result`.

That 5001 is also the first dividend of run 4's verdict fix: before it,
an answer carrying only an `Experimental-Result` was read as a **pass**, so
this scenario would have gone green against a peer that had rejected it.

## MAR/SAR (Cx) — PASSED, after two distinct identity rules

`5002 DIAMETER_ERROR_IDENTITIES_DONT_MATCH`, with the peer naming both halves:

```text
[hss] ERROR: User-Name[447700900001] Public-Identity[sip:447700900001@ims.local] is not associated
```

Sweeping the plausible forms against the live peer found exactly one that
works: `User-Name` = the bare MSISDN, `Public-Identity` =
`sip:<MSISDN>@ims.mnc<MNC>.mcc<MCC>.3gppnetwork.org` derived from the IMSI's
own PLMN. Not the IMSI in the user part, not a `+`, not a `.local` domain.

Two ordering facts, both learned by getting them wrong, now encoded in
`run-scenario.sh`:

- **A UAR registers the IMPI/IMPU pair** in the HSS's context. A MAR naming a
  pair no UAR has registered is 5002 — which is why `uar_uaa.yaml` had to
  stop using a `mcc234` domain while the Cx scenarios used `mcc001`. Every
  Cx scenario now uses the one domain the provisioned IMSI implies.
- **Cx must run after `uar_uaa`**, because SAR assigns an S-CSCF and the same
  UAR then correctly answers `2002 SUBSEQUENT_REGISTRATION` instead of
  `2001 FIRST_REGISTRATION`.

A peer quirk worth knowing: the first application request on a fresh
association is occasionally answered into a peer object freeDiameter has
already torn down — `DROPPED 'Unable to forward answer to deleted / closed
peer'` — and the client sees a timeout. A prior request on the same
association makes it reliable, which is what the ordering above provides.

## SCTP ABORT — the capability never worked

`scenarios/diameter/sctp_abort.yaml` has claimed a working RFC 4960 ABORT
since it shipped. It never sent one:

```text
connection fault (aborted) failed to arm: invalid argument
```

`AbortConn` used `SCTPWrite(nil, &SndRcvInfo{Flags: SCTP_ABORT})`, and that
cmsg path returns `EINVAL` — the library's own `Close()` hits the same wall
and ignores the error. **The test could not see it**: `FaultedResult` stamps
`connection_state = aborted` whether the fault armed or not, and the test
asserted only the state, never the error. A vacuous assertion over a dead
feature, kept green all along.

Fixed by arming `SO_LINGER{1,0}` and letting the close abort the
association, which is what sctp(7) documents and what `resetHeldConn`
already does for a TCP RST. The peer confirms the chunk arrives:

```text
[diam] ERROR: pid:Receiver (8) SCTP/noTLS) in fd_sctp_recvmeta@sctp.c:1271: ERROR: in 'ret' : Connection reset by peer
[diam] ERROR: pid:PSM/test.cellrift.local in fd_psm_change_state@p_psm.c:284: 'STATE_OPEN' -> 'STATE_CLOSED'
```

`ECONNRESET` on an SCTP socket is an ABORT, not a graceful SHUTDOWN. The
test now asserts the error does *not* say "failed to arm", and reverting the
fix fails it.

**Still open, and now precisely stated:** the liveness step after the abort
does not succeed against this peer. The timeline shows Cellrift re-dialling
and completing a fresh CER/CEA (`CONNECTED TO ... soc#19`) — recovery works
— but the following UAR's answer never arrives.

> **Correction, 2026-09-29.** This paragraph originally continued:
>
> > Our read loop then reports its *last* error (`use of closed network
> > connection`) instead of the first, which hides the cause; that masking is
> > a separate defect worth fixing before this gap can be diagnosed properly.
>
> **That diagnosis was wrong.** No receive path in this codebase reports a
> last error over a first one — `nextMessage`, `receiveAnswer`,
> `receiveAnswers` and SIP's `waitForFinalResponse` all return their first
> error immediately, and `receiveFromHeldConn` performs a single read with no
> loop at all.
>
> What actually produced that string is `readFromSCTP`'s timeout error
> (`internal/protocols/common/sctp.go`), which was deliberately built to
> wrap **both** `net.ErrClosed` (so `isDeadConnErr` drops the dead
> association) and `os.ErrDeadlineExceeded` (so the timeout is classifiable).
> Driving that path directly prints:
>
> ```text
> sctp read: timed out after 50ms: use of closed network connection: i/o timeout
> ```
>
> It is a **timeout**, and it says so first — but the `net.ErrClosed` text
> sitting in the middle reads like a connection-state fault, and that is what
> it was mistaken for.
>
> **How the mistake was made:** the string was read off a failing run and
> explained by a plausible mechanism that was never checked against the code.
> Exactly the "should work / inherits the fix / same builder" category this
> file's own closing section warns about — an argument recorded as a finding.
>
> **What this changes:** there are two defects here, not one. The error text
> is genuinely misleading and worth fixing, but it is small, and fixing it
> will *not* make the liveness step pass — the UAR's answer still never
> arrives. The real cause of that is still unknown, and run 11 reproduces it before
> any remedy is proposed, rather than scoping a fix from a second unverified
> guess.

## CCR — not attempted, and here is why

**This harness** has no RFC 4006 Credit-Control peer to attempt it against.
The image ships `open5gs-pcrfd`, but a PCRF speaks **Gx** — command code 272
under Application-Id **16777238**, not the bare Credit-Control application
**4** Cellrift sends CCR under. Cellrift's own post-CEA application gate
would correctly refuse to send into a peer that never advertised app 4.

**Correction, added later:** this section originally said no such peer
existed at all. That was an overclaim about the ecosystem from a fact about
one Docker image. Open-source OCS implementations do exist — SigScale OCS
(Apache-2.0, Erlang) serves Ro/Gy, which *is* Credit-Control Application-Id
4, and ships Docker instructions. Closing CCR is therefore harness work of
the same kind as run 4's missing MSISDN, not the new-application feature
this entry first described. See `docs/todo_next.md` item 3.

## Status

| Command | Real peer | Transport |
| --- | --- | --- |
| CER/CEA, DWR/DWA | proven | TCP + SCTP |
| UAR/UAA (Cx) | proven | TCP + SCTP |
| AIR/AIA, ULR/ULA (S6a) | **proven, this run** | TCP + SCTP |
| MAR/MAA, SAR/SAA (Cx) | **proven, this run** | TCP + SCTP |
| CCR/CCA | untested: no OCS in this harness (candidates exist) | — |
| SCTP ABORT | chunk reaches the peer; liveness-after-abort still unproven | SCTP |

---

# Run 7 — 2026-09-24 (CI): the reaping window, caught in the act

The TCP leg passed all five commands. The SCTP leg failed on
`s6a_air_ulr.yaml`, and the peer's own log shows the request was never the
problem:

```text
'Authentication-Information-Answer'
  Command Code: 318   ApplicationId: 16777251
  AVP: 'Authentication-Info'(1413) vend='3GPP'(10415) val=(grouped)
     AVP: 'E-UTRAN-Vector'(1414) ... RAND / XRES / AUTN / KASME
  AVP: 'Result-Code'(268) val='DIAMETER_SUCCESS' (2001)
DROPPED 'Unable to forward answer to deleted / closed peer 'test.cellrift.local'.'
```

The HSS authenticated the subscriber, generated a full E-UTRAN vector,
stamped `2001`, and then **could not deliver it**. The client waited out its
timeout and reported a failure for a request the peer had already answered
correctly.

## Cause: one peer object, many processes

freeDiameter keeps a single peer object per `Origin-Host`. Each scenario in
the job runs as its own `cellrift` process, so each one tears down an
association and builds a new one. In the window where the old object is
being reaped, freeDiameter will accept the new association — it logs
`CONNECTED TO ... (SCTP,soc#9)` — while still routing that association's
first answer to the dying entry, which discards it.

This is the same quirk run 6 noted in passing ("a prior request on the same
association makes it reliable"). Run 6 underestimated it: a prior request
helps *within* one process, and does nothing for the next process's first
request.

Why the SCTP leg and not TCP: nothing about the protocol. The legs run on
different runners and the window is a race, so it is luck which one lands in
it. Treating this as "an SCTP problem" would be the wrong conclusion from a
single sample.

## Mitigation

`run-scenario.sh` now waits `PEER_SETTLE_SECONDS` (default 5) before each
scenario and retries once if it fails, announcing the retry loudly. The
retry is bounded at one and a genuinely broken scenario fails both attempts.

**Honesty about this fix:** the *window* could not be reproduced locally —
six runs with the settle removed entirely all passed — so the mitigation is
validated by the mechanism the peer's log states, not by a local
reproduction of the race.

What *was* verified locally, once disk space allowed it:

- **The retry path fires and still fails a real failure.** Pointed at a dead
  port, the runner logs `attempt 1`, announces `RETRYING`, logs `attempt 2`,
  then `INTEROP FAILED ... twice` and exits 1. A broken scenario cannot pass
  by being retried.
- **Both legs pass from a fresh harness with zero retries consumed**, so the
  settle is not papering over a permanent failure — TCP and SCTP each ran all
  five commands on the first attempt.

If the SCTP leg fails here again, the retry's announcement in the log is the
thing to read first: a failure on both attempts means something other than
this window.

A fix that removed the window rather than widening it would be one
association for the whole job, which means one `cellrift` process running
every command — worth doing if the retry proves insufficient.

---

# Run 8 — 2026-09-24: the retry disproved its own premise

Run 7 blamed a brief peer-reaping window and mitigated it with a settle and
one retry. The next CI run failed **both attempts**, which was the honest
test that theory needed — and it failed it. The peer's log again showed
answers it had built correctly and never delivered: a complete
`Authentication-Information-Answer` with an E-UTRAN vector, and an
`Update-Location-Answer` carrying full `Subscription-Data` (MSISDN, AMBR,
APN-Configuration-Profile), both `Result-Code 2001`.

## Actual cause: one peer object, three clients wearing one name

freeDiameter keeps a single peer object per `Origin-Host`. All three
scenarios advertised `test.cellrift.local`, and each ran in its own process,
so three sequential clients contended for one object. The peer accepted each
new association — logging `CONNECTED TO` — while still routing that
association's answers to the entry it was tearing down.

This was never a timing window to wait out. A settle cannot help, which is
exactly what the doubled failure showed.

## Fix: distinct identities

| Origin-Host | Scenario |
| --- | --- |
| `test.cellrift.local` | `uar_uaa.yaml` |
| `s6a.cellrift.local` | `s6a_air_ulr.yaml` |
| `cx.cellrift.local` | `cx_mar_sar.yaml` |

All three are declared as `ConnectPeer` entries. This is also better
modelling than the shared name: S6a traffic comes from an MME and Cx traffic
from an I-CSCF, which are different Diameter nodes.

Verified locally on both transports **with the settle set to zero and no
retries at all** — the mitigation is not doing the work, the fix is:

```text
=== sctp leg: 0 retries, all five commands
=== tcp  leg: 0 retries, all five commands
```

## The retry was removed, not kept as insurance

It never turned a red run green, and a bounded retry over a peer bug is
precisely the mechanism that converts a real failure into a passing build.
Keeping it "just in case" would trade this project's central property — that
a green result means something — for a quieter CI page.

One narrower change was kept: `No_SCTP` on the `ConnectPeer` lines, which
constrains the HSS's outbound dial attempts only. The global SCTP listener
stays on and inbound SCTP still reaches `STATE_OPEN`; nothing listens on
Cellrift's side, so those outbound attempts can only fail, and keeping them
off a retransmitting transport avoids churning the peer state machine.

---

# Run 9 — 2026-09-24 (CI): confirmed, and the clock is the proof

The SCTP leg at `175f102`, with distinct Origin-Hosts and no settle or retry
anywhere:

```text
17:44:17.745  OK over SCTP: stage 1 (CER/CEA reached I-Open) and stage 2 (UAR answered 2001) both passed
17:44:17.746  --- scenarios/diameter/s6a_air_ulr.yaml over SCTP
17:44:18.086  --- scenarios/diameter/cx_mar_sar.yaml over SCTP
17:44:18.429  OK over SCTP: AIR/ULR (S6a) and MAR/SAR (Cx) also answered by the real peer
```

**The timings are the confirmation, not the green tick.** Each scenario
completed in about 0.34 s. The runs that failed took ten seconds apiece —
two steps each burning a 5 s timeout while the HSS's correctly-built answers
went to a peer object it was reaping. Answers now arrive immediately, which
is what a fixed collision looks like and what a lucky pass would not.

It also rules out the reaping-window theory retroactively: this run has no
settle at all, so if a window existed it would have been at its widest here.

## Proven in CI on every push, both transports

| Exchange | Result |
| --- | --- |
| CER/CEA → `STATE_OPEN` | TCP + SCTP |
| UAR/UAA (Cx) | `Experimental-Result-Code 2001 DIAMETER_FIRST_REGISTRATION` |
| AIR/AIA, ULR/ULA (S6a) | `Result-Code 2001` |
| MAR/MAA, SAR/SAA (Cx) | answered by the real peer |

Five of six Diameter commands, against `gradiant/open5gs:2.8.0`. CCR remains
the exception — this harness runs no Credit-Control peer, since the image
ships none and its PCRF speaks Gx (see `docs/todo_next.md`, item 3).

---

# Run 10 — 2026-09-24: CCR/CCA against a real OCS

The last unproven Diameter command, and the one this file twice said could
not be proven. Both claims were wrong in different ways, and the peer was one
`docker pull sigscale/ocs` away.

## Result: PASSED

```text
OK: CCR-I and CCR-U answered 2001 by a real SigScale OCS over TCP
```

SigScale OCS is Apache-2.0 Erlang, serving the 3GPP Ro/Gy interfaces — which
are Credit-Control **Application-Id 4**, exactly what Cellrift sends CCR
under. No Gx work was needed; that earlier diagnosis was a wrong turn.

## Four corrections, all named by the peer

`3010 DIAMETER_UNKNOWN_PEER` first — the OCS accepts only registered clients,
the same lesson the HSS's `ConnectPeer` taught. Then, in a single answer, the
OCS reported three errors at once, which is more than freeDiameter ever did:

```text
errors: [{5001, ... 'Rating-Group', 100 ...},
         {5005, ... 'Auth-Application-Id' ...},
         {5005, ... 'Service-Context-Id' ...}]
```

- **`Auth-Application-Id` (258) missing.** Credit-Control is a *bare*
  application, so it carries this AVP where a vendor-scoped command carries
  `Vendor-Specific-Application-Id`. Now auto-injected by
  `appendAuthApplicationID`, which is deliberately the mirror image of
  `appendVendorSpecificApplicationID`: a command is one or the other, never
  both, and `TestCCR_AVPCodeSetIsPinned` now pins that CCR has 258 and no 260.
- **`Service-Context-Id` (461) missing.** Service data a client cannot
  derive, so the scenario declares it (`32251@3gpp.org`, TS 32.299's PS
  charging context). Added to the dictionary.
- **`Rating-Group` at the top level → 5001.** It belongs inside
  `Multiple-Services-Credit-Control` (RFC 4006 §8.16), which was also missing
  from the dictionary and is now a Grouped AVP with `Rating-Group` and
  `Requested-Service-Unit` as members.

Then `5030 DIAMETER_USER_UNKNOWN`: the message was finally well-formed and
the subscriber simply did not exist. Provisioning it took SigScale's TMF
chain — offering, product, service carrying the MSISDN, balance — now
scripted in `up-ocs.sh`, with the two shapes that cost a 400 each recorded in
comments (`price.taxIncludedAmount` is a string; a service's `product` is a
plain id, not an object).

## Harness

`up-ocs.sh` / `down-ocs.sh` / `run-ocs-scenario.sh`, and a second CI job. The
OCS is deliberately its own harness on port **3869**: the Open5GS HSS runs
with `--network host` and owns 3868, and keeping the peers separate means an
OCS that will not boot cannot fail the five commands the HSS already proves.

## What this does not prove

TCP only — the OCS is reached through a published container port, which
carries no SCTP; the SCTP path is proven against the HSS instead. Two CCRs of
one session, against one implementation. Cellrift sends no
`Requested-Service-Unit`, so this exercises the request/answer shape rather
than quota management or a mid-session credit re-authorisation.

## All six commands

| Command | Peer | Transport |
| --- | --- | --- |
| CER/CEA, DWR/DWA | Open5GS HSS | TCP + SCTP |
| UAR/UAA (Cx) | Open5GS HSS | TCP + SCTP |
| AIR/AIA, ULR/ULA (S6a) | Open5GS HSS | TCP + SCTP |
| MAR/MAA, SAR/SAA (Cx) | Open5GS HSS | TCP + SCTP |
| **CCR/CCA (Ro/Gy)** | **SigScale OCS** | **TCP** |

---

# Run 11 — 2026-09-30 (local): the liveness-after-abort gap, reproduced and captured

Runs 6 and 7 each explained this peer's behaviour with a mechanism nobody had
observed, and both were wrong. This run observes instead: a root packet
capture on the association, and the HSS's own timestamped log. It proposes no
fix and changes no production code.

## Result: CAUSE ESTABLISHED, with two open questions named separately

The naming was wrong. **This is not a liveness-after-abort failure.** The
abort is not the cause.

> **Credit where it is due — run 8 had this mechanism first, and this entry
> originally read as though it were new.** Run 8 (2026-09-24) already wrote
> "freeDiameter keeps a single peer object per `Origin-Host`" and "the peer
> accepted each new association — logging `CONNECTED TO` — while still routing
> that association's answers to the entry it was tearing down", and already
> concluded "this was never a timing window to wait out". Run 9 retired the
> reaping-window theory. The `DROPPED` line was logged as a known "peer quirk"
> earlier still, in the Cx identity section above.
>
> So the mechanism was six days old before this run began, and run 6's text
> went on blaming the abort anyway — the finding existed but was never
> connected back to the gap it explained. **That** is the actual failure here,
> and it is a duller one than a wrong diagnosis: a repository can hold the
> answer and the open question in the same file without noticing.
>
> **What this run genuinely adds**, none of which run 8 established:
> controlled disproof that the abort (e3), SCTP (e4) or a waiting window (e5)
> is the trigger; packet-level proof that no UAA exists on the wire at all,
> rather than inference from a client timeout; Hop-by-Hop correlation tying the
> received request to the dropped answer; the nondeterminism (e6 answered 1 of
> 3); and committed artefacts a reader can re-derive it from without CI access.

**Established, each with a control:**

- The liveness UAR reaches the HSS, is parsed, and the HSS **builds an answer
  and then drops it at routing time**, because freeDiameter routes the answer
  to a per-Origin-Host peer object it considers deleted or closed. Cellrift did
  not miss an answer: no UAA exists on the wire.
- The **trigger is a second association under the same Origin-Host.** A
  distinct registered Origin-Host on the second association is answered, 3 runs
  out of 3.
- It is **not the abort**: a run with no fault at all reproduces it (e3), and so
  does a graceful close (e2).
- It is **not SCTP**: it reproduces over TCP after a `reset` (e4).
- It is **not a waiting or reaping window**: a 35 s settle still drops (e5). Run
  7's "reaping window" theory should not be raised again.

**Not a rule.** "A second association under the same Origin-Host always fails"
is false: with a valid UAR, one of three runs was answered (e6 run 3).

**Open, stated separately so they do not soften the above:**

- (a) Why freeDiameter treats the name as closed while the new association is
  demonstrably alive and exchanging DWR/DWA on it.
- (b) What decides the e6 outcome, given two drops and one answer from the same
  scenario on a fresh peer each time.

CI confirmation: e3 reproduces on a clean GitHub-hosted runner (ubuntu-24.04).

**The artefacts are committed, not linked: [`evidence/run-11-e3/`](evidence/run-11-e3/).**
That directory holds the peer's own complete log, the per-chunk dissection, the
raw pcap, the scenario that ran, both Cellrift reports and the fork check —
about 48 KB. It carries [its own README](evidence/run-11-e3/README.md) with the
commands to re-verify every claim below, including re-dissecting the pcap from
scratch rather than trusting the CSV.

The run that produced them, for provenance only:

- Run: <https://github.com/riftcell/cellrift/actions/runs/36727082186> (run id 36727082186)
- Artifact: <https://github.com/riftcell/cellrift/actions/runs/36727082186/artifacts/11103401974> (artifact id 11103401974)
- HSS container start: `2026-09-30T14:12:15.598196541Z`

> **Both links are expected to stop resolving, and a 404 is not evidence the run
> never happened.** GitHub deletes Actions artifacts, and eventually run logs,
> once their retention window passes. That is precisely why the artefacts are in
> the tree. A citation a reader cannot follow has to
> carry its own content — the same rule this file applies to private working notes.
>
> An earlier confirmation of the same experiment ran at
> `2026-09-30T13:24:01Z` (run 36721197273) and is superseded by the one above,
> not contradicted by it: it predated the PLMN correction, so its scenario used
> `mcc234` where this one uses `mcc001`. Both produced the identical result —
> 2 requests, 1 answer, one `DROPPED` — which is itself a small negative
> control, since it shows the drop does not depend on `Public-Identity`.

- Rig integrity on the runner: `warnings: none`, no `used tcp` in `report.json`
  or `report-2.json`, and real SCTP chunk types in the capture (0 DATA, 1 INIT,
  2 INIT_ACK, 10 COOKIE_ECHO, 11 COOKIE_ACK, 14 SHUTDOWN_COMPLETE). Both
  scenario runs exited 1 (`run.exit` and `run-2.exit`), as expected.
- The runner's own image matches the local fork check exactly: counts 1 / 1 / 8
  for the three literals, `libfdcore.so.1.5.0`, Open5GS v2.8.0, Debian 11.
- One occurrence of `DROPPED 'Unable to forward answer to deleted / closed peer '`,
  and the wire correlates it with no fault injected anywhere:

```text
frame  9  t=1790777556.503532  43043->3868  cmd=300 req=True   hbh=0xa2ab6b0f  process 1 UAR
frame 11  t=1790777556.504221  3868->43043  cmd=300 req=False  hbh=0xa2ab6b0f  ANSWERED
          14:12:36.708  'STATE_OPEN' -> 'STATE_CLOSED' 'test.cellrift.local'   (hss.log:91)
          14:12:38.278  CONNECTED TO 'test.cellrift.local' (SCTP,soc#18)       (hss.log:93)
frame 25  t=1790777558.278296  57238->3868  cmd=300 req=True   hbh=0xcbc229d7  process 2 UAR
          14:12:38.280  DROPPED 'Unable to forward answer to deleted /
                        closed peer 'test.cellrift.local'.'                    (hss.log:210)
          14:12:43.279  'STATE_OPEN' -> 'STATE_CLOSED'                         (hss.log:244)

No req=False row exists for 0xcbc229d7 anywhere in the capture.
```

Two UARs, one UAA: process 1 answered on its own Hop-by-Hop, process 2's answer
dropped.

**Refinement, and a reading to avoid.** The runner's log shows a
`fd_msg_parse_or_error ... 'Bad message'` and `PARSING ERROR, returning:
'3GPP/User-Authorization-Answer'` (a bare 20-byte header) on **both** processes.
That parse error is **not** what closed the peer: process 1 hit the identical
error and still had its answer delivered (frame 11). It explains the 5005, the
missing Visited-Network-Identifier recorded above, and nothing about the drop.

What the timeline does add is a sequence. Process 1 connects, is answered and
exits normally, and freeDiameter logs `'STATE_OPEN' -> 'STATE_CLOSED'` for the
name `test.cellrift.local`. Process 2's fresh association is then accepted
(`CONNECTED TO ... soc#18`). Yet answer-routing, which looks the peer up by
name, finds an object that is not `STATE_OPEN` and drops. So the stale object
is left by the **previous process's ordinary teardown**, and the new
association's `CONNECTED` does not restore the name-keyed object that
answer-routing consults. This partially answers open question (a). It is
consistent with the answer-routing behaviour in freeDiameter master, but the
shipped fork was not diffed against master, so it is "consistent with", not
confirmed in the source. Question (b) is untouched by this run.

## Rig

```sh
sudo modprobe sctp          # does not survive a reboot
bash test/interop/capture-liveness.sh <e0|e1|e2|e3|e4|e5|e6>
```

`test/interop/capture-liveness.sh` runs one experiment per invocation against
a **fresh** `gradiant/open5gs:2.8.0` HSS (freeDiameter peer objects carry state
across client processes, run 8), captures with
`sudo tcpdump -i any -s 0 -U -w <exp>.pcap sctp` (`tcp port 3868` for e4),
dissects with `tshark -d sctp.port==3868,diameter`, and collects
`docker logs -t hss`. tshark 4.6.8 and tcpdump 4.99.6. Both containers run
`--network host`, so a host capture sees the whole association.

The script **exits 1 rather than grade** a run that measured the wrong thing.
Exit 0 means the artefacts are trustworthy, not that the scenario passed. Each
of the four gates was made to fire once, with its verbatim stdout and exit
code. A gate that has never fired is untested, and this repository already
shipped an ABORT assertion that stayed green over a capability that never
armed.

| Gate | Fired by | Verbatim stdout | Exit code |
| --- | --- | --- | --- |
| `/proc/net/sctp` absent | `SCTP_PROC_PATH=/nonexistent/sctp` (a test-only seam; `rmmod sctp` refuses with 16 users) | `LIVENESS RIG REFUSED: /proc/net/sctp is absent. Run 'sudo modprobe sctp' first - without it Cellrift substitutes TCP with a recorded warning and this run would produce a confident result about the wrong transport.` | 1 (no container started) |
| report carries `used tcp` | `CELLRIFT_TRANSPORT_FALLBACK=tcp` | `LIVENESS RIG REFUSED: this run asked for SCTP and Cellrift substituted TCP; nothing here says anything about SCTP (seen in /tmp/cellrift-liveness/e0/report.json)` | 1 |
| pcap has no SCTP packet | a stub `tshark` first on `PATH` that prints nothing | `LIVENESS RIG REFUSED: the capture contains no SCTP packet - a pcap with no SCTP proves nothing about SCTP` | 1 |
| no report written | a stub `go` first on `PATH` that exits 1 | `LIVENESS RIG REFUSED: cellrift wrote no report - this is a build or startup failure, not a result about the peer` | 1 |

The `used tcp` gate checks every `report*.json`, because e3 writes two. A
refusal exits before teardown so the artefacts survive, so `down.sh` was run
after each proof, the stubs were removed and verified gone, and a clean e0 was
re-run afterwards and exited 0.

Per experiment, `/proc/net/sctp` was present (not required for e4, which is
TCP on purpose), no report carried `used tcp`, and every experiment booted a
fresh container:

| Exp | Container start (UTC) |
| --- | --- |
| e0 | 2026-09-30T09:14:10.532547307Z |
| e1 | 2026-09-30T09:16:34.963141109Z |
| e2 | 2026-09-30T09:17:05.092209222Z |
| e3 | 2026-09-30T09:16:54.366146666Z |
| e4 | 2026-09-30T09:17:15.977164278Z |
| e5 | 2026-09-30T09:18:54.892080415Z |
| e6 (last of 3) | 2026-09-30T09:18:24.908092648Z |

## Which freeDiameter the peer runs

`open5gs-hssd -v`: Open5GS v2.8.0, on Debian GNU/Linux 11. The library is
`/opt/open5gs/lib/x86_64-linux-gnu/libfdcore.so.7`. The image has no
`strings`, so literals were counted with `grep -ao | wc -l` against that file:

| Literal | Count |
| --- | --- |
| `Unable to forward answer to deleted / closed peer` | 1 |
| `Invalid state to receive a new connection attempt` | 1 |
| `STATE_OPEN` | 8 |

All three are present in the binary that actually ran, so the statements below
about the answer-drop path are about this build, not upstream master. No claim
is made about the "Invalid state" path: it never appeared in any log.

## What the wire shows

Citations are line numbers of each experiment's `chunks.csv` (line 1 is the
header, so frame F is line F+1).

**e0, the baseline (`sctp_abort.yaml`, unmodified).** Association 1 is client
port 35492 (`chunks.csv:2`), ended by an ABORT chunk (`chunks.csv:11`).
Association 2 is port 47379 (`chunks.csv:14`). Tags: association 1 client tag
0x0b0d40b5, peer tag 0xff4c3cb5; association 2 client tag 0x170ca494, peer tag
0xc4318153. A different 4-tuple and different tags, so this is a plain new
association and the RFC 4960 section 5.2.4 restart rules do not apply.

- Q-A, did the UAR leave and get acknowledged: yes. The liveness UAR is frame
  21, `chunks.csv:22`: port 47379 to 3868, TSN 1, stream 0, PPID 0, command 300
  request, Hop-by-Hop 0x599daf6c, after a full INIT / INIT-ACK / COOKIE
  exchange and CER/CEA. The peer's SACK with cumulative TSN ack 1 is frame 22,
  `chunks.csv:23`.
- No UAA exists toward the client. The only command-300 rows in e0 are
  `chunks.csv:10` and `chunks.csv:22`, both requests.

**Teardown boundary.** The liveness UAR is at 1790759652.2179, so its 5000 ms
budget ends at 1790759657.2179. Frame 35 (`chunks.csv:36`, t=1790759657.220119)
is a **SHUTDOWN sent by the client**. That SHUTDOWN, and every frame after it,
is Cellrift's own close: `readFromSCTP` closes the connection itself when its
read times out. It is not peer behaviour, and no claim in this run rests on a
frame at or after 35. Frames 29-34 are HEARTBEAT / HEARTBEAT_ACK, kernel
keepalive.

**e1, distinct identity for the liveness step.** Liveness UAR frame 19,
`chunks.csv:20`, port 46295, Hop-by-Hop 0xed852f80. The peer's answer is on the
wire at frame 22, `chunks.csv:23`: DATA TSN 2, stream 1, PPID 46, command 300,
request flag false, same Hop-by-Hop. Its SHUTDOWN at frame 23 is Cellrift's
close after receiving it.

**e3, no fault, two processes 1 s apart, same Origin-Host.** Process 1's answer
is on the wire (`chunks.csv:13`, frame 12). Process 2's UAR is frame 24,
`chunks.csv:25` (port 43081, Hop-by-Hop 0x57a0d699), and no answer follows it.

**e4, TCP plus reset.** Liveness UAR frame 17, `chunks.csv:18`, port 55530.
No answer row: the only command-300 rows are requests, `chunks.csv:9` and `:18`.

**e2, graceful close.** Step 1 ends with a SHUTDOWN (`chunks.csv:12`). Liveness
UAR `chunks.csv:24`, port 56426, no answer row.

## What the peer logged

Collected with `docker logs -t hss`; ANSI colour stripped. e0, association 2,
showing the request was received, answered and the answer dropped:

```text
hss.log:8   09:14:12.216966 CONNECTED TO 'test.cellrift.local' (SCTP,soc#8)
hss.log:9   09:14:12.217066 pid:PSM/test.cellrift.local ... 'STATE_OPEN' -> 'STATE_CLOSED' 'test.cellrift.local'
hss.log:161 09:14:12.217831 CONNECTED TO 'test.cellrift.local' (SCTP,soc#19)
hss.log:162 09:14:12.218145 Conflicting rule: the number of occurences (0) is < the rule min (1) for 'Visited-Network-Identifier'.
hss.log:166 09:14:12.218189 PARSING ERROR: 'DIAMETER_MISSING_AVP' from 'test.cellrift.local'
hss.log:244 09:14:12.218638 ROUTING ERROR 'Unable to forward answer to deleted / closed peer 'test.cellrift.local'.'
hss.log:278 09:14:12.218721 DROPPED 'Unable to forward answer to deleted / closed peer 'test.cellrift.local'.'
hss.log:312 09:14:17.220331 pid:PSM/test.cellrift.local ... 'STATE_OPEN' -> 'STATE_CLOSED' 'test.cellrift.local'
```

- Q-B, did the peer receive it: yes. The request dump with Hop-by-Hop
  `0x599DAF6C` is at `hss.log:180`, in the same millisecond as the frame.
- Q-C, did it answer and what became of the answer: the HSS **built** a
  DIAMETER_MISSING_AVP answer with that Hop-by-Hop (dump at `hss.log:224`) and
  **dropped** it (`hss.log:244`, `:278`). Only two `STATE_` transitions occur
  in e0: the ABORT at :9 and Cellrift's own close at :312, 5 s later. There is
  no unexplained transition on the new association.
- The aborted first UAR's answer is dropped with the same message
  (`hss.log:93`, `:127`). That one is unsurprising, its association being gone.

e1 shows the same two drops (`hss.log:132`, `:166`) naming
`'test.cellrift.local'`, the first step's identity, and **no drop** for
`cx.cellrift.local`: its request was parsed at `hss.log:204` and answered on
the wire.

The same drop line appears with the same text in e2 (`hss.log:244`, `:278`),
e3 for process 2 (`hss.log:176`, `:210`) and e4 over TCP (`hss.log:246`,
`:280`).

## The controls

**e1, distinct Origin-Host for the liveness step.** The liveness step
re-handshakes as `cx.cellrift.local`, already a registered `ConnectPeer`. The
UAA arrives, 3 runs out of 3. The step still fails its `== 2001` assertion
(see the finding below), but it fails with an answer and not a timeout. This
eliminates everything about the association, the ABORT and the capture
timing, and isolates the reused identity. **The harness as first written
measured nothing here:** the adapter prefers `origin_host` from the scenario's
`protocols` block over a step's `Origin-Host` field, so the peer log showed
`CONNECTED TO 'test.cellrift.local'` on the second association. The script
was corrected so the block declares no identity and each step names its own.

**e3, no fault at all.** A one-step UAR run twice in two separate processes 1 s
apart, both as `test.cellrift.local`, with no impairment. Process 1 is
answered; process 2's answer is built and dropped (`hss.log:176`, `:210`). This
eliminates the abort as a cause and eliminates "a stale connection-error event
closing the new association": nothing was faulted, and the new association
stays open until Cellrift closes it.

**e2, graceful close instead of an ABORT.** Same drop. e2 used a
transport-level SCTP SHUTDOWN, which is **not** a Diameter DPR.

**e4, TCP plus reset (`CELLRIFT_TRANSPORT_FALLBACK=tcp`).** Same drop
(`hss.log:246`, `:280`). Not SCTP.

**e5, a 35 s settle between the steps.** Same drop (answer built and dropped
at 09:19:31.582). 35 s is past the HSS's 30 s connect-retry timer, so a waiting
or reaping window is disproved. The peer did act on its own inside the window:
at 09:19:25.751 it logged `fd_p_ce_msgrcv ... Invalid argument` and
`DROPPED 'Internal error ('test.cellrift.local'): unhandled message.'` for a
Capabilities-Exchange-Answer. No SCTP frame carries that CEA (the capture
filter is `sctp`, and the `ConnectPeer` is `No_SCTP`), so its origin is UNKNOWN.

**e6, a valid UAR.** e0 with the `Visited-Network-Identifier` this HSS requires
and `uar_uaa.yaml`'s identities, run three times against a fresh peer each
time. Runs 1 and 2 dropped both answers. Run 3 dropped the aborted UAR's answer
and **answered** the liveness UAR (`chunks.csv:30`, a command-300 answer with
the liveness Hop-by-Hop 0x08f216d9; the step passed). So the same scenario
gives different outcomes, which is why the result above carries no "always".
One earlier e6 run lost a different race (the second association's CER got no
CEA and the HSS sent a SHUTDOWN); that run's artefacts were overwritten and are
not relied on.

## Candidates ruled out

- The UAR never reached the wire: ruled out, `chunks.csv:22` SACKed at `:23`.
- The UAR was SACKed but never delivered to the application: ruled out, parsed
  at `hss.log:166`.
- The peer answered and Cellrift missed it: ruled out. No UAA toward the client
  in e0, e2, e3 (process 2) or e4. Where an answer is on the wire (e1, e6 run
  3) Cellrift received it.
- A stale connection-error event closing the new association: ruled out by e3
  (no fault) and by e0's only `STATE_` lines being `hss.log:9` and `:312`.
- The aborted UAR's pending answer interfering: ruled out as **necessary**. e3
  has no aborted UAR and still drops, and e0's second association carries no
  DATA from 3868 with the first UAR's Hop-by-Hop. It is not ruled out as a
  contributor to e6's variance.
- A waiting or reaping window: ruled out by e5.

## What remains open

- (a) and (b) above.
- **An untested lead: Diameter-level disconnect.** Cellrift has no DPR/DPA
  support: command code 282 (Disconnect-Peer-Request, RFC 6733 section 5.4) is
  not defined anywhere in `internal/protocols/diameter/`. Cellrift never sends
  a Diameter-level disconnect; it only tears down the transport. Section 5.4
  says a node closing a connection SHOULD send DPR and await DPA.
  - It **plausibly** addresses the clean-reconnect case (e3: no fault, same
    Origin-Host, still dropped), which is what a user hits running two
    scenarios back to back, and e6's nondeterminism is consistent with a race a
    clean DPR exchange would make deterministic.
  - It **cannot** address the deliberate-fault cases (e0, e2, e4): a fault
    cannot be gracefully announced. For fault injection the only client-side
    lever seen is a distinct Origin-Host, which e1 shows works.
  - It is **untested**. e2 was a transport SHUTDOWN, not a DPR, so DPR has never
    been tried. It is a lead, not a fix, and it is not implemented here.

## Findings recorded, deliberately not fixed

**`scenarios/diameter/sctp_abort.yaml` can never pass its own assertion against
this HSS.** Both its UARs omit `Visited-Network-Identifier`, which the HSS
requires (`hss.log:11`: "the number of occurences (0) is < the rule min (1) for
'Visited-Network-Identifier'"). Every UAR in it is answered 5005
DIAMETER_MISSING_AVP, so `response.Experimental-Result-Code == 2001` could not
pass even with a delivered answer (e1 and e3 show answers arriving as 5005). It
is **left unfixed on purpose**: changing the scenario would change what e0
measured. e6 is the same scenario with the AVP added.

**Reusing one Origin-Host is a real user-facing limitation of Cellrift, not a
harness quirk.** Any user pointing Cellrift at freeDiameter-based
infrastructure and reconnecting under the same identity can hit this, with no
fault injected: e3 is two ordinary back-to-back runs. The identity is the
variable; the workaround observed is a distinct registered Origin-Host per
association (run 8's fix, and e1).

**Incidental.** Cellrift's DATA chunks carry PPID 0 on stream 0
(`chunks.csv:22`), where the HSS uses PPID 46 on stream 1 (e1 `chunks.csv:23`).
No evidence it matters.

## What this does not prove

One peer, one image version (`gradiant/open5gs:2.8.0`), one host, loopback only.
Single runs except e1 (3) and e6 (3), so nothing here measures a rate. The
cause inside freeDiameter is **not** known: the log states what the routing
step did, not why it considers the name closed. What would settle (a) is either
a freeDiameter build with debug logging of the peer-lookup at answer-routing
time, or a DPR-before-close run, which is the untested lead above. (b) needs
repeated runs with the first association's close ordering recorded.

## Status

| Command | Real peer | Transport |
| --- | --- | --- |
| CER/CEA, DWR/DWA | proven | TCP + SCTP |
| UAR/UAA, AIR/AIA, ULR/ULA, MAR/SAR (Cx, S6a) | proven | TCP + SCTP |
| CCR/CCA | proven (run 10) | TCP |
| SCTP ABORT | chunk reaches the peer (run 6) | SCTP |
| A second association under one Origin-Host | **answer built and dropped by the peer; established, cause inside freeDiameter open; not caused by the abort or by SCTP** | SCTP + TCP |

---

# Run 12 — 2026-10-01 (local, then in CI): the first automated SIP run against software Cellrift did not write

A real third-party SIP stack, Asterisk 22.10.1 with `chan_pjsip`, answers
Cellrift's REGISTER and INVITE automatically on every push, over UDP and over
TLS with the certificate verified. **Asterisk is not a P-CSCF.** This run proves
nothing about IMS semantics, so every existing `MOCK-ONLY SCENARIO` marker
stays, and the three scenarios it ran carry a positive
`PROVEN AGAINST: Asterisk (non-IMS SIP)` header instead, enforced by Guard D.
The hand verification recorded in `docs/getting_started_asterisk.md` was against
"Asterisk 18+"; the pinned peer is 22.10.1, which is newer, so this is a new
result and not a continuation of that one.

## Result: PASSED — Asterisk answered Cellrift's REGISTERs and INVITEs, but Cellrift did not ACK the 200 and the BYE went out regardless, so no dialog was acknowledged from the peer's side

Every pass of every scenario exited 0 (six exits: three scenarios, two passes
each), locally and on a GitHub-hosted runner, and the harness printed:

```text
OK: asterisk answered register_impaired, basic_call (udp/15060) and basic_call_tls with certificate verification ON (tls/15061); the peer's own log confirms it received each scenario's requests
```

That last clause is the harness's own wording and is limited to receipt: it
means the peer's log shows each scenario's requests arriving. It says nothing
about an acknowledged dialog. What the peer's log does show is under
"What the peer logged", and what it does not show is under "What this does not
prove".

## Rig

```sh
bash test/interop/up-sip.sh
bash test/interop/run-sip-scenario.sh; echo "exit=$?"
bash test/interop/down-sip.sh
```

- **Peer:** `andrius/asterisk:22.10.1_debian-trixie`, pinned as
  `@sha256:212326bd3f973a597be9e4747be5d6653802e4950159e95aa7d728e394de2012`.
  That is the multi-arch **index** digest, which is what `docker pull` prints
  and what was pinned. A second digest exists for the same tag, the amd64
  **manifest** digest (it begins `sha256:f4f36f06`; its full value was not
  recorded). They are not interchangeable and the index digest is the pinned one.
  The pin was exercised by a cold pull on the runner (see "In CI").
- **Version:** locally, `Asterisk 22.10.1 built by root @ buildkitsandbox on a x86_64
  running Linux on 2026-09-30 14:28:01 UTC` from `core show version`; the same
  version is in the log's startup banner (`asterisk.log:3`) and in every
  `Server: Asterisk PBX 22.10.1` header.
- **Docker:** 29.8.1 locally. The runner's Docker version was not recorded in
  the artefacts.
- **Network:** `--network host`, `chan_pjsip` only (`chan_sip` was removed in
  Asterisk 21). Ports **15060** (UDP) and **15061** (TLS), not 5060/5061: a
  developer machine often runs its own Asterisk on the defaults, and a
  host-networked container then fails to bind, still logs `Asterisk Ready.`, and
  the scenarios run against the other server.
- **Readiness** is the container's own `pjsip show transports` listing both bind
  addresses, never the `Ready` line, plus a check that the log carries no
  `Transport ... could not be started` line, then `pjsip set logger on` so the
  peer's own log carries every request.
- **TLS:** a per-run CA and a server certificate with
  `subjectAltName = IP:127.0.0.1, DNS:localhost` (Cellrift has no `server_name`,
  so Go verifies against the dial host and does not fall back to CN),
  `method=tlsv1_2`, and separate `cert_file` and `priv_key_file` (a combined PEM
  fails the handshake with `no shared cipher`). The TLS scenario is a derived
  copy of the shipped `basic_call_tls.yaml` whose `insecure_skip_verify` line is
  replaced by `ca_file`; the shipped file is never edited.
- **Two passes per scenario.** The harness runs each scenario **twice** against
  the peer: pass 1 for the terminal rendering, pass 2 for the JSON report. They
  are separate runs because `--output` replaces stdout and one reporter is
  selected per run, so no single invocation yields both. The peer therefore
  receives each scenario's requests twice and `asterisk.log` holds two passes.
  Per-pass counts are recorded in `<name>.receipts` and `<name>.receipts.pass2`.

## What the peer logged

**Per pass, and cumulative, side by side.** Per pass (each `.receipts` and
`.receipts.pass2` file reads the same number): `register_impaired` **3**
requests, `basic_call` **2**, `basic_call_tls` **2**. Cumulative over both
passes, as `peer-counts.txt` and the log count them: **10** requests from UDP
(3 + 3 + 2 + 2) and **4** from TLS (2 + 2), 14 in all. Every statement about one
scenario below uses the per-pass figure. The same numbers were observed locally
(four cycles) and on the runner.

All line numbers refer to
[`asterisk.log`](evidence/run-12-asterisk/asterisk.log) from the CI artifact.

**REGISTER, `register_impaired`, pass 1: three single, ordinary REGISTERs**
(requests at `:209`, `:237`, `:263`; the log's second pass follows at `:289`,
`:315`, `:341`), each answered `200 OK` once:

```text
asterisk.log:211  Via: SIP/2.0/UDP [::]:53671;branch=z9hG4bK108173888208940624
asterisk.log:231  Contact: <sip:447700900123@10.0.0.1:5060>;expires=3599
asterisk.log:232  Server: Asterisk PBX 22.10.1
asterisk.log:236    == Endpoint 447700900123 is now Reachable
```

Call-ID, CSeq and Via branch of each response match its request (for the first:
Call-ID `:215`/`:226`, CSeq `:216`/`:229`, Via `:211`/`:225`).

**INVITE, `basic_call`, pass 1 (UDP):** request at `:367` from
`UDP:127.0.0.1:55164`, then the peer's own responses:

```text
asterisk.log:395  SIP/2.0 100 Trying
asterisk.log:409  SIP/2.0 180 Ringing
asterisk.log:423  SIP/2.0 200 OK
asterisk.log:441  m=audio 11640 RTP/AVP 0 8 101
asterisk.log:442  a=rtpmap:0 PCMU/8000
asterisk.log:450  <--- Received SIP request (393 bytes) from UDP:127.0.0.1:55164 --->   (the BYE)
asterisk.log:464  SIP/2.0 200 OK                                                       (answering the BYE)
```

The 180 is a **180**, not a 183; the dialplan runs `Ringing()` then `Wait(1)`
then `Answer()` (`:406`, `:407`, `:421`), so the 200 arrives one second after the
180, which is the 1004 ms in `basic_call.stdout`.

The peer's log also holds the 200 to this INVITE sent again to the same source
port at `:532`, `:670` and `:921`. The gaps between consecutive sends roughly
double — 0.5 s, then 1 s, then 2 s (timestamps 02.799, 03.299, 04.299, 06.298),
so the offsets from the first 200 are +0.5 s, +1.5 s and +3.5 s. The doubling is
the point; an earlier draft of this sentence gave the intervals but said "after
the first", which named the wrong quantity.
The peer does that for a 200 that is never acknowledged (RFC 3261 §13.3.1.4),
and the missing ACK is what it records; see the next section.

**No ACK:** `grep -c 'Received SIP request (.*ACK' asterisk.log` is **0**. The
trap in re-checking it is real: a bare `grep -c ACK asterisk.log` is **14**, and
every one of those is the word `ACK` inside an `Allow:` header on a response the
peer sent.

**TLS, `basic_call_tls`, pass 1:** the INVITE arrives over TLS, with certificate
verification on:

```text
asterisk.log:615  <--- Received SIP request (742 bytes) from TLS:127.0.0.1:47790 --->
asterisk.log:643  SIP/2.0 100 Trying
asterisk.log:657  SIP/2.0 180 Ringing
asterisk.log:727  SIP/2.0 200 OK
asterisk.log:754  <--- Received SIP request (398 bytes) from TLS:127.0.0.1:47796 --->   (the BYE)
asterisk.log:768  SIP/2.0 200 OK                                                       (answering the BYE)
```

The derived scenario ([`basic_call_tls.verified.yaml`](evidence/run-12-asterisk/basic_call_tls.verified.yaml))
carries a live `ca_file: /tmp/cellrift-sip-certs/ca.crt` and no live
`insecure_skip_verify` key (the word appears five times, in comments and prose),
and neither output carries a verification warning.

**Measured values, `register_impaired`:** `impair_delay_ms` is 652 in the
terminal pass and 691 in the JSON pass for step 1 (random jitter, so the passes
differ), and 200 for step 3. Locally the step-1 values were 628, 655 and 672.

## The gates, each fired once

`run-sip-scenario.sh` and `up-sip.sh` exit 1 rather than grade a run that
measured the wrong thing. A gate that has never fired is untested, and this
repository has already shipped an assertion that stayed green over a capability
that never armed. All 20 `refuse` call sites were made to fire (counted with
`grep -n 'refuse "'`: 5 in `up-sip.sh`, 15 in `run-sip-scenario.sh`; 20 rows
below). The firing commands, full output and post-refusal controls are in
[`gate-proofs.txt`](evidence/run-12-asterisk/gate-proofs.txt); no gate is
recorded as unfired and none is recorded as unreachable. The stdout column is
the scripts' output quoted unaltered, except that the machine-specific scratch
directory the probes ran in is shown as `<scratch>`.

| Gate | Fired by | Verbatim stdout | Exit code |
| --- | --- | --- | --- |
| certificate SAN self-check (`up-sip.sh:75`) | up-sip copy with SAN rewritten to DNS:wrong.invalid (section U5) | `SIP RIG REFUSED: the server certificate has no IP:127.0.0.1 SAN, so it would not verify against a 127.0.0.1 target` | 1 |
| bind failure in the log (assert_no_bind_failure) (`up-sip.sh:108`) | a third transport colliding on 15062 (section U2) | `SIP RIG REFUSED: a transport failed to bind (port collision on 15060/15061?). The peer still logs readiness in this state, so its startup message is not a readiness signal, and scenarios would hit whatever else holds the port.` | 1 |
| required transports not listed (wait_for_transports) (`up-sip.sh:131`) | a bind conflict on 15060; a missing TLS cert_file (section U1, U3) | `SIP RIG REFUSED: transports never showed 0.0.0.0:15060 and 0.0.0.0:15061 in 'pjsip show transports'` | 1 |
| endpoints absent (wait_for_endpoints) (`up-sip.sh:144`) | pjsip-reject.conf with EXPECT_ENDPOINTS unset (section U4) | `SIP RIG REFUSED: endpoints 447700900123 and 447700900001 are absent from 'pjsip show endpoints'` | 1 |
| SIP logger not confirmed (`up-sip.sh:155`) | docker stop before enable_sip_logger (section U6) | `SIP RIG REFUSED: the peer did not confirm its SIP logger is on (got: Error response from daemon: container c9519b7712bcddceb20c48a1fdc7402305ecddc1a2b2fe2612a1b0057591df5e is not running)` | 1 |
| gate_peer_is_ours (`run-sip-scenario.sh:91`) | a non-pinned container named asterisk (section A1) | `SIP RIG REFUSED: the running 'asterisk' container is not the pinned image this harness booted (got: alpine:3), so nothing graded here would be about the intended peer` | 1 |
| gate_transports_bound (run time) (`run-sip-scenario.sh:102`) | docker stop between boot and run (section B2) | `SIP RIG REFUSED: 'pjsip show transports' does not list both 0.0.0.0:15060 and 0.0.0.0:15061; a run now would be graded against an unbound transport` | 1 |
| scenario file unreadable (`run-sip-scenario.sh:129`) | UDP_SCENARIOS naming an absent file (section A3b) | `SIP RIG REFUSED: scenario file scenarios/ims/does-not-exist.yaml is not readable; there is nothing to aim at the peer` | 1 |
| gate_no_adversarial_scenario, adversarial branch (`run-sip-scenario.sh:131`) | UDP_SCENARIOS=malformed_sip_headers.yaml; peer request count 0 before and after (section A3) | `SIP RIG REFUSED: scenarios/ims/malformed_sip_headers.yaml contains a malform: block or an action: raw step; adversarial traffic must never be aimed at a real peer` | 1 |
| gate_no_adversarial_scenario, marker-absent branch (`run-sip-scenario.sh:133`) | UDP_SCENARIOS=register_deregister.yaml (section A2) | `SIP RIG REFUSED: scenarios/ims/register_deregister.yaml carries no '# PROVEN AGAINST: Asterisk (non-IMS SIP)' marker, so it is not one of the scenarios proven against this peer` | 1 |
| check_ca_present (`run-sip-scenario.sh:139`) | CA_CRT=/nonexistent/ca.crt (section A4) | `SIP RIG REFUSED: CA file /nonexistent/ca.crt is missing or empty; up-sip.sh was not run or wrote elsewhere, and the TLS leg cannot verify anything` | 1 |
| derive live-key pre-check (`run-sip-scenario.sh:159`) | a source copy with zero live keys (section A5) | `SIP RIG REFUSED: expected exactly one live insecure_skip_verify: true line in <scratch>/tlsprobe/zero-live.yaml, found 0; the derivation below would change the wrong number of lines` | 1 |
| derive live-key post-check (`run-sip-scenario.sh:163`) | a source copy with a second, un-substituted key (section A6) | `SIP RIG REFUSED: the derived TLS scenario still carries a live insecure_skip_verify key` | 1 |
| derive missing ca_file (`run-sip-scenario.sh:165`) | CA_CRT in a directory whose name contains & (section A8) | `SIP RIG REFUSED: the derived TLS scenario does not contain ca_file: /tmp/cellrift-amp&probe/ca.crt` | 1 |
| gate_cellrift_ran (`run-sip-scenario.sh:204`) | a stub go that exits 1 (section B1) | `SIP RIG REFUSED: cellrift produced no terminal output or no JSON report for register_impaired; that is a build or startup failure, not a result about the peer` | 1 |
| gate_peer_received (`run-sip-scenario.sh:235`) | SIP_UDP_TARGET=127.0.0.1:15070; also the wrong-CA harness leg (Probe B) (section B3) | `SIP RIG REFUSED: the peer's own log shows no request received over UDP during register_impaired. The target was wrong, the peer was not listening, OR the transport never completed (for TLS, the handshake, e.g. a rejected certificate). See /tmp/cellrift-sip/register_impaired.stdout for which.` | 1 |
| gate_tls_verified, warning branch (`run-sip-scenario.sh:252`) | GATE_TLS_WARNING_SOURCE seam fed the real warning (section B4) | `SIP RIG REFUSED: the TLS leg ran without certificate verification (<scratch>/insecure.stdout carries the warning); it proves only that the transport works` | 1 |
| gate_tls_verified, live-key re-check (`run-sip-scenario.sh:255`) | a tamper wrapper rewriting the derived file after the run (section A7) | `SIP RIG REFUSED: the derived TLS scenario has a live insecure_skip_verify after the run` | 1 |
| grade, peer did not pass (`run-sip-scenario.sh:264`) | real Asterisk with no endpoints (a grade failure, 401) (section Probe A) | `SIP RIG REFUSED: the real peer did not pass register_impaired (exit 1); this is a result about the peer, not the rig` | 1 |
| grade, passes disagree (`run-sip-scenario.sh:266`) | a wrapper failing only the JSON pass (section B5) | `SIP RIG REFUSED: the terminal pass of register_impaired exited 0 but the JSON pass exited 1; the two passes disagree` | 1 |

Two rows were found by the probes and not by reading. The row for line 165 had
been argued unreachable; a CA path containing `&` makes it fire, because `&` in a
`sed` replacement is the matched text. And a failed transport is absent from
`pjsip show transports` while Asterisk still logs `Asterisk Ready.`, so the
`wait_for_transports` gate fires first for a bind collision and
`assert_no_bind_failure` fires only for a third transport.

**The two failure probes:**

- **Rejecting peer** (`pjsip-reject.conf`, no endpoints): the real Asterisk
  answered `401`; the failure came from `grade` at `run-sip-scenario.sh:264`, not
  from a refusal, exit 1, receipt delta 3, so `gate_peer_received` did not mask it.
- **Wrong CA:** the TLS scenario aimed at the real peer with a CA that did not
  sign its certificate failed with
  `tls: failed to verify certificate: x509: certificate signed by unknown authority`
  and exit 1 in all three output formats
  ([`wrongca-terminal.txt`](evidence/run-12-asterisk/wrongca-terminal.txt),
  [`wrongca-report.json`](evidence/run-12-asterisk/wrongca-report.json),
  [`wrongca-report.xml`](evidence/run-12-asterisk/wrongca-report.xml)). Before
  this run the JSON reporter dropped that cause; it now carries it (see the
  findings below). The harness leg refuses at `gate_peer_received` with a delta of
  0, which is not a certificate diagnosis.

**Adversarial traffic stays away from the peer.** `malformed_sip_headers.yaml`
named in `UDP_SCENARIOS` is refused at `run-sip-scenario.sh:131`, and the peer's
`Received SIP request` count was 0 before and 0 after.

## Guard D, and its five mutations

`scripts/check-scenario-labels.sh` gained Guard D: a scenario the sip-interop
harness runs must carry `PROVEN AGAINST: Asterisk (non-IMS SIP)` as a header
line, carry no `MOCK-ONLY SCENARIO` marker, declare no `malform:` block or
`action: raw` step, and appear in the harness's scenario list. A guard nobody has
seen fail is indistinguishable from no guard, so each condition was made to fail
([`guard-d-mutations.txt`](evidence/run-12-asterisk/guard-d-mutations.txt),
verbatim output):

| Mutation | Expected | Observed exit code |
| --- | --- | --- |
| 1. the `PROVEN AGAINST` header deleted from `register_impaired.yaml` | red, missing header | 1 |
| 2. the marker moved from a header into the description (anchoring proof) | red, missing header | 1 |
| 3a. an `action: raw` step added to `basic_call.yaml` | red, adversarial step and missing mock-only header | 1 |
| 3b. the same, with a `MOCK-ONLY SCENARIO` header added to silence Guard B | red, a file cannot carry both markers | 1 |
| 5. `basic_call_tls.yaml` removed from `TLS_SCENARIO` but still mentioned elsewhere in the script (drift cross-check) | red, marker and scenario list diverge | 1 |
| control: clean tree | green | 0 |

Guards A, B and C were compared byte for byte against the pre-Guard-D output with
no difference. **Guard A's list and every existing `MOCK-ONLY SCENARIO` marker are
unchanged.** Removing one on this evidence would have restated a claim more
strongly than it supports: the markers exist because the `register_*` scenarios
have no real P-CSCF to answer their SIP halves, and Asterisk is not one.

## In CI

- **Run:** <https://github.com/riftcell/cellrift/actions/runs/36902813507>, at
  commit `1b694734805f06105e624243c86fe0f7e4a0f60d`. Artifact
  `sip-interop-evidence` (id 11182372217, 25 files, 33,496 bytes, zip sha256
  `ed88395967f3de706563e1be448716dce67e71dacd7c5630d4f168a565b6ffc8`), whose text
  files are committed under `evidence/run-12-asterisk/`.
- **Runner:** GitHub-hosted `ubuntu-24.04`, image version `20260927.320.1`
  (Azure eastus, runner 2.337.0).
- **Conclusion:** all four jobs green: `diameter-interop` on both transports,
  `credit-control-interop` and `sip-interop`. The two Diameter jobs were
  unaffected by the new job.
- **Timing:** `up-sip.sh` 17:54:24 to ready 17:54:33 UTC (9 s, including a cold
  image pull); scenarios 17:54:33 to 17:55:06 (33 s).
- **The digest pin was proven by a cold pull.** The job log (not part of the
  committed artefacts) shows `Unable to find image ... locally`, a pull, then
  `Digest: sha256:212326bd3f973a597be9e4747be5d6653802e4950159e95aa7d728e394de2012`.
- **Job-log lines** (also from the job log): `sip interop harness ready: asterisk
  on udp/15060 and tls/15061, certs in /tmp/cellrift-sip-certs, sip logger on`,
  then the `OK:` line quoted under the result.
- **Observations identical to the local runs:** the six exits, the per-pass
  counts 3, 2 and 2, the 10 UDP and 4 TLS requests, `100 Trying`, `180 Ringing`,
  `200 OK` for each INVITE, zero ACK requests.
- **`impair_delay_ms`** against its bounds (`register_impaired.yaml` asserts
  `>= 300` for step 1 and `>= 200` for step 3): 652 (terminal pass) and 691 (JSON
  pass) for step 1; 200 for step 3 in both.
- **A question the runner answered:** whether the harness's key file, mode 644,
  is readable by the container's uid on a runner. It had been unknown because
  the container runs as uid 1000, which matched the researcher's host uid. On the
  runner the TLS leg read the key and passed with verification on, and ports
  15060 and 15061 were free.
- **The fork gate was not exercised.** The job is gated off fork pull requests,
  and it is the only fork-gated job in the repository. A same-repo push
  exercises only the `github.event_name != 'pull_request'` half of the condition;
  only a fork pull request can test the `head.repo.full_name ==
  github.repository` comparison, and no fork pull request exists. The expression
  was built from documented fields (`head.repo.full_name` is required in the
  pull request payload schema; `github.repository` is in the contexts
  reference), not copied from a working example here, and it has not been seen
  to refuse.
- **Node 20 deprecation warning** for `actions/checkout@v4`, `actions/setup-go@v5`
  and `actions/upload-artifact@v4` also appeared. Still open as item 7 of
  `docs/todo_next.md`.

## What this does not prove

- **No call was established.** Asterisk answered the INVITE with 100, 180 and
  200 OK with SDP and accepted the BYE, per its own log. Cellrift did not ACK the
  200 (RFC 3261 §13.2.1 requires a UAC to ACK a 2xx to INVITE), so the dialog was
  never confirmed from the peer's side. `basic_call.yaml` passes because its BYE
  is accepted before the peer gives up on the 200. Zero ACK requests reached the
  peer. Do not read this run as a complete call flow. The peer's repeated 200 in
  the log is a consequence of the missing ACK; no scenario asserts it and no
  timing was measured against RFC 3261's timers.
- **`register_impaired.yaml` demonstrates nothing about the peer's retransmission
  or timer behaviour.** The peer received three single, ordinary REGISTERs per
  pass, and a REGISTER 200 OK is not retransmitted. `impair_delay_ms` for
  `delay_ms` is a declaration echo: the code sleeps for the configured duration
  and returns that same configured duration, so step 3 read exactly 200 in every
  run on two machines while step 1, which carries random jitter, differed every
  time. The scenario's `>= 200` assertion therefore shows that the value was
  reported, and does not show that a delay occurred. What the run does show is
  narrower: a real SIP stack answers a REGISTER that arrives late, three times,
  with correct Call-ID, CSeq and Via correlation. The scenario's own header
  carried the stronger claim until this run corrected it.
- **The 180 is not asserted.** Provisional responses are consumed silently.
  `basic_call.yaml` asserts the final status code and the captured codec, and no
  step names the 180. The 180 is evidenced only by the peer's log quoted above
  (`asterisk.log:409`, `:657`).
- **The IMS extension headers were tolerated, not handled.** Asterisk ignores
  `P-Access-Network-Info` and `P-Charging-Vector` (both present in the INVITE at
  `asterisk.log:379`, `:380`) and answers normally. That shows it does not reject
  them and exercises no IMS semantics.
- **Not a P-CSCF.** Asterisk is a plain SIP server. This run retires no
  `MOCK-ONLY SCENARIO` marker; that needs a real P-CSCF, such as a Kamailio
  IMS stack. No authentication was exercised: the two endpoints are deliberately
  unauthenticated, and Cellrift has no digest or IMS-AKA computation.
- **The fork gate remains not exercised**, as stated under "In CI".
- **Mutual TLS is not proven.** The Cellrift `cert_file` and `key_file` fields
  and the Asterisk `verify_client` keys are documented in
  `test/interop/README.md`, and no run here has used them. The recipe for
  substituting your own CA, certificate and key has not been run either.
- **One peer, one image version, loopback only.** One CI run and four local
  cycles, so nothing here measures a rate. The runner's Docker version was not
  recorded.

## Findings recorded, deliberately not fixed

Detail and the observation behind each are in `docs/todo_next.md` item 9.

1. **Cellrift never ACKs a 2xx to an INVITE** (RFC 3261 §13.2.1); the only
   `ACK` handling in the SIP adapter is for an ACK a scenario author writes as a
   step. Observed above: zero ACK requests, and the peer sending its 200 again.
2. **The Via sent-by is `[::]:<port>`** (`asterisk.log:211`, `:369`), not a
   routable address. Asterisk tolerates it by replying to the packet source with
   `rport` and `received`; a stack that routed by the Via host would not.
3. **`local_port: 5060` in `basic_call.yaml` is ignored.** The adapter listens on
   `:0` and the peer's log shows ephemeral source ports.
4. **Cellrift has no `server_name` for TLS.** `BuildTLSConfig` never sets
   `tls.Config.ServerName`, so a peer reached by IP but certified by a DNS name
   cannot be verified today.

**A finding about what an assertion proves, not a missing guard.**
`impair_delay_ms` for `delay_ms` is a declaration echo (see the second item under
"What this does not prove"). The feature itself is guarded: four Go tests across
three packages measure real elapsed time and fail if the sleep is deleted, which
was checked by deleting it.

**A change made alongside this run, not a finding.** The JSON and JUnit reporters both
let an assertion failure displace the specific transport error, so a TLS
`x509` cause was lost from both. Both now prefer the transport error, one test
covers all three reporters (the terminal reporter was already correct), and the
wrong-CA probe above is what proves it against a real peer in both formats.

## Status

Cellrift did not ACK the 200 to either INVITE, so no dialog was acknowledged from
the peer's side; the BYE was accepted and the scenarios pass regardless. With
that limit stated, the four claims this run set out to support stand as
follows. This is an assessment, not a sign-off.

| Claim | What supports it | What remains open |
| --- | --- | --- |
| A digest-pinned real SIP server in CI on every push, gated off fork PRs | The `sip-interop` job ran green on a runner against the digest-pinned image, and the pin was proven by a cold pull. | The fork gate is not exercised. |
| The job fails the build on a rejecting peer and refuses to grade a run that never happened | All 20 refusal sites fired; the rejecting-peer and wrong-CA probes ran against a real Asterisk. | The CI job itself has not been seen red on a runner; the probes ran through the same scripts locally. |
| The run recorded in `EVIDENCE.md`, with the peer's own log lines | This entry, with the artefacts committed. | None beyond the limits in "What this does not prove". |
| `PROVEN AGAINST` headers on every proven scenario, enforced by a mutation-tested Guard D, existing markers unchanged, adversarial scenarios still refused | Guard D with five mutations and a green control; Guard A's list and every `MOCK-ONLY SCENARIO` marker unchanged; adversarial traffic refused before any transmission. | None known. |

**The artefacts are committed, not linked: [`evidence/run-12-asterisk/`](evidence/run-12-asterisk/).**
A CI artifact expires on GitHub's retention window, and the run's logs follow it,
so a link-only citation would eventually dangle.


# Run 13 — 2026-10-02 (local, then in CI): the first authenticated run against a real peer, which proves the digest arithmetic and not the header grammar

A real third-party SIP stack, Asterisk 22.10.1 with `chan_pjsip`, was configured
to require digest authentication on three endpoints, and Cellrift's new in-step
digest retry registered against all three. **The figures below are from a local
run; the `## In CI` section records the GitHub Actions run that followed it, and
the two agree.** **Asterisk is not a P-CSCF**, so this run proves nothing
about IMS semantics, and no existing `MOCK-ONLY SCENARIO` marker is touched.

## Result: PASSED — a real Asterisk required digest authentication on three endpoints and accepted Cellrift's digest for MD5, for SHA-256 and for a both-offered challenge; the peer validates the digest ARITHMETIC and not the header's wire format, which is pinned separately by in-process tests

Asterisk required digest authentication on three endpoints and accepted
Cellrift's computed digest for MD5, for SHA-256, and for an endpoint offering
both, returning `200 OK` after the re-send in each case, with its own log
showing the `401` it sent first. That is a proof of the digest **arithmetic**.

It is **not** a proof of the header's wire format. This peer was measured,
before this run, returning `200 OK` for an unpadded `nc`, for
uppercase hex in `nc`, for a quoted `qop`, for the `opaque` of the other
challenge header, for a `uri` differing from the request line, and for an omitted
`algorithm`; it returns `401` only when the hash disagrees with what the header
declares. This run did not repeat those measurements. Every grammar and echo rule
is therefore pinned by in-process tests: `internal/protocols/sip/digest_test.go`
for the RFC vectors and the quoting table, and `internal/protocols/sip/auth_test.go`
for the strict verifier, whose expected digests are literal hex strings computed
outside production code. The mock is the grammar oracle; this peer is the
arithmetic oracle; neither substitutes for the other.

The harness printed, and exited 0 after:

```text
OK: asterisk answered register_impaired, basic_call, register_digest_auth (udp/15060) and basic_call_tls with certificate verification ON (tls/15061); the peer's own log confirms it received each scenario's requests and that it challenged the authenticated one. Asterisk accepts several malformed Authorization headers, so this is a proof of the digest ARITHMETIC; the header grammar is pinned by unit tests.
```

## The vectors came first

A digest checked only against the peer that will accept it is a
self-consistency proof of the arithmetic, so the oracle had to exist before any
peer was configured. The RFC 7616 section 3.9.1 MD5 and SHA-256 values and the RFC 2617
section 3.5 MD5 value reproduced in `internal/protocols/sip/digest_test.go`
before any peer was configured, plus two REGISTER values computed with
`openssl dgst` outside production code. Section 1 of
[`vector-proofs.txt`](evidence/run-13-asterisk-digest/vector-proofs.txt) is the
`go test -v` capture the implementing session wrote at 11:49:29 +0300 on
2026-10-02, not a regeneration; section 2 is a re-run in this session, labelled as
one. The ordering was also checked in the pre-publication git history: the
first commit touching `digest_test.go` (`f9a3614`, 2026-10-02 11:48:55 +03:00)
preceded the only commit that put an `auth=` line in the Asterisk configuration
(`d77f5af`, 17:34:32 the same day) by about six hours. The required order
holds. Commit timestamps are the committer's
local clock, so they are this entry's evidence for the order and not an
independent timestamp.

**What the vectors do not cover.** They are HTTP `GET` examples, so
`Method=REGISTER` and `uri` equal to the SIP Request-URI are pinned by the two
`openssl` literals and not by any RFC. **UNKNOWN:** RFC 7616 section 3.9.2's
SHA-512-256 vector did not reproduce during research. SHA-512-256 is
unsupported, and section 3.9.2 is cited nowhere as a vector.

## Rig

```sh
bash test/interop/up-sip.sh
bash test/interop/run-sip-scenario.sh; echo "exit=$?"
bash test/interop/down-sip.sh
```

- **Peer:** `andrius/asterisk:22.10.1_debian-trixie`, pinned as
  `@sha256:212326bd3f973a597be9e4747be5d6653802e4950159e95aa7d728e394de2012`,
  the multi-arch **index** digest that `docker pull` prints. The amd64
  **manifest** digest is a different value (it begins `sha256:f4f36f06`) and the
  two are not interchangeable.
- **Version:** `Asterisk 22.10.1 built by root @ buildkitsandbox on a x86_64
  running Linux on 2026-09-30 14:28:01 UTC`, from `core show version`.
- **Docker:** 29.8.1.
- **Network:** `--network host`, `chan_pjsip` only. Ports **15060** (UDP) and
  **15061** (TLS), not 5060/5061: a developer machine often runs its own Asterisk
  on the defaults, and a host-networked container then fails to bind, still logs
  `Asterisk Ready.`, and the scenarios run against the other server.
- **Readiness** is the container's own `pjsip show transports`, `pjsip show
  endpoints` and **`pjsip show auths`**, never the `Asterisk Ready.` line. The
  boot printed `digest auth wiring verified (auth-md5, auth-sha256, auth-both)`;
  that line is about configuration and says nothing about the digest arithmetic,
  which only the peer's `200` after a retry speaks to.
- **Three auth objects**, one username (`impi-user`, deliberately not the From
  user: the IMS private-identity shape), one password (the scenario's synthetic
  lab value) and one realm (`cellrift.interop`), differing only in
  `supported_algorithms_uas`: `auth-md5` offers `MD5`, `auth-sha256` offers
  `SHA-256`, `auth-both` offers `SHA-256,MD5`. Endpoints 447700900201, 202 and 203
  point at them; the original two endpoints stay unauthenticated.
- **The option exists on this image.** `config show help res_pjsip auth
  supported_algorithms_uas` returned, on the pinned image, the synopsis
  `Comma separated list of algorithms to support when this auth is used as a UAS`
  and `[Since] 20.12.0, 21.7.0, 22.2.0`, listing `md5`, `sha-256` and
  `sha-512-256` as valid values.
- **Two passes per scenario.** As in run 12, the harness runs each scenario twice
  (terminal pass, then JSON pass), so `asterisk.log` holds two passes of each.

## What the peer logged

**Per pass, and cumulative, side by side.** Per pass, from the harness's own
files: `register_digest_auth` **6** requests (three REGISTERs without
credentials, three retries with them) and **3** challenges; the three earlier
scenarios are unchanged from run 12, `register_impaired` **3**, `basic_call`
**2**, `basic_call_tls` **2** requests, each with 0 challenges. Cumulative over
both passes of all four scenarios, as `peer-counts.txt` records: **22** requests
from UDP (6 + 4 + 12) and **4** from TLS. The cumulative total is informational
and no per-scenario statement below uses it. The artefacts of the three earlier
scenarios are not committed again; the directory's README says so.

All line numbers refer to
[`asterisk.log`](evidence/run-13-asterisk-digest/asterisk.log).

**The 401 count and the absence that matters.** The log holds six `SIP/2.0 401
Unauthorized` lines (three per pass) and **zero** `Failed to authenticate`
NOTICE lines. That NOTICE is what a rejected credential looks like in this peer's
log, which is why its absence here is meaningful and why its presence is what the
deliberately-failing run will produce. There are eight `WWW-Authenticate` lines,
four per pass.

**Endpoint 447700900203, the both-offered endpoint, pass 1.** One nonce and two
different opaques, SHA-256 first, then MD5:

```text
asterisk.log:758  WWW-Authenticate: Digest realm="cellrift.interop",nonce="1790956312/4bcd8485f48d60c67f1df60cab0a12b5",opaque="3ff50f6b3170c2dd",algorithm=SHA-256,qop="auth"
asterisk.log:759  WWW-Authenticate: Digest realm="cellrift.interop",nonce="1790956312/4bcd8485f48d60c67f1df60cab0a12b5",opaque="79d7aef36a97281d",algorithm=MD5,qop="auth"
```

The retry carries the SHA-256 challenge's opaque, not the first header's by
position and not the MD5 one:

```text
asterisk.log:773  Authorization: Digest username="impi-user", realm="cellrift.interop", nonce="1790956312/4bcd8485f48d60c67f1df60cab0a12b5", uri="sip:447700900203@127.0.0.1:15060", response="0b30ace089a53acdbc1ab93725234c1ead5d44d0828d1c525c10404a8c0e52ff", algorithm=SHA-256, cnonce="dd2a7272615ac06b", opaque="3ff50f6b3170c2dd", qop=auth, nc=00000001
```

Pass 2 repeats it with new opaques, since the peer generates one per challenge:
`:917` SHA-256 `opaque="02031ae51dc5c6ae"`, `:918` MD5
`opaque="59f48da61f9ffe24"`, and the retry at `:932` echoes `02031ae51dc5c6ae`.
This pair is the whole RFC 8760 selection evidence: selection picked a challenge
and echoed that challenge's own opaque. The scenario also asserts
`response.auth.algorithm == SHA-256` on that step. What this does not show is
that the peer checks the echoed opaque, which it does not (see the grammar
paragraph above); that echo rule is held by `auth_test.go`.

**The other two endpoints, pass 1.** 447700900201 (MD5 only): header at `:650`,
`opaque="50c3267177195e67"`, retry at `:664` with the same opaque and
`algorithm=MD5`. 447700900202 (SHA-256 only): header at `:704`,
`opaque="040b4abb15ea5976"`, retry at `:718` with `algorithm=SHA-256`. Each is
answered `200 OK` after the retry, and the peer logged
`Added contact` for each endpoint at `:669`, `:723` and `:778`. Pass 2 logs no
`Added contact` line for them, because the contacts were already registered.

**Cellrift's side** (`register_digest_auth.stdout`): `3/3 steps passed`, each step
a single step whose own result is the final `200`; `response.auth.challenged`,
`retried`, `algorithm` and `realm` are asserted on each.

## The three integrity gates

Three separate checks stop a mistyped `auth=` from producing an immediate `200`, a
passing scenario and a green job that exercised no digest at all. Having three
is not the same as having three independent ones, so they are named and
distinguished:

1. **`wait_for_auths`** (boot, the container's own CLI): `pjsip show auths` must
   list the three objects by exact id, and `pjsip show endpoint` for each of
   447700900201/202/203 must report the expected `auth` value.
2. **`gate_peer_challenged`** (run time, the peer's own log): the peer must have
   logged at least one `401` during the scenario.
3. **`response.auth.challenged == true`** (client side, Cellrift's own
   observation), asserted on every step.

A fourth fact belongs here because it is part of the run's story: `wait_for_auths`
as first written **could not fail**. It searched `pjsip show auths` for
substrings, so renaming the sections to `[auth-md5-TYPO]` still matched
`auth-md5` and the boot exited 0 and reported ready; and a mistyped endpoint
`auth=` never appears in a listing of auth objects at all. It was rewritten before
it shipped, to match whole ids and to read each endpoint's own reported value.
Both mutations now refuse (below).

## The gates, each fired once

Two new `refuse` call sites exist: `wait_for_auths` in `up-sip.sh` and
`gate_peer_challenged` in `run-sip-scenario.sh`. Counted with
`grep -c 'refuse "' test/interop/up-sip.sh test/interop/run-sip-scenario.sh`,
that is 6 and 16, **22** in all, against run 12's 20 (5 and 15).
`gate_no_adversarial_scenario`'s two refusals gained a new input
(`$AUTH_SCENARIO`) and are not new sites. Both new sites were re-fired in this
session, and a clean control passed after each; commands, full output and exit
codes are in
[`gate-proofs.txt`](evidence/run-13-asterisk-digest/gate-proofs.txt). These gates establish that a digest exchange took place at all; the arithmetic
is what the peer's `200` speaks to, and the grammar is the unit tests' business.
The other
20 sites were not re-fired for this run, and run 12's table stands for them. The
stdout column is the scripts' output quoted unaltered; the failure dump after the
second refusal is elided and counted in the file.

| Gate | Fired by | Verbatim stdout | Exit code |
| --- | --- | --- | --- |
| `wait_for_auths` (`up-sip.sh:203`) | endpoint 447700900202's `auth=` mistyped as `auth-sha265`, all three objects still loaded (section W1) | `SIP RIG REFUSED: 'pjsip show auths' does not list auth-md5, auth-sha256 and auth-both as objects, or endpoints 447700900201/202/203 do not report auth= auth-md5/auth-sha256/auth-both, so a REGISTER would be answered 200 with no challenge` | 1 |
| `wait_for_auths` (`up-sip.sh:203`) | the three `[auth-*]` sections renamed to `[auth-*-TYPO]` (section W2), same text | `SIP RIG REFUSED: 'pjsip show auths' does not list auth-md5, auth-sha256 and auth-both as objects, or endpoints 447700900201/202/203 do not report auth= auth-md5/auth-sha256/auth-both, so a REGISTER would be answered 200 with no challenge` | 1 |
| `gate_peer_challenged` (`run-sip-scenario.sh:331`) | a rig with every `auth=` line removed, run with a copy of the scenario whose `response.auth.*` assertions were stripped so that Cellrift's own account was green (`3/3 steps passed`); the unmodified scenario refuses identically (section G1) | `SIP RIG REFUSED: the peer's own log shows it never sent a 401 during stripped (waited up to 5000 ms for it), so the rig required no authentication and this run exercised no digest. Check that pjsip.conf's auth= lines name loaded [auth] objects ('pjsip show auths').` | 1 |

The third row is the one that matters most: Cellrift graded that run green, and
only the peer's own log said no challenge had been sent. Nothing in this table is
recorded as unfired or unreachable.

## Guard D, and its mutations

`scripts/check-scenario-labels.sh` enumerates `register_digest_auth.yaml` the way
it enumerates the three earlier proven scenarios: the `PROVEN AGAINST: Asterisk
(non-IMS SIP)` header, no `MOCK-ONLY SCENARIO` marker, no `malform:` block or
`action: raw` step, and a name in the harness's scenario variables. Each condition
was made to fail against the new file, in this session
([`guard-d-mutations.txt`](evidence/run-13-asterisk-digest/guard-d-mutations.txt),
verbatim output):

| Mutation | Expected | Observed exit code |
| --- | --- | --- |
| 1. the `PROVEN AGAINST` header deleted | red, missing header | 1 |
| 2. the marker moved from a header into the description (anchoring proof) | red, missing header | 1 |
| 3. an `action: raw` step appended | red, adversarial step and missing mock-only header | 1 |
| 4. the same step, with a `MOCK-ONLY SCENARIO` header added to silence Guard B | red, a file cannot carry both markers | 1 |
| 5. `AUTH_SCENARIO` pointed at another file, the name left on a separate comment line | red, marker and scenario list diverge | 1 |
| control: clean tree | green | 0 |

A sixth mutation, not one of the five, is also in the file: the name left in a
trailing comment on the **same line** as the `AUTH_SCENARIO` assignment, an
evasion that once passed. It is red (exit 1). A first attempt at mutation 5 used a
`sed` expression that sed rejected, left the file unchanged and so produced a
green result; that attempt is not recorded as a result and the file explains it.

Guards A, B and C were compared with the guard's output from before this run's
guard change and show no difference. **Guard A's list and every existing
`MOCK-ONLY SCENARIO` marker are unchanged.** Asterisk is not a P-CSCF, and
retiring those markers needs one.

## Password

The scenario's password is a synthetic lab value, not a secret, and it is
present in `pjsip.conf` and in the scenario in this repository, so its presence
in tracked files is not new. What matters for the masking claim is whether
Cellrift's own output carries it, so every committed artefact was searched:

```sh
grep -rn 'shared-pw' test/interop/evidence/run-13-asterisk-digest/ || echo "clean"
```

```text
clean
```

That covers `asterisk.log`, which Asterisk writes with no knowledge of
Cellrift's masker: the peer logs the digest `response=` hash and never the
secret. Cellrift's report and terminal output are free of it too.

## In CI

Observed. The `sip-interop` job ran this scenario at commit `5bccb73` and it
passed — meaning the peer accepted the digest
ARITHMETIC on a runner, which is the same scope the local run had and not a
statement about the header's wire format. The artifact is committed under
`evidence/run-13-asterisk-digest/ci/`, so the record does not depend on an
artifact that expires:

| From the runner | Value |
| --- | --- |
| `register_digest_auth.challenges` | 3 |
| `register_digest_auth.exit` | 0 |
| `register_digest_auth.receipts` / `.pass2` | 6 / 6 |
| peer log `401 Unauthorized` | 6 |
| peer log `Failed to authenticate` | 0 |
| peer log occurrences of the password | 0 |

The challenge count is the part that matters. `gate_peer_challenged` was
already in the commit under test, so a green job is not merely "the steps
passed": the gate refuses unless the peer's own log shows it challenged
during the authenticated scenario. Three challenges per pass is what
satisfied it, and a rig that had silently loaded no authentication would have
produced 200s with 0 challenges and failed the job. A challenge proves that
authentication was exercised at all; the 200 that follows it proves only the
ARITHMETIC.

On the both-offered endpoint the peer sent two `WWW-Authenticate` headers
under one nonce `1790956154/a8c554820bd8a476426ae712cd95a1e9`:
SHA-256 with `opaque="218637b60f4fa546"` and MD5 with
`opaque="3835fd1d07cdeb2b"`. The retry carried
`algorithm=SHA-256, opaque="218637b60f4fa546"` — the chosen challenge's own
opaque, not the other header's.

That observation is consistent with RFC 8760 selection but **does not
discriminate**: Asterisk lists SHA-256 first here, so "take the topmost",
"take the strongest" and "always take SHA-256" all produce the same bytes. What
it does prove is the per-challenge opaque pairing — the retry echoed the chosen
header's opaque and not the other one's, which is a distinct claim and the one
the challenge parser was built to keep. The evidence that selection is strength-ordered rather
than positional is the in-process fixture whose FIRST challenge is unusable and
carries its own nonce and opaque; a peer that offers them in this order cannot
tell the two apart. Listing MD5 first on the rig would discriminate, and that
is not done here.

What the peer validated is still the digest ARITHMETIC. Asterisk accepts
`nc=1`, a quoted `qop`, a wrong `opaque` and a mismatched `uri`, so the
header grammar is pinned by the in-process strict verifier, not by this run.

The green run's own page URL was not captured: the artifact was supplied
directly rather than from the run page, so the commit, the runner image and
the artifact contents are recorded instead of a link.

`.github/workflows/interop.yml` needed no edit. The `sip-interop` job calls
`run-sip-scenario.sh` with no scenario list, and its artifact upload path
`/tmp/cellrift-sip/**` collects the new `register_digest_auth.challenges` file,
which was confirmed to exist in that directory after the local run.

## What this does not prove

- **The header grammar.** The peer accepted malformed `Authorization` headers when
  measured, so a `200` from it is not evidence that the header Cellrift
  writes is well formed. That is pinned by `digest_test.go` and `auth_test.go`
  against an in-process verifier, not by this run.
- **The authenticated TLS leg.** It was not run. A nonce is not expected to be
  bound to a connection, and that is an expectation, not a measurement.
- **Any peer other than Asterisk.** The To-tag reuse, `stale=true` on a replayed
  nonce and 401 retransmission behaviour of Kamailio and other stacks are
  UNKNOWN. The adapter never folds a challenge into the dialog tracker; that
  mitigation is defensive and was not measured against those peers.
- **IMS semantics.** Asterisk is not a P-CSCF, and no `register_*` IMS scenario
  has a real P-CSCF.
- **`auth-int`, the `-sess` variants, SHA-512-256 and IMS-AKA.** None is
  supported, and none was tested here.
- **`reorder` members.** They are not authenticated, by design, and this run did
  not exercise them. **`parallel` sub-steps ARE authenticated** — an earlier
  version of this list said they were not, which was an inference from the
  reorder case and was wrong: `executeParallel` reaches `ExecuteStep` by the
  same call as a top-level step, so a challenged sub-step is retried normally.
  Verified by running a parallel scenario against a challenging peer: the
  sub-step was challenged, retried and passed, with the peer accepting the
  digest ARITHMETIC exactly as it does for a top-level step, and the peer saw
  two REGISTERs. No test pins it yet.
- **That a stronger password check exists.** This run did not show the peer
  rejecting a wrong password; run 14 does, for MD5, and a separate check during
  verification saw it for SHA-256 as well.
- **A complete call.** Cellrift still sends no ACK for a 2xx to an INVITE, as
  run 12 recorded. This run did not change that.
- **One peer, one image version, loopback only.** One local run; nothing here
  measures a rate.

## Status

This run supports three claims, one of them jointly with run 14:

| Claim | State |
| --- | --- |
| Digest for MD5 and SHA-256 with `qop=auth`, `nc` and `cnonce`, proven against published vectors before any peer is involved | The vectors reproduced before any peer was configured (history above), and a real peer accepted the digest arithmetic for both algorithms. |
| The authenticated scenario against a real peer, proven in CI | **Satisfied.** Green on `ubuntu-24.04` at `5bccb73`, 3 challenges observed per pass, artifact committed under `ci/`. |
| The `sip-interop` job fails the build on a rejecting peer | **Addressed by run 14**, which observed both of its clauses red on a runner. |

A peer's `200` here is a proof of the digest arithmetic. It is not a proof of the
header's wire format, which Asterisk does not enforce.

**The artefacts are committed, not linked: [`evidence/run-13-asterisk-digest/`](evidence/run-13-asterisk-digest/).**

# Run 14 — 2026-10-02 (in CI, twice): the sip-interop job observed RED, replacing an inference with an observation

The `sip-interop` job claims two things, which needed proving by deliberate
probe runs rather than by the job merely passing: that it genuinely fails the build **when the peer
rejects**, and that it **refuses to grade a run that never happened**. Each
clause fails at a different point, so each needed its own probe. Both were run
on branch `probe/peer-02-deliberate-red`, which exists only to be red and is
deleted afterwards; `main` stayed green throughout.

Artefacts: `evidence/run-14-red-ci/`.

## What this replaced

Run 12 fired all 20 refusal sites in `up-sip.sh` and `run-sip-scenario.sh`
locally, each with exit 1, and both probes ran against a real Asterisk. What
was missing was the build-level claim. The reasoning was: the scripts exit
non-zero, the steps run under `bash -e` with no `continue-on-error`, therefore
the build fails. That is sound, and it has now turned out to be correct — but
it was an inference, and runs 12 and 13 each disproved inferences of exactly
that shape, including one that had already been verified and endorsed by a
reviewer. This run exists to refuse that argument and record the observation.

## Clause (b): refuses to grade a run that never happened

Run <https://github.com/riftcell/cellrift/actions/runs/37040015812>, commit
`8a7a6f4`. Endpoint 447700900202 referenced `auth-sha265`; `auth-sha256` is
what exists. Asterisk loads such an endpoint anyway and would answer its
REGISTER 200 with no challenge, which is the silent failure `wait_for_auths`
exists to catch.

```
17:21:20 SIP RIG REFUSED: 'pjsip show auths' does not list auth-md5,
         auth-sha256 and auth-both as objects, or endpoints
         447700900201/202/203 do not report auth= ...
17:21:20 ##[error]Process completed with exit code 1.
```

The step ran under `shell: /usr/bin/bash -e {0}`. The scenario step never
executed, and the uploaded artifact contained **one** file, because
`/tmp/cellrift-sip/**` matched nothing. Nothing ran, and nothing was graded.

The peer log for that run is `ci-red-run-clause-b.log`'s companion: Asterisk
booted at 17:20:44, was polled by `asterisk -rx` for about 30 s — matching
`READY_RETRIES=30` with a 1 s sleep — and the SIP logger was never enabled,
which places the stop inside `wait_for_auths`, the line immediately before
`enable_sip_logger`.

## Clause (a): fails the build when the peer rejects

Run <https://github.com/riftcell/cellrift/actions/runs/37045975976>, commit
`eb211ba`. Only `auth-md5`'s `password` was changed. The auth objects and
every endpoint's `auth=` stayed correct, so this is deliberately **not** a
readiness refusal: the boot gates had to pass for the probe to mean anything.

```
18:14:07 sip interop harness ready: ... digest auth wiring verified
         (auth-md5, auth-sha256, auth-both), sip logger on
18:14:07 ##[group]Run bash test/interop/run-sip-scenario.sh
18:14:31 SIP RIG REFUSED: the real peer did not pass register_digest_auth
         (exit 1); this is a result about the peer, not the rig
18:14:31 ##[error]Process completed with exit code 1.
```

The boot step completed, so the red is attributable to the scenario step. The
peer's own log says `Failed to authenticate` for 447700900201, and Cellrift
reported `credentials rejected after one digest retry (realm="cellrift.interop",
algorithm=MD5)` — the diagnosis distinguishing a rejected credential from a
rig fault, behaving in the wild as its unit tests require.

Two details make the red attributable rather than incidental:

- `register_sha256` and `register_both_offered` still **passed**, so the
  failure is scoped to the one broken credential.
- The artifact carried **26** files against clause (b)'s 1. The run happened,
  it was graded, and the grade was the thing that failed.

The both-offered endpoint still behaved correctly under the break: two
`WWW-Authenticate` headers under one nonce, SHA-256 with
`opaque="532f278724ea1f04"` and MD5 with `opaque="2ac4b7b019227273"`, and the
retry carried the SHA-256 header's own opaque.

## What this does not prove

- Nothing about the digest header's wire format. Asterisk accepts `nc=1`, a
  quoted `qop`, a wrong `opaque` and a mismatched `uri`; the grammar is pinned
  by in-process tests, and these runs only show the ARITHMETIC being accepted
  or rejected and the job responding to that result.
- Nothing about IMS semantics. Asterisk is not a P-CSCF, so every
  `MOCK-ONLY SCENARIO` marker stays as it is.
- Not that every one of the 22 refusal sites fails the build. Two were
  observed on a runner; the rest remain fired locally with exit 1. What is now
  established is that a non-zero exit from either script does turn the job red,
  which was the step the inference could not supply.

## Status

| Claim | Outcome |
| --- | --- |
| The job fails the build when the peer rejects | **Satisfied** by clause (a), run 37045975976 |
| The job refuses to grade a run that never happened | **Satisfied** by clause (b), run 37040015812 |

`pjsip.conf` on `main` is unchanged throughout: the break existed only on the
probe branch, and `main`'s copy was never edited.
