package sip

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

// DigestParams is the complete set of challenge-derived and client-chosen
// inputs to one digest response. It flows resolved-credentials + parsed
// challenge -> computeDigestResponse -> formatAuthorization and is never
// stored: a new value is built per challenge answered.
type DigestParams struct {
	Algorithm, Username, Realm, Password string
	Method, URI, Nonce, NC, CNonce, QOP  string
}

// hashConstructors maps an uppercased challenge algorithm token to the hash it
// names. It is looked up once per challenge answered, by computeDigestResponse
// and by SupportedDigestAlgorithm, so there is one table and one answer.
// Table-driven rather than branching on the name, and the lookup failing IS the "we do not
// understand this challenge" answer (RFC 8760 §2.4): MD5-sess, SHA-512-256 and
// AKAv1-MD5 are absent on purpose. None has a verified vector, and AKA needs a
// different password derivation.
type hashConstructors map[string]func() hash.Hash

var digestHashes = hashConstructors{"MD5": md5.New, "SHA-256": sha256.New}

// hexHash returns the lowercase hex digest of s (RFC 3261 §25.1 LHEX).
func hexHash(newHash func() hash.Hash, s string) string {
	h := newHash()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// computeDigestResponse computes the RFC 7616 §3.4.1-§3.4.3 response for
// qop=auth:
//
//	A1       = username ":" realm ":" password
//	A2       = Method ":" request-uri
//	response = H( H(A1) ":" nonce ":" nc ":" cnonce ":" qop ":" H(A2) )
//
// H is lowercase hex (RFC 8760 §2.2) and the hashed strings carry no quotes.
// An algorithm outside digestHashes is an error, never an MD5 fallback.
//
// Never format p, or p.Password, into an error string: not with %v, not with
// %+v, not through a struct print. Error text reaches reports.
func computeDigestResponse(p DigestParams) (string, error) {
	nh, ok := digestHashes[strings.ToUpper(p.Algorithm)]
	if !ok {
		return "", fmt.Errorf("unsupported digest algorithm %q", p.Algorithm)
	}
	ha1 := hexHash(nh, p.Username+":"+p.Realm+":"+p.Password)
	ha2 := hexHash(nh, p.Method+":"+p.URI)
	return hexHash(nh, strings.Join([]string{ha1, p.Nonce, p.NC, p.CNonce, p.QOP, ha2}, ":")), nil
}

// SupportedDigestAlgorithm reports whether token names an algorithm
// computeDigestResponse can compute. The comparison is case-insensitive
// because the token arrives from a peer's header.
func SupportedDigestAlgorithm(token string) bool {
	_, ok := digestHashes[strings.ToUpper(token)]
	return ok
}

// formatNC renders a nonce count as RFC 3261 §25.1 nc-value = 8LHEX. The
// lowercase, zero-padded form is one a permissive peer will not enforce, so
// only a unit test stands behind it.
func formatNC(n int) string {
	return fmt.Sprintf("%08x", n)
}

// quoteSIP wraps s in a SIP quoted-string (RFC 3261 §25.1), backslash-escaping
// only `\` and `"`. Go's %q is NOT a substitute: it also escapes non-ASCII to
// \u sequences and applies Go's own escape set, neither of which is SIP
// quoting, and a username carrying a non-ASCII byte would reach the wire
// transformed.
func quoteSIP(s string) string {
	return `"` + sipQuoteEscaper.Replace(s) + `"`
}

var sipQuoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// formatAuthorization renders one digest credential header VALUE (the part
// after "Authorization: " or "Proxy-Authorization: "). response and opaque are
// passed separately because response is computed and opaque belongs to the
// challenge that was chosen, not to the client's parameters.
//
// Quoted (RFC 7616 §3.4): username realm nonce uri response cnonce opaque.
// Unquoted: algorithm qop nc. uri is quoted for SIP specifically — RFC 2617's
// BNF left it unquoted and RFC 3261 §22.4 item 2 / RFC 8760 §2.6 item 2
// override that. opaque is omitted entirely when the challenge carried none.
//
// A peer may well accept a quoted qop, an unpadded nc or an uppercase hex nc;
// the pinned Asterisk 22.10.1 accepts all three. That is why this grammar is
// pinned by unit tests here and not by an interop run.
func formatAuthorization(p DigestParams, response, opaque string) string {
	parts := []string{
		"username=" + quoteSIP(p.Username),
		"realm=" + quoteSIP(p.Realm),
		"nonce=" + quoteSIP(p.Nonce),
		"uri=" + quoteSIP(p.URI),
		"response=" + quoteSIP(response),
		"algorithm=" + p.Algorithm,
		"cnonce=" + quoteSIP(p.CNonce),
	}
	if opaque != "" {
		parts = append(parts, "opaque="+quoteSIP(opaque))
	}
	parts = append(parts, "qop="+p.QOP, "nc="+p.NC)
	return "Digest " + strings.Join(parts, ", ")
}
