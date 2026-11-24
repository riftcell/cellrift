package sip

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixtures. The expected response= values are LITERALS computed outside this
// package and re-derived when this file was written:
//
//	H() := lowercase hex digest; A1 = "impi-user:cellrift.interop:shared-pw";
//	A2 = "REGISTER:sip:447700900201@registrar.invalid";
//	response = H(H(A1):nonce:00000001:0a4f113b:auth:H(A2))
//	printf %s "$A1" | openssl dgst -md5        (and the same chain through python3 hashlib)
//
// No test in this file calls computeDigestResponse: a test that checks
// computeDigestResponse with computeDigestResponse proves nothing.
const (
	authURI       = "sip:447700900201@registrar.invalid"
	authCNonce    = "0a4f113b"
	authNonce01   = "cellrift-mock-nonce-01"
	authNonce02   = "cellrift-mock-nonce-02"
	hexMD5Nonce01 = "8b4c47cb796ad1e4254bedba0a4ee051"
	hexSHA256N01  = "bc8968d7da2444df1c4e69ef27cd5f028381a5810a8c64f408917a454a1e1643"
	hexMD5Nonce02 = "8c39bee65841bb321c026fc650814d9c"

	challengeMD5   = `Digest realm="cellrift.interop",nonce="cellrift-mock-nonce-01",opaque="opaque-md5-01",algorithm=MD5,qop="auth"`
	challengeSHA   = `Digest realm="cellrift.interop",nonce="cellrift-mock-nonce-01",opaque="opaque-sha256-01",algorithm=SHA-256,qop="auth"`
	challenge407MD = `Digest realm="cellrift.interop",nonce="cellrift-mock-nonce-02",opaque="opaque-md5-02",algorithm=MD5,qop="auth"`
)

// authFixture is the shared mock-and-adapter helper. It neutralises the ambient
// credential (layer 3 reads the PROCESS environment, so a box exporting
// CELLRIFT_SIP_PASSWORD would hand a password to rows that must have none)
// and pins the cnonce.
func authFixture(t *testing.T) (*MockUASServer, *Adapter) {
	t.Helper()
	mock, a := newChallengeFixture(t) // t.Setenv("CELLRIFT_SIP_PASSWORD", "") lives there
	t.Setenv("CELLRIFT_SIP_PASSWORD", "")
	orig := newCNonce
	newCNonce = func() string { return authCNonce }
	t.Cleanup(func() { newCNonce = orig })
	return mock, a
}

func authFields() map[string]string {
	return map[string]string{
		"Request-URI": authURI,
		"From":        "<sip:447700900201@ims.local>;tag=auth001",
		"To":          "<sip:447700900201@registrar.invalid>",
		"Contact":     "<sip:447700900201@10.0.0.1:5060>",
		"Call-ID":     "auth-retry-test@cellrift",
	}
}

func authConfig() map[string]string {
	return map[string]string{"username": "impi-user", "password": "shared-pw", "timeout_ms": "1500"}
}

// authExpect is what one retry's credential header must carry.
type authExpect struct {
	Header, Alg, Nonce, Opaque, Hex string
}

// expectAuthorization asserts the whole grammar of the retry's credential
// header with anchored patterns, on a request that crossed a socket and was
// parsed by the mock. Hex is a literal; nothing here computes a digest.
func expectAuthorization(req *SIPMessage, want authExpect) error {
	if req.RequestURI != authURI {
		return fmt.Errorf("request line uri %q, want %q", req.RequestURI, authURI)
	}
	v, ok := req.Headers[want.Header]
	if !ok {
		return fmt.Errorf("no %s header on the retry", want.Header)
	}
	other := map[string]string{"Authorization": "Proxy-Authorization", "Proxy-Authorization": "Authorization"}[want.Header]
	if _, bad := req.Headers[other]; bad {
		return fmt.Errorf("retry carries %s as well as %s", other, want.Header)
	}
	rules := []string{
		`username="impi-user"`, `realm="cellrift.interop"`,
		`nonce="` + want.Nonce + `"`, `uri="` + authURI + `"`,
		`response="` + want.Hex + `"`, `algorithm=` + want.Alg,
		`cnonce="` + authCNonce + `"`, `opaque="` + want.Opaque + `"`,
		`qop=auth`, `nc=00000001`,
	}
	if !strings.HasPrefix(v, "Digest ") {
		return fmt.Errorf("credential does not start with the Digest scheme")
	}
	for _, r := range rules {
		if !regexp.MustCompile(`(^|, )` + regexp.QuoteMeta(r) + `(,|$)`).MatchString(strings.TrimPrefix(v, "Digest ")) {
			return fmt.Errorf("credential lacks %s", r)
		}
	}
	return nil
}

// verifierLog captures the verifier's own verdicts. The responder runs on the
// mock's read goroutine, so access is guarded.
type verifierLog struct {
	mu   sync.Mutex
	errs []error
}

func (l *verifierLog) wrap(want authExpect) func(*SIPMessage) error {
	return func(req *SIPMessage) error {
		err := expectAuthorization(req, want)
		l.mu.Lock()
		defer l.mu.Unlock()
		l.errs = append(l.errs, err)
		return err
	}
}

func (l *verifierLog) failures() []error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []error
	for _, e := range l.errs {
		if e != nil {
			out = append(out, e)
		}
	}
	return out
}

func runRegister(t *testing.T, a *Adapter, addr string, fields, cfg map[string]string) *common.StepResult {
	t.Helper()
	res, err := a.Execute("send", addr, "REGISTER", fields, cfg)
	require.NoError(t, err)
	return res
}

func runHappyPath(t *testing.T, status int, hdr string, chal []string, want authExpect) (*MockUASServer, *common.StepResult) {
	t.Helper()
	mock, a := authFixture(t)
	var log verifierLog
	mock.SetResponder(DigestChallengeResponder(status, "Challenge", hdr, chal, log.wrap(want)))
	res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
	assert.Equal(t, common.StatusPassed, res.Status, res.Error)
	assert.Equal(t, 200, res.Response["status_code"])
	assert.Empty(t, log.failures())
	assert.Len(t, mock.WaitForRequests(t, 2), 2, "exactly one original and one retry")
	return mock, res
}

func TestAuthRetry_401MD5(t *testing.T) {
	runHappyPath(t, 401, HeaderWWWAuth, []string{challengeMD5},
		authExpect{"Authorization", "MD5", authNonce01, "opaque-md5-01", hexMD5Nonce01})
}

// The dual-challenge fixture is ONE header line carrying two comma-separated
// challenges, which is what ParseMessage produces from two physical lines. It
// is NOT proof that two physical lines arrive correctly: that proof is
// TestParseChallenges in challenge_test.go, which uses a real peer's bytes.
func TestAuthRetry_401DualChallengeChoosesSHA256WithItsOwnOpaque(t *testing.T) {
	mock, _ := runHappyPath(t, 401, HeaderWWWAuth, []string{challengeSHA, challengeMD5},
		authExpect{"Authorization", "SHA-256", authNonce01, "opaque-sha256-01", hexSHA256N01})
	retry := mock.ReceivedRequests()[1]
	assert.NotContains(t, string(retry.Raw), "opaque-md5-01", "the MD5 challenge's opaque must not be echoed")
}

func TestAuthRetry_407UsesProxyAuthorization(t *testing.T) {
	mock, _ := runHappyPath(t, 407, HeaderProxyAuth, []string{challenge407MD},
		authExpect{"Proxy-Authorization", "MD5", authNonce02, "opaque-md5-02", hexMD5Nonce02})
	retry, err := ParseMessage(mock.ReceivedRequests()[1].Raw)
	require.NoError(t, err)
	assert.NotContains(t, retry.Headers, "Authorization")
}

func TestAuthRetry_ReusesCallIDAndFromAndBumpsCSeqWithAFreshBranch(t *testing.T) {
	mock, a := authFixture(t)
	var log verifierLog
	want := authExpect{"Authorization", "MD5", authNonce01, "opaque-md5-01", hexMD5Nonce01}
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, log.wrap(want)))
	fields := authFields()
	fields["Via"] = "SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKauthor1;rport"
	fields["CSeq"] = "7 REGISTER"
	res := runRegister(t, a, mock.Addr(), fields, authConfig())
	require.Equal(t, common.StatusPassed, res.Status, res.Error)

	reqs := mock.WaitForRequests(t, 2)
	first, err := ParseMessage(reqs[0].Raw)
	require.NoError(t, err)
	retry, err := ParseMessage(reqs[1].Raw)
	require.NoError(t, err)
	assert.Equal(t, first.Headers[HeaderCallID], retry.Headers[HeaderCallID])
	assert.Equal(t, first.Headers[HeaderFrom], retry.Headers[HeaderFrom])
	assert.Equal(t, first.Headers[HeaderTo], retry.Headers[HeaderTo])
	assert.NotContains(t, retry.Headers[HeaderTo], "tag=", "the challenge's To-tag must not be copied into the request")
	assert.Equal(t, "7 REGISTER", first.Headers[HeaderCSeq])
	assert.Equal(t, "8 REGISTER", retry.Headers[HeaderCSeq])
	assert.NotEqual(t, first.Headers[HeaderVia], retry.Headers[HeaderVia])
	assert.Regexp(t, `;branch=z9hG4bK\d+;rport$`, retry.Headers[HeaderVia], "new branch, author's other Via parameters kept")
	assert.NotContains(t, retry.Headers[HeaderVia], "z9hG4bKauthor1")
	assert.Equal(t, first.RequestURI, retry.RequestURI)
	assert.Equal(t, first.Headers[HeaderContact], retry.Headers[HeaderContact])
}

func TestAuthRetry_ReplacesAnAuthorBuiltAuthorizationField(t *testing.T) {
	mock, a := authFixture(t)
	var log verifierLog
	want := authExpect{"Authorization", "MD5", authNonce01, "opaque-md5-01", hexMD5Nonce01}
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, log.wrap(want)))
	fields := authFields()
	fields["Authorization"] = "Digest stale=junk"
	res := runRegister(t, a, mock.Addr(), fields, authConfig())
	require.Equal(t, common.StatusPassed, res.Status, res.Error)
	reqs := mock.WaitForRequests(t, 2)
	assert.Contains(t, string(reqs[0].Raw), "stale=junk", "the author's declared field goes out on the first request")
	assert.NotContains(t, string(reqs[1].Raw), "stale=junk")
	assert.Empty(t, log.failures())
}

// A retry answered with a non-credential failure must not be diagnosed as a
// credential failure: that sends the next reader after the password.
func TestAuthRetry_RetryAnsweredWith403IsNotDiagnosedAsRejectedCredentials(t *testing.T) {
	for _, tc := range []struct {
		code   int
		reason string
	}{{403, "Forbidden"}, {486, "Busy Here"}} {
		t.Run(tc.reason, func(t *testing.T) {
			mock, a := authFixture(t)
			n := 0
			var mu sync.Mutex
			mock.SetResponder(func(req *SIPMessage) []*SIPMessage {
				mu.Lock()
				n++
				first := n == 1
				mu.Unlock()
				h := mockDialogHeaders(req)
				if first {
					h[HeaderWWWAuth] = challengeMD5
					return []*SIPMessage{BuildResponse(401, "Unauthorized", h)}
				}
				return []*SIPMessage{BuildResponse(tc.code, tc.reason, h)}
			})
			res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
			assert.Equal(t, common.StatusFailed, res.Status)
			assert.Contains(t, res.Error, fmt.Sprintf("SIP %d %s (expected 2xx)", tc.code, tc.reason))
			assert.Contains(t, res.Error, "not a credential failure")
			assert.NotContains(t, res.Error, "credentials rejected")
		})
	}
}

// TestAuthRetry_VerifierCanFail proves the strict verifier REFUSES, which every
// other test in this file assumes. A deliberately wrong expected hex must
// produce the peer-rejected path specifically, not merely a failed step, which
// a closed port would also produce. Three assertions, each ruling out a
// different impostor failure:
//  1. the mock observed exactly 2 requests: the retry really went out;
//  2. the verifier recorded its own mismatch: the refusal came from the
//     grammar/hex check and not from the transport;
//  3. the step's Error says credentials were rejected after one retry: the
//     second 401 was read as a rejection, end to end.
func TestAuthRetry_VerifierCanFail(t *testing.T) {
	mock, a := authFixture(t)
	var log verifierLog
	wrong := authExpect{"Authorization", "MD5", authNonce01, "opaque-md5-01", strings.Repeat("0", 32)}
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, log.wrap(wrong)))
	res := runRegister(t, a, mock.Addr(), authFields(), authConfig())

	assert.Equal(t, common.StatusFailed, res.Status)
	assert.Len(t, mock.WaitForRequests(t, 2), 2, "one retry, ever: a rejected retry is not retried")
	fails := log.failures()
	require.Len(t, fails, 1)
	assert.Contains(t, fails[0].Error(), "response=")
	assert.Contains(t, res.Error, "credentials rejected after one digest retry")
	assert.Contains(t, res.Error, `realm="cellrift.interop", algorithm=MD5`)
	assert.NotContains(t, res.Error, "shared-pw")
	assert.NotContains(t, res.Error, "impi-user")
}

func TestAuthRetry_NoCredentialsAttemptsNoRetry(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, func(*SIPMessage) error { return nil }))
	res := runRegister(t, a, mock.Addr(), authFields(), map[string]string{"timeout_ms": "1500"})
	assert.Equal(t, common.StatusFailed, res.Status)
	assert.Contains(t, res.Error, "SIP 401 Unauthorized (expected 2xx)")
	assert.Contains(t, res.Error, `the peer challenged (realm="cellrift.interop", algorithm=MD5) and no credentials are configured`)
	assert.Len(t, mock.ReceivedRequests(), 1)
}

// A broken credentials_file is a misconfiguration, not "no credentials".
func TestAuthRetry_BrokenCredentialsFileIsNotRelabelled(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, func(*SIPMessage) error { return nil }))
	missing := filepath.Join(t.TempDir(), "typo.yaml")
	res := runRegister(t, a, mock.Addr(), authFields(), map[string]string{"credentials_file": missing, "timeout_ms": "1500"})
	assert.Contains(t, res.Error, "could not be loaded")
	assert.Contains(t, res.Error, missing)
	assert.NotContains(t, res.Error, "no credentials are configured")
	require.NoError(t, os.WriteFile(missing, []byte("passwd: x\n"), 0o600))
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, func(*SIPMessage) error { return nil }))
	res = runRegister(t, a, mock.Addr(), authFields(), map[string]string{"credentials_file": missing, "timeout_ms": "1500"})
	assert.Contains(t, res.Error, "could not be loaded")
	assert.NotContains(t, res.Error, "no credentials are configured")
}

func TestAuthRetry_NoUsableChallenge(t *testing.T) {
	mock, a := authFixture(t)
	bad := `Digest realm="r",nonce="n",algorithm=SHA-512-256,qop="auth"`
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{bad}, func(*SIPMessage) error { return nil }))
	res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
	assert.Contains(t, res.Error, "no usable challenge offered (algorithm=SHA-512-256 qop=auth)")
	assert.Len(t, mock.ReceivedRequests(), 1)
}

func TestAuthRetry_MalformedCSeqFailsWithoutPanic(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, func(*SIPMessage) error { return nil }))
	fields := authFields()
	fields["CSeq"] = "REGISTER"
	res := runRegister(t, a, mock.Addr(), fields, authConfig())
	assert.Equal(t, common.StatusFailed, res.Status)
	assert.Contains(t, res.Error, "CSeq")
}

// One absolute deadline covers both waits: a peer that challenges and then
// never answers the retry costs ~timeout_ms, not twice that.
func TestAuthRetry_OneDeadlineForTheWholeStep(t *testing.T) {
	mock, a := authFixture(t)
	calls := 0
	var mu sync.Mutex
	mock.SetResponder(func(req *SIPMessage) []*SIPMessage {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if !first {
			return nil
		}
		h := mockDialogHeaders(req)
		h[HeaderWWWAuth] = challengeMD5
		time.Sleep(200 * time.Millisecond)
		return []*SIPMessage{BuildResponse(401, "Unauthorized", h)}
	})
	cfg := authConfig()
	cfg["timeout_ms"] = "500"
	begin := time.Now()
	res := runRegister(t, a, mock.Addr(), authFields(), cfg)
	elapsed := time.Since(begin)
	assert.Equal(t, common.StatusFailed, res.Status)
	assert.Less(t, elapsed, 650*time.Millisecond, "a second full timeout would be ~700ms")
	assert.GreaterOrEqual(t, elapsed, 450*time.Millisecond)
}

// A reorder member is read positionally and is deliberately not retried, but a
// challenged member must say so rather than fail as a bare 401.
func TestAuthRetry_ReorderMemberIsNotRetriedAndSaysSo(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, func(*SIPMessage) error { return nil }))
	members := common.ReorderMembers{{Target: mock.Addr(), Message: "REGISTER", Fields: authFields(), Config: authConfig(), SendOrder: 1}}
	results, err := a.ExecuteReorder(members, 0)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, common.StatusFailed, results[0].Status)
	assert.Contains(t, results[0].Error, "digest authentication is not attempted inside a reorder group")
	assert.Len(t, mock.WaitForRequests(t, 1), 1)
}

// The first header the peer lists is not always the one Cellrift answers. Here
// the first challenge names an algorithm Cellrift cannot compute and carries
// its OWN nonce and opaque; the retry must echo the CHOSEN (second) challenge's
// nonce and opaque. In the dual-challenge fixture both challenges share a nonce
// and the chosen one is first, so that fixture cannot tell chosen from first.
func TestAuthRetry_EchoesTheChosenChallengesNonceAndOpaqueNotTheFirstHeaders(t *testing.T) {
	unusable := `Digest realm="cellrift.interop",nonce="first-header-nonce",opaque="first-header-opaque",algorithm=SHA-512-256,qop="auth"`
	mock, _ := runHappyPath(t, 401, HeaderWWWAuth, []string{unusable, challengeMD5},
		authExpect{"Authorization", "MD5", authNonce01, "opaque-md5-01", hexMD5Nonce01})
	retry := string(mock.ReceivedRequests()[1].Raw)
	assert.NotContains(t, retry, "first-header-nonce")
	assert.NotContains(t, retry, "first-header-opaque")
}

// realNewCNonce captures the production generator at package initialisation,
// before any test runs. authFixture reassigns newCNonce to pin it, so reading
// the variable inside a test could return that pin; this copy cannot be swapped.
var realNewCNonce = newCNonce

// TestNewCNonce_ProductionGeneratorVariesAndIsHex exercises the real generator
// rather than the pinned one: RFC 7616 §3.4 requires a fresh cnonce per request,
// and a constant one would silently remove the client's share of replay
// resistance while every interop peer, which accepts any cnonce, stayed happy.
func TestNewCNonce_ProductionGeneratorVariesAndIsHex(t *testing.T) {
	shape := regexp.MustCompile(`^[0-9a-f]{16}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c := realNewCNonce()
		if !shape.MatchString(c) {
			t.Fatalf("cnonce %q is not 16 lowercase hex characters", c)
		}
		seen[c] = true
	}
	if len(seen) != 200 {
		t.Errorf("200 calls produced %d distinct cnonces; each request needs a fresh one", len(seen))
	}
}
