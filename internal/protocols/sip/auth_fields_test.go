package sip

import (
	"net"
	"strings"
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authMap returns result.Response["auth"] asserted to a PLAIN
// map[string]interface{}. That assertion is the point: it is the very one the
// assertion engine's path walk performs, and it is false for a named map type.
// A named type stored under "auth" therefore fails here, loudly, instead of
// reading as an absent field.
func authMap(t *testing.T, result *common.StepResult) map[string]interface{} {
	t.Helper()
	require.NotNil(t, result)
	raw, present := result.Response[authResponseKey]
	require.True(t, present, "response.auth must be present on every send result")
	m, ok := raw.(map[string]interface{})
	require.True(t, ok, "response.auth must be a plain map[string]interface{}, got %T: the assertion engine's walk cannot descend into a named map type", raw)
	return m
}

// TestAuthFields_StoredAsAPlainMapNotANamedType pins the nested-map trap at the
// unit level for BOTH writers, the challenged stamp and the always-present
// funnel. A named type fails a type assertion to map[string]interface{}, and
// the assertion engine would then report every response.auth.* path as absent.
func TestAuthFields_StoredAsAPlainMapNotANamedType(t *testing.T) {
	type namedAuth map[string]interface{}
	var probe interface{} = namedAuth{}
	_, namedPasses := probe.(map[string]interface{})
	require.False(t, namedPasses, "premise: a named map type fails the assertion the engine's walk performs")

	st := challengeState{Status: 401, Pair: challengeHeaders[401], Chosen: Challenge{Algorithm: "MD5"}}
	stamped := stampAuthFields(&common.StepResult{}, st, true)
	_, ok := stamped.Response[authResponseKey].(map[string]interface{})
	assert.True(t, ok, "stampAuthFields must store a plain map")
	ensured := ensureAuthChallenged(&common.StepResult{})
	_, ok = ensured.Response[authResponseKey].(map[string]interface{})
	assert.True(t, ok, "ensureAuthChallenged must store a plain map")
}

func TestEnsureAuthChallenged_NilSafe(t *testing.T) {
	assert.Nil(t, ensureAuthChallenged(nil))
	r := ensureAuthChallenged(&common.StepResult{})
	assert.Equal(t, false, authMap(t, r)["challenged"])
}

func TestEnsureAuthChallenged_NeverOverwritesAChallengedMap(t *testing.T) {
	st := challengeState{Status: 401, Pair: challengeHeaders[401]}
	r := ensureAuthChallenged(stampAuthFields(&common.StepResult{}, st, false))
	assert.Equal(t, true, authMap(t, r)["challenged"])
}

// sendPath is one way a send step can end. Each runs through Execute, the
// funnel's real call site, and must carry response.auth.challenged == false.
type sendPath struct {
	name      string
	responder func(*SIPMessage) []*SIPMessage
	fields    func() map[string]string
	config    map[string]string
	target    string // "" means the mock's address
}

func noReply(*SIPMessage) []*SIPMessage { return nil }

func baseFields() map[string]string { return authFields() }

// unchallengedSendPaths enumerates the send return paths that never see a
// challenge. Garbage-answer is exercised separately because it needs a raw peer.
func unchallengedSendPaths() []sendPath {
	return []sendPath{
		{name: "passed 200", config: map[string]string{}},
		{name: "non-2xx final", responder: ErrorResponder(500, "Server Internal Error"), config: map[string]string{}},
		{name: "receive timeout", responder: noReply, config: map[string]string{"timeout_ms": "150"}},
		{name: "dropped", config: map[string]string{"drop": "true"}},
		{name: "truncated", config: map[string]string{"truncate": "true"}},
		{name: "duplicated", config: map[string]string{"duplicate": "true"}},
		{name: "connection fault", config: map[string]string{"reset": "true"}},
		{name: "send failed", config: map[string]string{}, target: "256.0.0.1:5060"},
		{name: "body build failure", config: map[string]string{}, fields: func() map[string]string {
			f := baseFields()
			f["__body_template"] = "no-such-template"
			return f
		}},
	}
}

// TestAuthFields_ChallengedFalseOnEverySendReturnPath asserts the key's
// presence on each enumerated path, error and timeout paths included, rather
// than arguing the funnel is total from reading it.
func TestAuthFields_ChallengedFalseOnEverySendReturnPath(t *testing.T) {
	for _, p := range unchallengedSendPaths() {
		t.Run(p.name, func(t *testing.T) {
			mock, a := authFixture(t)
			if p.responder != nil {
				mock.SetResponder(p.responder)
			}
			fields := baseFields()
			if p.fields != nil {
				fields = p.fields()
			}
			target := mock.Addr()
			if p.target != "" {
				target = p.target
			}
			res, err := a.Execute("send", target, "REGISTER", fields, p.config)
			require.NoError(t, err)
			t.Logf("status=%s error=%q", res.Status, res.Error)
			assert.Equal(t, false, authMap(t, res)["challenged"])
			assert.NotContains(t, authMap(t, res), "realm")
		})
	}
}

// A peer that answers with bytes that are not SIP exercises the parse-failure
// return path.
func TestAuthFields_ChallengedFalseWhenTheAnswerDoesNotParse(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_PASSWORD", "")
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer conn.Close()
	go func() {
		buf := make([]byte, 65535)
		if _, addr, err := conn.ReadFrom(buf); err == nil {
			_, _ = conn.WriteTo([]byte("not sip at all\r\n\r\n"), addr)
		}
	}()
	_, a := authFixture(t)
	res, err := a.Execute("send", conn.LocalAddr().String(), "REGISTER", baseFields(), map[string]string{"timeout_ms": "500"})
	require.NoError(t, err)
	assert.Equal(t, common.StatusFailed, res.Status)
	assert.Equal(t, false, authMap(t, res)["challenged"])
}

func TestAuthFields_ChallengedAndRetried(t *testing.T) {
	for _, tc := range []struct {
		status  int
		reason  string
		header  string
		chal    string
		wantHdr string
		opaque  string
	}{
		{401, "Unauthorized", HeaderWWWAuth, challengeMD5, "WWW-Authenticate", "opaque-md5-01"},
		{407, "Proxy Authentication Required", HeaderProxyAuth, challenge407MD, "Proxy-Authenticate", "opaque-md5-02"},
	} {
		t.Run(tc.wantHdr, func(t *testing.T) {
			mock, a := authFixture(t)
			mock.SetResponder(DigestChallengeResponder(tc.status, tc.reason, tc.header, []string{tc.chal}, acceptAnything))
			res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
			require.Equal(t, common.StatusPassed, res.Status, res.Error)
			m := authMap(t, res)
			assert.Equal(t, true, m["challenged"])
			assert.Equal(t, true, m["retried"])
			assert.Equal(t, tc.wantHdr, m["challenge_header"])
			assert.Equal(t, tc.status, m["challenge_status"])
			assert.Equal(t, "cellrift.interop", m["realm"])
			assert.Equal(t, "auth", m["qop"])
			assert.Equal(t, "MD5", m["algorithm"])
			assert.Equal(t, tc.opaque, m["opaque"])
			assert.NotEmpty(t, m["nonce"])
			assert.Equal(t, 200, res.Response["status_code"], "response.status_code is the FINAL response's, not the challenge's")
		})
	}
}

func TestAuthFields_OpaqueOmittedWhenThePeerSentNone(t *testing.T) {
	mock, a := authFixture(t)
	bare := `Digest realm="cellrift.interop",nonce="n1",algorithm=MD5,qop="auth"`
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{bare}, acceptAnything))
	m := authMap(t, runRegister(t, a, mock.Addr(), authFields(), authConfig()))
	assert.NotContains(t, m, "opaque")
	assert.Equal(t, "n1", m["nonce"])
}

func TestAuthFields_BlockedChallengeIsChallengedNotRetried(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, acceptAnything))
	res := runRegister(t, a, mock.Addr(), authFields(), map[string]string{"timeout_ms": "1500"})
	m := authMap(t, res)
	assert.Equal(t, true, m["challenged"])
	assert.Equal(t, false, m["retried"])
	assert.Equal(t, 401, res.Response["status_code"])
}

// A retry that gets no answer still reports the challenge and that a retry was sent.
func TestAuthFields_RetryTimeoutStillReportsTheChallenge(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(provisionalThenSilence())
	cfg := authConfig()
	cfg["timeout_ms"] = "300"
	res := runRegister(t, a, mock.Addr(), authFields(), cfg)
	m := authMap(t, res)
	assert.Equal(t, true, m["challenged"])
	assert.Equal(t, true, m["retried"])
}

func provisionalThenSilence() func(*SIPMessage) []*SIPMessage {
	return func(req *SIPMessage) []*SIPMessage {
		if _, ok := req.Headers[HeaderAuthorization]; ok {
			return nil
		}
		h := mockDialogHeaders(req)
		h[HeaderWWWAuth] = challengeMD5
		return []*SIPMessage{BuildResponse(401, "Unauthorized", h)}
	}
}

// containsAnywhere reports whether want appears as a key or a value at any depth.
func containsAnywhere(v interface{}, want string) bool {
	switch x := v.(type) {
	case string:
		return x == want || (len(want) > 3 && strings.Contains(x, want))
	case map[string]interface{}:
		for k, e := range x {
			if k == want || containsAnywhere(e, want) {
				return true
			}
		}
	}
	return false
}

// TestAuthFields_CarryNoCredentialDerivedValue runs a challenged step with a
// distinctive username, password and pinned cnonce and asserts none of the
// three, nor the computed response hex, appears anywhere in the auth map, at
// any depth, as a key or a value. The hex is the literal computed outside this
// package for these exact inputs.
func TestAuthFields_CarryNoCredentialDerivedValue(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, acceptAnything))
	res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
	require.Equal(t, common.StatusPassed, res.Status, res.Error)
	m := authMap(t, res)
	for _, secret := range []string{"impi-user", "shared-pw", authCNonce, hexMD5Nonce01} {
		assert.False(t, containsAnywhere(m, secret), "response.auth must not contain %q", secret)
	}
	for _, key := range []string{"username", "password", "cnonce", "response", "authorization"} {
		assert.NotContains(t, m, key)
	}
}
