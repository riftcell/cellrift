# test/interop — Real-Peer Interop Harnesses (Open5GS, SigScale OCS, Asterisk)

This directory holds three real, third-party peer harnesses: the Open5GS HSS
(Diameter, below), the SigScale OCS (Diameter credit control) and Asterisk
(SIP, see [the Asterisk SIP harness](#up-sipsh--run-sip-scenariosh--the-asterisk-sip-harness)).
The Diameter sections below vendor everything needed to stand up a real
Diameter peer (`gradiant/open5gs:2.8.0`'s HSS) and run Cellrift's own
`uar_uaa.yaml` scenario against it — proving the CER/CEA handshake and the UAR/UAA exchange against software
Cellrift did not write, not just its own in-repo mock.

Every fact and command below was empirically verified by a real Docker spike
against the image. Nothing here was guessed from documentation.

## Why two containers

`gradiant/open5gs:2.8.0` ships **no database and no provisioning tooling** —
no `open5gs-dbctl`, no `mongosh`, no `node`, no `python3` anywhere in the
image. The HSS's own `hss.yaml` hard-codes `db_uri: mongodb://localhost/open5gs`,
and there is nothing inside the image that can populate that database. A
`mongo:7.0` sidecar supplies both the database itself and the `mongosh`
binary the vendored provisioning script shells out to.

Both containers run with `--network host`, so `localhost` inside each
container resolves to the runner's own loopback — this is what lets Mongo and
the HSS reach each other, and lets `127.0.0.1:3868` reach the HSS from the
host, without a `docker network create` step.

## Vendored files and their provenance

| File | Source | Notes |
|------|--------|-------|
| `hss.yaml` | Hand-written for this harness, based on the image's own baked-in config (`/opt/open5gs/etc/open5gs/hss.yaml`) | Sets `db_uri: mongodb://localhost/open5gs`. See "DB_URI env var" correction below — the YAML alone is not sufficient; `up.sh` also passes `-e DB_URI=mongodb://localhost/open5gs` on the HSS container. |
| `hss-freeDiameter.conf` | Extracted verbatim from the running image (`docker cp hss-extract:/opt/open5gs/etc/freeDiameter/hss.conf`), then edited | See "The two edits" below. |
| `open5gs-dbctl` | Upstream Open5GS source, fetched once at authoring time from `https://raw.githubusercontent.com/open5gs/open5gs/v2.8.0/misc/db/open5gs-dbctl` (pinned to the `v2.8.0` tag, matching the deployed image) and committed unmodified | **Licence: GNU AGPLv3** (Open5GS's own project licence — see the upstream repository's `LICENSE` file). This script is vendored verbatim, byte-for-byte, and is NOT modified by Cellrift. It is invoked as a subprocess inside the `mongo` container at harness run time; it is not linked into, imported by, or distributed as part of the `cellrift` Go binary. Cellrift itself remains MIT-licensed (see repo root `LICENSE`) — this file's AGPLv3 terms apply only to itself, as a standalone vendored script under `test/interop/`, not to the rest of the codebase. |

No file in this directory is fetched over the network at harness *run* time —
`up.sh`/`run-scenario.sh` only pull the two pinned Docker images
(`gradiant/open5gs:2.8.0`, `mongo:7.0`). The `curl` that produced
`open5gs-dbctl` was a one-time, authoring-time step; re-running it is only
needed if the image tag changes.

### The two edits to `hss-freeDiameter.conf`

The image's own shipped `hss.conf` is extracted unmodified except for exactly
two changes, both marked inline with `# Cellrift edit N/2` comments:

1. **Comment out the shipped `ConnectPeer = "mme.gradiant" { ConnectTo = "mme"; ... };`.**
   This is fatal, not cosmetic — on any network with no host literally named
   `mme`, freeDiameter treats the `ConnectTo` DNS-resolution failure as a
   fatal config-parse error and `open5gs-hssd` core-dumps before binding its
   Diameter port at all:
   ```text
   ERROR: /opt/open5gs/etc/freeDiameter/hss.conf:265.49 : Name or service not known
   FATAL: hss_fd_init: Assertion `rv == 0' failed. (../src/hss/hss-fd-path.c:87)
   ```

2. **Append `ConnectPeer = "test.cellrift.local" { No_TLS; No_SCTP; };`.**
   Without a matching `ConnectPeer` entry, the HSS rejects Cellrift's CER with
   a CEA carrying `Result-Code 3010 DIAMETER_UNKNOWN_PEER`. No `ConnectTo` is
   needed — Cellrift always dials the HSS; the HSS never dials back.

The shipped global `No_SCTP;` directive (already present, untouched) is left
as-is: TCP-only is this image's default transport, which is exactly
what the TCP leg needs and requires no per-peer override.

## DB_URI env var — the image's baked-in value always wins

It is tempting to conclude that the image's baked-in `DB_URI` environment
variable "does nothing" and that mounting a custom `hss.yaml` alone is
sufficient to point `hssd` at Mongo. Empirically re-verified while building
this harness, that is **not** the case in this image: Open5GS's own config
loader (`lib/app/ogs-init.c`, confirmed against the `v2.8.0` source) parses
`hss.yaml`'s `db_uri` key first, then — in a later stage of the same
initialization function — unconditionally overwrites it with `getenv("DB_URI")`
if that variable is set at all. The image bakes in
`DB_URI=mongodb://mongo/open5gs` (a hostname that doesn't exist on this
harness's `--network host` topology), so without an explicit override the HSS
always tries to reach a MongoDB host named `mongo` and fails to initialize,
regardless of what the mounted `hss.yaml` says.

**Fix applied in `up.sh`:** the HSS container is started with
`-e DB_URI=mongodb://localhost/open5gs`, which wins the same override and
makes the env var agree with the mounted YAML instead of contradicting it.
`hss.yaml`'s own `db_uri` line is kept for clarity/documentation even though
the env var is now what actually takes effect.

## Cross-Reference — three identities, each duplicated in two files

Every scenario the interop job drives connects under its **own** Origin-Host,
and each name appears in exactly two places that must stay byte-identical:

| Origin-Host | Declared in the HSS conf as | Advertised by |
| --- | --- | --- |
| `test.cellrift.local` | `ConnectPeer` | `scenarios/diameter/uar_uaa.yaml` |
| `s6a.cellrift.local` | `ConnectPeer` | `scenarios/diameter/s6a_air_ulr.yaml` |
| `cx.cellrift.local` | `ConnectPeer` | `scenarios/diameter/cx_mar_sar.yaml` |

If a pair diverges the failure is silent until the CEA comes back
`3010 DIAMETER_UNKNOWN_PEER` — a confusing symptom to debug without knowing
the two files are linked. Each file carries a comment pointing at the other;
if you change one, change both.

**Why three names rather than one.** freeDiameter keeps a single peer object
per Origin-Host. When all three scenarios shared `test.cellrift.local`, the
three sequential `cellrift` processes fought over that one object: the peer
accepted each new association and still routed its answers to the entry it
was reaping, so CI lost a complete AIA and a complete ULA that the HSS had
already built with `Result-Code 2001`. Separate identities remove the
collision, and they model reality better — S6a comes from an MME, Cx from an
I-CSCF. See `EVIDENCE.md`, run 8.

`No_SCTP` on those `ConnectPeer` lines constrains the HSS's **outbound**
attempts only. The global SCTP listener stays enabled and inbound SCTP
associations still reach `STATE_OPEN`; nothing listens on Cellrift's side, so
outbound attempts can only fail, and keeping them off SCTP avoids churning
the peer state machine on a transport that retransmits.

## Synthetic test data

The provisioned subscriber uses IMSI `001010000000001`, in the
3GPP-documented test PLMN range (MCC 001 / MNC 01) — never a real subscriber
identifier. The K/OPc values are synthetic test keys, never a real
subscriber's. This follows the same Ofcom/`.lab.local`/
`.cellrift.local` synthetic-data policy as the rest of this repo (see
`TERMS.md`, `scripts/check-synthetic-data.sh`).

## Running it

```sh
bash test/interop/up.sh
```

```sh
bash test/interop/run-scenario.sh
```

```sh
bash test/interop/down.sh
```

`up.sh` boots the `mongo` and `hss` containers (removing any previous run
first), waits for Mongo to answer a ping, provisions the synthetic
subscriber, boots the HSS, and waits for its `HSS initialize...done` log
line before returning. `run-scenario.sh` runs `uar_uaa.yaml` against the
booted HSS and asserts the CER/CEA handshake and the UAR/UAA outcome as two
separate, distinctly-named stages. `down.sh` removes both containers and is
safe to call even when nothing is running.

For local debugging without going through `run-scenario.sh`'s two-stage
assertion, the bare equivalent is:

```sh
go run ./cmd/cellrift run --target-host 127.0.0.1:3868 scenarios/diameter/uar_uaa.yaml
```

**Always include the port in `--target-host`.** It replaces the scenario
step's entire `Target` field verbatim (`cmd/cellrift/main.go`) — a bare host
with no port produces `dial tcp: address 127.0.0.1: missing port in address`,
not a default-port fallback.

## capture-liveness.sh — one liveness-after-abort experiment

`bash test/interop/capture-liveness.sh <e0|e1|e2|e3|e4|e5>` boots a fresh
peer (`up.sh`; freeDiameter peer objects carry state across client processes),
runs one experiment with a root `tcpdump` on the association, collects the
HSS's own timestamped log (`docker logs -t hss`), and writes a dissected
`chunks.csv`, the peer's log lines of interest and a `SUMMARY.txt` under
`$OUT_ROOT/<id>` (default `/tmp/cellrift-liveness`). `e0` runs
`scenarios/diameter/sctp_abort.yaml` unmodified; the others change one variable
each (identity, fault shape, no fault, transport, settle time).

Exit codes: `0` means the artefacts are trustworthy, **not** that the scenario
passed (the baseline fails by design). `1` means the rig refused to grade a run
that measured the wrong thing: `/proc/net/sctp` absent, a report carrying the
`used tcp` substitution warning, no report written, or a capture with no SCTP
packet. Discard artefacts from an exit-1 run. A refusal exits before teardown,
so run `bash test/interop/down.sh` afterwards.

Prerequisite: `sudo modprobe sctp`. The module does **not** survive a reboot,
and without it Cellrift silently substitutes TCP. Also needs `tshark`,
`tcpdump` and passwordless `sudo`.

## up-sip.sh / run-sip-scenario.sh — the Asterisk SIP harness

```sh
bash test/interop/up-sip.sh
bash test/interop/run-sip-scenario.sh
bash test/interop/down-sip.sh
```

`up-sip.sh` removes any previous run, generates a per-run CA and server
certificate (see the TLS section below), boots the peer, waits until the
container's own `pjsip show transports` lists both bind addresses, checks the
container log for a bind error, waits for the two scenario endpoints, and turns
on the peer's SIP logger (`pjsip set logger on`). `run-sip-scenario.sh` runs
three scenarios against it and grades them. `down-sip.sh` removes the container
and is safe to call when nothing is running; it deliberately leaves the
certificate directory in place so a refused run can be inspected.

**The peer:** `andrius/asterisk:22.10.1_debian-trixie`, pinned by the index
digest (`@sha256:212326bd3f973a597be9e4747be5d6653802e4950159e95aa7d728e394de2012`),
run with `--network host`. It is configured for `chan_pjsip` only: `chan_sip`
was removed in Asterisk 21. There is no official `library/asterisk` image.
Asterisk is a plain SIP server, not a P-CSCF; see `EVIDENCE.md` run 12 for what
that does and does not prove.

**Why ports 15060/15061 and not 5060/5061.** A developer machine often runs its
own Asterisk on the defaults. A host-networked container then fails to bind,
still logs `Asterisk Ready.`, and the scenarios run against the other server.
That is an observed failure, not a hypothetical one. The harness uses 15060
(UDP) and 15061 (TLS) and never targets the defaults.

**Readiness is the container's own `pjsip show transports`**, never the
`Ready` log line, plus a check that the log carries no `Transport ... could not
be started` line. A transport that failed to bind is absent from the transport
list while the `Ready` line still appears.

### What it runs

| Scenario | Target | Notes |
| --- | --- | --- |
| `scenarios/ims/register_impaired.yaml` | `127.0.0.1:15060` (UDP) | three REGISTERs under injected delay |
| `scenarios/ims/basic_call.yaml` | `127.0.0.1:15060` (UDP) | INVITE then BYE |
| `scenarios/ims/basic_call_tls.yaml` | `127.0.0.1:15061` (TLS) | run from a derived copy that verifies the certificate |

`register_basic.yaml` is not among them. It is a mixed scenario (a Diameter step
and an `hss_target` alongside its SIP steps), so it needs an HSS running next to
Asterisk, which this harness does not start.

Each scenario runs twice: once for the terminal rendering and once for the JSON
report, because `--output` replaces stdout and one reporter is selected per run.
The peer therefore receives each scenario's requests twice, and its log holds
both passes. Per-pass receipt counts are written to `$OUT/<name>.receipts` and
`$OUT/<name>.receipts.pass2`; quote those, not a total from the log.

### Exit codes and gates

`0` means all three scenarios passed and every rig-integrity gate held. Non-zero
means either a real failure against the peer or a refusal; a refusal prints
`SIP RIG REFUSED:` and the reason. The gates, each of which has been made to
fire once (the verbatim output is in `evidence/run-12-asterisk/gate-proofs.txt`):

- `up-sip.sh`: the server certificate carries `IP:127.0.0.1`; no transport
  failed to bind; both transports are listed by `pjsip show transports`; both
  endpoints exist; the peer acknowledges that its SIP logger is on.
- `run-sip-scenario.sh`: the running container is the pinned image; both
  transports are still bound; no scenario carries a `malform:` block or an
  `action: raw` step; every scenario carries the `PROVEN AGAINST: Asterisk
  (non-IMS SIP)` marker; the CA file exists; the derived TLS scenario has no
  live `insecure_skip_verify` key and does have a `ca_file`; Cellrift produced
  output and a report; the peer's own log shows it received each scenario's
  requests; the TLS leg carried no verification warning; the terminal and JSON
  passes agree; the peer passed.

### Environment overrides

| Variable | Default | Used by | Meaning |
| --- | --- | --- | --- |
| `CERTS_DIR` | `/tmp/cellrift-sip-certs` | both | where the per-run CA and server certificate are written and read |
| `PJSIP_CONF` | `test/interop/asterisk/pjsip.conf` | `up-sip.sh` | the pjsip configuration mounted into the container |
| `EXPECT_ENDPOINTS` | `1` | `up-sip.sh` | `0` skips the endpoint gate (for `pjsip-reject.conf`, which has none) |
| `OUT` | `/tmp/cellrift-sip` | `run-sip-scenario.sh` | where outputs, reports and receipt counts are written |
| `CA_CRT` | `$CERTS_DIR/ca.crt` | `run-sip-scenario.sh` | the CA the TLS leg trusts |
| `SIP_UDP_TARGET` | `127.0.0.1:15060` | `run-sip-scenario.sh` | UDP target |
| `SIP_TLS_TARGET` | `127.0.0.1:15061` | `run-sip-scenario.sh` | TLS target |
| `STEP_TIMEOUT` | `5000` | `run-sip-scenario.sh` | per-step timeout in milliseconds |

The bare local equivalent of one leg, without the gates:

```sh
go run ./cmd/cellrift run --target-host 127.0.0.1:15060 scenarios/ims/basic_call.yaml
```

The `--target-host` rule from the Diameter section applies here unchanged:
**always include the port**, because the flag replaces every step's `target`
verbatim.

### TLS: the generated certificates, and using your own

**Which files the harness writes.** `up-sip.sh` writes these into `$CERTS_DIR`
(default `/tmp/cellrift-sip-certs`), fresh on every run, never committed:

| File | What it is |
| --- | --- |
| `$CERTS_DIR/ca.key` | the per-run CA's private key |
| `$CERTS_DIR/ca.crt` | the per-run CA certificate; this is what the TLS leg trusts |
| `$CERTS_DIR/asterisk.key` | the server's private key |
| `$CERTS_DIR/asterisk.csr` | the server's signing request |
| `$CERTS_DIR/asterisk.crt` | the server certificate, signed by the CA |
| `$CERTS_DIR/ext.cnf` | the extensions used when signing (SAN, key usage) |

All are synthetic, valid for two days and set to mode 644 so that any container
uid can read them (a CI runner's uid need not match the image's). Asterisk reads
the server pair from `/etc/asterisk/keys/asterisk.crt` and
`/etc/asterisk/keys/asterisk.key` through read-only bind mounts of
`asterisk.crt` and `asterisk.key`.

**Two Asterisk settings that are easy to get wrong**, both in
`test/interop/asterisk/pjsip.conf`:

- `cert_file` and `priv_key_file` are separate files. A combined certificate and
  key PEM in `cert_file` alone fails the handshake with `no shared cipher`.
- `method=tlsv1_2` is set. Without it Cellrift's Go TLS stack (minimum TLS 1.2)
  is refused with `tls: protocol version not supported`.

**The SAN rule.** Cellrift has no `server_name` field and never sets
`tls.Config.ServerName`. Go therefore infers the verification name from the host
portion of the address being dialled, and does **not** fall back to the
certificate's CN. The server certificate's `subjectAltName` must contain the
host you pass to `--target-host`: an **IP SAN** for an IP target
(`IP:127.0.0.1`), a DNS SAN for a name (`DNS:localhost`). The harness generates
`subjectAltName = IP:127.0.0.1, DNS:localhost`, and `up-sip.sh` refuses if the
generated certificate lacks the IP entry. A mismatch fails with
`x509: certificate is valid for X, not Y`. A peer reached by IP but certified
only by a DNS name cannot be verified today; the options are an IP SAN or
`insecure_skip_verify`.

**Substituting your own CA, certificate and key.** `up-sip.sh` does not support
this directly: `generate_certs` deletes and recreates `$CERTS_DIR` on every run,
and the container's bind mounts are taken from that directory at boot, so files
placed there beforehand are removed. The working recipe uses the harness
scripts around your own container:

1. Do not run `up-sip.sh`. Start the container yourself with the same command
   as `start_asterisk` in `up-sip.sh`, replacing the two key mounts with your
   own files: `-v /path/to/your.crt:/etc/asterisk/keys/asterisk.crt:ro` and
   `-v /path/to/your.key:/etc/asterisk/keys/asterisk.key:ro`. Your certificate
   must carry the SAN described above.
2. Run `docker exec asterisk asterisk -rx "pjsip set logger on"`;
   `run-sip-scenario.sh` reads the peer's log to check each scenario's requests
   arrived.
3. Run `CA_CRT=/path/to/your-ca.crt bash test/interop/run-sip-scenario.sh` so
   the derived TLS scenario trusts your CA.

This recipe has not been run in this repository. It follows from reading the two
scripts.

**Pointing a scenario at your own peer** is the more common case. Edit the
`protocols.sip` block of your copy of `basic_call_tls.yaml`: set `ca_file` to
your CA, or keep `insecure_skip_verify: true` for a self-signed lab peer and
accept the warning the run emits. The shipped `basic_call_tls.yaml` keeps
`insecure_skip_verify` deliberately, and the harness never edits it. The CI job
derives a copy in which the `insecure_skip_verify` line is replaced by a
`ca_file` naming the per-run CA, runs that copy, and refuses if the derived file
still carries the key or if the run printed the verification warning.

**Mutual TLS.** Set `cert_file` and `key_file` in the scenario's `protocols.sip`
block to a client certificate and key. On the Asterisk side, add
`ca_list_file=<your CA>` and `verify_client=yes` to the `[transport-tls]`
section of `pjsip.conf`, with the client certificate signed by that CA. **Mutual
TLS is not proven here:** the Cellrift fields exist and the Asterisk keys are
the documented ones, but no run in this repository has exercised them.

## What this harness does NOT prove yet

`run-scenario.sh` makes two separate, distinctly-named assertions: stage 1
is the CER/CEA handshake and stage 2 is the UAR/UAA exchange. Keeping them
apart means a handshake failure is never reported as an application failure,
or the reverse. Open5GS's own Cx dictionary rejects a `Public-Identity` AVP
with `5001 DIAMETER_AVP_UNSUPPORTED` unless 3GPP AVPs carry the
Vendor-Specific bit and Vendor-Id 10415 (TS 29.229 defines every Cx AVP as
vendor-specific); before Cellrift did that, stage 1 passed and stage 2
failed, which is how the two were shown to be independent.
