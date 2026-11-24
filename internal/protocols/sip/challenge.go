package sip

import (
	"strings"
)

// Challenge is one parsed authentication challenge: one "Digest ..." credentials
// production from one WWW-Authenticate or Proxy-Authenticate header. A peer may
// offer several, and each carries its OWN opaque (the pinned Asterisk sends two
// with one nonce and two different opaques), so a Challenge is never merged with
// another and the opaque that goes back on the wire is the chosen one's.
type Challenge struct {
	Scheme    string // "Digest" as received, for the failure message
	Realm     string
	Nonce     string
	Opaque    string   // empty when the challenge carried none; then no opaque= is sent back
	Algorithm string   // normalised to upper case; "MD5" when the parameter was absent
	QOPs      []string // the challenge's list, split and trimmed; selection picks "auth"
	// Stale is populated from stale=true but deliberately unused: exactly one
	// retry is made per request. Whether any peer sends it on a second 401 is
	// not known; the pinned Asterisk does not.
	Stale  bool
	Header string // "WWW-Authenticate" or "Proxy-Authenticate": which header carried it
}

// challengeParams is one challenge's parameter name (lowercased) -> value map,
// quotes already stripped. It lives only inside challengeFromTokens, between
// the tokenizer and the Challenge it populates. A peer may quote any parameter
// or leave it a bare token, so the parser accepts both; quoting rules apply
// only to what Cellrift SENDS.
type challengeParams map[string]string

// challengeHeaderPair names the two headers one authentication status code
// uses: the one the peer challenges with, and the one Cellrift answers in.
type challengeHeaderPair struct{ Challenge, Credential string }

// challengeHeaderTable maps an authentication status code to its header pair.
// It is a table rather than an if/else on the code so that 401 and 407 travel
// ONE code path: a 407 handled by a near-copy of the 401 branch is how a rule
// ends up enforced on one side only.
type challengeHeaderTable map[int]challengeHeaderPair

var challengeHeaders = challengeHeaderTable{
	401: {Challenge: HeaderWWWAuth, Credential: HeaderAuthorization},
	407: {Challenge: HeaderProxyAuth, Credential: HeaderProxyAuthorization},
}

// unquoteSIP strips one matching pair of double quotes and unescapes `\\` and
// `\"`. It is the read counterpart to quoteSIP and deliberately permissive:
// quoteSIP is what Cellrift writes, under RFC 7616's quoting table; this is
// what a peer may have written, quoted or bare.
func unquoteSIP(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return sipUnquoteReplacer.Replace(s)
}

var sipUnquoteReplacer = strings.NewReplacer(`\\`, `\`, `\"`, `"`)

// splitTopLevel splits value on commas that are outside double-quoted strings,
// honouring backslash escapes inside them (RFC 3261 §25.1). Tokens are trimmed
// and empty ones dropped. A plain split on commas would cut a nonce or a
// qop="auth, auth-int" list in half.
func splitTopLevel(value string) []string {
	var tokens []string
	inQuote, escaped, start := false, false, 0
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case escaped:
			escaped = false
		case inQuote && c == '\\':
			escaped = true
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			tokens = appendToken(tokens, value[start:i])
			start = i + 1
		}
	}
	return appendToken(tokens, value[start:])
}

func appendToken(tokens []string, t string) []string {
	if t = strings.TrimSpace(t); t != "" {
		return append(tokens, t)
	}
	return tokens
}

// startsChallenge reports whether a token opens a new challenge: a bare scheme
// word followed by whitespace, where what follows is not '='. RFC 3261 §25.1
// allows whitespace around '=', so `nonce = "n"` also has a word then
// whitespace; only the '=' after the whitespace tells a parameter from a scheme.
func startsChallenge(token string) bool {
	ws := strings.IndexAny(token, " \t")
	if ws <= 0 || strings.Contains(token[:ws], "=") {
		return false
	}
	return !strings.HasPrefix(strings.TrimLeft(token[ws:], " \t"), "=")
}

// groupChallengeTokens groups tokens per challenge. Tokens before the first
// scheme word are malformed and discarded.
func groupChallengeTokens(tokens []string) [][]string {
	var groups [][]string
	for _, t := range tokens {
		switch {
		case startsChallenge(t):
			groups = append(groups, []string{t})
		case len(groups) > 0:
			groups[len(groups)-1] = append(groups[len(groups)-1], t)
		}
	}
	return groups
}

// parseParam splits "name=value" into a lowercased name and an unquoted value.
func parseParam(token string) (string, string, bool) {
	name, val, ok := strings.Cut(token, "=")
	if !ok {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(name)), unquoteSIP(strings.TrimSpace(val)), true
}

// splitQOPs splits a qop value on commas and trims each element.
func splitQOPs(v string) []string {
	var out []string
	for _, q := range strings.Split(v, ",") {
		if q = strings.TrimSpace(q); q != "" {
			out = append(out, q)
		}
	}
	return out
}

// challengeFromTokens builds one Challenge from its token group.
func challengeFromTokens(headerName string, group []string) Challenge {
	ws := strings.IndexAny(group[0], " \t") // startsChallenge guarantees ws > 0
	scheme, rest := group[0][:ws], group[0][ws+1:]
	params := challengeParams{}
	for _, tok := range append([]string{strings.TrimSpace(rest)}, group[1:]...) {
		if name, val, ok := parseParam(tok); ok {
			params[name] = val
		}
	}
	alg := strings.ToUpper(params["algorithm"])
	if alg == "" {
		alg = "MD5" // RFC 3261 §22.4 / RFC 2617 §3.2.1 default
	}
	return Challenge{
		Scheme: scheme, Realm: params["realm"], Nonce: params["nonce"],
		Opaque: params["opaque"], Algorithm: alg, QOPs: splitQOPs(params["qop"]),
		Stale: strings.EqualFold(params["stale"], "true"), Header: headerName,
	}
}

// parseChallenges splits one (possibly merged, possibly multi-challenge) header
// value into its challenges, in the order the peer listed them.
func parseChallenges(headerName, value string) []Challenge {
	var out []Challenge
	for _, g := range groupChallengeTokens(splitTopLevel(value)) {
		out = append(out, challengeFromTokens(headerName, g))
	}
	return out
}

// selectChallenge returns the topmost challenge this build can answer and true,
// or the zero Challenge and false when none is usable.
//
// RFC 8760 §2.4: a client uses the most-preferred challenge it supports and
// ignores the ones it does not understand. "Most preferred" is the peer's
// ordering, not Cellrift's, so this is a first-match scan in received order
// and NOT a ranking of our own, which would silently override the peer.
func selectChallenge(cs []Challenge) (Challenge, bool) {
	for _, c := range cs {
		if challengeUsable(c) {
			return c, true
		}
	}
	return Challenge{}, false
}

// challengeUsable is the predicate selectChallenge scans with. Algorithm
// support is asked of SupportedDigestAlgorithm rather than listed again here:
// a second list is where a newly added algorithm would be forgotten. No test
// enforces that; it is a design choice.
func challengeUsable(c Challenge) bool {
	return strings.EqualFold(c.Scheme, "Digest") &&
		SupportedDigestAlgorithm(c.Algorithm) &&
		c.Nonce != "" && c.Realm != "" &&
		containsFold(c.QOPs, "auth")
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// describeOfferedChallenges renders what the peer offered, for the failure
// message when nothing was usable: "algorithm=X qop=a,b" per challenge,
// separated by "; ", with "(absent)" for a missing parameter. It is the whole
// diagnosis for "the peer and Cellrift share no algorithm", so it is never
// shortened to a count. It names no credential-derived value.
func describeOfferedChallenges(cs []Challenge) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		qop := "(absent)"
		if len(c.QOPs) > 0 {
			qop = strings.Join(c.QOPs, ",")
		}
		parts = append(parts, "algorithm="+c.Algorithm+" qop="+qop)
	}
	return strings.Join(parts, "; ")
}
