package diameter_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uarFields is the minimal field set every impairment test in this file
// sends as a UAR — reused from the existing MockHSS-driven UAR tests
// (adapter_test.go) so the answer path itself is already proven correct,
// independent of any impairment this file adds on top of it.
var uarFields = map[string]string{
	"Session-Id":      "cellrift;test;impair",
	"Origin-Host":     "cellrift.ims.local",
	"Origin-Realm":    "ims.local",
	"Public-Identity": "sip:447700900001@ims.local",
}

// newImpairTestAdapter stands up a lenient MockHSS and a Diameter adapter
// built over impair.Wrap of a real TCP transport, so every test in this file
// (and in session_test.go's coalesced-fast-path test) drives impairment
// through the real decorator, not a stub. Returns the mock, the adapter, and
// a cleanup func closing both — extracted here because the latency and
// delay tests both need the identical "stand up, wrap, build" sequence.
func newImpairTestAdapter(t *testing.T, timeout time.Duration) (*diameter.MockHSS, *diameter.Adapter, func()) {
	t.Helper()

	hss := diameter.NewMockHSS(t)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	adapter := diameter.NewAdapterWithTransport(impair.Wrap(tr), timeout)

	cleanup := func() {
		adapter.Close()
		hss.Close()
	}
	return hss, adapter, cleanup
}

// executeUAR sends one UAR through adapter with config, returning the wall
// time Execute took and the StepResult it produced.
func executeUAR(t *testing.T, adapter *diameter.Adapter, addr string, config map[string]string) (time.Duration, *common.StepResult) {
	t.Helper()

	start := time.Now()
	result, err := adapter.Execute("send", addr, "UAR", uarFields, config)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.NotNil(t, result)
	return elapsed, result
}

// TestAdapter_Impairment_Latency proves, for Diameter:
// latency_ms measurably delays a step's Send, observable both from outside
// (elapsed wall time) and from the step's own reported
// response.impair_delay_ms — and that an unimpaired step afterward does not
// inherit it.
func TestAdapter_Impairment_Latency(t *testing.T) {
	hss, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	elapsed, result := executeUAR(t, adapter, hss.Addr(), map[string]string{"latency_ms": "200"})
	assert.GreaterOrEqual(t, elapsed, 190*time.Millisecond, "latency_ms:200 must measurably delay the step")

	delay, ok := result.Response[impair.ImpairDelayKey]
	require.True(t, ok, "an impaired step must report impair_delay_ms")
	assert.GreaterOrEqual(t, delay.(int64), int64(200))

	elapsedUnimpaired, _ := executeUAR(t, adapter, hss.Addr(), nil)
	assert.Less(t, elapsedUnimpaired, 100*time.Millisecond,
		"a step with no impairment config must not inherit the previous step's latency")
}

// TestSession_JitterPaysOncePerSplitMessage proves, for
// Diameter, the invariant only Diameter can exercise: an answer
// deliberately split across three TCP writes pays the receive-side draw
// once for the whole message, not once per Receive call. session.nextMessage
// needs three Receive calls to reassemble a three-chunk answer; a bug that
// charged the receive-side draw per Receive call would push impair_delay_ms
// toward ~400ms (one send-side draw plus three receive-side draws) instead
// of the ~200ms one send-side plus one receive-side draw this test expects.
//
// SetAnswerChunks(3) is confirmed honoured via LastAnswerWriteCount, a
// deterministic counter, rather than inferred from a wall-clock lower bound
// — a timing-based confirmation would be one more flaky assertion layered
// on top of the timing assertion this test already makes about
// impair_delay_ms itself.
func TestSession_JitterPaysOncePerSplitMessage(t *testing.T) {
	hss, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()
	hss.SetAnswerChunks(3)

	_, result := executeUAR(t, adapter, hss.Addr(), map[string]string{"latency_ms": "100"})

	require.Equal(t, 3, hss.LastAnswerWriteCount(), "the answer must actually have been split into 3 writes")

	delay, ok := result.Response[impair.ImpairDelayKey]
	require.True(t, ok)
	got := delay.(int64)

	assert.GreaterOrEqual(t, got, int64(180), "expected roughly one send-side plus one receive-side draw")
	assert.Less(t, got, int64(350), "a per-Receive-call charge over 3 chunks would land near 300-400ms, not ~200ms")
}

// TestAdapter_Impairment_WrappedTransportStillHandshakes proves that
// impair.Wrap did not break diameter.Adapter.Connect's common.Connector type
// assertion (adapter.go:269): an adapter built over impair.Wrap(tcpTransport)
// must still complete the RFC 6733 §5.3 CER/CEA handshake and successfully
// send an application request.
func TestAdapter_Impairment_WrappedTransportStillHandshakes(t *testing.T) {
	hss, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	_, result := executeUAR(t, adapter, hss.Addr(), nil)
	assert.Equal(t, common.StatusPassed, result.Status)
	assert.Zero(t, hss.Rejected(), "the CER/CEA handshake must have succeeded over the wrapped transport")
}

// TestAdapter_Impairment_PostReceiveDelay proves, for
// Diameter: delay_ms is applied strictly after receiveAnswer returns a
// parsed, correlated answer — not before Send, and not inside the transport.
// hss.LastRequestAt bounds when the UAR itself actually arrived at the mock,
// which must be almost immediate; Execute itself must not return until the
// full 300ms has elapsed afterward.
func TestAdapter_Impairment_PostReceiveDelay(t *testing.T) {
	hss, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	execStart := time.Now()
	elapsed, result := executeUAR(t, adapter, hss.Addr(), map[string]string{"delay_ms": "300"})

	arrivedAfter := hss.LastRequestAt().Sub(execStart)
	assert.Less(t, arrivedAfter, 50*time.Millisecond,
		"delay_ms must not hold back the send — the request should reach the mock almost immediately")

	assert.GreaterOrEqual(t, elapsed, 290*time.Millisecond,
		"Execute must not return until the post-receive delay has elapsed")

	delay, ok := result.Response[impair.ImpairDelayKey]
	require.True(t, ok)
	assert.GreaterOrEqual(t, delay.(int64), int64(300))
}

// TestAdapter_Impairment_DoesNotCarryOverToNextStep proves that impairment
// configured on one step never bleeds into the next, on the SAME adapter and
// established session — the sequence a real two-step scenario produces, and
// exactly the case adapter.go's impair.Clear/impair.Configure calls on every
// send exist to guard against.
func TestAdapter_Impairment_DoesNotCarryOverToNextStep(t *testing.T) {
	hss, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	elapsed1, result1 := executeUAR(t, adapter, hss.Addr(), map[string]string{"latency_ms": "300"})
	assert.GreaterOrEqual(t, elapsed1, 290*time.Millisecond)
	_, ok1 := result1.Response[impair.ImpairDelayKey]
	require.True(t, ok1)

	elapsed2, result2 := executeUAR(t, adapter, hss.Addr(), nil)
	assert.Less(t, elapsed2, 120*time.Millisecond,
		"the second, unimpaired step must not inherit the first step's latency")

	// Key ABSENCE, not a zero value: a step that was never impaired must not
	// even have the key, so a scenario author's `response.impair_delay_ms`
	// capture fails loudly rather than silently reading a stale 0.
	_, ok := result2.Response["impair_delay_ms"]
	require.False(t, ok, "an unimpaired step must not report a self-inflicted delay")
}

// TestAdapter_Impairment_RecordsDelayOnFailurePath proves a red step still
// explains how much of its duration Cellrift inflicted on itself: the
// convention (a self-inflicted transport fault is a StepResult, not
// a Go error) holds even when that step was also impaired.
func TestAdapter_Impairment_RecordsDelayOnFailurePath(t *testing.T) {
	hss, adapter, cleanup := newImpairTestAdapter(t, 500*time.Millisecond)
	defer cleanup()
	hss.SetFault(diameter.HSSFaultSilence) // applies to the UAR only; CER/CEA still succeeds

	_, result := executeUAR(t, adapter, hss.Addr(), map[string]string{"latency_ms": "150"})

	assert.Equal(t, common.StatusFailed, result.Status)
	delay, ok := result.Response[impair.ImpairDelayKey]
	require.True(t, ok, "a failed impaired step must still report impair_delay_ms")
	assert.GreaterOrEqual(t, delay.(int64), int64(150))
}

// TestAdapter_Drop_NeverReachesWireAndReportsPassed proves drop's core
// contract: a dropped step's application request never reaches the peer, the
// step itself reports a result (not a hang), and that result is passed with
// response.dropped == true. Two things are asserted explicitly rather than
// implied: that the CER is still on the wire (a drop implemented too early,
// before ensureConnected, would also produce an empty RequestsOfCommand, so
// asserting only that would not catch it), and that the call returns well
// under the adapter's own timeout (without this, a drop implemented as "send
// nothing, then wait anyway" would still report passed, just late).
func TestAdapter_Drop_NeverReachesWireAndReportsPassed(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	elapsed, result := executeUAR(t, adapter, mock.Addr(), map[string]string{"drop": "true"})

	assert.Equal(t, common.StatusPassed, result.Status, "a dropped step must report passed: %s", result.Error)
	dropped, ok := result.Response[impair.DroppedKey]
	require.True(t, ok, "a dropped step must report response.dropped")
	assert.Equal(t, true, dropped)

	assert.Empty(t, mock.RequestsOfCommand(diameter.CmdUserAuthorization),
		"a dropped step's application request must never reach the peer")
	assert.Greater(t, len(mock.ReceivedRequests()), 0,
		"the CER handshake must still have gone out — only the application request is dropped")

	assert.Less(t, elapsed, 500*time.Millisecond,
		"a dropped step must return promptly, not burn its receive timeout")
}

// TestAdapter_Drop_RunContinues proves a dropped step does not poison the
// adapter or session for the step that follows it: an ordinary UAR on the
// SAME adapter after a dropped one must still reach the peer and pass.
func TestAdapter_Drop_RunContinues(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	_, dropped := executeUAR(t, adapter, mock.Addr(), map[string]string{"drop": "true"})
	require.Equal(t, common.StatusPassed, dropped.Status)

	_, result := executeUAR(t, adapter, mock.Addr(), nil)
	require.Equal(t, common.StatusPassed, result.Status, "the step after a dropped one must complete normally: %s", result.Error)
	_, hasDropped := result.Response[impair.DroppedKey]
	assert.False(t, hasDropped, "an ordinary step must not report dropped")

	assert.Len(t, mock.RequestsOfCommand(diameter.CmdUserAuthorization), 1,
		"only the second step's UAR should ever have reached the peer")
}

// TestAdapter_Drop_DoesNotMaskABuildFailure proves drop is applied AFTER
// prepareRequest, not instead of it: an unknown message type must still fail
// with its ordinary Go error, never silently swallowed by the drop branch.
func TestAdapter_Drop_DoesNotMaskABuildFailure(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	_, err := adapter.Execute("send", mock.Addr(), "NOPE", uarFields, map[string]string{"drop": "true"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown diameter message type",
		"drop must never mask a build failure behind a passed result")

	assert.Empty(t, mock.RequestsOfCommand(diameter.CmdUserAuthorization))
}

// TestAdapter_Duplicate_PeerObservesTwoIdenticalArrivals proves duplicate's
// core contract: a duplicated step's request lands on the wire twice,
// byte-identical, and the step itself still reports one ordinary passed
// result carrying response.duplicated == true.
func TestAdapter_Duplicate_PeerObservesTwoIdenticalArrivals(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	_, result := executeUAR(t, adapter, mock.Addr(), map[string]string{"duplicate": "true"})

	// A duplicated step completes one receive cycle, which orders the FIRST
	// arrival against this line but not the second: the answer to copy one
	// can arrive before copy two has been read. Wait for both.
	reqs := mock.WaitForRequestsOfCommand(t, diameter.CmdUserAuthorization, 2)
	require.Len(t, reqs, 2, "the peer must observe exactly two arrivals for one duplicated step")
	assert.True(t, bytes.Equal(reqs[0].Raw, reqs[1].Raw), "the two arrivals must be byte-identical")
	assert.Equal(t, reqs[0].HopByHopID, reqs[1].HopByHopID)

	assert.Equal(t, common.StatusPassed, result.Status, "a duplicated step should still succeed: %s", result.Error)
	duplicated, ok := result.Response[impair.DuplicatedKey]
	require.True(t, ok, "a duplicated step must report response.duplicated")
	assert.Equal(t, true, duplicated)
}

// TestAdapter_Duplicate_OnlyFirstSendPaysLatency pins the one-shot arming
// consequence: with duplicate plus latency_ms, only the first Send is
// delayed — the second, duplicate Send finds sendArmed already consumed and
// fires immediately. Per impair.Transport's design (transport.go), one
// SetImpairment arms BOTH the send-side and the receive-side one-shot flags,
// so even an ORDINARY (non-duplicated) step already pays roughly 2x the
// declared latency — one draw on Send, one draw on the correlated Receive —
// which is why the upper bound below is calibrated against that baseline,
// not against a single latency draw: it proves the duplicate's second Send
// did NOT additionally arm/consume a THIRD draw (which would push the total
// toward 3x), not that only one draw happened at all.
func TestAdapter_Duplicate_OnlyFirstSendPaysLatency(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	elapsed, result := executeUAR(t, adapter, mock.Addr(), map[string]string{"duplicate": "true", "latency_ms": "300"})

	reqs := mock.RequestsOfCommand(diameter.CmdUserAuthorization)
	require.Len(t, reqs, 2)
	gap := reqs[1].ReceivedAt.Sub(reqs[0].ReceivedAt)
	assert.Less(t, gap, 150*time.Millisecond, "the second copy must not be delayed again")

	assert.GreaterOrEqual(t, elapsed, 300*time.Millisecond, "the first Send must still have paid the declared latency")

	delay, ok := result.Response[impair.ImpairDelayKey]
	require.True(t, ok)
	got := delay.(int64)
	assert.GreaterOrEqual(t, got, int64(300))
	assert.Less(t, got, int64(850),
		"send-side plus receive-side draw from the one armed SetImpairment call is ~600ms; a third draw from the duplicate's second Send would push this toward ~900ms")
}

// TestAdapter_Duplicate_StaleAnswerDoesNotWedgeNextStep proves the peer's
// answer to the duplicate's second copy does not wedge the following step:
// an ordinary CCR sent right after a duplicated UAR, on the same session,
// must pass — the existing receiveAnswer discard (by already-consumed
// Hop-by-Hop ID) absorbs any stray second answer rather than letting it be
// misread as the CCR's own answer.
func TestAdapter_Duplicate_StaleAnswerDoesNotWedgeNextStep(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	_, dup := executeUAR(t, adapter, mock.Addr(), map[string]string{"duplicate": "true"})
	require.Equal(t, common.StatusPassed, dup.Status)

	ccrFields := map[string]string{
		"Session-Id":        "cellrift;test;impair-ccr",
		"Origin-Host":       "cellrift.ims.local",
		"Origin-Realm":      "ims.local",
		"CC-Request-Type":   "1",
		"CC-Request-Number": "0",
	}
	result, err := adapter.Execute("send", mock.Addr(), "CCR", ccrFields, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status,
		"a CCR right after a duplicated UAR must not be wedged by the duplicate's stale second answer: %s", result.Error)
}
