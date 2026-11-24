// conn_fault_test.go proves reset, truncate and abrupt_close against a real
// Diameter MockHSS over real TCP sockets, plus the reconnection regression: a step that follows a
// connection-faulted step must re-handshake and succeed, rather than fail
// on a connection the previous step already destroyed. sctp_abort's own
// proof is out of scope here — it needs the SCTP MockHSS and lives in
// sctp_abort_test.go.
package diameter_test

import (
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// faultStepTimeout is generous enough that a faulted step returning fast is
// unambiguous — a DurationMs well under this bound can only mean the receive
// was skipped, not that it happened to be quick.
const faultStepTimeout = 3 * time.Second

// newFaultAdapter builds a Diameter adapter over a REAL TCP transport
// wrapped exactly the way cmd/cellrift/main.go wraps it, pointed at a real
// in-process MockHSS. Nothing here fabricates a transport double: a
// connection fault that does not touch a real socket would prove nothing
// (CLAUDE.md's no-mocks rule for protocol adapters in integration tests).
func newFaultAdapter(t *testing.T) (*diameter.Adapter, *diameter.MockHSS) {
	t.Helper()
	m := diameter.NewMockHSS(t)
	t.Cleanup(m.Close)
	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	a := diameter.NewAdapterWithTransport(impair.Wrap(tr), faultStepTimeout)
	t.Cleanup(func() { _ = a.Close() })
	return a, m
}

// faultFields is the minimal UAR field set every fault case in this file
// sends, mirroring TestAdapter_SendUAR_MockHSS's own fixture (adapter_test.go)
// rather than inventing a second shape.
func faultFields() map[string]string {
	return map[string]string{
		"Session-Id":      "cellrift;test;fault",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}
}

// runConnFaultStep sends one UAR with impairKey declared against a fresh
// adapter/mock pair, returning the step's result and the mock so each test
// can assert its own connection_state and arrival-history shape.
func runConnFaultStep(t *testing.T, impairKey string) (*common.StepResult, *diameter.MockHSS) {
	t.Helper()
	a, m := newFaultAdapter(t)

	result, err := a.Execute("send", m.Addr(), "UAR", faultFields(), map[string]string{impairKey: "true"})
	require.NoError(t, err, "a faulted step reports its outcome via result, not a Go error")
	return result, m
}

// assertFaultReturnedFast pins "no receive was attempted": a faulted step
// must return in well under its own step timeout, not scrape through near
// the deadline the way a real timeout would.
func assertFaultReturnedFast(t *testing.T, result *common.StepResult) {
	t.Helper()
	assert.Less(t, result.DurationMs, faultStepTimeout.Milliseconds()/2,
		"a faulted step must not wait for an answer it can never receive")
}

// assertConnFaultNotTimedOut pins the mutual exclusion between
// connection_state and timed_out: a connection fault reports HOW the
// connection died, while timed_out reports that the peer stayed silent, and
// a faulted step never reaches the receive that could set it. Every test in
// this file that asserts result.Response[impair.ConnectionStateKey] also
// calls this, so the two can never silently both be true on one step.
func assertConnFaultNotTimedOut(t *testing.T, result *common.StepResult) {
	t.Helper()
	assert.Equal(t, false, result.Response[impair.TimedOutKey],
		"a connection fault reports HOW the connection died; timed_out reports that the peer stayed silent, and a faulted step never reaches the receive that could set it")
}

func TestAdapter_Reset_AttributedToTheFaultingStep(t *testing.T) {
	result, m := runConnFaultStep(t, impair.ResetConfigKey)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, impair.ConnStateReset, result.Response[impair.ConnectionStateKey])
	assertConnFaultNotTimedOut(t, result)
	assertFaultReturnedFast(t, result)

	// The request genuinely reached the peer before the connection died —
	// this is the assertion that distinguishes reset from drop. It WAITS for
	// the arrival rather than assuming the peer's goroutine has already run:
	// a faulted step returns the moment its bytes are written, so nothing
	// orders the peer's read against this line. See mockwait.go.
	assert.Len(t, m.WaitForRequestsOfCommand(t, diameter.CmdUserAuthorization, 1), 1,
		"the peer must have received the request before its connection died")
}

func TestAdapter_AbruptClose_AttributedToTheFaultingStep(t *testing.T) {
	result, m := runConnFaultStep(t, impair.AbruptCloseConfigKey)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, impair.ConnStateClosed, result.Response[impair.ConnectionStateKey])
	assertConnFaultNotTimedOut(t, result)
	assertFaultReturnedFast(t, result)

	assert.Len(t, m.WaitForRequestsOfCommand(t, diameter.CmdUserAuthorization, 1), 1,
		"the peer must have received the request before its connection died")
}

func TestAdapter_Truncate_PeerSeesShortRead(t *testing.T) {
	result, m := runConnFaultStep(t, impair.TruncateConfigKey)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, impair.ConnStateTruncated, result.Response[impair.ConnectionStateKey])
	assertConnFaultNotTimedOut(t, result)
	assertFaultReturnedFast(t, result)

	// The history is non-empty before the truncated write too — the CER is
	// itself a read — which is why this waits for the SHAPE of the history
	// (a final short read) rather than for a count, and why it asserts the
	// LAST entry rather than the only entry.
	reads := m.WaitForRawRead(t, "the peer to read the truncated prefix", func(reads [][]byte) bool {
		return len(reads) > 0 && len(reads[len(reads)-1]) == impair.TruncatedBytes
	})
	require.NotEmpty(t, reads)
	assert.Len(t, reads[len(reads)-1], impair.TruncatedBytes,
		"the last thing the peer read should be exactly the truncated prefix")
	assert.Empty(t, m.RequestsOfCommand(diameter.CmdUserAuthorization),
		"a truncated request must never frame into a complete message")
}

// TestAdapter_Reset_NextStepRecovers is the regression for the
// connection-state-vs-peer-state desync a connection fault would otherwise
// leave behind.
//
// Clearing connState alone is not enough. ensureConnected's entire gate is
// peer.requireState(peerOpen); it never consults connState. With peer state
// left at Open, the second step below would skip Connect, attempt a
// Send/Receive on a connection the FIRST step deliberately destroyed, and
// report that failure against itself — a failure caused by step 1 blamed on
// step 2, which is precisely the misattribution the reconnection fix closes.
//
// The proof is the CER count, not merely "step 2 passed": a second
// CmdCapabilitiesExchange on the wire is direct evidence that a genuine
// redial-and-re-handshake happened, which no amount of luck or retry could
// produce on its own.
func TestAdapter_Reset_NextStepRecovers(t *testing.T) {
	for _, impairKey := range []string{impair.ResetConfigKey, impair.AbruptCloseConfigKey} {
		t.Run(impairKey, func(t *testing.T) {
			a, m := newFaultAdapter(t)

			first, err := a.Execute("send", m.Addr(), "UAR", faultFields(), map[string]string{impairKey: "true"})
			require.NoError(t, err)
			assert.Equal(t, common.StatusFailed, first.Status)

			second, err := a.Execute("send", m.Addr(), "UAR", faultFields(), nil)
			require.NoError(t, err)
			assert.Equal(t, common.StatusPassed, second.Status, "step 2 error: %s", second.Error)
			assert.Less(t, second.DurationMs, faultStepTimeout.Milliseconds(),
				"step 2 must succeed comfortably within its own timeout, not scrape through near the deadline")

			assert.Len(t, m.RequestsOfCommand(diameter.CmdCapabilitiesExchange), 2,
				"the faulted step must leave peer state closed so the next step re-handshakes")
			assert.Len(t, m.RequestsOfCommand(diameter.CmdUserAuthorization), 2)
		})
	}
}
