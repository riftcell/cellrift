# Run 14 — the sip-interop job observed red

The `sip-interop` job claims two things, which needed proving by deliberate
probe runs rather than by the job merely passing:

1. the job genuinely fails the build **when the peer rejects**, and
2. it **refuses to grade a run that never happened**.

Each clause needs its own break, because they fail at different points and a
single probe cannot show both.

## Clause (b): refuses to grade a run that never happened

`ci-red-run-clause-b.log` — GitHub Actions run 37040015812, branch
`probe/peer-02-deliberate-red`, commit 8a7a6f4. One character was changed:
endpoint 447700900202 referenced `auth-sha265`, which does not exist.

Asterisk loads such an endpoint anyway and would answer its REGISTER 200 with
no challenge at all, so `wait_for_auths` refused at boot:

```
SIP RIG REFUSED: 'pjsip show auths' does not list auth-md5, auth-sha256
and auth-both as objects, or endpoints 447700900201/202/203 do not report
auth= ...
##[error]Process completed with exit code 1.
```

The step ran under `bash -e` with no `continue-on-error`, the scenario step
never executed, and the uploaded artifact contained one file because
`/tmp/cellrift-sip/**` matched nothing — no run happened, and none was graded.

## Clause (a): fails the build when the peer rejects

`ci-red-run-clause-a.log` — GitHub Actions run 37045975976, commit eb211ba.
`local-red-run.log` and `asterisk-red.log` are the local reproduction that
preceded it. Only
`auth-md5`'s `password` was changed, leaving the auth object and every
endpoint's `auth=` correct, so this is deliberately NOT a readiness refusal:

- the boot gates PASS: `digest auth wiring verified (auth-md5, auth-sha256, auth-both)`
- the peer challenges, Cellrift retries once, and the peer rejects the digest
- the peer's own log says `Failed to authenticate` (twice, one per pass)
- Cellrift reports `credentials rejected after one digest retry (realm="cellrift.interop", algorithm=MD5)`
- the harness refuses with `the real peer did not pass register_digest_auth
  (exit 1); this is a result about the peer, not the rig`
- SHA-256 and the both-offered endpoint still pass, so the break is scoped to
  the one credential and the red is not a general rig failure

On the runner the same sequence held, and the ordering is what matters:

```
18:14:07 sip interop harness ready: ... digest auth wiring verified
         (auth-md5, auth-sha256, auth-both), sip logger on
18:14:07 ##[group]Run bash test/interop/run-sip-scenario.sh
18:14:31 SIP RIG REFUSED: the real peer did not pass register_digest_auth
         (exit 1); this is a result about the peer, not the rig
18:14:31 ##[error]Process completed with exit code 1.
```

The boot step completed, so the failure is attributable to the SCENARIO step
and not to readiness. The artifact carried 26 files against clause (b)'s 1:
the run happened and was graded, and the grade was the thing that failed.
The peer's own log in that run says `Failed to authenticate` for
447700900201, and the both-offered endpoint still offered SHA-256 with
opaque 532f278724ea1f04 and MD5 with 2ac4b7b019227273 under one nonce, with
the retry carrying the SHA-256 header's own opaque.

## What this replaced

Before these runs, both claims rested on an inference: `up-sip.sh` and
`run-sip-scenario.sh` exit non-zero, the steps run under `bash -e` with no
`continue-on-error`, therefore the build fails. All 20 refusal sites had been
fired locally with exit 1. The reasoning was sound and the conclusion is now
known to be correct — but it was still an inference, and runs 12 and 13
each disproved inferences of exactly that shape, including one that had
already been verified and endorsed. These runs replace it with an observation.
