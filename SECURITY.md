# Security Policy

This document covers vulnerabilities *in* Cellrift itself — bugs in this
tool's code that an attacker could exploit. It does not cover how you use
Cellrift against someone else's infrastructure; see [TERMS.md](TERMS.md) for
that.

## Supported Versions

Cellrift is a single-maintainer, pre-1.0 project. There is no long-term
support branch and no backport matrix — only the latest tagged release and
`main` receive fixes. If you are running an older tag, upgrade to the latest
release before reporting an issue; there is no capacity to patch history.

## Reporting a Vulnerability

**Do not open a public issue for a suspected vulnerability.** A public issue
gives every reader — including anyone running Cellrift against production-
adjacent infrastructure — advance notice of an exploitable defect before a
fix exists.

Instead, use GitHub's built-in private vulnerability reporting: go to this
repository's **Security** tab and select **"Report a vulnerability."** This
opens a private draft advisory (backed by GitHub Security Advisories) visible
only to you and the maintainer, with no separate mailing list or inbox to
stand up or keep alive. This is the primary and preferred channel.

If GitHub's private reporting is genuinely unavailable to you, open a draft
security advisory is still the right first attempt — there is no separate
`security@` mailbox for this project; none exists to route mail to, and
inventing one here would be worse than not listing one at all.

Please include, where you can:

- A description of the vulnerability and its potential impact
- Steps to reproduce it, or a minimal scenario YAML file that triggers it
- The Cellrift version or commit hash you tested against
- Whether you believe the issue is remotely triggerable by a peer's response,
  or requires local access to the machine running Cellrift

## Scope

Cellrift is a CLI that *emits* SIP and Diameter signalling to a target the
operator chooses, and parses whatever comes back. Its attack surface is the
parsing and state-handling of that response, not a web front end.

**In scope:**

- A crafted or malformed peer response (a hostile CEA, UAA, or SIP response)
  that causes a panic, an infinite loop, unbounded memory growth, or a parser
  reading past a buffer
- A corrupt Diameter `Message-Length` field that escapes the framing checks
  in the message-reassembly path
- TLS certificate verification being skipped or weakened where the
  configuration says it should be enforced
- Secrets, credentials, or unmasked PII leaking into generated reports,
  logs, or error messages

**Out of scope:**

- Web-application vulnerability classes that do not apply to a CLI —
  cross-site scripting, CSRF, SQL injection, and similar
- Anything that requires the reporter to already control the machine running
  the Cellrift binary (that is a compromised-host problem, not a Cellrift
  vulnerability)
- Using Cellrift against infrastructure you do not own or have permission to
  test — that is a responsible-use question, not a vulnerability in Cellrift.
  See [TERMS.md](TERMS.md).

## If Cellrift finds a flaw in someone else's stack

Cellrift can send deliberately malformed and cross-protocol traffic, so it
will occasionally make a third-party implementation misbehave — a parser
crash, a hang, an unhandled AVP length. **That is a vulnerability in that
implementation, not in Cellrift, and it is not ours to disclose.** Report it
to that project or vendor under their own security policy, on their timeline.

We ask two things. Do not open a public Cellrift issue containing a working
crash reproducer for unpatched third-party software — use this repository's
private vulnerability reporting (Security tab → "Report a vulnerability", as
above) and we will help you route it to the right project. And if a finding turns out to be
Cellrift generating invalid traffic it did not intend to generate, that *is*
ours: report it as a normal bug, because a fuzzer that lies about what it
sent is worse than no fuzzer.

## Response Expectations

This is maintained by one person, so the commitments below are honest rather
than an enterprise SLA:

- **Acknowledgement:** within about five business days of a report reaching
  the private advisory.
- **Assessment:** after triage, you'll get an initial read on severity and a
  rough plan — fix timeline, whether it needs a coordinated disclosure, or
  whether it's determined not to be a security issue after all.
- **Disclosure:** once a fix is available, the advisory is published and
  credited to the reporter (unless you ask not to be credited), following
  GitHub's coordinated-disclosure flow.

## Related

- [TERMS.md](TERMS.md) — responsible-use guidance for running Cellrift
  against real infrastructure, and a Data Processing Agreement template.
  This is about how you use the tool, not about vulnerabilities in it.
- [CONTRIBUTING.md](CONTRIBUTING.md) — for ordinary, non-security bug reports
  and feature requests, which should go through normal GitHub issues instead
  of the private advisory flow above.
