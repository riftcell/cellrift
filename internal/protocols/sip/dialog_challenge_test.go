package sip

import (
	"strconv"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newChallengeFixture(t *testing.T) (*MockUASServer, *Adapter) {
	t.Helper()
	// Layer 3 of the credential precedence reads the PROCESS environment.
	// A box exporting CELLRIFT_SIP_PASSWORD would hand a password to rows that
	// are supposed to have none.
	t.Setenv("CELLRIFT_SIP_PASSWORD", "")
	mock := NewMockUAS(t)
	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	a := NewAdapterWithTransport(impair.Wrap(tr), "udp", 2*time.Second)
	t.Cleanup(func() {
		a.Close()
		mock.Close()
	})
	return mock, a
}

var registerFields = map[string]string{
	"From":    "<sip:447700900001@ims.local>;tag=chal001",
	"To":      "<sip:447700900001@ims.local>",
	"Call-ID": "challenge-dialog-test@cellrift",
	"CSeq":    "1 REGISTER",
}

func sendRegister(t *testing.T, a *Adapter, addr string) *common.StepResult {
	t.Helper()
	res, err := a.Execute("send", addr, "REGISTER", cloneFields(registerFields), map[string]string{"timeout_ms": "1000"})
	require.NoError(t, err)
	return res
}

func cloneFields(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// TestChallengeDoesNotTerminateTheDialog: a 401 or 407 is a challenge, not a
// dialog termination. No credentials are configured so no retry is attempted;
// the tracker's behaviour is observable on its own.
func TestChallengeDoesNotTerminateTheDialog(t *testing.T) {
	for _, code := range []int{401, 407} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			mock, a := newChallengeFixture(t)
			mock.SetResponder(ErrorResponder(code, "Challenge"))
			sendRegister(t, a, mock.Addr())
			assert.False(t, a.Dialogs().HasDialogInState(StateTerminated))
		})
	}
}

// TestAssertStateTerminatedIsNotVacuousAfterChallenge: before the challenge
// skip, a 401 terminated the dialog, so `assert_state: terminated` passed after
// any challenge for the wrong reason.
func TestAssertStateTerminatedIsNotVacuousAfterChallenge(t *testing.T) {
	mock, a := newChallengeFixture(t)
	mock.SetResponder(ErrorResponder(401, "Unauthorized"))
	sendRegister(t, a, mock.Addr())
	res, err := a.Execute("assert_state", "", "", nil, map[string]string{"check": "terminated", "timeout_ms": "150"})
	require.NoError(t, err)
	assert.Equal(t, common.StatusFailed, res.Status)
}

// TestChallengeThenSharedToTag200Registers: the mock reuses one To-tag for the
// challenge and the 200, as a real peer may. The 200 must still register.
func TestChallengeThenSharedToTag200Registers(t *testing.T) {
	mock, a := newChallengeFixture(t)
	mock.SetResponder(ErrorResponder(401, "Unauthorized"))
	sendRegister(t, a, mock.Addr())
	mock.SetResponder(nil)
	res := sendRegister(t, a, mock.Addr())
	assert.Equal(t, common.StatusPassed, res.Status, res.Error)
	assert.True(t, a.Dialogs().HasDialogInState(StateRegistered))
}

// TestNonAuthFinalStillTerminatesTheDialog protects
// scenarios/ims/call_rejected.yaml, which asserts `assert_state: terminated`
// after a 486. Narrowing the challenge skip to every >= 300 would break it.
func TestNonAuthFinalStillTerminatesTheDialog(t *testing.T) {
	for _, tc := range []struct {
		code   int
		reason string
	}{{486, "Busy Here"}, {500, "Server Internal Error"}} {
		t.Run(strconv.Itoa(tc.code), func(t *testing.T) {
			mock, a := newChallengeFixture(t)
			mock.SetResponder(ErrorResponder(tc.code, tc.reason))
			sendRegister(t, a, mock.Addr())
			assert.True(t, a.Dialogs().HasDialogInState(StateTerminated))
		})
	}
}

// cseqResponder answers one request with a final response per entry of cseqs,
// each stamped with that CSeq number and status.
func cseqResponder(cseqs []int, statuses []int) func(req *SIPMessage) []*SIPMessage {
	return func(req *SIPMessage) []*SIPMessage {
		var out []*SIPMessage
		for i, n := range cseqs {
			h := mockDialogHeaders(req)
			h["CSeq"] = strconv.Itoa(n) + " REGISTER"
			out = append(out, BuildResponse(statuses[i], "X", h))
		}
		return out
	}
}

func sendThenWait(t *testing.T, mock *MockUASServer, a *Adapter, want int) (*SIPMessage, *common.StepResult) {
	t.Helper()
	msg := BuildRequest("REGISTER", "sip:"+mock.Addr(), cloneFields(registerFields))
	require.NoError(t, a.transport.Send(Serialise(msg), mock.Addr()))
	m, _, res := a.waitForFinalResponse(time.Now(), time.Now().Add(time.Second), want)
	return m, res
}

func TestWaitForFinalResponse_CSeqFilterSkipsTheWrongTransaction(t *testing.T) {
	mock, a := newChallengeFixture(t)
	mock.SetResponder(cseqResponder([]int{1, 7}, []int{401, 200}))
	m, res := sendThenWait(t, mock, a, 7)
	require.Nil(t, res)
	assert.Equal(t, 200, m.StatusCode)
}

func TestWaitForFinalResponse_ZeroTakesTheFirstFinal(t *testing.T) {
	mock, a := newChallengeFixture(t)
	mock.SetResponder(cseqResponder([]int{1, 7}, []int{401, 200}))
	m, res := sendThenWait(t, mock, a, 0)
	require.Nil(t, res)
	assert.Equal(t, 401, m.StatusCode)
}

func TestWaitForFinalResponse_CSeqFilterIsBounded(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_MAX_PROVISIONAL", "2")
	mock, a := newChallengeFixture(t)
	mock.SetResponder(cseqResponder([]int{1, 1, 1}, []int{401, 401, 401}))
	_, res := sendThenWait(t, mock, a, 7)
	require.NotNil(t, res)
	assert.Contains(t, res.Error, "CELLRIFT_SIP_MAX_PROVISIONAL")
}

// TestWaitForFinalResponse_UnparseableCSeqIsNotSkipped: dropping a response
// whose CSeq cannot be read would be a new way to hang a step.
func TestWaitForFinalResponse_UnparseableCSeqIsNotSkipped(t *testing.T) {
	mock, a := newChallengeFixture(t)
	mock.SetResponder(func(req *SIPMessage) []*SIPMessage {
		h := mockDialogHeaders(req)
		h["CSeq"] = "garbage"
		return []*SIPMessage{BuildResponse(200, "OK", h)}
	})
	m, res := sendThenWait(t, mock, a, 7)
	require.Nil(t, res)
	assert.Equal(t, 200, m.StatusCode)
}
