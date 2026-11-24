package diameter

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTransport is a minimal common.Transport double for session tests. It
// never touches a socket: recvFn supplies each Receive() call's return
// value directly (data or error), so tests can script coalescing,
// splitting, timeouts, transport errors and an unbounded chatty
// peer without any real network I/O. sent records every Send() call's bytes
// so a test can decode and assert on what session.go actually wrote to the
// wire.
type fakeTransport struct {
	mu        sync.Mutex
	recvFn    func(callIndex int, timeout time.Duration) ([]byte, error)
	recvCalls int
	sent      [][]byte
	sendErr   error
}

func (f *fakeTransport) Receive(timeout time.Duration) ([]byte, net.Addr, error) {
	f.mu.Lock()
	idx := f.recvCalls
	f.recvCalls++
	f.mu.Unlock()

	data, err := f.recvFn(idx, timeout)
	return data, nil, err
}

func (f *fakeTransport) Send(msg []byte, addr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, append([]byte(nil), msg...))
	return f.sendErr
}

func (f *fakeTransport) Close() error        { return nil }
func (f *fakeTransport) LocalAddr() net.Addr { return nil }

func (f *fakeTransport) receiveCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recvCalls
}

func (f *fakeTransport) sentMessages() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent
}

// scriptedReceive returns a recvFn that hands back chunks[0], chunks[1], ...
// in order, erroring loudly if called more times than chunks provides —
// tests use this to assert an EXACT number of Receive calls (e.g. exactly
// one Receive for two coalesced messages).
func scriptedReceive(t *testing.T, chunks ...[]byte) func(int, time.Duration) ([]byte, error) {
	t.Helper()
	return func(idx int, _ time.Duration) ([]byte, error) {
		if idx >= len(chunks) {
			t.Fatalf("fakeTransport: unexpected Receive call #%d, only %d scripted", idx+1, len(chunks))
		}
		return chunks[idx], nil
	}
}

// withHopByHop overwrites a built message's Hop-by-Hop (bytes [12:16]) and
// End-to-End (bytes [16:20]) fields with explicit values, so correlation
// tests use IDs the test itself chose rather than BuildRequest's random
// ones — an assertion that always passes because the Hop-by-Hop ID it
// compares against was never actually sent proves nothing, so this test
// must use distinct, test-chosen IDs instead.
func withHopByHop(raw []byte, hop, end uint32) []byte {
	out := append([]byte(nil), raw...)
	binary.BigEndian.PutUint32(out[12:16], hop)
	binary.BigEndian.PutUint32(out[16:20], end)
	return out
}

func buildTestDWR(t *testing.T) []byte {
	t.Helper()
	raw, _, err := BuildRequest(CmdDeviceWatchdog, 0, []AVP{
		NewStringAVP(AVPCodeOriginHost, "peer.ims.local"),
		NewStringAVP(AVPCodeOriginRealm, "ims.local"),
	})
	require.NoError(t, err)
	return raw
}

func buildTestAnswer(t *testing.T) []byte {
	t.Helper()
	raw, err := BuildAnswer(CmdUserAuthorization, AppIDCx, []AVP{
		NewUint32AVP(AVPCodeResultCode, ResultSuccess),
	})
	require.NoError(t, err)
	return raw
}

// --- framing ---

func TestSession_NextMessage_SplitsCoalescedMessages(t *testing.T) {
	dwr := buildTestDWR(t)
	answer := withHopByHop(buildTestAnswer(t), 42, 43)
	coalesced := append(append([]byte(nil), dwr...), answer...)

	fake := &fakeTransport{recvFn: scriptedReceive(t, coalesced)}
	s := &session{tr: fake}

	_, msg1, err := s.nextMessage(time.Second)
	require.NoError(t, err)
	assert.Equal(t, CmdDeviceWatchdog, msg1.CommandCode)

	_, msg2, err := s.nextMessage(time.Second)
	require.NoError(t, err)
	assert.Equal(t, CmdUserAuthorization, msg2.CommandCode)
	assert.Equal(t, uint32(42), msg2.HopByHopID)

	assert.Equal(t, 1, fake.receiveCallCount(), "both messages must come from a single Receive call")
}

func TestSession_NextMessage_ReassemblesSplitMessage(t *testing.T) {
	full := buildTestAnswer(t)
	require.Greater(t, len(full), 10, "test message must be splittable")
	first, second := full[:10], full[10:]

	fake := &fakeTransport{recvFn: scriptedReceive(t, first, second)}
	s := &session{tr: fake}

	raw, msg, err := s.nextMessage(time.Second)
	require.NoError(t, err)
	assert.Equal(t, CmdUserAuthorization, msg.CommandCode)
	assert.Equal(t, full, raw)
	assert.Equal(t, 2, fake.receiveCallCount())
}

func TestSession_NextMessage_PropagatesTimeout(t *testing.T) {
	partial := buildTestAnswer(t)[:5] // incomplete: not even a full header yet
	s := &session{
		tr: &fakeTransport{recvFn: func(int, time.Duration) ([]byte, error) {
			return nil, os.ErrDeadlineExceeded
		}},
		buf: append([]byte(nil), partial...),
	}

	_, _, err := s.nextMessage(50 * time.Millisecond)
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	assert.Equal(t, partial, s.buf, "buffered bytes must survive a timeout for the next call")
}

func TestSession_NextMessage_RejectsGarbage(t *testing.T) {
	t.Run("bad version byte", func(t *testing.T) {
		raw := append([]byte(nil), buildTestAnswer(t)...)
		raw[0] = 0x99 // not Version (1)

		s := &session{tr: &fakeTransport{recvFn: scriptedReceive(t, raw)}}
		_, _, err := s.nextMessage(time.Second)
		assert.Error(t, err)
	})

	t.Run("declared length below header", func(t *testing.T) {
		raw := append([]byte(nil), buildTestAnswer(t)...)
		raw[1], raw[2], raw[3] = 0, 0, 10 // length 10 < HeaderLength (20)

		s := &session{tr: &fakeTransport{recvFn: scriptedReceive(t, raw)}}
		_, _, err := s.nextMessage(time.Second)
		assert.Error(t, err)
	})
}

func TestSession_NextMessage_RejectsOversizedLength(t *testing.T) {
	raw := append([]byte(nil), buildTestAnswer(t)...)
	bogusLen := maxSessionMessageSize + 1
	raw[1] = byte(bogusLen >> 16)
	raw[2] = byte(bogusLen >> 8)
	raw[3] = byte(bogusLen)

	calls := 0
	s := &session{tr: &fakeTransport{recvFn: func(int, time.Duration) ([]byte, error) {
		calls++
		return raw, nil
	}}}

	_, _, err := s.nextMessage(time.Second)
	require.Error(t, err)
	assert.Equal(t, 1, calls, "an oversized length must fail on the read that revealed it, not loop waiting for more bytes")
}

// --- receiveAnswer ---

func TestReceiveAnswer_DiscardsMismatchedHopByHop(t *testing.T) {
	const wantHop uint32 = 555
	const wrongHop uint32 = 999
	require.NotEqual(t, wantHop, wrongHop)

	wrongAnswer := withHopByHop(buildTestAnswer(t), wrongHop, 1)
	rightAnswer := withHopByHop(buildTestAnswer(t), wantHop, 2)

	fake := &fakeTransport{recvFn: scriptedReceive(t, wrongAnswer, rightAnswer)}
	s := &session{tr: fake}

	msg, raw, err := s.receiveAnswer(wantHop, time.Now().Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, wantHop, msg.HopByHopID)
	assert.Equal(t, rightAnswer, raw)
}

func TestReceiveAnswer_AnswersInboundDWR(t *testing.T) {
	const wantHop uint32 = 77
	dwr := buildTestDWR(t)
	dwrMsg, err := ParseMessage(dwr)
	require.NoError(t, err)

	answer := withHopByHop(buildTestAnswer(t), wantHop, 78)

	fake := &fakeTransport{recvFn: scriptedReceive(t, dwr, answer)}
	s := &session{tr: fake, target: "hss:3868", host: "client.ims.local", realm: "ims.local"}

	msg, _, err := s.receiveAnswer(wantHop, time.Now().Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, wantHop, msg.HopByHopID)

	sent := fake.sentMessages()
	require.Len(t, sent, 1, "exactly one DWA must have been sent")

	dwa, err := ParseMessage(sent[0])
	require.NoError(t, err)
	assert.Equal(t, CmdDeviceWatchdog, dwa.CommandCode)
	assert.False(t, dwa.IsRequest(), "DWA must have the R-bit clear")
	assert.Zero(t, dwa.Flags&FlagProxiable, "DWA must have the P-bit clear")
	assert.Equal(t, dwrMsg.HopByHopID, dwa.HopByHopID, "DWA must echo the DWR's Hop-by-Hop ID")
	assert.Equal(t, dwrMsg.EndToEndID, dwa.EndToEndID, "DWA must echo the DWR's End-to-End ID")
	assert.Equal(t, ResultSuccess, dwa.ResultCode())
	assert.Equal(t, "client.ims.local", findStringAVP(dwa, AVPCodeOriginHost))
	assert.Equal(t, "ims.local", findStringAVP(dwa, AVPCodeOriginRealm))
}

func TestReceiveAnswer_DeadlineDoesNotResetPerMessage(t *testing.T) {
	dwr := buildTestDWR(t)
	fake := &fakeTransport{recvFn: func(int, time.Duration) ([]byte, error) {
		return dwr, nil // a fresh DWR forever, the answer never comes
	}}
	s := &session{tr: fake, target: "hss:3868", host: "client.ims.local", realm: "ims.local"}

	const budget = 50 * time.Millisecond
	start := time.Now()
	_, _, err := s.receiveAnswer(1, start.Add(budget))
	elapsed := time.Since(start)

	require.ErrorIs(t, err, errAnswerTimeout)
	assert.Less(t, elapsed, 3*budget, "a chatty peer must not extend the wait past ~the original budget")
}

func TestReceiveAnswer_TimeoutErrorIsDistinct(t *testing.T) {
	const hopByHop uint32 = 4242
	fake := &fakeTransport{recvFn: func(int, time.Duration) ([]byte, error) {
		t.Fatal("receiveAnswer must not call Receive once the deadline has already passed")
		return nil, nil
	}}
	s := &session{tr: fake}

	_, _, err := s.receiveAnswer(hopByHop, time.Now().Add(-time.Millisecond))
	require.Error(t, err)
	require.ErrorIs(t, err, errAnswerTimeout)
	assert.Contains(t, err.Error(), "4242")

	transportErr := errors.New("boom: connection reset")
	assert.NotErrorIs(t, transportErr, errAnswerTimeout, "a transport error must remain distinguishable from the budget timeout")
}

func TestReceiveAnswer_PropagatesTransportError(t *testing.T) {
	transportErr := errors.New("diameter session test: simulated transport failure")
	fake := &fakeTransport{recvFn: func(int, time.Duration) ([]byte, error) {
		return nil, transportErr
	}}
	s := &session{tr: fake}

	_, _, err := s.receiveAnswer(1, time.Now().Add(time.Second))
	require.ErrorIs(t, err, transportErr)
	assert.NotErrorIs(t, err, errAnswerTimeout, "a real transport error must not be reported as a budget timeout")
}

// --- receiveAnswers (plural, set-correlated) ---

// buildAnswerForRequest builds a real request via BuildRequest to obtain a
// genuine Hop-by-Hop ID (never a hand-typed one that was never actually
// sent), then builds the matching answer and stamps it with that request's
// identifiers via copyAnswerIdentifiers, mirroring how a real exchange
// correlates.
func buildAnswerForRequest(t *testing.T) (hopByHop uint32, answer []byte) {
	t.Helper()
	reqRaw, hop, err := BuildRequest(CmdUserAuthorization, AppIDCx, []AVP{
		NewStringAVP(AVPCodeOriginHost, "client.ims.local"),
	})
	require.NoError(t, err)
	reqMsg, err := ParseMessage(reqRaw)
	require.NoError(t, err)

	ans, err := BuildAnswer(CmdUserAuthorization, AppIDCx, []AVP{
		NewUint32AVP(AVPCodeResultCode, ResultSuccess),
	})
	require.NoError(t, err)
	return hop, copyAnswerIdentifiers(ans, reqMsg)
}

func TestSession_ReceiveAnswers_CollectsOutOfOrderArrivals(t *testing.T) {
	idA, answerA := buildAnswerForRequest(t)
	idB, answerB := buildAnswerForRequest(t)

	fake := &fakeTransport{recvFn: scriptedReceive(t, answerB, answerA)}
	s := &session{tr: fake}

	msgs, raws, err := s.receiveAnswers([]uint32{idA, idB}, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, idA, msgs[idA].HopByHopID)
	assert.Equal(t, idB, msgs[idB].HopByHopID)
	assert.Equal(t, answerA, raws[idA])
	assert.Equal(t, answerB, raws[idB])
}

func TestSession_ReceiveAnswers_CoalescedInOneReceive(t *testing.T) {
	idA, answerA := buildAnswerForRequest(t)
	idB, answerB := buildAnswerForRequest(t)
	coalesced := append(append([]byte(nil), answerA...), answerB...)

	fake := &fakeTransport{recvFn: scriptedReceive(t, coalesced)}
	s := &session{tr: fake}

	msgs, _, err := s.receiveAnswers([]uint32{idA, idB}, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, 1, fake.receiveCallCount(), "both messages must come from a single Receive call")
}

func TestSession_ReceiveAnswers_AnswersDWRWhileWaiting(t *testing.T) {
	idA, answerA := buildAnswerForRequest(t)
	idB, answerB := buildAnswerForRequest(t)
	dwr := buildTestDWR(t)
	dwrMsg, err := ParseMessage(dwr)
	require.NoError(t, err)

	fake := &fakeTransport{recvFn: scriptedReceive(t, dwr, answerB, answerA)}
	s := &session{tr: fake, target: "hss:3868", host: "client.ims.local", realm: "ims.local"}

	msgs, _, err := s.receiveAnswers([]uint32{idA, idB}, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, msgs, 2)

	sent := fake.sentMessages()
	require.Len(t, sent, 1, "exactly one DWA must have been sent")
	dwa, err := ParseMessage(sent[0])
	require.NoError(t, err)
	assert.Equal(t, dwrMsg.HopByHopID, dwa.HopByHopID, "DWA must echo the DWR's Hop-by-Hop ID")
	assert.Equal(t, dwrMsg.EndToEndID, dwa.EndToEndID, "DWA must echo the DWR's End-to-End ID")
}

func TestSession_ReceiveAnswers_DiscardsUnawaitedID(t *testing.T) {
	idA, answerA := buildAnswerForRequest(t)
	idB, answerB := buildAnswerForRequest(t)
	_, strayAnswer := buildAnswerForRequest(t)

	fake := &fakeTransport{recvFn: scriptedReceive(t, strayAnswer, answerA, answerB)}
	s := &session{tr: fake}

	msgs, _, err := s.receiveAnswers([]uint32{idA, idB}, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, idA, msgs[idA].HopByHopID)
	assert.Equal(t, idB, msgs[idB].HopByHopID)
}

func TestSession_ReceiveAnswers_PartialTimeoutReportsWhatArrived(t *testing.T) {
	idA, _ := buildAnswerForRequest(t)
	idB, answerB := buildAnswerForRequest(t)
	_, strayAnswer := buildAnswerForRequest(t) // keeps the loop busy without matching either awaited ID

	fake := &fakeTransport{recvFn: func(idx int, _ time.Duration) ([]byte, error) {
		if idx == 0 {
			return answerB, nil
		}
		return strayAnswer, nil
	}}
	s := &session{tr: fake}

	msgs, _, err := s.receiveAnswers([]uint32{idA, idB}, time.Now().Add(80*time.Millisecond))
	require.Error(t, err)
	assert.True(t, errors.Is(err, errAnswerTimeout))
	assert.Contains(t, err.Error(), "1 of 2")
	require.Len(t, msgs, 1)
	assert.Equal(t, idB, msgs[idB].HopByHopID)
}

func TestSession_ReceiveAnswers_TransportErrorAborts(t *testing.T) {
	idA, answerA := buildAnswerForRequest(t)
	idB, _ := buildAnswerForRequest(t)
	transportErr := errors.New("diameter session test: simulated transport failure")

	fake := &fakeTransport{recvFn: func(idx int, _ time.Duration) ([]byte, error) {
		if idx == 0 {
			return answerA, nil
		}
		return nil, transportErr
	}}
	s := &session{tr: fake}

	msgs, _, err := s.receiveAnswers([]uint32{idA, idB}, time.Now().Add(time.Second))
	require.ErrorIs(t, err, transportErr)
	require.Len(t, msgs, 1)
	assert.Equal(t, idA, msgs[idA].HopByHopID)
}

func TestSession_ReceiveAnswers_ChattyDWRCannotExtendDeadline(t *testing.T) {
	dwr := buildTestDWR(t)
	fake := &fakeTransport{recvFn: func(int, time.Duration) ([]byte, error) {
		return dwr, nil // a fresh DWR forever, the answers never come
	}}
	s := &session{tr: fake, target: "hss:3868", host: "client.ims.local", realm: "ims.local"}

	const budget = 200 * time.Millisecond
	start := time.Now()
	_, _, err := s.receiveAnswers([]uint32{1, 2}, start.Add(budget))
	elapsed := time.Since(start)

	require.ErrorIs(t, err, errAnswerTimeout)
	assert.Less(t, elapsed, budget+250*time.Millisecond, "a chatty peer must not extend the wait past ~the original budget")
}

func TestSession_ReceiveAnswers_SingleIDMatchesReceiveAnswer(t *testing.T) {
	idA, answerA := buildAnswerForRequest(t)

	fakePlural := &fakeTransport{recvFn: scriptedReceive(t, answerA)}
	sPlural := &session{tr: fakePlural}
	msgs, raws, err := sPlural.receiveAnswers([]uint32{idA}, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	fakeSingle := &fakeTransport{recvFn: scriptedReceive(t, answerA)}
	sSingle := &session{tr: fakeSingle}
	singleMsg, singleRaw, err := sSingle.receiveAnswer(idA, time.Now().Add(time.Second))
	require.NoError(t, err)

	assert.Equal(t, singleMsg, msgs[idA])
	assert.Equal(t, singleRaw, raws[idA])
}

func TestSession_ReceiveAnswers_DuplicateIDInRequestSetTerminates(t *testing.T) {
	idA, answerA := buildAnswerForRequest(t)
	fake := &fakeTransport{recvFn: scriptedReceive(t, answerA)}
	s := &session{tr: fake}

	msgs, _, err := s.receiveAnswers([]uint32{idA, idA}, time.Now().Add(time.Second))
	require.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, 1, fake.receiveCallCount(), "a duplicate ID must not cause a second Receive to be awaited")
}

// --- coalesced-fast-path impairment leak ---

// wrapImpairedSession builds a session over impair.Wrap(fake), returning the
// session and the common.ImpairmentControl handle a test arms/disarms
// exactly as adapter.go's impair.Clear/impair.Configure do.
func wrapImpairedSession(t *testing.T, fake *fakeTransport) (*session, common.ImpairmentControl) {
	t.Helper()
	wrapped := impair.Wrap(fake)
	ic, ok := wrapped.(common.ImpairmentControl)
	require.True(t, ok, "impair.Wrap must return a value satisfying common.ImpairmentControl")
	return &session{tr: wrapped}, ic
}

// TestSession_ImpairmentDoesNotLeakThroughCoalescedFastPath reproduces,
// deterministically, the one failure mode a plausible partial implementation
// would miss: an armed impairment profile surviving through
// session.nextMessage's buffered fast path, which returns a message WITHOUT
// ever calling Receive. Nothing inside the transport can tell that a
// scenario step ended; only the adapter's next SetImpairment call can — see
// adapter.go's impair.Clear (before the handshake) and impair.Configure
// (before every Send), which is exactly the mandatory per-step call this
// test proves is load-bearing.
func TestSession_ImpairmentDoesNotLeakThroughCoalescedFastPath(t *testing.T) {
	t.Run("mandatory SetImpairment(nil) disarms the leaked profile", func(t *testing.T) {
		msgA := withHopByHop(buildTestAnswer(t), 1, 1)
		msgB := withHopByHop(buildTestAnswer(t), 2, 2)
		msgC := withHopByHop(buildTestAnswer(t), 3, 3)
		coalesced := append(append([]byte(nil), msgA...), msgB...)

		fake := &fakeTransport{recvFn: func(idx int, _ time.Duration) ([]byte, error) {
			if idx == 0 {
				return coalesced, nil
			}
			return msgC, nil
		}}
		s, ic := wrapImpairedSession(t, fake)

		// 1. Prime: a single Receive returns two coalesced messages.
		_, msg1, err := s.nextMessage(time.Second)
		require.NoError(t, err)
		assert.Equal(t, uint32(1), msg1.HopByHopID)
		require.Equal(t, 1, fake.receiveCallCount())

		// 2. Arm: a receive-side profile, exactly like a scenario step's
		// impair: block would.
		require.NoError(t, ic.SetImpairment(map[string]string{"latency_ms": "300"}))

		// 3. The leak: msgB is already buffered, so nextMessage returns it
		// through the fast path with NO Receive call — the arming is never
		// consumed. This IS the leak, and it is expected at this point.
		start := time.Now()
		_, msg2, err := s.nextMessage(time.Second)
		fastPathElapsed := time.Since(start)
		require.NoError(t, err)
		assert.Equal(t, uint32(2), msg2.HopByHopID)
		assert.Less(t, fastPathElapsed, 50*time.Millisecond, "the buffered fast path must not itself apply any delay")
		assert.Equal(t, 1, fake.receiveCallCount(), "the fast path must not have called Receive")

		// 4. The mandatory per-step call.
		require.NoError(t, ic.SetImpairment(nil))

		// 5. A real Receive now happens, undelayed — the leaked arming is gone.
		start = time.Now()
		_, msg3, err := s.nextMessage(time.Second)
		elapsed := time.Since(start)
		require.NoError(t, err)
		assert.Equal(t, uint32(3), msg3.HopByHopID)
		assert.Equal(t, 2, fake.receiveCallCount())
		assert.Less(t, elapsed, 50*time.Millisecond, "SetImpairment(nil) must have disarmed the leaked profile")
	})

	// Negative control: without this row, the test above would pass even if
	// the leak were never closed at all, since both the leaked and the
	// correctly-disarmed case return quickly through a fast path that never
	// itself sleeps. Skipping the mandatory SetImpairment(nil) call must
	// leave the SAME arming in place for the next real Receive, proving the
	// arming genuinely survives the fast path rather than being silently
	// consumed or reset by nextMessage itself.
	t.Run("negative control: skipping SetImpairment(nil) leaves the next Receive delayed", func(t *testing.T) {
		msgA := withHopByHop(buildTestAnswer(t), 4, 4)
		msgB := withHopByHop(buildTestAnswer(t), 5, 5)
		msgC := withHopByHop(buildTestAnswer(t), 6, 6)
		coalesced := append(append([]byte(nil), msgA...), msgB...)

		fake := &fakeTransport{recvFn: func(idx int, _ time.Duration) ([]byte, error) {
			if idx == 0 {
				return coalesced, nil
			}
			return msgC, nil
		}}
		s, ic := wrapImpairedSession(t, fake)

		_, _, err := s.nextMessage(time.Second)
		require.NoError(t, err)

		require.NoError(t, ic.SetImpairment(map[string]string{"latency_ms": "300"}))

		_, _, err = s.nextMessage(time.Second) // fast path, still armed
		require.NoError(t, err)

		// The mandatory SetImpairment(nil) call is deliberately skipped here.

		start := time.Now()
		_, msg3, err := s.nextMessage(time.Second)
		elapsed := time.Since(start)
		require.NoError(t, err)
		assert.Equal(t, uint32(6), msg3.HopByHopID)
		assert.GreaterOrEqual(t, elapsed, 290*time.Millisecond,
			"without the mandatory disarm, the leaked profile must still delay the next real Receive")
	})
}
