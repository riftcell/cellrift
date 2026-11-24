// sctp_abort_test.go proves sctp_abort end-to-end: a real RFC 4960 SCTP ABORT
// chunk, sent over a real association against a real SCTP-listening
// MockHSS, produces response.connection_state == "aborted" attributed to
// the step that caused it; the step after it re-handshakes over a fresh
// association; and impair: { reset: true } on SCTP refuses rather than
// silently degrading to a graceful close. It also proves that a deliberate
// connection fault is never confused with the silent SCTP-to-TCP
// substitution, because the two
// live in different result fields and never co-occur.
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

// sctpFaultStepTimeout mirrors conn_fault_test.go's faultStepTimeout: generous
// enough that a faulted step returning fast is unambiguous.
const sctpFaultStepTimeout = 3 * time.Second

// newSCTPFaultAdapter builds a Diameter adapter over a REAL SCTP transport,
// wrapped exactly the way cmd/cellrift/main.go wraps it, pointed at a real
// SCTP-listening mock HSS.
//
// It skips the test twice over. NewSCTPMockHSS skips when the kernel cannot
// listen; this function skips again when NewSCTPOrFallbackTransport returns
// a warning, because that warning means a TCP transport was silently
// substituted — which could not dial an SCTP listener anyway, and which is
// precisely the substitution sctp_abort's reporting exists to keep distinguishable from a
// deliberate fault. Asserting an "abort" outcome against a TCP substitute
// would be the exact confusion this requirement forbids.
func newSCTPFaultAdapter(t *testing.T) (*diameter.Adapter, *diameter.MockHSS) {
	t.Helper()
	m := diameter.NewSCTPMockHSS(t)
	t.Cleanup(m.Close)

	tr, warnings, err := common.NewSCTPOrFallbackTransport(":0")
	require.NoError(t, err)
	if len(warnings) > 0 {
		t.Skipf("real SCTP unavailable, transport was substituted: %v", warnings)
	}

	a := diameter.NewAdapterWithTransport(impair.Wrap(tr), sctpFaultStepTimeout)
	t.Cleanup(func() { _ = a.Close() })
	return a, m
}

// TestAdapter_SCTPAbort_AttributedToTheFaultingStep proves the first half of
// the sctp_abort claim: a real ABORT chunk on a real association is reported as
// connection_state == "aborted" on exactly the step that declared it, with
// no Go error, and only after the request genuinely reached the peer.
func TestAdapter_SCTPAbort_AttributedToTheFaultingStep(t *testing.T) {
	a, m := newSCTPFaultAdapter(t)

	result, err := a.Execute("send", m.Addr(), "UAR", faultFields(), map[string]string{impair.SCTPAbortConfigKey: "true"})
	require.NoError(t, err, "a faulted step reports its outcome via result, not a Go error")

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, impair.ConnStateAborted, result.Response[impair.ConnectionStateKey])

	// The fault must have ARMED, not merely been attempted. connection_state
	// is stamped either way — FaultedResult records what was tried — so
	// without this line the test passes on a fault that never fired, which
	// is exactly what it did: SCTPWrite with an SCTP_ABORT cmsg returned
	// EINVAL on every run, unnoticed, and this assertion was the
	// missing half that would have said so.
	assert.NotContains(t, result.Error, "failed to arm",
		"the abort must reach the wire, not just be attempted: %s", result.Error)
	assert.Less(t, result.DurationMs, sctpFaultStepTimeout.Milliseconds()/2,
		"a faulted step must not wait for an answer it can never receive")

	assert.Len(t, m.WaitForRequestsOfCommand(t, diameter.CmdUserAuthorization, 1), 1,
		"the peer must have received the request before its association was aborted")
}

// TestAdapter_SCTPAbort_NextStepRecovers is the reconnection fix proven over SCTP, not
// just TCP: an aborted association must leave peer state closed so the next
// step re-handshakes over a fresh association rather than misattributing the
// abort to itself.
func TestAdapter_SCTPAbort_NextStepRecovers(t *testing.T) {
	a, m := newSCTPFaultAdapter(t)

	first, err := a.Execute("send", m.Addr(), "UAR", faultFields(), map[string]string{impair.SCTPAbortConfigKey: "true"})
	require.NoError(t, err)
	assert.Equal(t, common.StatusFailed, first.Status)

	second, err := a.Execute("send", m.Addr(), "UAR", faultFields(), nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, second.Status, "step 2 error: %s", second.Error)

	assert.Len(t, m.RequestsOfCommand(diameter.CmdCapabilitiesExchange), 2,
		"an aborted association must leave peer state closed so the next step re-handshakes over a fresh one")
	assert.Equal(t, common.StatusPassed, second.Status)
}

// TestAdapter_Reset_RefusedOnSCTPTransport is the "no silent substitution"
// guard at the adapter level: SCTPTransport.ResetConn always refuses rather
// than performing a graceful close, so a following ordinary step passes
// without any re-handshake. The scenario validator already rejects
// sctp_abort on a non-SCTP transport at validate time, but it does NOT
// reject reset on an SCTP one — that combination is legal to write, and this
// is the test proving what it actually does.
func TestAdapter_Reset_RefusedOnSCTPTransport(t *testing.T) {
	a, m := newSCTPFaultAdapter(t)

	result, err := a.Execute("send", m.Addr(), "UAR", faultFields(), map[string]string{impair.ResetConfigKey: "true"})
	require.NoError(t, err)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Contains(t, result.Error, "sctp_abort")
	assert.Equal(t, impair.ConnStateReset, result.Response[impair.ConnectionStateKey])

	second, err := a.Execute("send", m.Addr(), "UAR", faultFields(), nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, second.Status, "step 2 error: %s", second.Error)

	assert.Len(t, m.RequestsOfCommand(diameter.CmdCapabilitiesExchange), 1,
		"a refused reset must not have touched the association")
}

// TestConnectionState_DistinctFromSCTPFallbackWarning proves that a
// deliberate SCTP association fault is never confused with a silent
// SCTP-unavailable substitution.
//
// The distinction is STRUCTURAL first. A deliberate fault writes
// StepResult.Response["connection_state"] — per-step, written by the
// fault-handling code at the moment the fault fires. A silent substitution
// writes the []string a transport constructor returns, which the runner
// surfaces as RunResult.Warnings — run-level, written once at
// adapter-construction time, before any step exists. They are different
// fields on different types populated at different times, so no string
// choice is load-bearing for the criterion.
//
// The substring check below is secondary belt-and-braces: even if a future
// refactor merged the two into one reporting surface, a fallback warning
// would still never read as any of the four connection_state values.
//
// This test does NOT need real SCTP and must NOT skip: it is the half of
// criterion 4 that is observable on every machine, which matters precisely
// because the tests above may skip. t.Setenv marks it non-parallel
// automatically, so no t.Parallel() is called anywhere in this file.
func TestConnectionState_DistinctFromSCTPFallbackWarning(t *testing.T) {
	t.Setenv("CELLRIFT_TRANSPORT_FALLBACK", "tcp")

	m := diameter.NewMockHSS(t) // plain TCP mock — the substitute dials this fine
	t.Cleanup(m.Close)

	tr, warnings, err := common.NewSCTPOrFallbackTransport(":0")
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "requested sctp, used tcp")

	a := diameter.NewAdapterWithTransport(impair.Wrap(tr), sctpFaultStepTimeout)
	t.Cleanup(func() { _ = a.Close() })

	result, err := a.Execute("send", m.Addr(), "UAR", faultFields(), nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status)

	_, present := result.Response[impair.ConnectionStateKey]
	assert.False(t, present,
		"a silently substituted transport must never look like a deliberate connection fault")

	for _, state := range []string{
		impair.ConnStateReset, impair.ConnStateTruncated,
		impair.ConnStateClosed, impair.ConnStateAborted,
	} {
		assert.NotContains(t, warnings[0], state)
	}
}
