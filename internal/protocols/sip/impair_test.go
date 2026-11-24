package sip_test

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMockAndAdapter starts a UDP mock UAS and returns it together with a SIP
// adapter built over impair.Wrap of a real UDP transport. Every test in this
// file drives the adapter through this real decorator, not a stub, and
// passes impairment keys through config exactly as
// internal/engine/executor.go's foldImpairConfig does.
func newMockAndAdapter(t *testing.T, timeout time.Duration) (*sip.MockUASServer, *sip.Adapter) {
	t.Helper()
	mock := sip.NewMockUAS(t)
	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	adapter := sip.NewAdapterWithTransport(impair.Wrap(tr), "udp", timeout)
	return mock, adapter
}

// executeStep resolves the shared "stand up mock, run one step" sequence
// every test in this file needs, so the timing tests do not each repeat the
// field map and Execute call. Returns the wall-clock time Execute took and
// the StepResult it produced.
func executeStep(t *testing.T, adapter *sip.Adapter, mockAddr, method string, config map[string]string) (time.Duration, *common.StepResult) {
	t.Helper()
	fields := map[string]string{"To": "sip:alice@example.test", "From": "sip:cellrift@example.test"}

	start := time.Now()
	result, err := adapter.Execute("send", mockAddr, method, fields, config)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.NotNil(t, result)
	return elapsed, result
}

// TestAdapter_Impairment_Latency proves, for SIP:
// latency_ms measurably delays a step's Send, and the delay is observable
// both from outside (elapsed wall time, and the mock's own recorded arrival
// time) and from the step's own reported response.impair_delay_ms.
func TestAdapter_Impairment_Latency(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	execStart := time.Now()
	elapsed, result := executeStep(t, adapter, mock.Addr(), "REGISTER", map[string]string{"latency_ms": "200"})
	assert.GreaterOrEqual(t, elapsed, 190*time.Millisecond, "latency_ms:200 must measurably delay the step")

	arrivedAfter := mock.FirstRequestAt().Sub(execStart)
	assert.GreaterOrEqual(t, arrivedAfter, 190*time.Millisecond,
		"the request must not reach the mock before the send-side latency elapses")

	delay, ok := result.Response[impair.ImpairDelayKey]
	require.True(t, ok, "an impaired step must report impair_delay_ms")
	assert.GreaterOrEqual(t, delay.(int64), int64(200),
		"impair_delay_ms must cover at least the send-side draw")

	elapsedUnimpaired, _ := executeStep(t, adapter, mock.Addr(), "REGISTER", nil)
	assert.Less(t, elapsedUnimpaired, 100*time.Millisecond,
		"a step with no impairment config must not inherit the previous step's latency")
}

// TestAdapter_Impairment_JitterWithinBounds proves, for
// SIP: jitter stays within the declared bounds across repeated runs and
// varies rather than collapsing to a constant.
//
// impair.Transport arms Send and Receive independently (one draw apiece —
// see impair.Transport.SetImpairment's doc comment), so a single step's
// total self-inflicted delay is the sum of two independent draws in
// [latency_ms, latency_ms+jitter_ms], not one draw. Bounds below account for
// both draws, with 90% headroom on the low end and 80ms on the high end per
// this file's tolerance rule.
func TestAdapter_Impairment_JitterWithinBounds(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	const (
		n       = 10
		minDraw = 50 * time.Millisecond
		maxDraw = 150 * time.Millisecond
	)
	lowerBound := 2 * minDraw * 9 / 10
	upperBound := 2*maxDraw + 80*time.Millisecond

	config := map[string]string{"latency_ms": "50", "jitter_ms": "100"}
	delays := make(map[int64]struct{}, n)
	for i := 0; i < n; i++ {
		elapsed, result := executeStep(t, adapter, mock.Addr(), "REGISTER", config)
		assert.GreaterOrEqual(t, elapsed, lowerBound)
		assert.LessOrEqual(t, elapsed, upperBound)

		delay, ok := result.Response[impair.ImpairDelayKey]
		require.True(t, ok)
		delays[delay.(int64)] = struct{}{}
	}

	assert.Greater(t, len(delays), 1, "jitter must vary the recorded delay across repeated runs")
}

// TestAdapter_Impairment_ProvisionalsPayJitterOnce proves the
// other half of the jitter claim: the receive-side draw is charged once for the message a
// step is waiting for, not once per Receive call inside
// waitForFinalResponse's provisional loop. INVITE is the one method
// mockResponses answers with a 180 Ringing ahead of the 200 OK, so no fourth
// mock type or mock change is needed to exercise the loop.
func TestAdapter_Impairment_ProvisionalsPayJitterOnce(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	_, result := executeStep(t, adapter, mock.Addr(), "INVITE", map[string]string{"latency_ms": "100"})

	delay, ok := result.Response[impair.ImpairDelayKey]
	require.True(t, ok)
	got := delay.(int64)

	// One send-side draw (100ms) plus one receive-side draw (100ms) charged
	// once for the whole exchange is ~200ms. A bug that charged the
	// receive-side draw per Receive call (once for 180 Ringing, once for
	// 200 OK) would land near 300ms instead.
	assert.InDelta(t, 200, got, 40,
		"expected one send-side and one receive-side draw, not one per provisional response")
}

// TestAdapter_Impairment_PostReceiveDelay proves, for
// SIP: delay_ms lands strictly after the final response is received and
// parsed, distinguishable from send-side latency by measuring when the
// request actually reached the mock.
func TestAdapter_Impairment_PostReceiveDelay(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	execStart := time.Now()
	elapsed, result := executeStep(t, adapter, mock.Addr(), "REGISTER", map[string]string{"delay_ms": "300"})

	arrivedAfter := mock.FirstRequestAt().Sub(execStart)
	assert.Less(t, arrivedAfter, 50*time.Millisecond,
		"delay_ms must not hold back the send — the request should reach the mock almost immediately")

	assert.GreaterOrEqual(t, elapsed, 290*time.Millisecond,
		"Execute must not return until the post-receive delay has elapsed")

	delay, ok := result.Response[impair.ImpairDelayKey]
	require.True(t, ok)
	assert.GreaterOrEqual(t, delay.(int64), int64(300))
}

// setImpairSpy sits between the adapter and the real impairment transport
// and records every SetImpairment call the adapter makes. It exists to
// assert on the calling discipline itself, not on timing: the adapter must
// call SetImpairment on every send, including sends that declare no
// impairment, because that call is the only thing that clears a profile
// armed by an earlier step.
type setImpairSpy struct {
	common.Transport // the wrapped impairment transport; Send/Receive/Close/LocalAddr pass through
	mu               sync.Mutex
	calls            []map[string]string
}

func (s *setImpairSpy) SetImpairment(cfg map[string]string) error {
	s.mu.Lock()
	copied := make(map[string]string, len(cfg))
	for k, v := range cfg {
		copied[k] = v
	}
	s.calls = append(s.calls, copied)
	s.mu.Unlock()
	return s.Transport.(common.ImpairmentControl).SetImpairment(cfg)
}

func (s *setImpairSpy) recordedCalls() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]string{}, s.calls...)
}

// TestAdapter_Impairment_SetImpairmentCalledOnEveryStep asserts the
// always-call-SetImpairment invariant directly, via a spy, rather than
// implying it from timing behaviour alone.
func TestAdapter_Impairment_SetImpairmentCalledOnEveryStep(t *testing.T) {
	mock := sip.NewMockUAS(t)
	defer mock.Close()

	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	spy := &setImpairSpy{Transport: impair.Wrap(tr)}
	adapter := sip.NewAdapterWithTransport(spy, "udp", 2*time.Second)
	defer adapter.Close()

	executeStep(t, adapter, mock.Addr(), "REGISTER", map[string]string{"latency_ms": "20"})
	executeStep(t, adapter, mock.Addr(), "REGISTER", nil)
	executeStep(t, adapter, mock.Addr(), "REGISTER", map[string]string{"latency_ms": "20"})

	calls := spy.recordedCalls()
	require.Len(t, calls, 3, "SetImpairment must be called once per send, impaired or not")

	unimpaired := calls[1]
	for _, key := range []string{"latency_ms", "jitter_ms", "delay_ms"} {
		_, present := unimpaired[key]
		assert.False(t, present, "the unimpaired step's config must carry none of the impairment keys")
	}
}

// TestAdapter_Impairment_DoesNotCarryOverToNextStep proves that impairment
// configured on one step never bleeds into the next: the invariant
// SetImpairmentCalledOnEveryStep exists to protect.
func TestAdapter_Impairment_DoesNotCarryOverToNextStep(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	elapsed1, result1 := executeStep(t, adapter, mock.Addr(), "REGISTER", map[string]string{"latency_ms": "300"})
	assert.GreaterOrEqual(t, elapsed1, 290*time.Millisecond)
	_, ok1 := result1.Response[impair.ImpairDelayKey]
	require.True(t, ok1)

	elapsed2, result2 := executeStep(t, adapter, mock.Addr(), "REGISTER", nil)
	assert.Less(t, elapsed2, 100*time.Millisecond,
		"the second, unimpaired step must not inherit the first step's latency")

	// Key ABSENCE, not a zero value: a step that was never impaired must not
	// even have the key, so a scenario author's `response.impair_delay_ms`
	// capture fails loudly rather than silently reading a stale 0.
	_, ok2 := result2.Response["impair_delay_ms"]
	assert.False(t, ok2, "an unimpaired step's response must carry no impair_delay_ms key at all")
}

// TestAdapter_Impairment_RecordsDelayOnFailurePath proves a red step still
// explains how much of its duration Cellrift inflicted on itself: the
// convention (a self-inflicted transport fault is a StepResult, not
// a Go error) holds even when that step was also impaired.
func TestAdapter_Impairment_RecordsDelayOnFailurePath(t *testing.T) {
	mock := sip.NewTCPMockUAS(t, sip.TCPFaultSilence)
	defer mock.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	adapter := sip.NewAdapterWithTransport(impair.Wrap(tr), "tcp", 400*time.Millisecond)
	defer adapter.Close()

	_, result := executeStep(t, adapter, mock.Addr(), "REGISTER", map[string]string{"latency_ms": "150"})

	assert.Equal(t, common.StatusFailed, result.Status)
	delay, ok := result.Response[impair.ImpairDelayKey]
	require.True(t, ok, "a failed impaired step must still report impair_delay_ms")
	assert.GreaterOrEqual(t, delay.(int64), int64(150))
}

// TestAdapter_Drop_NeverReachesWireAndReportsPassed proves, for
// SIP: a dropped step never writes to the wire, reports passed with
// response.dropped, and returns promptly rather than burning the full
// adapter timeout.
func TestAdapter_Drop_NeverReachesWireAndReportsPassed(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	elapsed, result := executeStep(t, adapter, mock.Addr(), "REGISTER", map[string]string{"drop": "true"})

	assert.Equal(t, common.StatusPassed, result.Status)
	dropped, ok := result.Response[impair.DroppedKey]
	require.True(t, ok, "a dropped step must report response.dropped")
	assert.Equal(t, true, dropped)
	assert.Less(t, elapsed, 500*time.Millisecond,
		"a drop implemented as send-nothing-then-wait would still report passed after burning the full timeout — only a time bound catches that")

	// The mock records on its own goroutine; sleeping before the negative
	// assertion is what makes "empty" meaningful rather than merely fast.
	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, mock.ReceivedRequests(), "a dropped step must never reach the wire")
}

// TestAdapter_Drop_RunContinues proves a dropped step does not poison the
// adapter or leave anything armed for the following step.
func TestAdapter_Drop_RunContinues(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	_, dropResult := executeStep(t, adapter, mock.Addr(), "REGISTER", map[string]string{"drop": "true"})
	require.Equal(t, common.StatusPassed, dropResult.Status)

	_, result := executeStep(t, adapter, mock.Addr(), "REGISTER", nil)
	require.Equal(t, common.StatusPassed, result.Status)
	assert.Equal(t, 200, result.Response["status_code"])
	_, hasDropped := result.Response[impair.DroppedKey]
	assert.False(t, hasDropped, "the ordinary step's result must carry no dropped key")

	assert.Len(t, mock.ReceivedRequests(), 1, "only the ordinary step's request should ever have reached the mock")
}

// TestAdapter_Drop_DoesNotMaskABuildFailure proves drop never short-circuits
// ahead of a genuine build error: the step still fails on its own terms.
func TestAdapter_Drop_DoesNotMaskABuildFailure(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	fields := map[string]string{
		"To":              "sip:alice@example.test",
		"From":            "sip:cellrift@example.test",
		"__body_template": "does-not-exist",
	}
	result, err := adapter.Execute("send", mock.Addr(), "REGISTER", fields, map[string]string{"drop": "true"})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Contains(t, result.Error, "body template")
	assert.Empty(t, mock.ReceivedRequests(), "a build failure must never reach the wire, dropped or not")
}

// TestAdapter_Duplicate_PeerObservesTwoIdenticalArrivals proves, for
// SIP: a duplicated step writes byte-identical bytes to the wire twice,
// sharing one Call-ID and CSeq, and reports response.duplicated.
func TestAdapter_Duplicate_PeerObservesTwoIdenticalArrivals(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	_, result := executeStep(t, adapter, mock.Addr(), "REGISTER", map[string]string{"duplicate": "true"})
	require.Equal(t, common.StatusPassed, result.Status)

	reqs := mock.ReceivedRequests()
	require.Len(t, reqs, 2, "the mock must observe exactly two arrivals for one duplicated step")
	assert.True(t, bytes.Equal(reqs[0].Raw, reqs[1].Raw), "the duplicate's two arrivals must be byte-identical")
	assert.Equal(t, reqs[0].CallID, reqs[1].CallID)
	assert.Equal(t, reqs[0].CSeq, reqs[1].CSeq)

	duplicated, ok := result.Response[impair.DuplicatedKey]
	require.True(t, ok, "a duplicated step must report response.duplicated")
	assert.Equal(t, true, duplicated)
}

// TestAdapter_Duplicate_OnlyFirstSendPaysLatency proves the second, duplicate
// Send does not draw a fresh delay from the transport: impair.Configure arms
// one send-side and one receive-side one-shot delay per step, and the first
// Send already consumes the send-side arming.
//
// impair.Transport.SetImpairment arms BOTH the send-side and receive-side
// one-shot flags from one call (internal/protocols/common/impair/transport.go),
// so even an ORDINARY step already pays roughly 2x the declared latency (one
// draw on Send, one draw on the correlated Receive) — the same behaviour this
// package's own TestAdapter_Impairment_ProvisionalsPayJitterOnce documents
// for latency_ms:100 (~200ms expected, not 100ms). For latency_ms:300 that
// baseline is ~600ms; the upper bound below is widened past that baseline
// with headroom, so what this test actually proves is that duplication does
// NOT add a THIRD draw (which would push the total toward ~900ms), not that
// duplication is free.
func TestAdapter_Duplicate_OnlyFirstSendPaysLatency(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	_, result := executeStep(t, adapter, mock.Addr(), "REGISTER", map[string]string{"duplicate": "true", "latency_ms": "300"})
	require.Equal(t, common.StatusPassed, result.Status)

	reqs := mock.ReceivedRequests()
	require.Len(t, reqs, 2)
	assert.Less(t, reqs[1].ReceivedAt.Sub(reqs[0].ReceivedAt), 150*time.Millisecond,
		"the second Send must not itself pay a fresh send-side draw")

	delay, ok := result.Response[impair.ImpairDelayKey]
	require.True(t, ok)
	got := delay.(int64)
	assert.GreaterOrEqual(t, got, int64(300))
	assert.Less(t, got, int64(850), "a third draw (send+send+receive) would push this well past the send+receive baseline")
}

// TestAdapter_Duplicate_EchoIsDrainedNotLeakedToNextStep is the
// correctness-critical duplicate test: it proves drainDuplicateEcho
// actually prevents the peer's stray second response from being misread by
// the NEXT step. Without the drain, the second step's waitForFinalResponse
// would consume the first step's leftover echo instead of its own response.
func TestAdapter_Duplicate_EchoIsDrainedNotLeakedToNextStep(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	fieldsFirst := map[string]string{"To": "sip:alice@example.test", "From": "sip:cellrift@example.test"}
	firstResult, err := adapter.Execute("send", mock.Addr(), "REGISTER", fieldsFirst, map[string]string{"duplicate": "true"})
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, firstResult.Status)

	fieldsSecond := map[string]string{"To": "sip:bob@example.test", "From": "sip:cellrift@example.test"}
	secondResult, err := adapter.Execute("send", mock.Addr(), "REGISTER", fieldsSecond, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, secondResult.Status)

	reqs := mock.ReceivedRequests()
	require.Len(t, reqs, 3, "duplicated step sends 2, ordinary step sends 1")

	headers, ok := secondResult.Response["header"].(map[string]interface{})
	require.True(t, ok, "the response must carry a header map")
	secondCallID, ok := headers[sip.HeaderCallID]
	require.True(t, ok, "the mock echoes Call-ID back in its response headers")
	assert.Equal(t, reqs[2].CallID, secondCallID,
		"the second step's response must reflect the SECOND step's own request, not the first step's drained echo")
	assert.NotEqual(t, reqs[0].CallID, secondCallID,
		"without the drain, the second step would read the first request's leftover echo")
}
