# Architecture

## Component Diagram

```
                          ┌──────────────────────────────┐
                          │         cellrift CLI          │
                          │  (cmd/cellrift — cobra)       │
                          └──────────┬───────────────────┘
                                     │
                    ┌────────────────┼────────────────┐
                    │                │                │
              ┌─────▼─────┐   ┌─────▼─────┐   ┌─────▼─────┐
              │  Scenario  │   │  Engine   │   │ Reporter  │
              │  Parser &  │   │  Runner,  │   │ Terminal, │
              │  Validator │   │  State,   │   │ JUnit,    │
              │            │   │  Executor │   │ JSON      │
              └─────┬──────┘   └─────┬─────┘   └───────────┘
                    │                │
                    │     ┌──────────┴──────────┐
                    │     │                     │
              ┌─────▼─────▼───┐          ┌──────▼──────┐
              │  SIP Adapter  │          │  Diameter   │
              │  codec,       │          │  Adapter    │
              │  dialog FSM   │          │  AVP codec, │
              │               │          │  message    │
              └───────┬───────┘          └──────┬──────┘
                      │                         │
              ┌───────▼─────────────────────────▼──────┐
              │         Transport Layer                │
              │   UDP  |  TCP  |  SCTP (fallback TCP)  │
              └────────────────────────────────────────┘
```

## Data Flow

A complete execution from `cellrift run` to report output:

1. **CLI** parses flags and file arguments, calls the scenario parser.
2. **Parser** reads YAML into `Scenario` structs; **Validator** checks schema.
3. **Engine Runner** creates a `State` store, loads variables (scenario → env → CLI overrides).
4. For each step in declaration order:
   a. **Executor** resolves `{{ var }}` templates in all string fields.
   - Engine-level actions (`parallel`, `wait`) are handled by the runner itself and never reach an adapter.
   b. Executor dispatches to the **Protocol Adapter** (`SIP` or `Diameter`).
   c. Adapter builds a protocol message, sends it via the **Transport**, waits for a response.
   d. Adapter returns a `StepResult` with parsed response fields.
   e. Executor runs **capture** rules to store values in `State`.
   f. Executor evaluates **assertions** against the state and response.
5. Runner collects all `StepReport`s into a `RunResult`.
6. **Reporter** formats the `RunResult` (terminal, JUnit XML, or JSON) and writes to stdout or a file.
7. CLI sets exit code: 0 (all passed), 1 (test failure), 2 (execution error).

## Key Design Decisions

### Single static binary

Cellrift compiles to a single Go binary with no runtime dependencies. This makes
it trivial to add to any CI runner (`curl` + `chmod +x`) without installing a
language runtime, package manager, or library. The Docker image adds only Alpine
for convenience.

### Pure Go for Phase 1

All protocol implementations (SIP codec, Diameter AVP codec, transport layer)
are written in pure Go. This avoids cgo, keeps cross-compilation simple, and
produces a fully static binary. C bindings for performance-critical paths may
be introduced in Phase 2 if profiling shows a need.

### YAML schema versioning

Every scenario file has a required `version` field. The validator checks it
from day one. This allows the schema to evolve (new fields, changed semantics)
without breaking existing files — the parser can branch on version and migrate
automatically. Retrofitting versioning after adoption is expensive.

### Protocol adapter interface

All protocol engines implement a common interface:

```go
type ProtocolAdapter interface {
    Execute(action, target, message string,
            fields map[string]string,
            config map[string]string) (*StepResult, error)
    Close() error
}
```

This decouples the engine from specific protocols. Adding GTP or 5G SBI in
later phases requires only implementing this interface — no changes to the
engine, runner, or reporter.

### Cross-protocol state sharing

The `State` store is a flat key-value map shared across all steps. Diameter
AVP values captured in step 1 can be referenced as `{{ step1.server_name }}`
in a SIP header field in step 2. This is the core differentiator from tools
that only handle a single protocol. The reserved capture path `response.__raw`
stores a step's unparsed response bytes in this same `State` store, so a later
step can reference them as `{{ step1.raw }}`; byte values render as their
literal text.

### Transport security

**Phase 1** ships SIP/TLS through Go's standard `crypto/tls` package, as a
client, with server certificate verification on by default (against the system
root pool, or a `ca_file`) and optional client certificates. Diameter runs over
plaintext TCP or SCTP.

**Phase 2** extends the same transport layer:

- mTLS for 5G SBI NF-to-NF calls. 3GPP Release 15+ (TS 33.501 §13.3) mandates
  mutual TLS for all SBI interfaces — any SBI adapter must negotiate client certs.
- `cellrift certs generate` CLI for self-signed lab CA and leaf cert pairs.

Cert paths are configured per-protocol-block in the scenario YAML (`cert_file`,
`key_file`, `ca_file`). The same configuration type is reused by both SIP/TLS
and SBI mTLS adapters.

**Export control:** Cellrift ships no compiled cryptographic code beyond what Go's
standard toolchain embeds. Distribution qualifies under EAR §740.13 (Technology and
Software — Unrestricted) OSS exemption — source is publicly available and no BIS
prior review, licence exception, or export control filing is required.
