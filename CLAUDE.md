# Cellrift — Claude Code Instructions

## Code complexity rules

Keep functions small and focused. Complexity accumulates silently — enforce these limits at write time, not in a later cleanup pass.

- **Function length cap: ~40 lines.** When a function grows past this, extract named helpers.
- **Nesting cap: 3 levels.** Deeply nested if/for/switch is a sign a helper is missing.
- **Single responsibility.** A function that resolves variables, builds headers, sends, *and* parses a response is doing too much. Split it.
- **Prefer table-driven over switch.** When a switch has ≥3 cases doing the same structural operation (e.g. map field name → AVP code), replace it with a `map` looked up at runtime.
- **No `pass` flag pattern.** Instead of `var pass bool; switch { ... }; if !pass { ... }`, extract a named boolean helper.
- **No copy-pasted error blocks.** If the same error-return shape appears 3+ times, extract a helper or change the data structure.
- **Named map types over raw primitives.** When a map has a specific semantic role — not just "some strings" — declare a named type and use it everywhere that concept appears. Examples from this codebase: `AdapterMap`, `ResponseFields`, `AVPDictionary`, `dialogRegistry`, `transitionTable`. Inline composite literals (`map[string]string{...}`) remain valid at call sites; only signatures and field declarations need the named type. Include a usage comment explaining what the map represents and how it flows through the system.
  - **Exception: a map stored as a nested value inside `ResponseFields` must be a plain `map[string]interface{}`.** The assertion engine's dotted-path walk (`extractResponseValue` in `internal/engine/executor.go`) type-asserts every nested value to `map[string]interface{}`, and that assertion is false for a named map type. A named type there makes every `response.<key>.*` path read as an absent field rather than raise an error, so an assertion expecting absence would pass for the wrong reason. Only the top-level `ResponseFields` is exempt, because the walk converts it explicitly. `response.header`, `response.body` and `response.auth` follow this rule; `TestAuthFields_StoredAsAPlainMapNotANamedType` pins it for `auth`.

## Auditing existing code

When reading or investigating existing code, **read what the code actually does — do not trust the name alone.**

- Read the function body before relying on its name to understand behaviour.
- If the name and the body disagree (e.g. a function called `validateStep` that also mutates state, or a helper called `buildHeaders` that also sends), flag the mismatch rather than silently working around it.
- **When name and behaviour are misaligned, ask the user before proceeding.** Propose either renaming the function to match what it does, or splitting it so each piece is accurately named. Do not leave the mismatch in place.

## Runtime tunables (CELLRIFT_* env vars)

All time-based variables use an `_MS` suffix so the unit is unambiguous. Parsed by
`internal/config/env.go`; invalid or missing values fall back to the default.

| Variable | Default | Description |
| --- | --- | --- |
| `CELLRIFT_SIP_POLL_INTERVAL_MS` | 50 ms | Sleep between state checks in `assert_state` |
| `CELLRIFT_SIP_MAX_PROVISIONAL` | 50 | Max 1xx responses before treating a request as timed out |
| `CELLRIFT_SIP_DUPLICATE_DRAIN_MS` | 100 ms | How long a `duplicate: true` SIP step spends discarding the peer's response to the second copy |
| `CELLRIFT_TCP_CONNECT_TIMEOUT_MS` | 5000 ms | Dial timeout for outbound TCP connections |
| `CELLRIFT_SCTP_CONNECT_TIMEOUT_MS` | 5000 ms | Bound on how long an outbound SCTP dial blocks the caller |
| `CELLRIFT_TRANSPORT_FALLBACK` | — | Set to `tcp` to force TCP instead of SCTP for Diameter |
| `CELLRIFT_SIP_PASSWORD` | — | Ambient SIP digest password: the lowest-precedence credential source, used only when the scenario declares neither `password` nor a `credentials_file` supplying one. Distinct from `CELLRIFT_VAR_SIP_PASSWORD`, which feeds a `{{ sip_password }}` template. |

When adding a new tunable, add it to `internal/config/env.go` and to this table.
Use `_MS` for millisecond values, no suffix for plain counts.
Not every `CELLRIFT_*` variable is a numeric tunable: `CELLRIFT_SIP_PASSWORD` is a
credential and therefore has no suffix and no default.

## Conventions specific to this codebase

- `__body_*` fields in the step `fields` map are reserved for body injection from executor → adapter. Never add protocol headers starting with `__`.
- Protocol adapters return `(*common.StepResult, error)`. Return `(result, nil)` even on protocol-level failures (wrong status code) — reserve the `error` return for unrecoverable infrastructure errors.
- PII masking: use `reporter.NewMasker()` once per report, then call `masker.Mask(s)` for all strings in that report so the same phone number gets the same `[REDACTED-N]` token.
- **Never cite a `.planning/` document in a code comment.** No `CONTEXT.md`, `RESEARCH.md`, `VALIDATION.md`, `PITFALLS.md`, `SUMMARY.md`, `PLAN.md`, or any path under `.planning/` — that directory is gitignored, so the reference dangles for everyone who clones the repo. Inline the reasoning itself; RFC sections and public issue trackers are fine to cite.

## Testing

```sh
go test ./...        # full suite
go test ./internal/protocols/sip/...   # one package
make test            # the suite, plus the single-threaded pass below
GOMAXPROCS=1 go test -race ./internal/protocols/... ./cmd/...   # scheduling pass
```

**Run the scheduling pass before pushing anything that touches a mock peer
or a step that does not wait for an answer.** A test asserting on what a
mock OBSERVED (`RawReads`, `ReceivedRequests`, `RequestsOfCommand`) right
after a drop, duplicate, connection fault, abort or `action: raw` step is
racing the mock's own read goroutine: those steps return as soon as their
bytes are written. A multi-core machine hides it; a CI runner does not.
Wait for the observation with the mocks' `WaitForRequestsOfCommand` /
`WaitForRequests` / `WaitForRawRead` accessors rather than asserting
immediately — see `internal/protocols/common/mockwait.go`.

All tests must pass before committing. No mocks for protocol adapters in integration tests — use the in-process mock UAS/server already in the test files.
