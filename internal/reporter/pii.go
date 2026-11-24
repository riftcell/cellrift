package reporter

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/riftcell/cellrift/internal/engine"
)

// e164Re matches E.164-format phone numbers: an optional leading '+' followed
// by 10–15 digits starting with a non-zero digit. The leading context group
// ([^0-9+] or start of string) stops a number being matched out of the
// middle of a longer digit sequence.
//
// The trailing boundary is deliberately NOT part of the pattern. Go's RE2
// engine has no lookahead, and a consuming trailing group ([^0-9]|$) ate the
// separator after each number — so in "+447700900001,+447700900002" the
// comma was swallowed by the first match and the second number, now with no
// preceding character to satisfy the leading group, leaked unmasked. Two
// numbers separated by exactly one character (a comma, a space) always lost
// the second one. maskNumbers checks the trailing boundary by hand instead.
//
// Examples matched: "+447700900001", "447700900001", "+12125550100"
// Examples NOT matched: "2001", "200", "123" (too short)
var e164Re = regexp.MustCompile(`(^|[^0-9+])(\+?[1-9][0-9]{9,14})`)

// Masker replaces E.164 phone numbers with stable per-instance tokens, and
// removes registered secrets outright.
//
// The same number always receives the same token ([REDACTED-1], [REDACTED-2], …)
// within one Masker's lifetime, allowing readers to correlate occurrences
// across a multi-step report without revealing the actual values.
//
// The two redactions are NOT the same kind and are deliberately asymmetric:
//   - A phone number is PII the operator may legitimately want to see against
//     their own lab gear, which is what --no-mask is for.
//   - A secret is a credential. It is replaced by one fixed token and --no-mask
//     does NOT reveal it: the operator's consent to see their own MSISDN is not
//     consent to write a password into an uploaded CI artifact.
//
// The secret list is never formatted, logged or written anywhere; Masker has no
// String method and is not a field of anything that is printed.
//
// Create one Masker per report (not per string). Use MaskPII for one-off
// single-string masking where cross-call correlation is not needed.
type Masker struct {
	mu       sync.Mutex
	tokens   map[string]string
	secrets  []string // kept sorted by descending length; see AddSecret
	counter  int
	disabled bool
}

// secretToken replaces every registered secret. It is fixed, not numbered:
// correlating occurrences of a credential is not something a report needs.
const secretToken = "[REDACTED-SECRET]"

// NewMasker creates a Masker with an empty token registry.
func NewMasker() *Masker {
	return &Masker{tokens: make(map[string]string)}
}

// NewPassthroughMasker returns a Masker whose Mask is the identity. It backs
// `cellrift run --no-mask`: against your own lab gear the masked
// `From: sip:[REDACTED-1]@...` in a --verbose dump hides the very MSISDN you
// are trying to verify, and there was no way to turn it off. Masking stays
// the default; this is an explicit per-run opt-out, and the reporters that
// write CI artifacts (JUnit, JSON) honour it too, so the operator, not the
// tool, decides what lands in an uploaded report.
func NewPassthroughMasker() *Masker {
	return &Masker{disabled: true}
}

// maskerFor returns the masker a reporter should use for one report, with every
// secret any run in the report produced already registered. All three reporters
// call it identically and at the same point, so the three implementations share
// one rule: the shape needed after an earlier phase found one rule implemented
// three ways with two of them wrong.
func maskerFor(noMask bool, results []*engine.RunResult) *Masker {
	m := NewMasker()
	if noMask {
		m = NewPassthroughMasker()
	}
	for _, r := range results {
		for _, secret := range r.Secrets {
			m.AddSecret(secret)
		}
	}
	return m
}

// AddSecret registers sec for outright removal from every subsequent Mask call.
//
// An EMPTY sec registers nothing, and that is load-bearing: strings.ReplaceAll
// with an empty old string inserts the replacement between every character, so
// one unset password would turn the whole report into tokens. An unset
// credential is the normal case for every scenario that does not authenticate.
//
// Duplicates are ignored. The list is kept sorted by descending length at
// registration time so maskSecrets replaces the longest match first: with both
// "pw" and "pw-extra" registered, replacing "pw" first would leave
// "[REDACTED-SECRET]-extra" and leak the suffix.
func (m *Masker) AddSecret(sec string) {
	if sec == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, have := range m.secrets {
		if have == sec {
			return
		}
	}
	m.secrets = append(m.secrets, sec)
	sort.SliceStable(m.secrets, func(i, j int) bool { return len(m.secrets[i]) > len(m.secrets[j]) })
}

// maskSecrets replaces each registered secret with secretToken. It copies the
// slice under the lock and replaces outside it, so a long string never holds
// the mutex that token also needs.
func (m *Masker) maskSecrets(s string) string {
	m.mu.Lock()
	secrets := append([]string(nil), m.secrets...)
	m.mu.Unlock()
	for _, sec := range secrets {
		s = strings.ReplaceAll(s, sec, secretToken)
	}
	return s
}

// Mask removes registered secrets, then replaces E.164 phone numbers in s with
// their assigned tokens. Short numeric codes (e.g. 2001, 200) are not affected.
//
// The secrets pass runs FIRST and BEFORE the disabled check. Placing it after
// the check, where every other line of this function sits, is what --no-mask
// would bypass. It also precedes the E.164 pass, so a password shaped like a
// phone number is removed as a secret rather than tokenised as a number.
func (m *Masker) Mask(s string) string {
	s = m.maskSecrets(s)
	if m.disabled {
		return s
	}
	var out strings.Builder
	last := 0
	for _, loc := range e164Re.FindAllStringSubmatchIndex(s, -1) {
		numStart, numEnd := loc[4], loc[5]
		// Trailing boundary, checked by hand rather than consumed by the
		// pattern: a digit right after the candidate means it is the prefix
		// of a longer digit run, not a number on its own.
		if numEnd < len(s) && isDigit(s[numEnd]) {
			continue
		}
		out.WriteString(s[last:numStart])
		out.WriteString(m.token(s[numStart:numEnd]))
		last = numEnd
	}
	out.WriteString(s[last:])
	return out.String()
}

// token returns the stable [REDACTED-N] replacement for number, assigning the
// next N on first sight.
func (m *Masker) token(number string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tok, ok := m.tokens[number]; ok {
		return tok
	}
	m.counter++
	tok := fmt.Sprintf("[REDACTED-%d]", m.counter)
	m.tokens[number] = tok
	return tok
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// MaskPII masks a single string using a fresh Masker.
// Numbers within the same string receive consistent tokens ([REDACTED-1],
// [REDACTED-2], …) but there is no correlation across separate calls.
// For cross-string correlation across a full report, create one Masker with
// NewMasker() and call Mask() throughout.
func MaskPII(s string) string {
	return NewMasker().Mask(s)
}
