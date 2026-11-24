package sip

import (
	"errors"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The hazard matrix: every way real traffic breaks a transparent digest retry.
// Every test goes through authFixture, which neutralises the ambient
// CELLRIFT_SIP_PASSWORD (layer 3 reads the PROCESS environment, so a box that
// exports it would hand a password to the rows that must have none) and pins
// the cnonce. A row that wants layer 3 sets it again itself.

// authSettle is how long assertRequestCount waits for a request that must NOT
// arrive. WaitForRequests proves a request DID arrive; nothing can prove one
// never will, so the "never N+1" direction is a bounded observation: the step
// has already returned, every request it sent has been written, and loopback
// delivery is microseconds. A brief sleep is the only honest way to say so.
const authSettle = 150 * time.Millisecond

// assertRequestCount asserts the mock saw exactly n requests: it waits (via the
// mock's own waiter, so it does not race the mock's read goroutine) for n to
// arrive, lets authSettle pass, then re-checks that no further one did.
func assertRequestCount(t *testing.T, mock *MockUASServer, n int) {
	t.Helper()
	mock.WaitForRequests(t, n)
	time.Sleep(authSettle)
	assert.Len(t, mock.ReceivedRequests(), n, "the mock must observe exactly %d request(s)", n)
}

func alwaysRejected(*SIPMessage) error { return errors.New("credentials never accepted") }

func acceptAnything(*SIPMessage) error { return nil }

// runChallengeRejected runs a REGISTER against a peer that challenges every
// request, including the retry.
func runChallengeRejected(t *testing.T, status int, reason, hdr, chal string) (*MockUASServer, *common.StepResult) {
	t.Helper()
	mock, a := authFixture(t)
	mock.SetResponder(DigestChallengeResponder(status, reason, hdr, []string{chal}, alwaysRejected))
	return mock, runRegister(t, a, mock.Addr(), authFields(), authConfig())
}

// A wrong password must cost one retry, not a request storm.
func TestAuthRetry_SecondChallengeRetriesExactlyOnce(t *testing.T) {
	mock, res := runChallengeRejected(t, 401, "Unauthorized", HeaderWWWAuth, challengeMD5)
	assertRequestCount(t, mock, 2)
	assert.Equal(t, common.StatusFailed, res.Status)
	assert.Contains(t, res.Error, "SIP 401 Unauthorized (expected 2xx)")
	assert.Contains(t, res.Error, "credentials rejected after one digest retry")
}

// The 407 twin: both status codes are pinned separately.
func TestAuthRetry_SecondProxyChallengeRetriesExactlyOnce(t *testing.T) {
	mock, res := runChallengeRejected(t, 407, "Proxy Authentication Required", HeaderProxyAuth, challenge407MD)
	assertRequestCount(t, mock, 2)
	assert.Equal(t, common.StatusFailed, res.Status)
	assert.Contains(t, res.Error, "SIP 407 Proxy Authentication Required (expected 2xx)")
	assert.Contains(t, res.Error, "credentials rejected after one digest retry")
}

// The 2xx gate behaves exactly as it did before authentication existed when
// there is nothing to answer with.
func TestAuthRetry_NoCredentialsSendsNoRetryAndKeepsTheUnchangedGateFailure(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, acceptAnything))
	res := runRegister(t, a, mock.Addr(), authFields(), map[string]string{"timeout_ms": "1500"})
	assertRequestCount(t, mock, 1)
	assert.Equal(t, common.StatusFailed, res.Status)
	assert.Contains(t, res.Error, "SIP 401 Unauthorized (expected 2xx)")
	assert.Contains(t, res.Error, "no credentials are configured")
}

// A shared-algorithm failure must be diagnosable from the message alone.
func TestAuthRetry_NoUsableChallengeNamesWhatWasOffered(t *testing.T) {
	mock, a := authFixture(t)
	only := `Digest realm="r",nonce="n",algorithm=SHA-512-256,qop="auth"`
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{only}, acceptAnything))
	res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
	assertRequestCount(t, mock, 1)
	assert.Equal(t, common.StatusFailed, res.Status)
	assert.Contains(t, res.Error, "SHA-512-256")
}

// TestAuthRetry_SharedToTagStillRegisters is the regression for the dialog
// tracker. UpdateFromResponse maps any status >= 300 to Terminated and
// Terminated has no outgoing transitions, so a 401 folded into the tracker makes
// the 200 answering the retry fail with "invalid dialog transition from
// terminated to registered" -- whenever the 200 carries the 401's To-tag. The
// in-process mock uses a CONSTANT To-tag (srv-mock-001), so this is the FIRST
// case a naive implementation hits; the pinned Asterisk happens to derive a
// fresh To-tag per Via branch and would hide it.
func TestAuthRetry_SharedToTagStillRegisters(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, acceptAnything))
	res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
	assert.Equal(t, common.StatusPassed, res.Status, res.Error)
	assert.NotContains(t, res.Error, "invalid dialog transition")
	assert.True(t, a.Dialogs().HasDialogInState(StateRegistered))
	// Both the 401 and the 200 carry the mock's constant srv-mock-001 To-tag.
	assertRequestCount(t, mock, 2)
}

// provisionalThen answers each request with `provisionals` x 100 Trying and
// then a final: a 401 for a request with no Authorization header, a 200 for one
// that carries it.
func provisionalThen(provisionals int) func(req *SIPMessage) []*SIPMessage {
	return func(req *SIPMessage) []*SIPMessage {
		var out []*SIPMessage
		for range provisionals {
			out = append(out, BuildResponse(100, "Trying", mockDialogHeaders(req)))
		}
		h := mockDialogHeaders(req)
		if _, ok := req.Headers[HeaderAuthorization]; ok {
			return append(out, BuildResponse(200, "OK", h))
		}
		h[HeaderWWWAuth] = challengeMD5
		return append(out, BuildResponse(401, "Unauthorized", h))
	}
}

// A provisional before a challenge is ordinary VoLTE traffic.
func TestAuthRetry_ProvisionalBeforeTheChallenge(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(provisionalThen(1))
	res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
	assert.Equal(t, common.StatusPassed, res.Status, res.Error)
	assertRequestCount(t, mock, 2)
}

// Each transaction gets its OWN provisional budget. With the budget at 2 and
// one 100 per transaction, each wait spends 2 iterations (the 100, then the
// final): 100,401 then 100,200. One budget shared across both waits would need
// 4 and be exhausted on the retry's 100.
func TestAuthRetry_EachTransactionHasItsOwnProvisionalBudget(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_MAX_PROVISIONAL", "2")
	mock, a := authFixture(t)
	mock.SetResponder(provisionalThen(1))
	res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
	assert.Equal(t, common.StatusPassed, res.Status, res.Error)
	assertRequestCount(t, mock, 2)
}

// authorizationAnswers challenges a request with no Authorization header and
// answers 200 to any request carrying one, so a retry that was attempted would
// SUCCEED and be visible.
func authorizationAnswers(req *SIPMessage) []*SIPMessage {
	h := mockDialogHeaders(req)
	if _, ok := req.Headers[HeaderAuthorization]; ok {
		return []*SIPMessage{BuildResponse(200, "OK", h)}
	}
	h[HeaderWWWAuth] = challengeMD5
	return []*SIPMessage{BuildResponse(401, "Unauthorized", h)}
}

// The duplicate's own 401 (same CSeq as the original) must not be read as the
// retry's answer. The OUTCOME is asserted, not the mechanism: the stray 401 is
// consumed either by the retry read's CSeq filter or by the duplicate drain,
// and either is correct, so pinning which one ate it would make the test brittle
// against a legitimate change. The request count goes through the waiter because
// the duplicate's second copy is written without waiting for an answer.
func TestAuthRetry_DuplicatedStepDoesNotReportAFalseRejection(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(authorizationAnswers)
	cfg := authConfig()
	cfg["duplicate"] = "true"
	res := runRegister(t, a, mock.Addr(), authFields(), cfg)
	assertRequestCount(t, mock, 3) // two copies and one retry
	assert.Equal(t, common.StatusPassed, res.Status, res.Error)
	assert.Empty(t, res.Error)
	assert.NotContains(t, res.Error, "credentials rejected")
}

// A challenged reorder member fails as it does today and is never retried. The
// setup is load-bearing: the members carry resolvable credentials AND the peer
// answers 200 to any request with an Authorization header, so a retry that WAS
// attempted would turn the member green and add a request. Without credentials
// the member would be blocked before any retry and the test would be green for
// the wrong reason. The count is asserted first so a failure names what changed.
func TestAuthRetry_ReorderMemberIsNotRetried(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(authorizationAnswers)
	members := common.ReorderMembers{
		reorderAuthMember(mock.Addr(), "a", 1),
		reorderAuthMember(mock.Addr(), "b", 2),
	}
	results, err := a.ExecuteReorder(members, 0)
	require.NoError(t, err)
	assertRequestCount(t, mock, 2)
	require.Len(t, results, 2)
	for i, r := range results {
		assert.Equal(t, common.StatusFailed, r.Status, "member %d must fail as it does without authentication", i)
		assert.Contains(t, r.Error, "SIP 401 Unauthorized (expected 2xx)")
	}
}

func reorderAuthMember(addr, label string, order int) common.ReorderMember {
	f := authFields()
	f["Call-ID"] = "auth-reorder-" + label + "@cellrift"
	return common.ReorderMember{Target: addr, Message: "REGISTER", Fields: f, Config: authConfig(), SendOrder: order}
}

// An author-supplied header reaches parseCSeq on the retry path.
func TestAuthRetry_MalformedAuthorSuppliedCSeqFailsWithoutPanicking(t *testing.T) {
	mock, a := authFixture(t)
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, acceptAnything))
	fields := authFields()
	fields["CSeq"] = "REGISTER"
	var res *common.StepResult
	require.NotPanics(t, func() { res = runRegister(t, a, mock.Addr(), fields, authConfig()) })
	assert.Equal(t, common.StatusFailed, res.Status)
	assert.Contains(t, res.Error, "CSeq")
	// The retry could not be built, but the peer DID challenge: the funnel's
	// fallback of challenged == false would be a false negative.
	m := authMap(t, res)
	assert.Equal(t, true, m["challenged"])
	assert.Equal(t, false, m["retried"])
}

// failSecondSend delegates to a real transport but fails every Send after the
// first, so the digest retry cannot be written. It wraps the transport handed
// to NewAdapterWithTransport; no production seam is involved.
type failSecondSend struct {
	common.Transport
	sends int
}

func (f *failSecondSend) Send(msg []byte, addr string) error {
	f.sends++
	if f.sends > 1 {
		return errors.New("injected: second send refused")
	}
	return f.Transport.Send(msg, addr)
}

// A retry that cannot be sent still reports the challenge, and retried false:
// nothing went out.
func TestAuthRetry_RetrySendFailureStillReportsTheChallengeAndNoRetry(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_PASSWORD", "")
	mock := NewMockUAS(t)
	udp, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	a := NewAdapterWithTransport(&failSecondSend{Transport: udp}, "udp", 2*time.Second)
	t.Cleanup(func() { a.Close(); mock.Close() })
	mock.SetResponder(DigestChallengeResponder(401, "Unauthorized", HeaderWWWAuth, []string{challengeMD5}, acceptAnything))
	res := runRegister(t, a, mock.Addr(), authFields(), authConfig())
	assert.Equal(t, common.StatusFailed, res.Status)
	assert.Contains(t, res.Error, "digest retry send failed")
	m := authMap(t, res)
	assert.Equal(t, true, m["challenged"])
	assert.Equal(t, false, m["retried"])
	assertRequestCount(t, mock, 1)
}
