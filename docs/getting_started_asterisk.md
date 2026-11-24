# Getting Started — Running a SIP Call Test Against Asterisk

This guide walks through running Cellrift's built-in `basic_call.yaml` scenario against a local or remote Asterisk instance. It covers what Asterisk needs, what the scenario does, the variables to override, and the friction points you will hit.

---

## What the scenario does

`scenarios/ims/basic_call.yaml` exercises a two-step SIP call flow:

1. Send `INVITE` → assert `180 Ringing` then `200 OK`
2. Send `BYE` → assert `200 OK`

Cellrift acts as the UAC (calling side). Asterisk acts as the UAS.

---

## Prerequisites

- Cellrift binary on PATH (`cellrift --version` works)
- An Asterisk instance reachable on UDP port 5060
- A dialplan extension that answers and waits (see below)

---

## Step 0 — Install Asterisk

If you do not have Asterisk installed, use the commands below for a minimal installation. These install Asterisk from distribution packages — sufficient for signalling testing with Cellrift. You do not need to compile from source.

### Ubuntu / Debian

```bash
sudo apt-get update
sudo apt-get install -y asterisk
sudo systemctl enable --now asterisk
```

Default config files land in `/etc/asterisk/`. Verify it is running:

```bash
sudo asterisk -rx "core show version"
```

### Fedora

```bash
sudo dnf install -y asterisk
sudo systemctl enable --now asterisk
```

Verify:

```bash
sudo asterisk -rx "core show version"
```

### Version note

Package versions vary by distro release. For this guide any Asterisk 16, 18, or 20 works. Check what you have:

```bash
asterisk -V
```

If your distro ships an older version (< 16) or you need PJSIP support, install from the Asterisk-provided script instead:

```bash
# Downloads and runs the official install script — review before running
curl -sL https://raw.githubusercontent.com/asterisk/asterisk/master/contrib/scripts/get_mp3_source.sh
# Or use the AsteriskNOW / DPMA installer from asterisk.org
```

For a quick local test, the distro package is always the fastest path.

---

## Step 1 — Configure Asterisk

### Config file locations

All Asterisk config files live in `/etc/asterisk/` on both Ubuntu/Debian and Fedora — the path is the same on both distros.

| File | Purpose |
|---|---|
| `/etc/asterisk/extensions.conf` | Dialplan — routes calls to actions |
| `/etc/asterisk/sip.conf` | chan_sip peer config (legacy, pre-2020 installs) |
| `/etc/asterisk/pjsip.conf` | chan_pjsip endpoint config (default on modern distros) |

The files ship pre-populated with commented examples. For a minimal test it is easiest to append your stanzas at the bottom of each file rather than replacing the defaults.

---

### Check which channel driver is active

Run this first — it determines which config file you need to edit:

```bash
sudo asterisk -rx "module show like chan_pjsip"
sudo asterisk -rx "module show like chan_sip"
```

- If `chan_pjsip` shows `Running` → follow the **pjsip.conf** section below (Fedora 37+, Ubuntu 22.04+)
- If `chan_sip` shows `Running` → follow the **sip.conf** section below (older installs)
- If neither shows `Running` → the module is likely commented out in `/etc/asterisk/modules.conf` (see below)

#### If neither module is running — uncomment in `modules.conf`

Open `/etc/asterisk/modules.conf` and look for the commented-out line:

```bash
sudo nano /etc/asterisk/modules.conf
```

Find and uncomment the relevant line by removing the leading `;`:

```ini
; Before (commented out — module will not load):
;require=chan_pjsip.so

; After (uncommented — module will load on next restart):
require=chan_pjsip.so
```

Then restart Asterisk to pick up the change:

```bash
sudo systemctl restart asterisk
```

Verify the module is now running:

```bash
sudo asterisk -rx "module show like chan_pjsip"
```

#### If the .so file itself is missing — install the separate package

If after uncommenting and restarting the module still does not load, check the Asterisk logs:

```bash
sudo journalctl -u asterisk --since "1 minute ago" | grep ERROR
```

If you see:

```
ERROR[...]: loader.c: Error loading module 'chan_pjsip.so':
/usr/lib64/asterisk/modules/chan_pjsip.so: cannot open shared object file:
No such file or directory
```

The `.so` file is not installed. chan_pjsip ships in a separate package on some distros:

**Fedora:**

```bash
sudo dnf install asterisk-pjsip
sudo systemctl restart asterisk
```

**Ubuntu / Debian:**

chan_pjsip is typically bundled in the main `asterisk` package on Ubuntu/Debian and should already be present. If it is missing, search for it:

```bash
apt-cache search asterisk | grep -i pjsip
```

Install whatever package appears (likely `asterisk` itself needs reinstalling or a meta-package is missing):

```bash
sudo apt-get install --reinstall asterisk
```

After installing, restart and re-check:

```bash
sudo systemctl restart asterisk
sudo asterisk -rx "module show like chan_pjsip"
```

---

### Dialplan — `/etc/asterisk/extensions.conf`

Required regardless of which channel driver you use.

Append to the bottom of the file:

```bash
sudo nano /etc/asterisk/extensions.conf
```

```
[default]
exten => 447700900002,1,Answer()
 same => n,Wait(5)
 same => n,Hangup()
```

This extension answers the call, waits 5 seconds, then hangs up — giving Cellrift a clean 200 OK without requiring RTP media.

Reload: `sudo asterisk -rx "dialplan reload"`

---

### PJSIP endpoint — `/etc/asterisk/pjsip.conf` (chan_pjsip — Fedora, Ubuntu 22.04+)

Append to the bottom of the file:

```bash
sudo nano /etc/asterisk/pjsip.conf
```

```ini
[transport-udp]
type=transport
protocol=udp
bind=0.0.0.0:5060

[cellrift]
type=endpoint
context=default
disallow=all
allow=ulaw,alaw
allow_overlap=no
aors=cellrift
; No auth= line — omitting it disables authentication for this endpoint.
; Do NOT set auth=none — chan_pjsip treats that as a reference to an auth
; object named "none", fails to find it, and returns 500 Internal Server Error.
; allow_overlap=no is required — the default is yes (overlap dialling, RFC 3578).
; With overlap enabled Asterisk returns 484 Address Incomplete for every call,
; even when the extension exists, because it waits for more digits to arrive.

[cellrift]
type=aor
max_contacts=1

[cellrift-identify]
type=identify
endpoint=cellrift
match=127.0.0.1        ; single IP — Cellrift running on the same machine
```

The `match=` field in `[cellrift-identify]` accepts several formats. Use whichever fits your setup:

```ini
; Single IP — Cellrift on the same host
match=127.0.0.1

; Single remote IP — Cellrift on a specific machine
match=192.168.1.50

; Subnet (CIDR) — accept from anywhere on the local network
match=192.168.1.0/24

; Multiple entries — accept from several IPs or subnets
match=127.0.0.1
match=192.168.1.0/24
match=10.0.0.0/8

; Accept from any IP — use only in isolated lab environments
match=0.0.0.0/0
```

`match=` lines are cumulative — you can repeat the key as many times as needed within the same stanza.

Three stanzas are required:

- `[transport-udp]` — binds chan_pjsip to UDP 5060; without it Cellrift receives a timeout
- `[cellrift]` endpoint + aor — defines the endpoint and its address-of-record
- `[cellrift-identify]` — maps incoming requests by source IP to the `cellrift` endpoint; **without this Asterisk cannot match the INVITE to the endpoint and will challenge with `401 Unauthorized` even though `auth=none` is set**

Reload: `sudo asterisk -rx "pjsip reload"`

---

### SIP peer — `/etc/asterisk/sip.conf` (chan_sip — legacy installs only)

Only follow this section if `chan_sip` is running (see driver check above). Skip if you are on Fedora 37+ or Ubuntu 22.04+.

Append to the bottom of the file:

```bash
sudo nano /etc/asterisk/sip.conf
```

```ini
[cellrift]
type=friend
host=dynamic
context=default
allow=ulaw,alaw
insecure=port,invite   ; skip auth for testing
```

Reload: `sudo asterisk -rx "sip reload"`

---

## Step 1b — Verify Asterisk is reachable on UDP 5060

A `receive timeout: i/o timeout` error from Cellrift means the INVITE packet reached no one — Asterisk is not listening, the port is firewalled, or it is bound to the wrong interface. Work through these checks in order before running the scenario.

### 1. Confirm Asterisk is listening on UDP 5060

```bash
sudo ss -ulnp | grep 5060
```

Expected output (chan_pjsip):

```
UNCONN  0  0  0.0.0.0:5060  0.0.0.0:*  users:(("asterisk",pid=...,fd=...))
```

If this line is missing, Asterisk is not bound to 5060. Check `/etc/asterisk/pjsip.conf` for a `[transport-udp]` section and add one if absent:

```bash
sudo nano /etc/asterisk/pjsip.conf
```

```ini
[transport-udp]
type=transport
protocol=udp
bind=0.0.0.0:5060
```

Reload and re-check:

```bash
sudo asterisk -rx "pjsip reload"
sudo ss -ulnp | grep 5060
```

### 2. Check the firewall

**Fedora (firewalld):**

```bash
sudo firewall-cmd --list-all | grep -i sip
```

If `sip` is not listed, open the port:

```bash
sudo firewall-cmd --add-port=5060/udp --permanent
sudo firewall-cmd --reload
```

**Ubuntu / Debian (ufw):**

```bash
sudo ufw status
```

If active and 5060 is not listed, allow it:

```bash
sudo ufw allow 5060/udp
```

### 3. Send a test packet from the Cellrift machine

If Asterisk is on a different host, test basic UDP reachability before running Cellrift:

```bash
# Install if needed: sudo dnf install nmap  /  sudo apt-get install nmap
sudo nmap -sU -p 5060 <asterisk-ip>
```

Expected: `5060/udp open|filtered sip`

Alternatively, send a raw OPTIONS ping with `sipsak` if installed:

```bash
sipsak -s sip:<asterisk-ip>
```

A `200 OK` reply means UDP 5060 is reachable and Asterisk is responding to SIP.

### 4. Verify Asterisk is not bound to localhost only

If `ss` shows Asterisk listening on `127.0.0.1:5060` instead of `0.0.0.0:5060`, it will only accept connections from the same machine. Fix the bind address in `pjsip.conf`:

```ini
[transport-udp]
type=transport
protocol=udp
bind=0.0.0.0:5060   ; was 127.0.0.1 — change to accept external connections
```

Reload: `sudo asterisk -rx "pjsip reload"`

---

## Step 2 — Validate the scenario

```bash
cellrift validate scenarios/ims/basic_call.yaml
```

Expected output: no errors, exit code 0.

---

## Step 3 — Run the scenario

Override the two variables that point at your Asterisk instance:

```bash
cellrift run \
  --var pcscf_host=<asterisk-ip> \
  --var callee_msisdn=447700900002 \
  --verbose \
  scenarios/ims/basic_call.yaml
```

`--verbose` prints the raw SIP messages for each step so you can see exactly what Asterisk returned.

**Example with a local Asterisk:**

```bash
cellrift run \
  --var pcscf_host=127.0.0.1 \
  --var callee_msisdn=447700900002 \
  --verbose \
  scenarios/ims/basic_call.yaml
```

---

## Step 4 — Interpret the output

A passing run looks like:

```
Scenario: VoLTE basic call
  ✓ [sip] send send_invite (143ms)
  ✓ [sip] send send_bye (12ms)
2/2 steps passed in 155ms
```

A failing run shows the assertion diff:

```
Scenario: VoLTE basic call
  ✗ [sip] send send_invite (51ms)
    Error: assertion failed: response.status_code == 200 (actual: 401)
1/2 steps passed in 51ms
```

---

## Known friction points

### Receive timeout (i/o timeout on both steps)

```text
✗ [sip] send send_invite (3002ms)
  Error: receive timeout: read udp [::]:57912: i/o timeout
```

Both steps timing out at exactly 3000ms means Cellrift sent the INVITE but received nothing back — the packet never reached Asterisk or Asterisk never responded. Follow **Step 1b** above in order: check `ss -ulnp`, check the firewall, then test raw UDP reachability with `nmap`.

---

### 401 Unauthorized on INVITE

```text
✗ [sip] send send_invite
  Error: assertion failed: response.status_code == 200 (actual: 401)
  --- raw response ---
  SIP/2.0 401 Unauthorized
  WWW-Authenticate: Digest realm="asterisk", ...
```

Asterisk challenged the INVITE with Digest auth. The most common cause with chan_pjsip is a missing `[identify]` stanza — without it Asterisk cannot match the incoming request to the `[cellrift]` endpoint and falls through to a default that requires authentication, regardless of `auth=none` on the endpoint.

**Fix:** ensure the `[cellrift-identify]` stanza is present in `pjsip.conf` (shown in Step 1 above) with the correct `match=` IP address.

For chan_sip, set `insecure=port,invite` in `sip.conf` as shown in the chan_sip section above.

### 484 Address Incomplete

```text
✗ [sip] send send_invite
  Error: assertion failed: response.status_code == 200 (actual: 484)
```

Asterisk matched the endpoint (auth is working) but could not route the dialled number in the dialplan. Either the extension was not saved, not reloaded, or landed in the wrong context.

**Diagnose:**

```bash
sudo asterisk -rx "dialplan show 447700900002@default"
```

- If it prints `There is no existence of '447700900002' extension in 'default' context` — the extension is not loaded. Verify `/etc/asterisk/extensions.conf` was saved, then reload and check again:

```bash
sudo asterisk -rx "dialplan reload"
sudo asterisk -rx "dialplan show 447700900002@default"
```

- To see everything currently loaded in the default context:

```bash
sudo asterisk -rx "dialplan show default"
```

**If the extension IS loaded but you still get 484**, the endpoint is likely not using `context=default`. Verify the effective endpoint configuration:

```bash
sudo asterisk -rx "pjsip show endpoint cellrift"
```

Look for the `context` line in the output. If it shows a context other than `default` (e.g. `from-internal`, `from-external`, or blank), that context is where Asterisk is looking for the extension — not `default`.

Enable verbose call tracing to see exactly what happens when the INVITE arrives:

```bash
sudo asterisk -rx "core set verbose 5"
```

Then run Cellrift and watch the log:

```bash
sudo journalctl -u asterisk -f
```

The log will show which context Asterisk searches and why it returns 484.

**Common causes:**

- `allow_overlap=yes` (the default) — overlap dialling (RFC 3578) causes Asterisk to return 484 for every call regardless of whether the extension exists, because it expects more digits to arrive via SIP INFO. **This is the most common cause.** Fix: add `allow_overlap=no` to the endpoint stanza and reload.

- Conflicting stanza — the default `pjsip.conf` shipped by the distro may already contain a `[cellrift]` or `[default]` section that overrides your appended config. Check for duplicates:

```bash
grep -n "^\[cellrift\]" /etc/asterisk/pjsip.conf
```

  If the section name appears more than once with `type=endpoint`, the first one wins. Rename your stanza (e.g. `[cellrift-test]`) and update `[cellrift-identify]` to match:

```ini
[cellrift-test]
type=endpoint
context=default
...
aors=cellrift-test

[cellrift-test]
type=aor
max_contacts=1

[cellrift-identify]
type=identify
endpoint=cellrift-test
match=127.0.0.1
```

- Wrong context — the `[cellrift]` endpoint stanza in `pjsip.conf` must have `context=default` to match the `[default]` dialplan section
- Partial pattern conflict — if another extension in `[default]` partially matches `447700900002` (e.g. a `_4.` catch-all that expects more digits), Asterisk returns 484. Try a shorter test number like `100`:

```bash
# In extensions.conf, add alongside the existing extension:
exten => 100,1,Answer()
 same => n,Wait(5)
 same => n,Hangup()
```

```bash
sudo asterisk -rx "dialplan reload"
./cellrift run --var pcscf_host=127.0.0.1 --var callee_msisdn=100 scenarios/ims/basic_call.yaml
```

---

### 500 Internal Server Error on INVITE (auth=none misconfiguration)

```text
✗ [sip] send send_invite
  Error: assertion failed: response.status_code == 200 (actual: 500)
```

Asterisk log shows: `Couldn't find auth 'none'. Cannot authenticate`

In chan_pjsip, `auth=` expects the name of an `[auth]` stanza. Writing `auth=none` causes Asterisk to look for an object literally named "none", fail, and return 500.

**Fix:** remove the `auth=` line from the `[cellrift]` endpoint entirely. Omitting it is how you disable authentication — there is no `auth=none` equivalent in chan_pjsip.

---

### 481 Call/Transaction Does Not Exist on BYE

```text
✗ [sip] send send_bye
  Error: assertion failed: response.status_code == 200 (actual: 481)
```

This is a cascade failure from a preceding 401. Cellrift sent the BYE with a new `Call-ID` because no dialog was ever established — Asterisk has no record of it. **Fix the 401 first**; the 481 disappears automatically once the INVITE succeeds and creates a dialog.

### From URI domain mismatch

The scenario sends `From: sip:447700900001@ims.local`. Asterisk may reject this if the domain does not match a known peer or context.

**Fix:** override `caller_msisdn` to a number Asterisk recognises, or set `From` to match your Asterisk peer configuration:

```bash
cellrift run \
  --var pcscf_host=127.0.0.1 \
  --var caller_msisdn=1000 \
  --var callee_msisdn=1001 \
  scenarios/ims/basic_call.yaml
```

### ACK after 200 OK

SIP requires the UAC to send `ACK` after receiving `200 OK` on an INVITE before the dialog is considered established. Verify the Cellrift SIP adapter sends ACK automatically on 200 OK for INVITE — if it does not, Asterisk will retransmit the 200 and eventually send `BYE` itself before step 2 runs.

### No RTP stream

Asterisk may hang up early if no RTP media arrives after call setup. The `Wait(5)` in the dialplan above keeps the call open for 5 seconds regardless, which is sufficient for the BYE step. If you see Asterisk tearing down the call before step 2, increase the wait duration or add `rtptimeout=0` to the peer config.

---

## Running in CI

```yaml
# .github/workflows/sip-test.yml
- name: Run SIP call test
  run: |
    cellrift run \
      --var pcscf_host=${{ secrets.ASTERISK_HOST }} \
      --var callee_msisdn=447700900002 \
      --format junit \
      --output results/basic_call.xml \
      scenarios/ims/basic_call.yaml

- name: Publish test results
  uses: mikepenz/action-junit-report@v4
  with:
    report_paths: results/*.xml
```

---

## Which built-in scenarios work with Asterisk

| Scenario | Asterisk compatible | Notes |
| --- | --- | --- |
| `ims/basic_call.yaml` | Yes | Fully working — proven above |
| `ims/call_rejected.yaml` | Yes, exits 1 by design | Requires a `Busy()` dialplan extension — see below. The 486 fails the INVITE step under the 2xx gate; the file exists to show what that looks like |
| `ims/register_basic.yaml` | Partial | Step 1 is Diameter UAR to an HSS — Asterisk has no Diameter stack. Steps 2–3 (SIP REGISTER) are pure SIP and work on their own. |
| `ims/register_deregister.yaml` | Partial | Same — step 1 is Diameter. Steps 2–3 (REGISTER + deregister with `Expires: 0`) work against Asterisk. |
| `diameter/uar_uaa.yaml` | No | Pure Diameter — Asterisk has no Diameter stack |
| `diameter/credit_control.yaml` | No | Pure Diameter — Asterisk has no Diameter stack |

### Running `call_rejected.yaml`

Add a busy extension to `/etc/asterisk/extensions.conf`:

```text
exten => 447700900099,1,Busy()
```

```bash
sudo asterisk -rx "dialplan reload"
./cellrift run --var pcscf_host=127.0.0.1 scenarios/ims/call_rejected.yaml
```

Asterisk's `Busy()` application returns exactly the 486 the scenario asserts on — and the run still exits 1. That is the documented outcome, not a misconfiguration: a SIP step passes only on a 2xx final, so the INVITE step is failed by the protocol gate while its own `response.status_code == 486` assertion holds, and the follow-up `assert_state: terminated` passes. Read the report to see the shape of a rejected call; do not put this file in a CI job that must go green. Declaring an expected error as the pass (negative testing) is an open design item.

### Running the REGISTER scenarios (SIP steps only)

The Diameter step (step 1) will fail immediately since Asterisk has no HSS, and the SIP step that follows depends on a value captured from it. To test just the SIP REGISTER flow against Asterisk, copy the scenario and delete the Diameter step and the `Authorization` field that references it. To see the mixed Diameter + SIP flow pass as written, use `cellrift selftest`, which supplies both peers in-process (there is no standalone `cellrift mock` command).

---

---

## Digest authentication

Cellrift answers a digest challenge by itself. When Asterisk replies `401
Unauthorized`, Cellrift computes an RFC 7616 digest (MD5 or SHA-256,
`qop=auth`), re-sends the request once with an `Authorization` header, and the
step's result is the final response to that retry. There is no separate
"answer the challenge" step. `407 Proxy Authentication Required` is handled the
same way with `Proxy-Authorization`.

To try it, give the endpoint an `[auth]` stanza in `pjsip.conf` instead of
leaving `auth=` out:

```ini
[cellrift-auth]
type=auth
auth_type=userpass
username=impi-user
password=shared-pw
realm=asterisk
; Optional: restrict what Asterisk offers. MD5 and SHA-256 are both supported.
supported_algorithms_uas=SHA-256,MD5

[cellrift]
type=endpoint
; ...the rest of the endpoint as above...
auth=cellrift-auth
```

and give the scenario the matching credentials:

```yaml
protocols:
  sip:
    transport: udp
    username: impi-user        # the digest username; need not equal the From user
    password: "{{ sip_password }}"
```

The password can instead come from a `credentials_file` or from the
`CELLRIFT_SIP_PASSWORD` environment variable, and it is masked in every report.
The full precedence and the `response.auth.*` fields you can assert on are in
the [CLI reference](cli-reference.md#sip-digest-authentication-401--407).
`scenarios/ims/register_digest_auth.yaml` is a complete example, and CI runs it
against a digest-requiring Asterisk on every push.

What is not supported:

- **A wrong password fails the step once.** If Asterisk challenges the retry
  too, Cellrift reports the credentials as rejected and does not try again.
- **IMS-AKA (`AKAv1-MD5`, RFC 3310) is not implemented**, nor are `-sess`
  algorithms, `SHA-512-256` or `qop=auth-int`. A real P-CSCF usually
  challenges with IMS-AKA, so Cellrift cannot yet register with one. IMS-AKA is
  tracked in [`docs/todo_next.md`](todo_next.md).

## SIP/TLS — Running encrypted scenarios

All scenarios above use plaintext UDP by default. If your Asterisk instance
requires TLS transport (or you want to verify that encrypted SIP works end-to-end),
this section covers the minimal Asterisk and Cellrift changes.

**Phase 1 scope:** Cellrift acts as a TLS client (UAC). It connects outbound to a
TLS-listening server. Mutual TLS (client certificates) is also supported via the
`cert_file`/`key_file` scenario fields. A `cellrift certs generate` CLI helper is
planned for Phase 2; use openssl for cert generation now.

### Step T1 — Create the keys directory and generate a self-signed certificate

**1a — Create the directory and set ownership before generating any files:**

```bash
sudo mkdir -p /etc/asterisk/keys
sudo chown root:asterisk /etc/asterisk/keys
sudo chmod 750 /etc/asterisk/keys
```

The `asterisk` group needs read+execute on the directory so the Asterisk process can open the key file at startup. Setting this before key generation ensures the files inherit a correct environment.

**1b — Generate the self-signed ECDSA certificate:**

```bash
sudo openssl req -x509 \
  -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
  -keyout /etc/asterisk/keys/asterisk.key \
  -out /etc/asterisk/keys/asterisk.crt \
  -days 365 -nodes \
  -subj "/CN=asterisk.lab.local" \
  -addext "subjectAltName=DNS:asterisk.lab.local,IP:127.0.0.1"
```

The `subjectAltName` line matters if you intend to verify this certificate from
Cellrift (Step T4's `ca_file` option): Go's TLS stack ignores the legacy `CN`
field and matches the dialled host — `127.0.0.1` in the scenarios — against the
SANs only. Add your host's LAN IP to the list if Cellrift runs on another
machine.

**1c — Lock down the private key:**

```bash
sudo chown root:asterisk /etc/asterisk/keys/asterisk.key
sudo chmod 640 /etc/asterisk/keys/asterisk.key
```

The cert file can be world-readable; the key must be readable only by root and the `asterisk` group.

Verify:

```bash
ls -la /etc/asterisk/keys/
# Expected:
# drwxr-x--- root asterisk   keys/
# -rw-r--r-- root root       asterisk.crt
# -rw-r----- root asterisk   asterisk.key
```

### Step T2 — Add a TLS transport to pjsip.conf

> **Order matters:** the cert and key files from Step T1 must exist before you reload. If they are missing when `pjsip reload` runs, Asterisk silently skips the `[transport-tls]` stanza and port 5061 is never opened — no error is printed.

Append to `/etc/asterisk/pjsip.conf` (the existing `[transport-udp]` stanza stays in place):

```ini
[transport-tls]
type=transport
protocol=tls
bind=0.0.0.0:5061
cert_file=/etc/asterisk/keys/asterisk.crt
priv_key_file=/etc/asterisk/keys/asterisk.key
method=tlsv1_2
```

Also update the `[cellrift-identify]` stanza (added in Step 1) to include `::1`. TLS connections from Cellrift may arrive as IPv6 loopback even when the target IP is `127.0.0.1`:

```ini
[cellrift-identify]
type=identify
endpoint=cellrift
match=127.0.0.1
match=::1
```

> **Avoid duplicates:** append each stanza only once. If `pjsip.conf` already contains a `[cellrift-identify]` stanza, edit it in place rather than appending a new one. Duplicate stanzas silently prevent the endpoint from loading — Asterisk returns 401 with "No matching endpoint found" in the logs (`journalctl -u asterisk | grep "No matching endpoint"`).

Reload and verify:

```bash
sudo asterisk -rx "pjsip reload"
sudo ss -tlnp | grep 5061
sudo asterisk -rx "pjsip show transports"
sudo asterisk -rx "pjsip show endpoint cellrift"
```

Expected: a line showing Asterisk listening on `0.0.0.0:5061`, and the endpoint shows `Match: 127.0.0.1/32` and `Match: ::1/128`.

### Step T3 — Open the firewall for TCP 5061

**Fedora (firewalld):**

```bash
sudo firewall-cmd --add-port=5061/tcp --permanent
sudo firewall-cmd --reload
```

**Ubuntu / Debian (ufw):**

```bash
sudo ufw allow 5061/tcp
```

### Step T4 — Update the scenario YAML

Add `transport: tls` to the `sip` protocol block. Cellrift verifies the server
certificate by default, and the self-signed Asterisk cert above is not in your
system's trust store, so either point `ca_file` at it (preferred — it *is* the CA
for itself) or opt out of verification explicitly:

```yaml
protocols:
  sip:
    transport: tls
    ca_file: /etc/asterisk/keys/asterisk.crt   # trust the self-signed cert directly
```

```yaml
protocols:
  sip:
    transport: tls
    insecure_skip_verify: true   # lab only — accepts any cert; the run prints a warning
```

For production environments with a real CA:

```yaml
protocols:
  sip:
    transport: tls
    ca_file: certs/ca.crt        # verify Asterisk server cert
    cert_file: certs/client.crt  # present client cert (mTLS)
    key_file:  certs/client.key
```

### Step T5 — Run the scenario over TLS

Use `scenarios/ims/basic_call_tls.yaml` — the checked-in TLS variant that sets `transport: tls` and uses a `pcscf_tls_port` variable (default 5061). Pass `pcscf_host` as the IP only; the scenario appends the port itself.

```bash
cellrift run \
  --var pcscf_host=127.0.0.1 \
  --var pcscf_tls_port=5061 \
  --var callee_msisdn=447700900002 \
  --verbose \
  scenarios/ims/basic_call_tls.yaml
```

Expected: `2/2 steps passed`. The raw response Via header should contain `SIP/2.0/TLS` and the Asterisk Contact should include `transport=TLS`.

---

### TLS friction points

#### Connection refused on 5061

```text
Error: send failed: dial tls "127.0.0.1:5061": connect: connection refused
```

Asterisk is not listening on TCP 5061. Check `ss -tlnp | grep 5061` — if empty, the
`[transport-tls]` stanza is missing or failed to load. Check logs:

```bash
sudo journalctl -u asterisk --since "1 minute ago" | grep -i tls
```

Common causes: cert file path wrong, key file not readable by the `asterisk` user,
or `method=` value not supported by your OpenSSL version.

#### 401 Unauthorized — "No matching endpoint found" in Asterisk logs

```text
✗ [sip] send send_invite
  Error: assertion failed: response.status_code == 200 (actual: 401)
```

Asterisk received the TLS INVITE but could not match it to the `cellrift` endpoint. Check the Asterisk logs:

```bash
sudo journalctl -u asterisk --since "1 minute ago" | grep "No matching endpoint"
```

Two common causes:

1. **Missing `match=::1`** — TLS connections from Cellrift on the same machine arrive as IPv6 `::1` even when the target IP is `127.0.0.1`. Add `match=::1` to `[cellrift-identify]` in `pjsip.conf` (see Step T2 above).

2. **Duplicate stanzas** — if the append commands were run more than once, `pjsip.conf` has multiple `[cellrift]` stanzas with the same `type=`. Asterisk silently rejects the endpoint. Check with:

```bash
grep -c "\[cellrift\]" /etc/asterisk/pjsip.conf
# Expected: 2 (one for type=endpoint, one for type=aor)
```

If the count is higher, edit `pjsip.conf` to remove duplicates, then `sudo asterisk -rx "pjsip reload"`.

#### TLS handshake error from Cellrift

```text
Error: dial tls "127.0.0.1:5061": tls: failed to verify certificate: x509: ...
```

`ca_file` is set but does not match the Asterisk server cert — or no `ca_file`
is set and the cert is self-signed, so the system trust store rejects it (the
default; omitting the cert fields does **not** disable verification). Point
`ca_file` at the self-signed cert itself, or set `insecure_skip_verify: true` to
bypass verification in the lab.

#### INVITE returns 488 Not Acceptable Here after TLS

Asterisk accepted the TLS connection but rejected the SDP offer. This is unrelated
to TLS — it means the codec negotiation failed. Add `allow=ulaw,alaw` to the
`[cellrift]` endpoint stanza in `pjsip.conf` and reload.

---

## Next steps

| Goal | What to do |
|---|---|
| Test call rejection | Run `scenarios/ims/call_rejected.yaml` with a `Busy()` dialplan extension — see above |
| Test SIP/TLS | Add `transport: tls` to the scenario YAML and configure a TLS transport in `pjsip.conf` — see "SIP/TLS" section above |
| Test SIP REGISTER | `register_basic.yaml` has a Diameter step and needs an HSS; for a SIP-only REGISTER against Asterisk, copy it and delete that step. `cellrift selftest` runs the whole flow against built-in mocks |
| Add authentication | Give the endpoint an `[auth]` stanza and the scenario a `username`/`password`; Cellrift answers the `401` itself. See [Digest authentication](#digest-authentication) above, or run `scenarios/ims/register_digest_auth.yaml` |
| Run all compatible scenarios | `cellrift run --var pcscf_host=127.0.0.1 --parallel scenarios/ims/basic_call.yaml scenarios/ims/register_impaired.yaml` (add `call_rejected.yaml` to see a by-design failure) |
