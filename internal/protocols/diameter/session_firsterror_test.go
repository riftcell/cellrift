package diameter

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests guard the first-error-wins property of the session's receive
// paths: nextMessage, receiveAnswer (both its transport-error return and its
// failed-DWA-send return) and receiveAnswers. All four already return the
// first error they see, immediately. The tests exist so a later edit cannot
// change that silently.
//
// The existing transport-error tests drive a fake that returns the SAME error
// on every call, and a constant-error fake cannot tell a first error from a
// last one: a function that retries, meets the same error again and returns
// it passes. That is why every script here returns DISTINCT values, why each
// test asserts an exact Receive count, and why scriptedOutcomes fails the
// test on any read past the end of its script.

// outcome is one scripted Receive result: data on success, err on failure.
type outcome struct {
	data []byte
	err  error
}

// scriptedOutcomes returns a recvFn that plays script back in order. The
// t.Fatalf past the end of the script is deliberate and is half the guard: a
// function that reads one time too many fails here before any call-count
// assertion runs. It mirrors scriptedReceive, which can only script data.
func scriptedOutcomes(t *testing.T, script ...outcome) func(int, time.Duration) ([]byte, error) {
	t.Helper()
	return func(idx int, _ time.Duration) ([]byte, error) {
		if idx >= len(script) {
			t.Fatalf("Receive called %d times, script has %d: a first-error path must not read again", idx+1, len(script))
		}
		return script[idx].data, script[idx].err
	}
}

// errFirstRead and errSecondRead must be distinct VALUES, not two copies of
// one message: errors.Is tells them apart by identity, and that distinction
// is what makes a last-error-wins mutant visible.
var (
	errFirstRead  = errors.New("diameter first-error guard: FIRST transport failure")
	errSecondRead = errors.New("diameter first-error guard: SECOND transport failure")
)

func TestSession_FirstErrorWins_NextMessage(t *testing.T) {
	fake := &fakeTransport{recvFn: scriptedOutcomes(t,
		outcome{err: errFirstRead},
		outcome{err: errSecondRead},
	)}
	s := &session{tr: fake}

	_, _, err := s.nextMessage(time.Minute)

	require.ErrorIs(t, err, errFirstRead, "the first transport error must be the one reported")
	assert.NotErrorIs(t, err, errSecondRead, "a later error must never replace the first")
	assert.Equal(t, 1, fake.receiveCallCount(), "a transport error ends the read; it must not be retried")
}

func TestSession_FirstErrorWins_ReceiveAnswer(t *testing.T) {
	hop, answer := buildAnswerForRequest(t)
	fake := &fakeTransport{recvFn: scriptedOutcomes(t,
		outcome{err: errFirstRead},
		outcome{data: answer}, // a mutant that swallows the error finds a valid answer here and succeeds
	)}
	s := &session{tr: fake}

	msg, _, err := s.receiveAnswer(hop, time.Now().Add(time.Minute))

	require.ErrorIs(t, err, errFirstRead, "the first transport error must be the one reported")
	assert.Nil(t, msg, "swallowing the error and reading on would have found a valid answer here; it must not")
	assert.NotErrorIs(t, err, errAnswerTimeout, "a real transport error must not be reported as a budget timeout")
	assert.Equal(t, 1, fake.receiveCallCount(), "a transport error ends the wait; it must not be retried")
}

// This covers the OTHER first-error return in receiveAnswer: the answerDWR
// branch, which the transport-error test above never reaches. A failed DWA
// send is the first error and must be reported, not masked by whatever the
// next read returns.
func TestSession_FirstErrorWins_ReceiveAnswerDWASendFailure(t *testing.T) {
	fake := &fakeTransport{
		sendErr: errFirstRead,
		recvFn:  scriptedOutcomes(t, outcome{data: buildTestDWR(t)}, outcome{err: errSecondRead}),
	}
	s := &session{tr: fake, target: "hss:3868", host: "client.ims.local", realm: "ims.local"}

	_, _, err := s.receiveAnswer(4242, time.Now().Add(time.Minute))

	require.ErrorIs(t, err, errFirstRead, "a failed DWA send is the first error and must be reported")
	assert.NotErrorIs(t, err, errSecondRead, "the next read's error must not mask the failed send")
	assert.Equal(t, 1, fake.receiveCallCount(), "a failed DWA send ends the wait; it must not read on")
}

// TestSession_FirstErrorWins_ReceiveAnswersDWASendFailure guards the twin of
// the branch above, inside the plural receiveAnswers. It exists because that
// branch was NOT covered when these guards first landed: mutating its
// `return msgs, raws, err` to `continue` left the entire diameter package
// green, even though receiveAnswer's identical branch was caught. An
// asymmetric guard over two structurally identical branches is the easiest
// kind of coverage hole to leave behind, and the only way it was found was by
// mutating the branch rather than by reading the code.
func TestSession_FirstErrorWins_ReceiveAnswersDWASendFailure(t *testing.T) {
	idA, _ := buildAnswerForRequest(t)
	fake := &fakeTransport{
		sendErr: errFirstRead,
		recvFn: scriptedOutcomes(t,
			outcome{data: buildTestDWR(t)},
			// A mutant that continues past the failed DWA send reads on and
			// would otherwise report this second error instead of the first.
			outcome{err: errSecondRead},
		),
	}
	s := &session{tr: fake, target: "hss:3868", host: "client.ims.local", realm: "ims.local"}

	msgs, _, err := s.receiveAnswers([]uint32{idA}, time.Now().Add(time.Minute))

	require.ErrorIs(t, err, errFirstRead, "a failed DWA send is the first error and must be reported")
	assert.NotErrorIs(t, err, errSecondRead, "the next read's error must not mask the failed send")
	assert.NotErrorIs(t, err, errAnswerTimeout, "a failed send must not be reported as a budget timeout")
	assert.Empty(t, msgs, "no answer was collected before the failed send")
	assert.Equal(t, 1, fake.receiveCallCount(), "a failed DWA send ends the wait; it must not read on")
}

func TestSession_FirstErrorWins_ReceiveAnswers(t *testing.T) {
	idA, answerA := buildAnswerForRequest(t)
	idB, answerB := buildAnswerForRequest(t)
	fake := &fakeTransport{recvFn: scriptedOutcomes(t,
		outcome{data: answerA},
		outcome{err: errFirstRead},
		outcome{data: answerB}, // a mutant that continues completes the group and returns a nil error
	)}
	s := &session{tr: fake}

	msgs, _, err := s.receiveAnswers([]uint32{idA, idB}, time.Now().Add(time.Minute))

	require.ErrorIs(t, err, errFirstRead, "the first transport error must be the one reported")
	assert.NotErrorIs(t, err, errAnswerTimeout, "a real transport error must not be reported as a budget timeout")
	// The partial map collected before the error is still returned: a
	// documented property of receiveAnswers, not an accident.
	require.Len(t, msgs, 1, "the answer collected before the error must be returned with it")
	assert.Equal(t, idA, msgs[idA].HopByHopID)
	assert.Equal(t, 2, fake.receiveCallCount(), "the read after the error must never happen")
}

// TestSession_FirstErrorWins_HarnessIsNotAlwaysRed is the guard's own guard:
// a scripted fake that failed everything would make the tests above
// meaningless, so this proves the same harness can also produce a success.
func TestSession_FirstErrorWins_HarnessIsNotAlwaysRed(t *testing.T) {
	_, answer := buildAnswerForRequest(t)
	fake := &fakeTransport{recvFn: scriptedOutcomes(t, outcome{data: answer})}
	s := &session{tr: fake}

	_, msg, err := s.nextMessage(time.Minute)

	require.NoError(t, err)
	assert.NotNil(t, msg)
	assert.Equal(t, 1, fake.receiveCallCount())
}
