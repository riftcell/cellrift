# CLI Reference

## `cellrift run`

Execute one or more scenario YAML files against a target environment.

```
cellrift run [flags] <scenario.yaml> [scenario2.yaml ...]
```

### Flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | string | `terminal` | Output format: `terminal`, `junit`, `json` |
| `--output` | string | *(stdout)* | Write report to file instead of stdout |
| `--target-host` | string | | Override target host for all steps |
| `--var` | string[] | | Override scenario variable, e.g. `key=value` (repeatable) |
| `--fail-fast` | bool | `false` | Stop on first assertion failure |
| `--verbose` | bool | `false` | Print raw protocol messages for each step |
| `--no-mask` | bool | `false` | Do not mask phone numbers in output. Lab use only — applies to every format, including JUnit/JSON artifacts. Credentials are removed regardless of this flag. |
| `--timeout` | int | `3000` | Global step timeout in milliseconds |
| `--parallel` | bool | `false` | Run multiple scenario files in parallel |

### Exit Codes

| Code | Meaning |
|---|---|
| 0 | All steps passed |
| 1 | One or more steps failed |
| 2 | Execution error (file not found, connection refused) |

### Examples

```sh
# Run a single scenario with terminal output.
cellrift run scenarios/ims/register_basic.yaml

# Run all VoLTE scenarios, output JUnit XML to a file.
cellrift run --format junit --output results.xml scenarios/ims/*.yaml

# Override a variable and target host.
cellrift run --var subscriber_msisdn=447700900002 \
             --target-host ims.staging.local:5060 \
             scenarios/ims/register_basic.yaml

# Run with fail-fast and verbose output.
cellrift run --fail-fast --verbose scenarios/ims/basic_call.yaml
```

---

## `cellrift validate`

Validate scenario file syntax and schema without executing.

> **Synthetic test data:** Scenario YAML files must use synthetic subscriber identifiers. Use E.164 numbers from the Ofcom test range (`447700900xxx`) and non-routable domains (`.lab.local`, `.cellrift.local`) — never real MSISDNs, IMSIs, or SIP identities. Scenario files committed to source control may be inspected by anyone with repository access. See [TERMS.md](../TERMS.md) for the full data handling guidance.

```
cellrift validate [flags] <scenario.yaml> [scenario2.yaml ...]
```

### Flags

None. Validation is always strict: unknown keys, assertions on fields that
do not exist, and steps whose protocol has no `protocols:` entry are all
rejected.

### Output

Each valid file is confirmed on stdout as `<path>: OK`. Errors go to stderr,
one per line, prefixed with the file and step.

### Exit Codes

| Code | Meaning |
|---|---|
| 0 | All files are valid |
| 1 | One or more files have validation errors |

### Examples

```sh
# Validate a single file.
cellrift validate scenarios/ims/register_basic.yaml

# Validate all scenario files in the library.
cellrift validate scenarios/**/*.yaml
```

---

## `cellrift selftest`

Run the bundled loopback scenario against in-process mock peers.

```
cellrift selftest [flags]
```

Starts a mock SIP UAS (UDP) and a strict RFC 6733 mock Diameter HSS (TCP)
on random loopback ports, runs the embedded `scenarios/selftest/loopback.yaml`
against them with `pcscf_target`/`hss_target` pointed at those ports, and
prints the result exactly as `cellrift run` would. A line on stderr names
the two peers' addresses.

A pass proves this binary, its encoders, the SIP dialog tracker, the
Diameter CER/CEA handshake and the chosen reporter work on this machine. It
proves nothing about a network element you own: the peers are Cellrift's
own mocks. SCTP is not exercised.

### Flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | string | `terminal` | Output format: `terminal`, `junit`, `json` |
| `--output` | string | stdout | Write output to a file |
| `--timeout` | int | `3000` | Global step timeout in ms |
| `--verbose` | bool | `false` | Print all protocol messages |
| `--no-mask` | bool | `false` | Do not mask phone numbers in output |

### Exit Codes

Same as `cellrift run`: `0` when every step passes, `1` when any fails,
`2` when a mock peer cannot be started or the report cannot be written.

### Examples

```sh
# First contact: is the install working?
cellrift selftest

# Prove the JUnit path in CI without any lab.
cellrift selftest --format junit --output selftest.xml
```

---

## `cellrift init`

Write the bundled example scenarios to a directory.

```
cellrift init [dir]
```

`dir` defaults to `./scenarios`. Every scenario embedded in the binary is
written under it, preserving the `ims/`, `diameter/` and `selftest/` layout
the README refers to. A file that already exists at its destination is
**never overwritten** — it is reported as `kept` — so re-running `init`
after a new release adds new examples without touching your edited copies.

### Exit Codes

| Code | Meaning |
|---|---|
| `0` | Library written (or already present) |
| `2` | Filesystem error (unwritable directory, disk full) |

### Examples

```sh
# After go install, in an empty project directory.
cellrift init
cellrift validate scenarios/ims/basic_call.yaml

# Somewhere else.
cellrift init examples/cellrift
```

---

## `cellrift list`

List the scenario library.

```
cellrift list [flags]
```

Scans `./scenarios` and `/scenarios` for parseable scenario files. When
neither exists — a `go install` outside a clone — it lists the copies
embedded in the binary instead, with a note on stderr saying so and
pointing at `cellrift init`.

### Flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | string | `table` | Output format: `table`, `json` |

### Examples

```sh
# Print scenario library as a table.
cellrift list

# Machine-readable JSON output.
cellrift list --format json
```

---

## `cellrift assert`

Evaluate assertions against a JSON or YAML input without running a full scenario. Useful as a standalone CI pipeline step to check protocol responses captured by an earlier step.

```
cellrift assert [flags] <expression> [expression ...]
```

### Flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `--input` | string | | Path to JSON or YAML input file |

### Exit Codes

| Code | Meaning |
|---|---|
| 0 | All assertions passed |
| 1 | One or more assertions failed |
| 2 | Execution error (input file not found, parse error) |

### Examples

```sh
# Assert values from a JSON file.
cellrift assert --input response.json 'result_code == 2001' 'server_name != null'

# Assert a numeric comparison.
cellrift assert --input metrics.yaml 'latency_ms < 500'
```

---

## `cellrift replay`

> **Not yet implemented.** Replaying a Wireshark PCAP capture as a scenario is
> on the roadmap, not in the binary. The command is hidden from `--help`, and
> invoking it prints an error and exits 2 so a CI step cannot succeed by
> accident. No release is committed to it; the intended flags
> (`--protocol`, `--export-scenario`, `--filter`) are placeholders.

---

## Global Flags

| Flag | Description |
|---|---|
| `--version` | Print the version string and exit |
| `--help` / `-h` | Print help for any command |

---

## Environment variables

| Variable | Role |
|---|---|
| `CELLRIFT_VAR_<KEY>` | Fills a `{{ key }}` template, exactly as `--var key=value` does (`CELLRIFT_VAR_SIP_PASSWORD` feeds `{{ sip_password }}`). A template in a credential field is part of credential layer 1. `--var` takes precedence over the environment. |
| `CELLRIFT_SIP_PASSWORD` | The ambient SIP digest password, credential layer 3: used only when the scenario declares no `password` and no `credentials_file` supplies one. |

These are **different mechanisms**. `CELLRIFT_VAR_SIP_PASSWORD` has an effect only if the scenario contains a `{{ sip_password }}` template; `CELLRIFT_SIP_PASSWORD` applies when the scenario asked for nothing. See [SIP digest authentication](#sip-digest-authentication-401--407) for the full precedence.

`CELLRIFT_VAR_<KEY>` predates the digest feature; it was simply not listed on this page until now.

The time and count tunables (`CELLRIFT_SIP_POLL_INTERVAL_MS`, `CELLRIFT_TCP_CONNECT_TIMEOUT_MS`, and the rest) are documented in one place, the runtime-tunables table in `CLAUDE.md`, and deliberately not repeated here.

---

## Scenario YAML — Protocol Configuration

The `protocols:` block at the top of a scenario YAML file configures transport and identity settings for each protocol used in the scenario. All fields are optional except `transport` defaults noted below.

### SIP

```yaml
protocols:
  sip:
    transport: udp        # udp (default), tcp, or tls
    local_port: 0         # accepted but not currently used to bind; the local port is ephemeral

    # TLS only. Server verification is always on unless insecure_skip_verify is set.
    cert_file: client.crt # PEM client certificate for mutual TLS
    key_file:  client.key # PEM private key matching cert_file
    ca_file:   ca.crt     # PEM CA cert to verify the server certificate (default: system roots)
    insecure_skip_verify: false # lab only — disables server verification and reports a warning

    # Digest authentication (answers a 401/407 challenge). See below.
    username: impi-user                 # digest username; need not equal the From user
    password: "{{ sip_password }}"      # literal or {{ var }} template
    credentials_file: creds.yaml        # YAML with username/password keys
```

| Field | Default | Description |
|---|---|---|
| `transport` | `udp` | Transport protocol: `udp`, `tcp`, or `tls` |
| `local_port` | *(none)* | Accepted and validated, but not currently used to bind: the local port is ephemeral whatever this is set to |
| `cert_file` | *(none)* | PEM client certificate — presented during TLS handshake (mTLS) |
| `key_file` | *(none)* | PEM private key paired with `cert_file` |
| `ca_file` | *(none)* | PEM CA certificate used to verify the server's certificate; when omitted the system root pool is used |
| `insecure_skip_verify` | `false` | Disable server certificate verification. Lab only; the run reports a warning when set |
| `username` | *(none)* | SIP digest username (the IMPI in IMS). May be a `{{ var }}` template. No default and no ambient fallback |
| `password` | *(none)* | SIP digest password: a literal or `{{ var }}` template. Layer 1 of the credential precedence below |
| `credentials_file` | *(none)* | YAML file with top-level `username` and `password` keys, relative to the working directory. Layer 2 |

The three credential keys are valid under `sip` only; `cellrift validate` rejects them under `diameter`.

**TLS behaviour when cert fields are omitted:**
If `transport: tls` is set with no cert fields, Cellrift verifies the server certificate against the system root pool — the same default as a browser. To talk to a lab peer with a self-signed certificate, either supply its CA via `ca_file` (preferred) or set `insecure_skip_verify: true`, which accepts any certificate and adds a warning to the run report. Do not use `insecure_skip_verify` against production infrastructure.

#### SIP digest authentication (401 / 407)

A challenge is answered transparently inside the step that was challenged: there
is no separate "respond to the challenge" step, and no flag to turn the behaviour
off. Cellrift computes an RFC 7616 digest (MD5 or SHA-256, `qop=auth`), re-sends
the request with the CSeq incremented and a fresh `Via` branch, and the step's
result is the final `2xx`. A peer that challenges the retry as well has rejected
the credentials: the step fails once, with no further attempt.

Both forms are handled: `401 Unauthorized` with `WWW-Authenticate`, answered in
`Authorization`; and `407 Proxy Authentication Required` with
`Proxy-Authenticate`, answered in `Proxy-Authorization`. When a peer offers
several challenges, the topmost one Cellrift supports is used (RFC 8760 §2.4) and
the rest are ignored; each challenge's own `opaque` is echoed back with it.

Not supported, and treated as "a challenge we do not understand": the `-sess`
algorithm variants, `SHA-512-256`, `qop=auth-int`, a challenge carrying no `qop`
at all, and `AKAv1-MD5` (RFC 3310 IMS-AKA). If no offered challenge is usable the
step fails with a message naming every algorithm and `qop` the peer offered.

**Where the credential comes from — three layers, most explicit first**

1. `protocols.sip.password` in the scenario: a literal, or a `{{ var }}` template.
   A template is resolved from `--var key=value` or from `CELLRIFT_VAR_<KEY>`
   (`CELLRIFT_VAR_SIP_PASSWORD` feeds `{{ sip_password }}`). That is still layer 1
   — the scenario named the template.
2. `protocols.sip.credentials_file`: a YAML file with `username` and `password`
   keys. Read only when a challenge arrives, and only for fields layer 1 did not
   supply. A named file that cannot be read, or that carries an unknown key, is an
   error.
3. `CELLRIFT_SIP_PASSWORD`: the ambient fallback, used only when neither of the
   above supplied a password. This is a **different mechanism** from
   `CELLRIFT_VAR_SIP_PASSWORD`: that one fills in a template the scenario asked
   for; this one applies when the scenario asked for nothing.

A `{{ var }}` in a credential field that cannot be resolved is an **error** and
fails the step. It never falls through to layer 2 or layer 3 — otherwise a typo
would quietly authenticate with a different secret than the scenario named.

The username has no ambient layer and no default: layer 1 or layer 2 only.

**What reaches a report.** Every resolved credential, every scenario-declared password, and the credential header are removed from terminal, JSON and JUnit output under both `--no-mask` states, including when the peer reflects them.

Because removal is by exact text, a credential shorter than 8 characters can
match unrelated text (a port number, a short hostname label). It is still
removed, and the run adds one warning that the report may contain redactions of
unrelated text.

**Asserting on the challenge itself**

Every `action: send` result carries `response.auth.challenged`, so a scenario can
assert the negative as well as the positive.

| Path | Type | Present | Meaning |
|---|---|---|---|
| `response.auth.challenged` | bool | always | the peer answered 401/407 |
| `response.auth.retried` | bool | when challenged | a retry was actually sent |
| `response.auth.challenge_header` | string | when challenged | `WWW-Authenticate` or `Proxy-Authenticate` |
| `response.auth.challenge_status` | int | when challenged | 401 or 407 |
| `response.auth.realm` | string | when a challenge was selected | the challenge's realm |
| `response.auth.nonce` | string | same | the challenge's nonce |
| `response.auth.qop` | string | same | the token used: `auth` |
| `response.auth.algorithm` | string | same | `MD5` or `SHA-256` |
| `response.auth.opaque` | string | same, when the peer sent one | the chosen challenge's own opaque |

`response.status_code` remains the **final** response's code — `200` on a
successful authenticated step, not the `401` that was answered. Nothing
credential-derived is ever captured: no username, no password, no computed
`response=` and no cnonce.

**Example — authenticated REGISTER:**

```yaml
protocols:
  sip:
    transport: udp
    username: impi-user
    password: "{{ sip_password }}"     # cellrift run --var sip_password=...

steps:
  - id: register
    protocol: sip
    action: send
    target: "pcscf.lab.local:5060"
    message: REGISTER
    fields:
      From: "<sip:447700900201@ims.lab.local>"
      To: "<sip:447700900201@ims.lab.local>"
      Contact: "<sip:447700900201@10.0.0.1:5060>"
    assert:
      - response.status_code == 200
      - response.auth.challenged == true
      - response.auth.algorithm == MD5
```

**Example — plaintext UDP (default):**

```yaml
protocols:
  sip:
    transport: udp
```

**Example — TLS with server verification disabled (lab / self-signed certs):**

```yaml
protocols:
  sip:
    transport: tls
```

**Example — TLS with CA verification and client cert (mTLS):**

```yaml
protocols:
  sip:
    transport: tls
    ca_file:   certs/ca.crt
    cert_file: certs/client.crt
    key_file:  certs/client.key
```

### Diameter

```yaml
protocols:
  diameter:
    transport: sctp        # sctp (default; falls back to tcp with a recorded
                            # warning if the kernel lacks SCTP) or tcp (forces
                            # TCP, no SCTP attempt, no warning)
    local_port: 0
    origin_host: cellrift.lab.local
    origin_realm: lab.local
```

| Field | Default | Description |
|---|---|---|
| `transport` | `sctp` (unset behaves the same) | Transport: `sctp` attempts real SCTP and falls back to TCP with a recorded warning if the kernel doesn't provide it; `tcp` forces TCP directly with no SCTP attempt and no warning |
| `local_port` | *(none)* | Accepted and validated, but not currently used to bind: the local port is ephemeral whatever this is set to |
| `origin_host` | `cellrift.diameter.invalid` | Diameter Origin-Host AVP. The default will not interoperate with a real peer; set it explicitly |
| `origin_realm` | `diameter.invalid` | Diameter Origin-Realm AVP. The default will not interoperate with a real peer; set it explicitly |
