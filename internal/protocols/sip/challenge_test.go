package sip

import (
	"reflect"
	"testing"
)

// Captured from the pinned andrius/asterisk:22.10.1 image: two header lines
// offering SHA-256 then MD5, one nonce, DIFFERENT opaques.
const (
	asteriskSHA256Challenge = `Digest realm="cellrift.interop",nonce="1790886989/45872934c0ab7e3fe840fb4fa27d4de1",opaque="06ac40ba425faf99",algorithm=SHA-256,qop="auth"`
	asteriskMD5Challenge    = `Digest realm="cellrift.interop",nonce="1790886989/45872934c0ab7e3fe840fb4fa27d4de1",opaque="0f96a2ab0099efad",algorithm=MD5,qop="auth"`
	asteriskNonce           = "1790886989/45872934c0ab7e3fe840fb4fa27d4de1"
)

// RFC 7616 §3.9.1: the SHA-256 and MD5 challenges, qop a quoted LIST.
const (
	rfc7616SHA256Challenge = `Digest realm="http-auth@example.org", qop="auth, auth-int", algorithm=SHA-256, nonce="7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v", opaque="FQhe/qaU925kfnzjCev0ciny7QMkPqMAFRtzCUYo5tdS"`
	rfc7616MD5Challenge    = `Digest realm="http-auth@example.org", qop="auth, auth-int", algorithm=MD5, nonce="7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v", opaque="FQhe/qaU925kfnzjCev0ciny7QMkPqMAFRtzCUYo5tdS"`
)

// Constructed; no real 407 was captured. Grammar is RFC 7616 §3.8 / RFC 3261
// §22.3, identical to the 401's.
const proxyChallenge = `Digest realm="proxy.example",nonce="pn",opaque="po",algorithm=SHA-256,qop="auth"`

func mergedAsterisk() string { return asteriskSHA256Challenge + ", " + asteriskMD5Challenge }

func TestParseChallenges_MergedHeaderKeepsPerChallengeOpaque(t *testing.T) {
	cs := parseChallenges(HeaderWWWAuth, mergedAsterisk())
	if len(cs) != 2 {
		t.Fatalf("got %d challenges, want 2", len(cs))
	}
	want := []struct{ alg, opaque string }{{"SHA-256", "06ac40ba425faf99"}, {"MD5", "0f96a2ab0099efad"}}
	for i, w := range want {
		c := cs[i]
		if c.Algorithm != w.alg || c.Opaque != w.opaque {
			t.Errorf("challenge %d = %s/%s, want %s/%s", i, c.Algorithm, c.Opaque, w.alg, w.opaque)
		}
		if c.Nonce != asteriskNonce || c.Realm != "cellrift.interop" || c.Header != HeaderWWWAuth ||
			!reflect.DeepEqual(c.QOPs, []string{"auth"}) {
			t.Errorf("challenge %d fields wrong: %+v", i, c)
		}
	}
}

func TestParseChallenges(t *testing.T) {
	tests := []struct {
		name, header, value string
		n                   int
		check               func(*testing.T, []Challenge)
	}{
		{"RFC 7616 qop list not split at comma", HeaderWWWAuth, rfc7616SHA256Challenge + ", " + rfc7616MD5Challenge, 2, func(t *testing.T, cs []Challenge) {
			for _, c := range cs {
				if !reflect.DeepEqual(c.QOPs, []string{"auth", "auth-int"}) {
					t.Errorf("QOPs = %q", c.QOPs)
				}
			}
			if cs[0].Algorithm == cs[1].Algorithm {
				t.Errorf("algorithms not distinct")
			}
		}},
		{"nonce containing a comma", HeaderWWWAuth, `Digest realm="r", nonce="ab,cd, ef", qop="auth"`, 1, func(t *testing.T, cs []Challenge) {
			if cs[0].Nonce != "ab,cd, ef" || cs[0].Realm != "r" {
				t.Errorf("got %+v", cs[0])
			}
		}},
		{"nonce containing equals", HeaderWWWAuth, `Digest realm="r",nonce="YWJj==",qop="auth"`, 1, func(t *testing.T, cs []Challenge) {
			if cs[0].Nonce != "YWJj==" {
				t.Errorf("nonce = %q", cs[0].Nonce)
			}
		}},
		{"nonce containing a scheme-looking phrase", HeaderWWWAuth, `Digest realm="r",nonce="a, Digest b=c",qop="auth"`, 1, func(t *testing.T, cs []Challenge) {
			if cs[0].Nonce != "a, Digest b=c" {
				t.Errorf("nonce = %q", cs[0].Nonce)
			}
		}},
		{"proxy header", HeaderProxyAuth, proxyChallenge, 1, func(t *testing.T, cs []Challenge) {
			c := cs[0]
			if c.Header != HeaderProxyAuth || c.Realm != "proxy.example" || c.Opaque != "po" || c.Algorithm != "SHA-256" {
				t.Errorf("got %+v", c)
			}
		}},
		{"absent algorithm is MD5", HeaderWWWAuth, `Digest realm="r",nonce="n",qop="auth"`, 1, func(t *testing.T, cs []Challenge) {
			if cs[0].Algorithm != "MD5" {
				t.Errorf("alg = %q", cs[0].Algorithm)
			}
		}},
		{"bare and quoted algorithm agree", HeaderWWWAuth, `Digest algorithm="md5",realm="r",nonce="n"`, 1, func(t *testing.T, cs []Challenge) {
			if cs[0].Algorithm != "MD5" {
				t.Errorf("alg = %q", cs[0].Algorithm)
			}
		}},
		{"escaped quote in realm", HeaderWWWAuth, `Digest realm="a\"b",nonce="n"`, 1, func(t *testing.T, cs []Challenge) {
			if cs[0].Realm != `a"b` {
				t.Errorf("realm = %q", cs[0].Realm)
			}
		}},
		{"parameter names case-insensitive", HeaderWWWAuth, `Digest REALM="r",Nonce="n",OPAQUE="o",Stale=TRUE`, 1, func(t *testing.T, cs []Challenge) {
			c := cs[0]
			if c.Realm != "r" || c.Nonce != "n" || c.Opaque != "o" || !c.Stale {
				t.Errorf("got %+v", c)
			}
		}},
		{"spaces around equals", HeaderWWWAuth, `Digest realm = "r", nonce = "n", algorithm = MD5, qop = "auth"`, 1, wantSpaced},
		{"tab around equals", HeaderWWWAuth, "Digest realm\t=\t\"r\",\tnonce\t=\t\"n\",\talgorithm\t=\tMD5,\tqop\t=\t\"auth\"", 1, wantSpaced},
		{"spaces after comma only", HeaderWWWAuth, `Digest realm="r",   nonce="n",   algorithm=MD5,   qop="auth"`, 1, wantSpaced},
		{"spaced form in a multi-challenge header", HeaderWWWAuth,
			`Digest realm = "r", nonce = "n", algorithm = SHA-256, qop = "auth", opaque = "o1", ` +
				`Digest realm = "r", nonce = "n", algorithm = MD5, qop = "auth", opaque = "o2"`, 2, func(t *testing.T, cs []Challenge) {
				if cs[0].Algorithm != "SHA-256" || cs[0].Opaque != "o1" || cs[1].Algorithm != "MD5" || cs[1].Opaque != "o2" {
					t.Errorf("got %+v / %+v", cs[0], cs[1])
				}
				for _, c := range cs {
					if c.Nonce != "n" || !reflect.DeepEqual(c.QOPs, []string{"auth"}) {
						t.Errorf("lost nonce or qop: %+v", c)
					}
				}
			}},
		{"lowercase scheme word", HeaderWWWAuth, `digest realm="r",nonce="n"`, 1, nil},
		{"empty", HeaderWWWAuth, ``, 0, nil},
		{"no scheme word", HeaderWWWAuth, `realm="r",nonce="n"`, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := parseChallenges(tt.header, tt.value)
			if len(cs) != tt.n {
				t.Fatalf("got %d challenges, want %d: %+v", len(cs), tt.n, cs)
			}
			if tt.check != nil {
				tt.check(t, cs)
			}
		})
	}
}

// The generic title-casing path produces the proxy header spellings, so no
// canonicalHeaderOverrides entry is needed. This test is what stops a future
// reader adding a redundant one.
func TestCanonicalHeaderName_ProxyHeaders(t *testing.T) {
	if got := canonicalHeaderName("proxy-authenticate"); got != "Proxy-Authenticate" {
		t.Errorf("got %q", got)
	}
	if got := canonicalHeaderName("proxy-authorization"); got != "Proxy-Authorization" {
		t.Errorf("got %q", got)
	}
	if HeaderProxyAuth != "Proxy-Authenticate" || HeaderProxyAuthorization != "Proxy-Authorization" {
		t.Errorf("constants wrong")
	}
	if _, ok := canonicalHeaderOverrides["proxy-authenticate"]; ok {
		t.Errorf("redundant override present")
	}
}

func TestChallengeHeaders_Table(t *testing.T) {
	if p := challengeHeaders[401]; p.Challenge != "WWW-Authenticate" || p.Credential != "Authorization" {
		t.Errorf("401 = %+v", p)
	}
	if p := challengeHeaders[407]; p.Challenge != "Proxy-Authenticate" || p.Credential != "Proxy-Authorization" {
		t.Errorf("407 = %+v", p)
	}
}

func ch(alg string, qops ...string) Challenge {
	return Challenge{Scheme: "Digest", Realm: "r", Nonce: "n", Algorithm: alg, QOPs: qops}
}

func TestSelectChallenge(t *testing.T) {
	basic := Challenge{Scheme: "Basic", Realm: "r"}
	noNonce, noRealm := ch("MD5", "auth"), ch("MD5", "auth")
	noNonce.Nonce, noRealm.Realm = "", ""
	tests := []struct {
		name string
		in   []Challenge
		want string // algorithm of the selection, "" for none
	}{
		{"SHA-256 then MD5 -> SHA-256", parseChallenges(HeaderWWWAuth, mergedAsterisk()), "SHA-256"},
		{"MD5 only -> MD5", parseChallenges(HeaderWWWAuth, asteriskMD5Challenge), "MD5"},
		{"SHA-512-256 first, MD5 second -> MD5", []Challenge{ch("SHA-512-256", "auth"), ch("MD5", "auth")}, "MD5"},
		{"MD5-sess alone -> none", []Challenge{ch("MD5-sess", "auth")}, ""},
		{"AKAv1-MD5 alone -> none", []Challenge{ch("AKAv1-MD5", "auth")}, ""},
		{"auth-int only -> none", []Challenge{ch("MD5", "auth-int")}, ""},
		{"no qop -> none", []Challenge{ch("MD5")}, ""},
		{"Basic -> none", []Challenge{basic}, ""},
		{"missing nonce -> none", []Challenge{noNonce}, ""},
		{"missing realm -> none", []Challenge{noRealm}, ""},
		{"qop auth in mixed case -> usable", []Challenge{ch("MD5", "AUTH")}, "MD5"},
		{"no challenges -> none", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ok := selectChallenge(tt.in)
			if ok != (tt.want != "") || c.Algorithm != tt.want {
				t.Errorf("got %q ok=%v, want %q", c.Algorithm, ok, tt.want)
			}
		})
	}
}

func TestSelectChallenge_SelectedOpaqueTravelsWithIt(t *testing.T) {
	c, ok := selectChallenge(parseChallenges(HeaderWWWAuth, mergedAsterisk()))
	if !ok || c.Algorithm != "SHA-256" || c.Opaque != "06ac40ba425faf99" {
		t.Errorf("got %+v ok=%v", c, ok)
	}
}

func TestSelectChallenge_TopmostSupportedNotOurPreference(t *testing.T) {
	merged := asteriskMD5Challenge + ", " + asteriskSHA256Challenge
	c, ok := selectChallenge(parseChallenges(HeaderWWWAuth, merged))
	if !ok || c.Algorithm != "MD5" || c.Opaque != "0f96a2ab0099efad" {
		t.Errorf("got %+v ok=%v", c, ok)
	}
}

func TestDescribeOfferedChallenges(t *testing.T) {
	got := describeOfferedChallenges([]Challenge{ch("SHA-512-256", "auth"), ch("MD5", "auth-int", "x")})
	if want := "algorithm=SHA-512-256 qop=auth; algorithm=MD5 qop=auth-int,x"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	got = describeOfferedChallenges(parseChallenges(HeaderWWWAuth, `Digest realm="r",nonce="n",algorithm=MD5`))
	if want := "algorithm=MD5 qop=(absent)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// wantSpaced checks the one challenge that the whitespace-variant rows of
// TestParseChallenges all spell differently (RFC 3261 §25.1 allows LWS around
// '=' and after ','): every field must survive, none may be dropped.
func wantSpaced(t *testing.T, cs []Challenge) {
	t.Helper()
	c := cs[0]
	if c.Realm != "r" || c.Nonce != "n" || c.Algorithm != "MD5" || !reflect.DeepEqual(c.QOPs, []string{"auth"}) {
		t.Errorf("got %+v", c)
	}
}
