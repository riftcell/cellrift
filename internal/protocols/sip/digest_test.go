package sip

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rfc7616Password is the password from RFC 7616 §3.9.1's worked example.
// The lowercase "of" is deliberate and load-bearing: §3.4.5's PROSE writes
// "Circle Of Life" with a capital O, and that string produces
// d142103549841c8d7adb7716345048f2, which matches nothing the RFC publishes.
// TestDigestResponse_PublishedVectors asserts both, so changing this constant
// to match the prose fails the suite instead of silently retiring the oracle.
const rfc7616Password = "Circle of Life"

// Every expected hex string below is a literal taken from an RFC or computed
// with openssl. None is produced by the code under test, so none can be
// circular.
const (
	rfc7616MD5      = "8ca523f5e9506fed4657c9700eebdbec"
	rfc7616SHA256   = "753927fa0e85d155564e2e272a28d1802ca10daf4496794697cf8db5856cb6c1"
	rfc7616ProseMD5 = "d142103549841c8d7adb7716345048f2"
	rfc2617MD5      = "6629fae49393a05397450978507c4ef1"
	registerMD5     = "c503e6104501cc7fdbd0f0117cc1b41e"
	registerSHA256  = "2d4b6fc964f5ee55f94a6b37783c0b5af6f4678996eeb3acb21711e05f552bbc"
)

func rfc7616Params(alg, password string) DigestParams {
	return DigestParams{
		Algorithm: alg, Username: "Mufasa", Realm: "http-auth@example.org",
		Password: password, Method: "GET", URI: "/dir/index.html",
		Nonce:  "7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v",
		NC:     "00000001",
		CNonce: "f2/wE4q74E6zIJEtWaHKaf5wv/H5QzzpXusqGemxURZJ",
		QOP:    "auth",
	}
}

func rfc2617Params() DigestParams {
	return DigestParams{
		Algorithm: "MD5", Username: "Mufasa", Realm: "testrealm@host.com",
		Password: "Circle Of Life", Method: "GET", URI: "/dir/index.html",
		Nonce: "dcd98b7102dd2f0e8b11d0f600bfb0c093", NC: "00000001",
		CNonce: "0a4f113b", QOP: "auth",
	}
}

// registerParams is a SIP-shaped input (REGISTER, sip: Request-URI). The two
// RFC vectors are HTTP GETs and prove nothing about either.
func registerParams(alg string) DigestParams {
	return DigestParams{
		Algorithm: alg, Username: "447700900201", Realm: "cellrift.interop",
		Password: "md5-pw-201", Method: "REGISTER",
		URI:   "sip:447700900201@127.0.0.1:15060",
		Nonce: "1790886971/653f6726d2eafc651da5a6f8919dba93", NC: "00000001",
		CNonce: "0a4f113b", QOP: "auth",
	}
}

func TestDigestResponse_PublishedVectors(t *testing.T) {
	// The REGISTER rows were computed with openssl, not by this package:
	//   HA1=$(printf '447700900201:cellrift.interop:md5-pw-201' | openssl dgst -md5 -r | cut -d' ' -f1)
	//   HA2=$(printf 'REGISTER:sip:447700900201@127.0.0.1:15060' | openssl dgst -md5 -r | cut -d' ' -f1)
	//   printf '%s:1790886971/653f6726d2eafc651da5a6f8919dba93:00000001:0a4f113b:auth:%s' "$HA1" "$HA2" \
	//     | openssl dgst -md5 -r | cut -d' ' -f1
	// The SHA-256 row is the same pipeline with `-sha256`.
	tests := []struct {
		name   string
		params DigestParams
		want   string
	}{
		{"RFC 7616 3.9.1 MD5", rfc7616Params("MD5", rfc7616Password), rfc7616MD5},
		{"RFC 7616 3.9.1 SHA-256", rfc7616Params("SHA-256", rfc7616Password), rfc7616SHA256},
		{"RFC 7616 3.4.5 prose casing is WRONG", rfc7616Params("MD5", "Circle Of Life"), rfc7616ProseMD5},
		{"RFC 2617 3.5 MD5", rfc2617Params(), rfc2617MD5},
		{"REGISTER literal MD5 (computed with openssl, not by this package)", registerParams("MD5"), registerMD5},
		{"REGISTER literal SHA-256 (computed with openssl, not by this package)", registerParams("SHA-256"), registerSHA256},
		{"algorithm token case is not significant: md5", rfc7616Params("md5", rfc7616Password), rfc7616MD5},
		{"algorithm token case is not significant: Md5", rfc7616Params("Md5", rfc7616Password), rfc7616MD5},
		{"algorithm token case is not significant: sha-256", rfc7616Params("sha-256", rfc7616Password), rfc7616SHA256},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := computeDigestResponse(tt.params)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDigestResponse_CasingTrapIsDistinct(t *testing.T) {
	got, err := computeDigestResponse(rfc7616Params("MD5", "Circle Of Life"))
	require.NoError(t, err)
	assert.NotEqual(t, rfc7616MD5, got,
		"the capital-O prose password must not reproduce the published hash")
}

func TestDigestResponse_UnsupportedAlgorithm(t *testing.T) {
	for _, alg := range []string{"SHA-512-256", "MD5-sess", "SHA-256-sess", "AKAv1-MD5", "", "Basic"} {
		t.Run(alg, func(t *testing.T) {
			got, err := computeDigestResponse(rfc7616Params(alg, rfc7616Password))
			require.Error(t, err)
			assert.Empty(t, got)
			assert.Contains(t, err.Error(), `unsupported digest algorithm "`+alg+`"`)
			assert.NotContains(t, err.Error(), rfc7616Password, "an error must never carry the password")
			assert.False(t, SupportedDigestAlgorithm(alg))
		})
	}
}

func TestSupportedDigestAlgorithm(t *testing.T) {
	assert.True(t, SupportedDigestAlgorithm("MD5"))
	assert.True(t, SupportedDigestAlgorithm("SHA-256"))
	assert.True(t, SupportedDigestAlgorithm("sha-256"))
	assert.False(t, SupportedDigestAlgorithm("SHA-512-256"))
}

func TestFormatNC(t *testing.T) {
	assert.Equal(t, "00000001", formatNC(1))
	assert.Equal(t, "0000000a", formatNC(10), "lowercase hex, 8 digits")
	assert.Equal(t, "000000ff", formatNC(255))
}

func TestQuoteSIP(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", `impi-user`, `"impi-user"`},
		{"embedded quotes", `he said "hi"`, `"he said \"hi\""`},
		{"backslash", `back\slash`, `"back\\slash"`},
		{"printable non-ASCII kept raw", "üser", "\"üser\""},
		// %q would emit \t and \xff here; SIP quoting leaves both bytes raw.
		{"tab kept raw, unlike %q", "a\tb", "\"a\tb\""},
		{"invalid UTF-8 byte kept raw, unlike %q", "a\xffb", "\"a\xffb\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, quoteSIP(tt.in))
		})
	}
}

func grammarHeader(t *testing.T, alg, opaque string) string {
	t.Helper()
	p := registerParams(alg)
	resp, err := computeDigestResponse(p)
	require.NoError(t, err)
	return formatAuthorization(p, resp, opaque)
}

func TestAuthorizationGrammar(t *testing.T) {
	out := grammarHeader(t, "MD5", "op-aque")
	assert.Regexp(t, regexp.MustCompile(`^Digest `), out, "scheme prefix")
	assert.Regexp(t, regexp.MustCompile(`nc=[0-9a-f]{8}(,|$)`), out, "nc unquoted, 8 lowercase hex")
	assert.NotContains(t, out, `nc="`)
	assert.Regexp(t, regexp.MustCompile(`qop=auth(,|$)`), out, "qop single unquoted token")
	assert.NotContains(t, out, `qop="`)
	assert.Regexp(t, regexp.MustCompile(`algorithm=(MD5|SHA-256)(,|$)`), out, "algorithm unquoted")
	assert.NotContains(t, out, `algorithm="`)
	assert.Contains(t, out, `uri="sip:447700900201@127.0.0.1:15060"`)
	assert.Regexp(t, regexp.MustCompile(`response="[0-9a-f]{32}"`), out)
	assert.Contains(t, out, `response="`+registerMD5+`"`)
	for _, name := range []string{"username", "realm", "nonce", "cnonce", "opaque"} {
		assert.Contains(t, out, name+`="`, "%s must be quoted", name)
	}
}

func TestAuthorizationGrammar_SHA256Width(t *testing.T) {
	out := grammarHeader(t, "SHA-256", "op-aque")
	assert.Regexp(t, regexp.MustCompile(`response="[0-9a-f]{64}"`), out)
	assert.Regexp(t, regexp.MustCompile(`algorithm=SHA-256(,|$)`), out)
}

func TestAuthorizationGrammar_PinnedOrder(t *testing.T) {
	want := `Digest username="447700900201", realm="cellrift.interop", ` +
		`nonce="1790886971/653f6726d2eafc651da5a6f8919dba93", ` +
		`uri="sip:447700900201@127.0.0.1:15060", response="` + registerMD5 + `", ` +
		`algorithm=MD5, cnonce="0a4f113b", opaque="op-aque", qop=auth, nc=00000001`
	assert.Equal(t, want, grammarHeader(t, "MD5", "op-aque"))
}

func TestAuthorizationGrammar_NoOpaqueOmitsTheParameter(t *testing.T) {
	assert.NotContains(t, grammarHeader(t, "MD5", ""), "opaque")
}
