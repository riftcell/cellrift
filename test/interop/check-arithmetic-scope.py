#!/usr/bin/env python3
"""Refuse an interop claim about the SIP peer that does not carry its own scope.

The pinned Asterisk returns 200 OK for an unpadded nc, an uppercase-hex nc, a
quoted qop, the wrong challenge's opaque, a uri differing from the request line
and an omitted algorithm. It 401s only on a hash mismatch. So it is an oracle for
the digest ARITHMETIC and not for the header's wire format, and a sentence that
lets a reader infer otherwise is an overclaim however it is phrased.

This is a POSITIVE check, not a blacklist. A blacklist enumerates the phrasings
someone already thought of; three attempts at one have already been defeated, the
last by the goal statement's own wording. Here, any paragraph that
makes an acceptance/validation claim about this peer in a digest context MUST also
say "arithmetic", or sit inside a "What this does not prove" section as a
negation. There is deliberately NO inline exemption marker: an opt-out is how a
gate stops being one.

The unit of scanning is a paragraph, a bullet, or a run of comment lines between
bare comment leaders, not a whole file or section: see _units. Fenced code blocks
are not scanned, raw captures in the evidence directories are not scanned, and the
CHANGELOG is scanned only in its digest authentication entry. Each of those is a
scope rule applied to a KIND of text, and none is an exemption an author can
claim for one sentence.

Exit 0 clean, 1 on an unqualified claim, 2 if the check is weaker than its own
fixtures.
"""
import pathlib, re, sys

SUBJECT = re.compile(r"asterisk|the peer|a real peer|real peer|the real SIP", re.I)
CLAIM = re.compile(
    r"accept|valid|verif|confirm|conform|complian|well.?formed|interop|wire.?format|authenticated|correct|endorse|\bfine\b|\bsound\b|known.good",
    re.I)
# Scope: only paragraphs that are ABOUT the digest. Without this the check fires
# on runs 1-12, whose claims are about other capabilities.
DIGEST = re.compile(
    r"digest|authoriz|authentic|challenge|credential|\b40[17]\b|qop|opaque|nonce|"
    r"WWW-Authenticate|Proxy-Auth|wire.?format|header", re.I)
QUALIFIED = re.compile(r"arithmetic", re.I)
NEGATION = re.compile(r"\bnot\b|\bno\b|\bnever\b|unproven|UNKNOWN|does not|cannot", re.I)
EXEMPT_HEADING = re.compile(r"what this does not prove", re.I)

# The six sentences that defeated the blacklist. Every one MUST be flagged, or
# this check is weaker than the thing it replaced.
FIXTURES = [
    "Asterisk validated the Authorization header and returned 200 OK.",
    "The peer verified the header, confirming interoperability.",
    "Cellrift's digest conforms to RFC 7616, as accepted by Asterisk.",
    "Asterisk accepted the well-formed Authorization header.",
    "Wire format verified against Asterisk.",
    "completes an authenticated REGISTER against a real peer",
]


def suspect(par: str) -> bool:
    return bool(SUBJECT.search(par) and CLAIM.search(par) and DIGEST.search(par))


# KNOWN LIMITATION of the SUBJECT, CLAIM and DIGEST conjuncts, recorded here
# rather than left to be found later. Each is a vocabulary list, so a sentence
# that avoids all three of one list passes. These four are overclaims in run 13's
# context and this predicate MISSES all four, because none carries a DIGEST term:
#
#     Asterisk validated Cellrift's REGISTER.
#     The peer confirmed interoperability.
#     Interoperability with Asterisk is confirmed.
#     Asterisk accepted it.
#
# They are NOT in FIXTURES, because adding them would make the self-test fail
# (exit 2) and the honest options are to widen DIGEST or to drop it. DIGEST exists
# so the check does not fire on runs 1-12, whose claims concern other
# capabilities, were graded under their own evidence, and are not this check's to
# rewrite. The same applies in principle to the CLAIM list, though it has since
# been widened past verbs to the adjectives an approving sentence reaches for
# ("correct", "endorse", "fine", "sound", "known good"), which closed the
# "shows ... was correct" family. The structural limit stands: CLAIM detects THAT
# a claim was made by vocabulary, so a sentence approving the peer's behaviour in
# words absent from the list still passes. This check therefore does not remove
# the need to read the entry; it removes the evasions anyone has actually found.
#
# A second cost: an overclaim written INSIDE a fenced code block is not seen. A
# fence is verbatim quotation, and the verbatim output of this repository's own
# tools trips the vocabulary lists above (paths, a guard's FAIL text), so
# scanning fences would make the check fail on evidence nobody asserted. Keep
# prose out of fences.
#
# Measured, not hypothetical: this invented sentence PASSES the check, because
# "shows ... was correct" contains none of the CLAIM verbs:
#
#     Asterisk's 200 OK for the SHA-256 REGISTER shows the Authorization header
#     it received was correct.
#
# So the CLAIM conjunct is a vocabulary list, and the check is positive only on
# the qualifier side: a sentence that avoids every CLAIM verb is not seen. Making
# it fully positive (dropping CLAIM, so any paragraph naming the peer and a digest
# term must say "arithmetic") flagged 33 paragraphs when tried, nearly all of them
# plain description, and was not adopted. A third gap: a bad sentence in the SAME
# unit as a qualified one (adjacent lines of one comment run, one paragraph) is
# covered by that unit's "arithmetic". Keep claims in their own paragraph.
#
# So this check is strong against the six phrasings that defeated the blacklist
# and weak against vaguer ones. The reviewer of run 13 must read the entry, not
# only run the script.


# A unit boundary is a blank line, a comment line holding nothing but its leader
# (a bare "#", ";" or "//"), or the start of a new top-level list item. The last
# two matter: splitting on blank lines alone makes a CHANGELOG section (a bullet
# list with no blank lines) or a script's header comment ONE unit of tens of
# kilobytes, and then a single occurrence of "arithmetic" anywhere in it would
# qualify every sentence in it, including a bad one written next to it.
BARE_COMMENT = re.compile(r"^\s*(#|;|//)\s*$")
LIST_ITEM = re.compile(r"^(\s*)([-*]|\d+[.)])\s")


FENCE = re.compile(r"^\s*```")


def _units(text: str):
    """Split text into units of contiguous prose; see the comment above.

    Fenced code blocks are not prose: they hold commands and the verbatim output
    of tools (a guard's FAIL line names "a real Asterisk container" and the path
    test/interop/..., which no author asserted). They end the current unit and
    are not scanned. See the KNOWN LIMITATION note on what this costs."""
    unit, indent, fenced = [], None, False
    for line in text.split("\n"):
        if FENCE.match(line):
            fenced = not fenced
            if unit:
                yield "\n".join(unit)
            unit, indent = [], None
            continue
        if fenced:
            continue
        if not line.strip() or BARE_COMMENT.match(line):
            if unit:
                yield "\n".join(unit)
            unit, indent = [], None
            continue
        item = LIST_ITEM.match(line)
        if item and unit and len(item.group(1)) <= (indent or 0):
            yield "\n".join(unit)
            unit = []
        if not unit:
            indent = len(item.group(1)) if item else 0
        unit.append(line)
    if unit:
        yield "\n".join(unit)


def paragraphs(text: str, markdown: bool = True):
    """Yield (heading, unit). Heading is the nearest preceding ## or # line.

    Only in markdown is a lone "#" line a heading. In a script, a scenario or a
    config file it is a one-line comment, which is prose like any other and MUST
    be scanned: treating it as a heading made a single-line comment invisible."""
    heading = ""
    for block in _units(text):
        first = block.split("\n")[0]
        if markdown and first.lstrip().startswith("#"):
            heading = first
            # A heading is prose too ("## Result: ..." carries the headline), so
            # it is yielded and scanned, not skipped.
            if "\n" not in block:
                yield heading, block
                continue
        yield heading, block


SENTENCE_END = re.compile(r"(?<=[.!?])\s+|\n\s*(?:[-*]|\d+[.)])\s")


def exempt_as_negation(heading: str, par: str) -> bool:
    """Inside 'What this does not prove', a paragraph may talk about the peer's
    acceptance only to deny it. The negation is required PER SENTENCE that makes a
    claim: one 'not' in the paragraph must not excuse the next sentence's
    affirmative claim ("X was not checked. Asterisk accepted the header.")."""
    if not EXEMPT_HEADING.search(heading):
        return False
    claims = [s for s in SENTENCE_END.split(par) if CLAIM.search(s)]
    return all(NEGATION.search(s) or QUALIFIED.search(s) for s in claims)


def scan(path: pathlib.Path, text: str):
    bad = []
    for heading, par in paragraphs(text, path.suffix in PROSE_SUFFIXES):
        if not suspect(par) or QUALIFIED.search(par):
            continue
        if exempt_as_negation(heading, par):
            continue
        bad.append((path, heading.strip(), par.strip().split("\n")[0][:120]))
    return bad


def self_test() -> int:
    """A check that cannot reject its fixtures proves nothing: refuse to run."""
    missed = [s for s in FIXTURES if not suspect(s)]
    if not missed:
        return 0
    print("CHECK IS WEAKER THAN ITS FIXTURES; these pass unflagged:")
    for s in missed:
        print("   ", s)
    return 2


PROSE_SUFFIXES = {".md", ".txt"}


def target_files():
    files = [
        pathlib.Path("scenarios/ims/register_digest_auth.yaml"),
        pathlib.Path("docs/cli-reference.md"),
        pathlib.Path("test/interop/README.md"),
        pathlib.Path("test/interop/asterisk/pjsip.conf"),
        pathlib.Path("test/interop/run-sip-scenario.sh"),
    ]
    # Evidence directories: only the prose files. The rest (asterisk.log, .json,
    # .stdout, .receipts) are raw captures that no author wrote a claim into.
    evidence = pathlib.Path("test/interop/evidence").glob("run-1[34]-*/*")
    return files + sorted(p for p in evidence if p.suffix in PROSE_SUFFIXES)


CHANGELOG_ENTRY = re.compile(r"^- \*\*SIP digest authentication\b", re.M)
TOP_LEVEL = re.compile(r"^(- |#)", re.M)


def scan_changelog_entry():
    """Scan only the digest authentication entry of the CHANGELOG. The rest of the
    file is earlier text about other capabilities; its loose vocabulary
    hits ("header", "the peer") are not digest claims and are not this check's to
    rewrite. Returns (bad, error-message-or-None); a missing entry is an error,
    because scanning nothing must never read as a pass."""
    path = pathlib.Path("CHANGELOG.md")
    if not path.exists():
        return [], None
    text = path.read_text()
    start = CHANGELOG_ENTRY.search(text)
    if start is None:
        return [], "CHANGELOG.md carries no '- **SIP digest authentication' entry; nothing to scope to"
    rest = text[start.end():]
    end = TOP_LEVEL.search(rest)
    return scan(path, text[start.start(): start.end() + (end.start() if end else len(rest))]), None


def scan_evidence_entry():
    """EVIDENCE.md is scanned from the first digest-authentication run onward. Runs 1-12 are
    out of scope: their claims are about other capabilities and were graded under
    their own evidence. Returns (bad, error-message-or-None)."""
    ev = pathlib.Path("test/interop/EVIDENCE.md")
    if not ev.exists():
        return [], None
    t = ev.read_text()
    m = re.search(r"^# Run 13\b", t, re.M)
    if m is None:
        return [], "EVIDENCE.md carries no '# Run 13' entry; nothing to scope to"
    return scan(ev, t[m.start():]), None


def scan_files(files):
    bad = []
    for p in files:
        if not (p.exists() and p.is_file()):
            continue
        try:
            bad += scan(p, p.read_text())
        except UnicodeDecodeError:
            continue
    return bad


def main() -> int:
    code = self_test()
    if code:
        return code
    bad, err = scan_evidence_entry()
    more, err2 = scan_changelog_entry()
    if err or err2:
        print(err or err2)
        return 1
    bad += more + scan_files(target_files())
    if not bad:
        print("OK: every digest claim about the peer carries its arithmetic scope")
        return 0
    print("UNQUALIFIED CLAIM ABOUT THE PEER (add the arithmetic scope, or move it")
    print("into 'What this does not prove' as a negation; do NOT add an exemption):")
    for path, heading, first in bad:
        print(f"  {path}  [{heading}]  {first}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
