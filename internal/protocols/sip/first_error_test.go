package sip_test

import (
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests guard the first-error-wins property of SIP's response wait.
// waitForFinalResponse already reports the first transport error it sees, and
// stamps response.timed_out from that same first error. A last-error-wins
// edit would report the wrong text AND stamp the wrong classification, and
// this test catches each independently: the first scripted error is NOT a
// deadline expiry and the second IS, so the stamp itself discriminates.
//
// The transport is scripted with distinct outcomes and fails the test on any
// Receive past the end of its script, so an extra read can never pass
// silently. The guard drives the public NewAdapterWithTransport + Execute
// seam, so it pins what a scenario author actually observes.

// sipOutcome is one scripted Receive result. When fromRequest is set it
// builds the bytes from the request last sent, so a response can echo the
// request's own From / To / Call-ID / CSeq / Via headers.
type sipOutcome struct {
	data        []byte
	err         error
	fromRequest func(req *sip.SIPMessage) []byte
}

type scriptedSIPTransport struct {
	t      *testing.T
	script []sipOutcome
	mu     sync.Mutex
	calls  int
	sent   [][]byte
}

func (s *scriptedSIPTransport) Send(msg []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, append([]byte(nil), msg...))
	return nil
}

func (s *scriptedSIPTransport) Receive(time.Duration) ([]byte, net.Addr, error) {
	s.mu.Lock()
	idx := s.calls
	s.calls++
	sent := s.sent
	s.mu.Unlock()

	if idx >= len(s.script) {
		s.t.Fatalf("Receive called %d times, script has %d: a first-error path must not read again", idx+1, len(s.script))
	}
	out := s.script[idx]
	if out.fromRequest != nil && len(sent) > 0 {
		req, err := sip.ParseMessage(sent[len(sent)-1])
		require.NoError(s.t, err)
		return out.fromRequest(req), nil, nil
	}
	return out.data, nil, out.err
}

func (s *scriptedSIPTransport) Close() error        { return nil }
func (s *scriptedSIPTransport) LocalAddr() net.Addr { return nil }

func (s *scriptedSIPTransport) receiveCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// responseEchoing returns a builder for a response with the given status,
// copying the request's own dialog headers so the adapter accepts it.
func responseEchoing(code int, reason string) func(*sip.SIPMessage) []byte {
	return func(req *sip.SIPMessage) []byte {
		return sip.Serialise(sip.BuildResponse(code, reason, map[string]string{
			"From": req.Headers["From"], "To": req.Headers["To"] + ";tag=guard",
			"Call-ID": req.Headers["Call-ID"], "CSeq": req.Headers["CSeq"], "Via": req.Headers["Via"],
		}))
	}
}

func executeInvite(t *testing.T, tr common.Transport) *common.StepResult {
	t.Helper()
	adapter := sip.NewAdapterWithTransport(tr, "udp", 2*time.Second)
	defer adapter.Close()
	result, err := adapter.Execute("send", "127.0.0.1:5060", "INVITE", map[string]string{
		"From": "<sip:447700900001@ims.local>;tag=fe001",
		"To":   "<sip:447700900002@ims.local>",
	}, nil)
	require.NoError(t, err, "a protocol-level failure is (result, nil); error is reserved for infrastructure")
	return result
}

func TestWaitForFinalResponse_FirstErrorWins(t *testing.T) {
	errFirstNotTimeout := errors.New("sip first-error guard: FIRST transport failure, not a deadline")
	// The second error is os.ErrDeadlineExceeded itself: a mutant reporting
	// the LAST error would stamp timed_out true where correct code stamps false.
	tr := &scriptedSIPTransport{t: t, script: []sipOutcome{
		{fromRequest: responseEchoing(180, "Ringing")},
		{err: errFirstNotTimeout},
		{err: os.ErrDeadlineExceeded},
	}}

	result := executeInvite(t, tr)

	require.Equal(t, common.StatusFailed, result.Status)
	assert.Contains(t, result.Error, "sip first-error guard: FIRST transport failure", "the first transport error must be the one reported")
	assert.NotContains(t, result.Error, os.ErrDeadlineExceeded.Error(), "a later error must never replace the first")
	assert.Equal(t, false, result.Response[impair.TimedOutKey],
		"a non-timeout first error must not be stamped as a peer-silence timeout")
	assert.Equal(t, 2, tr.receiveCallCount(), "the 180 and the first error; the third entry must never be reached")
}

// The control proves the scripted transport can produce a passing step, so
// the guard above is failing for the reason it claims.
func TestWaitForFinalResponse_FirstErrorWins_ControlCasePasses(t *testing.T) {
	tr := &scriptedSIPTransport{t: t, script: []sipOutcome{
		{fromRequest: responseEchoing(200, "OK")},
	}}

	result := executeInvite(t, tr)

	require.Equal(t, common.StatusPassed, result.Status, result.Error)
	assert.Equal(t, false, result.Response[impair.TimedOutKey])
	assert.Equal(t, 1, tr.receiveCallCount())
}
