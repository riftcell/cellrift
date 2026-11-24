// conn_fault_test.go proves reset, truncate and abrupt_close against a real
// SIP TCP mock UAS, plus SIP-over-UDP truncation and the empirical proof that
// a SIP connection fault is scoped to the step that inflicted it.
package sip_test

import (
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// faultStepTimeout is generous enough that a faulted step returning fast is
// unambiguous: a DurationMs well under this bound can only mean no receive
// was attempted.
const faultStepTimeout = 2 * time.Second

// faultFields is the minimal REGISTER field set every fault case in this
// file sends.
func faultFields() map[string]string {
	return map[string]string{
		"From":    "<sip:447700900001@ims.local>;tag=fault001",
		"To":      "<sip:447700900001@ims.local>",
		"Contact": "<sip:447700900001@10.0.0.1:5060>",
	}
}

// newTCPFaultAdapter builds a SIP adapter over a REAL TCP transport wrapped
// exactly the way cmd/cellrift/main.go wraps it, pointed at a real
// in-process TCP mock UAS. TCPFaultSilence is the right peer behaviour for
// every case here: it reads one request and then answers nothing, so it
// records what arrived without ever calling ParseMessage — which means a
// deliberately truncated request produces no spurious mock-side parse error
// to explain away.
func newTCPFaultAdapter(t *testing.T) (*sip.Adapter, *sip.TCPMockUASServer) {
	t.Helper()
	u := sip.NewTCPMockUAS(t, sip.TCPFaultSilence)
	t.Cleanup(u.Close)
	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	a := sip.NewAdapterWithTransport(impair.Wrap(tr), "tcp", faultStepTimeout)
	t.Cleanup(func() { _ = a.Close() })
	return a, u
}

// assertFaultReturnedFast pins "no receive was attempted": a faulted step
// must return in well under its own step timeout.
func assertFaultReturnedFast(t *testing.T, result *common.StepResult) {
	t.Helper()
	assert.Less(t, result.DurationMs, faultStepTimeout.Milliseconds()/2,
		"a faulted step must not wait for a response it can never receive")
}

// runResetOrAbruptClose runs one REGISTER step declaring impairKey against a
// fresh TCP fault fixture and asserts the shared reset/abrupt_close shape:
// FAILED, the expected connection_state, a fast return, and — the assertion
// that distinguishes reset/abrupt_close from drop — the peer having received
// the complete request before the connection died.
func runResetOrAbruptClose(t *testing.T, impairKey, wantState string) {
	t.Helper()
	a, u := newTCPFaultAdapter(t)

	result, err := a.Execute("send", u.Addr(), "REGISTER", faultFields(), map[string]string{impairKey: "true"})
	require.NoError(t, err, "a faulted step reports its outcome via result, not a Go error")

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, wantState, result.Response[impair.ConnectionStateKey])
	assertConnFaultTimedOutFalse(t, result)
	assertFaultReturnedFast(t, result)

	u.WaitForRawRead(t, "the mock's handleConn goroutine must record the read before we inspect it", func(reads [][]byte) bool { return len(reads) == 1 })

	reads := u.RawReads()
	require.Len(t, reads, 1)
	assert.Contains(t, string(reads[0]), "REGISTER ",
		"the peer must have received a complete request before the fault — this is what makes reset different from drop")
	assert.Greater(t, len(reads[0]), impair.TruncatedBytes)
}

// assertConnFaultTimedOutFalse asserts that a step reporting connection_state
// never also reports timed_out == true: connection_state says HOW the
// connection died, timed_out says the peer stayed silent, and a faulted step
// returns before any receive is attempted — the two can never both be true
// on one step.
func assertConnFaultTimedOutFalse(t *testing.T, result *common.StepResult) {
	t.Helper()
	assert.Equal(t, false, result.Response[impair.TimedOutKey],
		"a connection fault reports HOW the connection died; timed_out reports that the peer stayed silent, and a faulted step returns before any receive is attempted")
}

func TestAdapter_Reset_SIPPeerSeesFullRequestThenLoss(t *testing.T) {
	runResetOrAbruptClose(t, impair.ResetConfigKey, impair.ConnStateReset)
}

func TestAdapter_AbruptClose_SIPPeerSeesFullRequestThenLoss(t *testing.T) {
	runResetOrAbruptClose(t, impair.AbruptCloseConfigKey, impair.ConnStateClosed)
}

func TestAdapter_Truncate_SIPPeerSeesShortRead(t *testing.T) {
	a, u := newTCPFaultAdapter(t)

	result, err := a.Execute("send", u.Addr(), "REGISTER", faultFields(), map[string]string{impair.TruncateConfigKey: "true"})
	require.NoError(t, err, "a faulted step reports its outcome via result, not a Go error")

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, impair.ConnStateTruncated, result.Response[impair.ConnectionStateKey])
	assertConnFaultTimedOutFalse(t, result)
	assertFaultReturnedFast(t, result)

	u.WaitForRawRead(t, "the mock's handleConn goroutine must record the read before we inspect it", func(reads [][]byte) bool { return len(reads) == 1 })

	reads := u.RawReads()
	require.Len(t, reads, 1)
	assert.Len(t, reads[0], impair.TruncatedBytes)
	assert.Empty(t, u.Errors(), "TCPFaultSilence never parses, so a short read must produce no mock-side error")
}

// newUDPFaultAdapter builds a SIP adapter over a REAL UDP transport wrapped
// exactly the way cmd/cellrift/main.go wraps it, pointed at a real
// in-process UDP mock UAS.
func newUDPFaultAdapter(t *testing.T) (*sip.Adapter, *sip.MockUASServer) {
	t.Helper()
	u := sip.NewMockUAS(t)
	t.Cleanup(u.Close)
	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	a := sip.NewAdapterWithTransport(impair.Wrap(tr), "udp", faultStepTimeout)
	t.Cleanup(func() { _ = a.Close() })
	return a, u
}

// TestAdapter_Truncate_UDPPeerSeesShortDatagram is truncate's UDP half: a
// short datagram needs no connection lifecycle at all,
// since the datagram's own length IS its frame boundary.
func TestAdapter_Truncate_UDPPeerSeesShortDatagram(t *testing.T) {
	a, u := newUDPFaultAdapter(t)

	result, err := a.Execute("send", u.Addr(), "REGISTER", faultFields(), map[string]string{impair.TruncateConfigKey: "true"})
	require.NoError(t, err, "a faulted step reports its outcome via result, not a Go error")

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, impair.ConnStateTruncated, result.Response[impair.ConnectionStateKey])
	assertConnFaultTimedOutFalse(t, result)
	assertFaultReturnedFast(t, result)

	u.WaitForRawRead(t, "the mock's serve goroutine must record the read before we inspect it", func(reads [][]byte) bool { return len(reads) == 1 })

	reads := u.RawReads()
	require.Len(t, reads, 1)
	assert.Len(t, reads[0], impair.TruncatedBytes)
	assert.Empty(t, u.ReceivedRequests(), "an 8-byte datagram cannot parse into a SIP message")

	// The UDP socket is deliberately never closed by a truncated send: a
	// datagram's own length IS its frame boundary, so there is nothing to
	// tear down. A second ordinary step on the same adapter proving it still
	// works is what makes that contract observable rather than assumed.
	second, err := a.Execute("send", u.Addr(), "REGISTER", faultFields(), nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, second.Status, "second step error: %s", second.Error)
}

// TestAdapter_Reset_UDPReportsUnsupported is the unsupported-combination
// path: UDPTransport has no connection to reset, so the step must fail with
// a message naming the transport type rather than silently doing nothing.
func TestAdapter_Reset_UDPReportsUnsupported(t *testing.T) {
	a, u := newUDPFaultAdapter(t)

	result, err := a.Execute("send", u.Addr(), "REGISTER", faultFields(), map[string]string{impair.ResetConfigKey: "true"})
	require.NoError(t, err, "a faulted step reports its outcome via result, not a Go error")

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, impair.ConnStateReset, result.Response[impair.ConnectionStateKey],
		"connection_state is stamped even though the fault failed to arm, naming what was attempted")
	assertConnFaultTimedOutFalse(t, result)
	assert.Contains(t, result.Error, "UDPTransport")
}

// TestAdapter_ConnFault_ScopedToTheSameStep is the empirical companion to
// applyConnFault's SCOPE DISCLOSURE comment (adapter.go): a disclosure with
// no test behind it is an unverified claim.
//
// The Diameter adapter needs peer.setState(peerClosed) to make a later step
// recover cleanly from an earlier step's connection fault. SIP needs
// nothing: every Send dials a fresh connection regardless of what the
// previous step did to its own, so there is no stale reference for a later
// step to inherit in the first place. That is a structural property of this
// adapter's dial-per-send model, not a recovery mechanism it implements.
func TestAdapter_ConnFault_ScopedToTheSameStep(t *testing.T) {
	u := sip.NewTCPMockUAS(t, sip.TCPFaultNone)
	t.Cleanup(u.Close)
	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	a := sip.NewAdapterWithTransport(impair.Wrap(tr), "tcp", faultStepTimeout)
	t.Cleanup(func() { _ = a.Close() })

	first, err := a.Execute("send", u.Addr(), "REGISTER", faultFields(), map[string]string{impair.ResetConfigKey: "true"})
	require.NoError(t, err)
	assert.Equal(t, common.StatusFailed, first.Status)
	assert.Equal(t, impair.ConnStateReset, first.Response[impair.ConnectionStateKey])

	second, err := a.Execute("send", u.Addr(), "REGISTER", faultFields(), nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, second.Status,
		"SIP dials a fresh connection on every Send, so a previous step's connection fault cannot affect a later step — this is a structural property of this adapter, not a recovery mechanism it implements")

	u.WaitForRawRead(t, "the mock's handleConn goroutines must record both reads before we inspect them", func(reads [][]byte) bool { return len(reads) == 2 })
	assert.Len(t, u.RawReads(), 2,
		"two separate accepted connections: SIP never reuses one across steps")
}
