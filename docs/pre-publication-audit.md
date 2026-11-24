# Pre-publication audit — closed

A hostile review of the whole codebase (correctness, concurrency, security,
first-contact UX), recorded 2026-09-19. **All 26 findings are closed**, each
with the commit that closed it and, where it was a code fix, a regression
test written to fail without it.

This file was originally marked "delete before publishing," on the reasoning
that a known-bugs checklist reads oddly in a public repo. It is kept because
that reasoning no longer applies: nothing here is open. What it is now is a
record of what this project found when it went looking for its own defects —
false greens its tests could not see, a masker that leaked every second
phone number, TLS that silently skipped verification — and what it did about
each. Read alongside
[`test/interop/EVIDENCE.md`](../test/interop/EVIDENCE.md), which is the same
discipline applied to real peers.

Work identified *after* this audit lives in [`todo_next.md`](todo_next.md).

> **About the commit hashes below.** Each fix is cited by the commit that
> made it, in the project's pre-publication history. That history was
> squashed into this repository's single initial commit when it was
> published, so the hashes can no longer be looked up. They are kept because
> they still show which items landed together; the regression test named
> with each item is what proves the fix today.

## Status (updated 2026-09-24) — COMPLETE

**Every code-fix item is closed** — items 1–13, 15–20 and 24, across 16
commits (`994e774`..`71cca6d`), each with a regression test and each
verified with the full gate (build/vet/staticcheck/gofmt, race-enabled
suite, scenario label guard). Two items were **deliberately scoped**: item 5
runs assertions on failed steps but does not let a passing assertion rescue
one (the "rescue" variant that enables negative testing is a product call —
see the commit message on `a5eeec3`), and item 6 asserts on the raw steps
only, respecting the scenario's own documented reason not to assert on
peer behaviour.

One **additional real bug** surfaced while writing item 2's regression test
and is fixed in `841c3c9`: the SIP dialog state machine rejected any repeated
or re-ordered provisional response, so the canonical VoLTE flow
100 → 183 → 180 → 200 failed at the 183.

**Feature items closed** in `64b9d2e`, `c346cbe`, `75bc932`: 14 (library
embedded, `cellrift init`, `list` fallback), 21 (`cellrift selftest`), 23
(worked examples for `sctp_abort`, `parallel`, `wait`, `delay_ms`), 25
(`scenarios/README.md`; every shipped file now e2e-tested). Running the
self-test surfaced a **second additional real bug**: `assert_state: check:
registered` was in the schema and in `register_basic.yaml` but the dialog
tracker had no such state, so that step could never pass. Fixed in
`64b9d2e` with a `registered` state and a validator check on `check:`.

**Item 22 is CLOSED.** Four interop runs
(`test/interop/EVIDENCE.md`) each found exactly one more gap, because a real
peer reports only the first rule it hits: AVP 260, then `Session-Id` 263,
then `Origin-Host`/`Origin-Realm` 264/296, then a fixture gap (no MSISDN
provisioned) plus the discovery that a Cx UAA carries no base `Result-Code`
at all — which Cellrift had been silently treating as a PASS. All fixed.
`scenarios/diameter/uar_uaa.yaml` now passes against a real Open5GS HSS in
CI on every push; its `MOCK-ONLY` marker is removed, Guard A no longer lists
it, and a new Guard C fails the build if the marker ever returns. README,
roadmap, CHANGELOG and every affected scenario header state the new scope:
one command of six, one peer, over TCP.

**Nothing is open.** Item 26 was closed by the history reset that has
already been executed and re-verified; items 1-25 are done. The list is
finished.

Its successor is [`todo_next.md`](todo_next.md), which tracks the gap
between what Cellrift does and what has been demonstrated against software
this project did not write.

## How to read this

- **REPRODUCED** — demonstrated end to end against the real binary. Not a
  hypothesis; there is a repro recipe under the item.
- **CODE-VERIFIED** — traced in the source and confirmed by reading the actual
  function body, but not executed as a scenario.

Each item lists the fix location, so the work can start without re-deriving the
diagnosis.

---

## P0 — false-green bugs (a testing tool reporting success when it should fail)

These share one theme: **the tool says PASS when the thing under test is
broken.** For a CI regression gate this is the worst possible failure mode,
because it is silent and it defeats the project's core promise.

### [x] 1. SIP reports PASS on 4xx/5xx/6xx responses — REPRODUCED — DONE `994e774`

`internal/protocols/sip/adapter.go:411-418` (`finishSend`) sets
`Status: common.StatusPassed` unconditionally. It never looks at
`respMsg.StatusCode`.

Diameter does gate correctly — `internal/protocols/diameter/adapter.go:520-523`
(`buildStepResult`) fails the step when `Result-Code != 2001`. The two adapters
therefore disagree about what a protocol-level failure is, so a user whose
mental model formed on Diameter will be wrong about SIP.

Repro: point a scenario's `send` step (no `assert:` block) at a UDP peer that
answers `SIP/2.0 500 Server Internal Error`. Observed:

```text
✓ [sip] send options_to_broken_peer
1/1 steps passed in 0ms     → exit 0
```

Every SIP scenario lacking an explicit `response.status_code == 200` assertion
is a guaranteed false pass on any error response.

Fix: gate on the status code in `finishSend`, mirroring Diameter's shape.
Decide deliberately whether a 3xx is pass or fail, and whether a scenario can
opt out (for negative tests that *expect* a 4xx — see item 5, which is the
other half of that design question).

### [x] 2. SIP never really times out, then passes anyway — REPRODUCED (code path), CODE-VERIFIED (fall-through) — DONE `5560d3d`

`internal/protocols/sip/adapter.go:498-534` (`waitForFinalResponse`):

- The loop calls `a.transport.Receive(a.timeout)` with the **full** timeout on
  every one of up to `SIPMaxProvisional()` (default 50) iterations. There is no
  absolute deadline. A peer trickling `180 Ringing` holds one step for up to
  50 × timeout — 100 s at the 2 s default, 25 minutes at a 30 s scenario
  timeout.
- When the loop exhausts without a final response it falls out of the bottom at
  line 533 returning `(msg, raw, nil)` — a **1xx with a nil error**. `finishSend`
  then stamps it passed. A peer stuck in a provisional loop is reported healthy.

Both the doc comment on `config.SIPMaxProvisional` and the CLAUDE.md tunables
table say this path treats the request as "timed out". It does not.

Diameter already solves this correctly: `internal/protocols/diameter/session.go`
computes one absolute deadline and explicitly never re-arms it. `internal/protocols/sip/reorder.go`
documents the same hazard and defers it.

Fix: one absolute deadline computed before the loop, and return a failed result
(with `timed_out` stamped) when provisionals are exhausted.

### [x] 3. `validate` silently ignores unknown keys — REPRODUCED — DONE `0fb0233`

The YAML decoder does not use `KnownFields(true)`, so a typo'd key is dropped
without comment. A scenario containing `targett:` (typo of `target:`) and
`assertions:` (typo of `assert:`) validates **clean, exit 0, with no output** —
and then runs with zero assertions.

`docs/scenario.schema.json` is complete and correct here (it has
`additionalProperties: false` and covers `impair`/`malform`/`raw`/`send_order`).
The Go validator simply never consults it.

Fix: `yaml.Decoder.KnownFields(true)` in the scenario loader, or validate the
parsed document against the shipped schema. Cheapest high-value fix on this
list — it kills a whole class of silent failure.

### [x] 4. Unresolvable paths make assertions vacuously true — REPRODUCED — DONE `7f84e0d`

`internal/engine/executor.go` (`extractResponseValue` / assertion operand
resolution, ~lines 404-424 and 476-494): an unknown path resolves to `nil`
rather than erroring. Consequences:

- `responce.status_code == null` (typo'd namespace) passes forever.
- `response.totally_made_up == null` passes forever.
- A `capture:` of an absent value is a silent no-op (`executor.go:310-318` only
  calls `Set` when the value is non-nil), so a later
  `assert: ["granted_units != 0"]` passes vacuously against a value that was
  never captured.

This is the same bug class as the `responseFieldsMap` defect fixed
earlier (where every built-in Diameter scenario's
`response.Result-Code` silently resolved to nil). Worth fixing at the root this
time.

Fix: error when an assertion operand or a capture path resolves to nothing.
Requires deciding how a deliberate "field should be absent" assertion is
written (an explicit `is_absent` form, rather than relying on nil == null).

### [x] 5. Assertions are skipped entirely on a failed step — CODE-VERIFIED — DONE `a5eeec3`

`internal/engine/executor.go:267` gates assertion evaluation on
`result.Status == common.StatusPassed`. Two consequences:

- Negative testing is impossible: `assert: [response.Result-Code == 5001]`
  against a Diameter error answer still reports FAILED, because the step failed
  before assertions ran.
- `scenarios/ims/register_conn_fault.yaml`'s
  `response.connection_state == "truncated"` assertion is dead code — the fault
  result is already `failed`, so it never evaluates.

This contradicts the doc comment on `impair.FaultedResult`
(`internal/protocols/common/impair/connfault.go:155-157`), which states "the
step's assertions still run". A name/behaviour mismatch of the kind CLAUDE.md
says to surface rather than work around.

Needs a decision: either run assertions on failed results (which makes negative
tests work, and is likely the right answer given the impairment feature set), or
correct the doc claim and accept that faulted steps cannot be asserted on.

**Resolution (2026-09-21):** assertions now run on failed steps but can only
*add* a failure, never rescue one (`a5eeec3`). Negative testing — declaring a
step is *supposed* to receive an error — remains an open design item, not a
bug. Documented in README "How a step passes or fails", the `FaultedResult`
comment, and CHANGELOG.

### [x] 6. `drop:` and `action: raw` are green with no peer at all — REPRODUCED — DONE `4f38138`

`impair.DroppedResult` (`internal/protocols/common/impair/helpers.go:54`) and
the raw-send result (`internal/protocols/common/malform/raw.go:45`) both return
`StatusPassed`. A drop never sends and a raw step never receives, so neither can
observe a peer.

Repro: two steps (one `impair: {drop: true}`, one `action: raw`) targeting
`127.0.0.1:9` where nothing is listening → `2/2 steps passed`, exit 0.

Each is defensible in isolation — the drop *did* drop. The real problem is that
**both shipped malformed-traffic worked examples carry no assertions**
(`scenarios/ims/malformed_sip_headers.yaml`,
`scenarios/diameter/malformed_diameter.yaml`), so the flagship v1.1 examples are
unconditionally green and a user copying them builds a test that can never fail.

Fix: add assertions to both shipped scenarios (assert on
`response.raw_bytes_sent`, mock arrival, or a following liveness step), and
consider rendering drop/raw steps distinctly in reporter output so a green
"passed" is not mistaken for a verified exchange.

---

## P1 — security

### [x] 7. TLS silently disables certificate verification — CODE-VERIFIED — DONE `5b40112`

`internal/protocols/common/tls.go:41-43`:

```go
if certFile == "" && keyFile == "" && caFile == "" {
    cfg.InsecureSkipVerify = true //nolint:gosec // lab/test mode only
}
```

Reached whenever a scenario declares `transport: tls` with no cert block — the
most natural configuration. There is no warning on stdout, no run-level warning,
and no opt-in flag; the only signal is a source comment. A user pointing this at
a production IMS over TLS in CI reasonably believes the peer is verified.

Fix: require an explicit scenario key (e.g. `insecure_skip_verify: true`) to get
this behaviour. At minimum emit a run-level warning — the `Warnings` plumbing
already exists and reaches all three reporters.

### [x] 8. PII masker leaks numbers separated by exactly one character — REPRODUCED — DONE `98a8791`

`internal/reporter/pii.go:16` — the regex's trailing context group `([^0-9]|$)`
*consumes* the separator, so the next number has no preceding character left to
match against.

```text
IN : +447700900001,+447700900002
OUT: [REDACTED-1],+447700900002      ← second number leaks

IN : +447700900001 +447700900002
OUT: [REDACTED-1] +447700900002      ← second number leaks

IN : from=+447700900001;to=+447700900002
OUT: from=[REDACTED-1];to=[REDACTED-2]   ← 2+ separators: correct
```

Affects terminal, JSON and JUnit equally (all share `Masker.Mask`). Comma- and
space-joined MSISDN lists are common in telecom output.

Fix: re-scan from the start of the trailing context rather than past it, or
restructure to avoid consuming the separator.

### [x] 9. `cellrift assert` bypasses PII masking entirely — CODE-VERIFIED — DONE `317b2fa`

`cmd/cellrift/main.go:504-509` prints `AssertionError` straight to stderr and
never constructs a `Masker` (confirmed: zero `NewMasker` calls on that path).
The error text embeds the *actual* value
(`internal/engine/executor.go:21`), so
`cellrift assert --input captured.json "from_number == +441234567890"` puts a
live MSISDN in CI logs. The `run` path masks this same error correctly.

The `list` subcommand's JSON dump (`main.go:551`) is likewise unmasked.

---

## P1 — concurrency

### [x] 10. A `parallel` group shares one adapter instance per protocol — CODE-VERIFIED — DONE `347db70`

`internal/engine/runner.go:197-227`: `executeParallel` spawns a goroutine per
sub-step and resolves `adapters[s.Protocol]` (line 211). Two sub-steps declaring
the **same** protocol therefore receive the *same* adapter pointer, and neither
`diameter.Adapter` nor `sip.Adapter` has a mutex.

For two concurrent Diameter sub-steps this means: the session buffer is appended
and resliced from both goroutines (torn framing), and `receiveAnswer` discards
any Hop-by-Hop ID that is not its own — so one goroutine eats the other's answer
and the victim burns its full deadline and reports a **false timeout**.

Currently latent: no shipped scenario uses `action: parallel`, and the race
detector never sees it because the existing parallel tests use different
protocols per sub-step. But the validator has no rule against it
(`internal/scenario/validator.go:114` only checks that sub-steps exist), and
"send two requests at once" is an obvious thing for a user to try.

Fix: either give each parallel sub-step its own adapter instance, or reject
same-protocol parallel sub-steps at validate time until per-adapter locking
exists.

### [x] 11. `MockHSS.Close()` can hang forever — CODE-VERIFIED — DONE `33626b7`

`internal/protocols/diameter/testing.go:1114-1119`: `Close` closes `done` and
the listener, then `wg.Wait()`s — but never closes accepted connections.
`handleConn` is parked in `conn.Read` and only unblocks when the client closes
its side. Today's tests survive on defer LIFO ordering (`defer hss.Close()`
written before `defer adapter.Close()`). Invert those two lines, or forget
`adapter.Close()`, and the test hangs until `go test`'s 10-minute panic.

`MockHSS` is exported from a non-test file, so downstream users can hit this.
Also: `close(m.done)` panics on a double `Close()`.

### [x] 12. `writeChunked` sleeps while holding the mock's mutex — CODE-VERIFIED — DONE `33626b7`

`internal/protocols/diameter/testing.go:621-666`: `writeResponse` takes `m.mu`
and, when chunking is enabled, calls `writeChunked`, which sleeps 5 ms between
chunks *under the lock*. Every other `m.mu` user stalls for `(n-1) × 5ms`.

Not the earlier connection-fault self-deadlock (that fix is correct and no other hold-then-relock
was found), but blocking I/O plus a sleep inside a critical section is how that
class of bug returns. The lock only protects `lastAnswerWriteCount` and write
serialisation — a separate write mutex is the right split.

### [x] 13. Minor leaks — CODE-VERIFIED — DONE `33626b7`

- `internal/protocols/sip/testing.go:566-567`: `TCPFaultSilence` blocks on
  `<-u.done` then returns without closing `conn` — one fd leaked per
  silence-fault connection.
- `internal/protocols/diameter/testing.go:1122-1126`: `Errors()` returns the
  slice directly while `addError` appends to it. `TCPMockUASServer.Errors`
  (`internal/protocols/sip/testing.go:476`) copies correctly — make them
  consistent.
- `internal/protocols/common/sctp.go:191`: `time.After` in a read loop leaks a
  timer per physical read until it fires. Use `time.NewTimer` + `defer Stop()`.

---

## P2 — first-contact UX

### [x] 14. README quickstart is broken for `go install` users — DONE `64b9d2e` (library embedded; `cellrift init`; `list` falls back to the bundled copy)

Scenarios are not embedded in the binary. Outside a clone:

```console
$ cellrift list
no scenarios found
$ cellrift validate scenarios/ims/basic_call.yaml
... no such file or directory
```

Fix: `go:embed scenarios/`, or reorder the README so "build from source" is the
quickstart prerequisite.

### [x] 15. `--strict` is a documented no-op — DONE `fe771ab`

`cmd/cellrift/main.go:401,435` declare the flag; nothing reads it. It appears in
`validate --help` and `docs/cli-reference.md`, where users will assume it catches
items 3 and 4. Implement it or remove it.

### [x] 16. `cellrift replay` is a stub that exits 0 — DONE `fe771ab`

Prints `not yet implemented` and returns success, so CI cannot catch it.
`docs/cli-reference.md` documents it with a flag table and examples like a
shipped feature. Mark hidden/experimental, exit non-zero, and label it in docs.

### [x] 17. Per-step `timeout_ms` is silently ignored on Diameter — DONE `5560d3d (SIP), d580c0a (Diameter)`

Only `internal/protocols/sip/adapter.go:549` reads `stepCfg["timeout_ms"]`; the
Diameter adapter never does — it always waits on the adapter's
construction-time timeout. The schema and validator accept the key on any step.
It also does not bound *connect* on either protocol (a SIP step with
`timeout_ms: 300` against a black hole took ~5 s, governed by
`CELLRIFT_TCP_CONNECT_TIMEOUT_MS`).

Fix: implement it for Diameter, or reject it at validate time on Diameter steps
until implemented.

### [x] 18. `--verbose` masks the data you are debugging — DONE `fe771ab`

The raw dump is PII-masked (`From: sip:[REDACTED-1]@ims.local`). Against your own
lab gear this hides the MSISDN you are trying to verify. Add `--no-mask`, or do
not mask terminal `--verbose`.

### [x] 19. Missing `protocols:` block produces a nonsense error — DONE `34a8108`

Adapters are built only from declared protocols, so omitting the block yields
`no adapter for protocol "sip"`, which reads like a broken build. Should be a
validate-time error naming the missing `protocols:` entry.

### [x] 20. Doc and output drift — DONE `fe771ab, 71cca6d`

- `CHANGELOG.md` still claims "7 built-in scenarios"; the real count is 15
  (`expectedScenarioCount` in `internal/scenario/library_test.go`).
- `validate` prints nothing on success — the first command in the quickstart
  produces silence. Print `<file>: OK`.
- Sub-second durations render as `0ms`; the golden file
  (`docs/examples/terminal.txt`) shows per-step milliseconds.
- `internal/reporter/terminal.go:87-92` has two sequential `if`s that can print
  the same error twice when both `step.Result.Error` and `step.Error` are set.
  The golden file shows it once. Use `else if`.
- Dockerfile uses floating `golang:1.26-alpine` while the Makefile comment
  claims it matches the pinned `GO_VERSION := 1.26.6`.
- The Go toolchain is hard-pinned to 1.26.6 for a documented security reason; a
  user on an older Go with `GOTOOLCHAIN=local` gets a bare version error with no
  pointer to the rationale. Surface it in the README.

### [x] 21. No way to see a passing scenario without external infrastructure — DONE `64b9d2e` (`cellrift selftest` + `scenarios/selftest/loopback.yaml`; running it exposed the non-existent `registered` state, fixed in the same commit)

There is a capable in-process mock UAS in the test suite but nothing
user-facing. A `cellrift selftest` (or a loopback scenario) would be the single
highest-value addition for first contact — right now a new user's first run
fails, by design, and they cannot tell that apart from a broken install.

---

## Example scenario library (item 22 is publication-blocking; 23–25 are showcase quality)

The 15 shipped scenarios in `scenarios/` are the first thing a new user reads
and the thing they will copy. They need a deliberate pass for **currency,
correctness, usefulness, and feature coverage** — right now all four have gaps.

Inventory as of this review: 15 files (5 under `scenarios/diameter/`, 10 under
`scenarios/ims/`), 11 of which carry a `MOCK-ONLY` marker.

### [x] 22. Six scenario headers will be stale once the UAR fix is confirmed — DONE: four interop runs closed every gap, uar_uaa.yaml is proven in CI and no longer MOCK-ONLY, and every header now states the real scope

These files document the Diameter 5005 gap as a current limitation in their
header comments:

- `scenarios/diameter/uar_uaa.yaml`
- `scenarios/diameter/cx_mar_sar.yaml`
- `scenarios/diameter/credit_control.yaml`
- `scenarios/ims/register_basic.yaml`
- `scenarios/ims/register_deregister.yaml`
- `scenarios/ims/register_assert_liveness.yaml`

They say, in various wordings, that "Cellrift's UAR omits a
Vendor-Specific-Application-Id (AVP 260) grouped AVP freeDiameter's Cx
dictionary requires". As of the fix that added that AVP to every vendor-scoped
request, the code no longer omits it.

**Do not edit these yet.** The claim only becomes provably stale once the real
Open5GS interop job confirms a `Result-Code 2001`. Sequence it as: run the
interop CI → capture fresh evidence → then update all six headers *and*
reconsider each file's `MOCK-ONLY` marker in one pass. Updating them before the
real-peer run would replace a true statement with an unverified one, which is
the exact mistake worth avoiding right before publication.

Also re-check at that point: the same claim appears in `README.md`,
`docs/roadmap.md`, and `CHANGELOG.md`.

### [x] 23. Four implemented features have no worked example at all — DONE (sctp_abort.yaml, register_parallel_wait.yaml, delay_ms step in register_impaired.yaml, all e2e-tested)

Verified by grepping the whole `scenarios/` tree:

| Feature | Requirement | Scenarios using it |
| --- | --- | --- |
| `action: parallel` | flow control | **0** |
| `action: wait` | flow control | **0** |
| `delay_ms` (post-receive delay) | timing impairment | **0** |
| `sctp_abort:` | connection fault | **0** — appears only inside comments explaining why the file does *not* use it |

Two of these are notable beyond mere coverage:

- **`action: parallel` has no example and a known concurrency bug** (item 10).
  If a worked example is added, it must not be two same-protocol sub-steps until
  that bug is fixed — and if the validator ends up rejecting that shape, the
  example should demonstrate the cross-protocol form instead.
- **`sctp_abort` is a whole shipped requirement with no demonstration.** It needs
  a Diameter-over-SCTP scenario; the two files that mention it are SIP and
  explicitly cannot use it.

Well covered, for contrast — no action needed: all six Diameter commands
(UAR/CCR/AIR/ULR/MAR/SAR) appear, all four transports (udp/tcp/tls/sctp) appear,
and `capture:` is used in 11 of 15 files.

### [x] 24. Several examples cannot fail, and some demonstrate nothing assertable — DONE `4f38138`

- `scenarios/ims/malformed_sip_headers.yaml` and
  `scenarios/diameter/malformed_diameter.yaml` carry **no assertions at all**,
  and every step in them is a `malform:`/`action: raw` step whose result is
  unconditionally `passed` (item 6). Both are green even with no peer listening.
  As the worked examples for the entire v1.1 malformed-traffic feature set, they
  currently teach users to write tests that cannot fail.
- `scenarios/ims/register_conn_fault.yaml` contains an assertion
  (`response.connection_state == "truncated"`) that never executes, because
  assertions are skipped on failed steps (item 5) — and which would also fail on
  quoting if it did run (the RHS quotes are compared literally). Fixing items 5
  and 6 should include re-validating this file end to end.

### [x] 25. Decide what the scenario library is *for* — DONE (scenarios/README.md: self-test / lab template / showcase, per-file exit codes; every file now e2e-tested; call_rejected labelled fail-by-design)

Related to item 21 (no self-test mode): 11 of 15 scenarios are `MOCK-ONLY`, and
a new user running any of them against nothing gets a failure that is
indistinguishable from a broken install.

Worth deciding explicitly, and stating in `scenarios/README.md` (which does not
exist yet):

- Which scenarios are **runnable demos** (work out of the box, ideally against a
  bundled mock), versus **templates to copy and edit**, versus **regression
  fixtures for this repo's own CI**. Right now all three kinds sit in one
  directory with no signposting.
- Whether the feature-showcase examples should be a separate, clearly-labelled
  set — a small `scenarios/examples/` where each file demonstrates exactly one
  capability with assertions that genuinely verify it.

---

## Repository hygiene

### [x] 26. ~3 MB of deleted RFC text remains in git history — DONE

The pre-publication history was squashed into a single parentless commit
before publishing, so nothing from the internal pre-history survives.
Re-verified on the published repository (2026-11-24):

| Check | Command | Result |
| --- | --- | --- |
| No RFC text in any commit | `git log --all --name-only -- 'docs/rfcs/*'` | no output |
| Largest blobs | `git rev-list --objects --all` + `cat-file --batch-check` | biggest is `test/interop/EVIDENCE.md` at 120 KB; every one of the ten largest is our own source or committed interop evidence |
| Repository size | `git count-objects -vH` | 1.8 MB of objects (was ~3 MB of RFC text alone) |
| No secrets, `.planning/`, `dist/` or binaries | path scan across `git log --all --name-only`, plus `gitleaks git --log-opts=--all` | no output; gitleaks finds no leaks |
| No key material | `git grep` for PEM headers / `AKIA` / `ghp_` / `xox*` across every ref | matches only this file's own description of the check |

One caution still applies: `.planning/` must stay gitignored, which
`git status --short | grep -i planning` confirms it is.

---

## Verified sound — do not re-review

These were checked specifically and found correct:

- **Decoder robustness.** ~800k adversarial inputs fuzzed through the Diameter
  (`ParseMessage` → `DecodeAVPs` → `DecodeAVP` → `DecodeGroupedAVP`) and SIP
  parsers: zero panics. Length arithmetic cannot overflow (3-byte cap),
  `DecodeAVPs` cannot spin (8-byte minimum consumption), and allocation is
  bounded by the input buffer rather than the declared length. A peer claiming a
  16 MB message gets an error, not an allocation.
- **No command injection** — no `os/exec` outside test files.
- **No path traversal** — every path comes from user argv; no network-sourced
  data composes a path.
- **No credential leakage into reports** — the schema carries cert/key/CA
  *paths* only; key material never enters a report struct.
- **Exit codes are correct** — partial failure → 1, validation error → 2, and
  the three reporters agree on pass/fail.
- **Diameter answer correlation is sound** — a late answer arriving after its
  step gave up is discarded on the Hop-by-Hop check rather than corrupting the
  next step, and both deadlines are genuinely computed once and never re-armed.
- **Impairment state does not leak between steps** — `impair.Configure` is
  called unconditionally on every send and resets both armed flags.
- **`envInt` rejects non-positive overrides**, so `SIPMaxProvisional()` can never
  return 0 (no nil-deref in the provisional loop).
- **Both reorder implementations** build every member before writing any, start
  no goroutines, and abort the whole group rather than half-sending.

---

## Suggested order

1. **Item 1** (SIP status gate) — biggest credibility risk, smallest diff.
2. **Item 3** (`KnownFields(true)`) — cheap, removes a whole silent-failure class.
3. **Item 4** (absent operand is an error) — needs a small syntax decision for
   deliberate-absence assertions.
4. **Item 2** (SIP absolute deadline + fail on provisional exhaustion).
5. **Item 7** (TLS opt-in) and **items 8/9** (masking gaps).
6. **Item 5** (assertions on failed steps) — needs a design decision, and
   unblocks negative testing plus fixes the dead assertion in
   `register_conn_fault.yaml`.
7. **Item 10** (parallel adapter sharing) — validator rejection is an acceptable
   interim fix.
8. **Run the real Open5GS interop job**, then **item 22** (the six stale
   scenario headers) plus the matching claims in `README.md`,
   `docs/roadmap.md` and `CHANGELOG.md`, in one pass. This is the last thing
   that must be true before the repo goes public, and it cannot be done earlier
   — the headers are still accurate until the fix is proven against a real peer.
9. **Items 6 and 24 together** — adding assertions to the two malformed-traffic
   examples is the same edit as making them able to fail.
10. Everything else, including items 23 and 25 (missing worked examples,
    scenario-library structure).

## The pattern worth fixing at the root

Items 1, 4, and the `responseFieldsMap` defect fixed earlier are the same
bug class: **the mock peers always behave well.** Every mock answers
successfully, so no test ever exercised "the peer returned 500", "the AVP was
absent", or "the field name was wrong". The impairment suite made the *transport*
hostile but left the *responses* cooperative.

A small set of sad-path mock modes — error status codes, absent AVPs, malformed
answers — would have caught all three before they shipped, and would prevent the
next one. Worth doing before, or alongside, the individual fixes above.
