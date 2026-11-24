# Getting Started — Cellrift Python SDK

The Cellrift Python SDK is a thin wrapper around the `cellrift` CLI binary. It lets
you drive Telco signalling scenario tests from Python scripts, pytest suites, and
CI pipeline steps without constructing shell commands by hand.

---

## Prerequisites

- Python 3.8 or later
- The `cellrift` binary on `PATH`

The SDK shells out to the binary for every operation, so the binary must be installed
first. The package does not bundle the binary.

### Install the binary

```bash
# From source (requires Go 1.26.11+):
go install github.com/riftcell/cellrift/cmd/cellrift@latest

# Verify:
cellrift --version
# cellrift version 0.1.0-dev
```

Alternatively, download a pre-built release binary for your platform from the GitHub
Releases page and place it anywhere on `PATH`.

---

## Install the SDK

### From source (development)

```bash
git clone https://github.com/riftcell/cellrift
cd cellrift
pip install -e sdk/python/
```

### From PyPI (once published)

```bash
pip install cellrift
```

---

## Quick start

```python
import cellrift

result = cellrift.run(
    "scenarios/ims/basic_call.yaml",
    variables={
        "pcscf_host": "ims.lab.local",
        "callee_msisdn": "447700900002",
    },
)

print(result["status"])           # "passed" or "failed"
for step in result["steps"]:
    print(step["id"], step["status"], step["duration_ms"], "ms")
```

The `run()` function returns a dict parsed from the CLI's JSON output — the same
structure as `cellrift run --format json`.

---

## API reference

### `cellrift.run()`

```python
cellrift.run(
    scenario: str,
    *,
    target_host: str | None = None,
    variables: dict[str, str] | None = None,
    format: str = "json",
    fail_fast: bool = False,
    timeout_ms: int = 3000,
) -> dict
```

Runs a scenario file and returns the result.

| Parameter | Type | Default | Description |
| --- | --- | --- | --- |
| `scenario` | `str` | required | Path to a scenario YAML file |
| `target_host` | `str` | `None` | Override target host for all steps |
| `variables` | `dict` | `None` | Variable overrides (`--var key=value`) |
| `format` | `str` | `"json"` | Output format — keep `"json"` for programmatic use |
| `fail_fast` | `bool` | `False` | Stop on first assertion failure |
| `timeout_ms` | `int` | `3000` | Global step timeout in milliseconds |

**Return value** (when `format="json"`):

```python
{
    "scenario": "VoLTE basic call",
    "status": "passed",          # "passed" | "failed"
    "duration_ms": 10,
    "steps": [
        {
            "id": "send_invite",
            "protocol": "sip",
            "status": "passed",
            "duration_ms": 9,
            "error": None,       # None on pass; error message string on fail
        },
        ...
    ]
}
```

**Raises:**

- `FileNotFoundError` — `cellrift` binary not found on `PATH`
- `subprocess.CalledProcessError` — binary exited with code 2 (execution error, e.g. file not found, connection refused)
- Test failures (exit code 1) do **not** raise — they are returned as `status: "failed"` in the result dict, so your code can inspect which steps failed without a try/except.

---

### `cellrift.validate()`

```python
cellrift.validate(scenario: str) -> bool
```

Validates a scenario file without executing it. Returns `True` if the file is valid,
`False` otherwise. Equivalent to `cellrift validate <file>`.

```python
if not cellrift.validate("my_scenario.yaml"):
    print("scenario has errors — fix before running")
```

---

## Using in pytest

```python
import pytest
import cellrift

@pytest.fixture(scope="session")
def pcscf_host():
    return "ims.lab.local"

def test_basic_call(pcscf_host):
    result = cellrift.run(
        "scenarios/ims/basic_call.yaml",
        variables={
            "pcscf_host": pcscf_host,
            "callee_msisdn": "447700900002",
        },
        timeout_ms=5000,
    )
    assert result["status"] == "passed", f"scenario failed: {result}"

def test_call_rejected(pcscf_host):
    result = cellrift.run(
        "scenarios/ims/call_rejected.yaml",
        variables={"pcscf_host": pcscf_host},
    )
    assert result["status"] == "passed"

def test_scenario_validates():
    assert cellrift.validate("scenarios/ims/basic_call.yaml")
```

Run with:

```bash
pytest tests/ -v
```

---

## Checking step-level results

Inspect individual steps when a scenario fails to identify which assertion broke:

```python
result = cellrift.run("scenarios/ims/register_basic.yaml", variables={...})

if result["status"] == "failed":
    for step in result["steps"]:
        if step["status"] == "failed":
            print(f"FAIL  {step['id']} ({step['protocol']}): {step['error']}")
        else:
            print(f"pass  {step['id']} ({step['duration_ms']}ms)")
```

---

## Overriding variables at runtime

All scenario variables can be overridden from Python without editing the YAML file:

```python
import os
import cellrift

result = cellrift.run(
    "scenarios/ims/basic_call.yaml",
    target_host=os.environ["PCSCF_HOST"],
    variables={
        "caller_msisdn": "447700900001",
        "callee_msisdn": "447700900002",
    },
)
```

`target_host` overrides the `target:` field for every step in the scenario.
`variables` overrides individual `{{ variable }}` references in the scenario YAML.

---

## SIP/TLS scenarios

Use `scenarios/ims/basic_call_tls.yaml` for TLS transport. Pass the TLS port
via `variables`:

```python
result = cellrift.run(
    "scenarios/ims/basic_call_tls.yaml",
    variables={
        "pcscf_host": "ims.lab.local",
        "pcscf_tls_port": "5061",
        "callee_msisdn": "447700900002",
    },
)
assert result["status"] == "passed"
```

The scenario sets `transport: tls` with `insecure_skip_verify: true`, an explicit
lab-only opt-out that accepts any server certificate; the run reports a warning
for it. Server verification is on by default — omit that key and set `ca_file`
to verify a real peer. See
[`docs/cli-reference.md`](cli-reference.md) §"Scenario YAML — Protocol Configuration"
for how to add `cert_file`, `key_file`, and `ca_file` for mTLS.

---

## Error handling

```python
import cellrift, subprocess

try:
    result = cellrift.run("scenarios/ims/basic_call.yaml", variables={...})
except FileNotFoundError as e:
    # cellrift binary not on PATH
    print(f"binary missing: {e}")
except subprocess.CalledProcessError as e:
    # exit code 2 — execution error (bad YAML, connection refused, etc.)
    print(f"execution error: {e.stderr}")
else:
    # exit code 0 (all passed) or 1 (assertions failed) — inspect result
    if result["status"] == "failed":
        failed_steps = [s for s in result["steps"] if s["status"] == "failed"]
        for s in failed_steps:
            print(f"step {s['id']} failed: {s['error']}")
```

---

## Synthetic test data

All scenario YAML files must use synthetic subscriber identifiers.
Use E.164 numbers from the Ofcom test range (`447700900xxx`) and non-routable
domains (`.lab.local`, `.cellrift.local`). Never pass real MSISDNs or SIP identities
as `variables`. See [CONTRIBUTING.md](../CONTRIBUTING.md) §"Synthetic test data"
for the full rule.

---

## Next steps

| Goal | What to do |
| --- | --- |
| Understand the scenario format | [`docs/cli-reference.md`](cli-reference.md) §"Scenario YAML — Protocol Configuration" |
| Run against a real Asterisk | [`docs/getting_started_asterisk.md`](getting_started_asterisk.md) |
| Add SIP/TLS support to Asterisk | [`docs/getting_started_asterisk.md`](getting_started_asterisk.md) §"SIP/TLS — Running encrypted scenarios" |
| Write your own scenario | [`docs/cli-reference.md`](cli-reference.md) — YAML schema reference |
| Run from the CLI directly | `cellrift --help` |
