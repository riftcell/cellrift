package diameter_test

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timedOutUARFields mirrors fault_test.go's own UAR fixture — a distinct
// copy (not a shared package var) since this file's concern is the
// timed_out key, not AVP construction.
func timedOutUARFields() map[string]string {
	return map[string]string{
		"Session-Id":      "cellrift;test;uar",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}
}

// TestAdapter_TimedOut_AcrossFaultOutcomes is timed_out's Diameter half:
// response.timed_out must be present on every send result, true only when
// the peer genuinely stayed silent, false (present, not absent) on every
// other outcome — success, a non-timeout receive failure, drop, a
// connection-level fault, or a failed handshake.
func TestAdapter_TimedOut_AcrossFaultOutcomes(t *testing.T) {
	tests := []struct {
		name         string
		fault        diameter.HSSFault
		wantStatus   common.StepStatus
		wantTimedOut bool
	}{
		{
			name:         "silence",
			fault:        diameter.HSSFaultSilence,
			wantStatus:   common.StatusFailed,
			wantTimedOut: true,
		},
		{
			name:         "no-fault",
			fault:        diameter.HSSFaultNone,
			wantStatus:   common.StatusPassed,
			wantTimedOut: false,
		},
		{
			name:         "eof",
			fault:        diameter.HSSFaultEOF,
			wantStatus:   common.StatusFailed,
			wantTimedOut: false,
		},
		{
			name:         "peer-truncated-answer",
			fault:        diameter.HSSFaultTruncate,
			wantStatus:   common.StatusFailed,
			wantTimedOut: false,
		},
		{
			name:         "reset",
			fault:        diameter.HSSFaultReset,
			wantStatus:   common.StatusFailed,
			wantTimedOut: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hss := diameter.NewMockHSS(t)
			defer hss.Close()
			hss.SetFault(tc.fault)

			tr, err := common.NewTCPTransport(":0")
			require.NoError(t, err)
			adapter := diameter.NewAdapterWithTransport(tr, 300*time.Millisecond)
			defer adapter.Close()

			result, err := adapter.Execute("send", hss.Addr(), "UAR", timedOutUARFields(), nil)
			require.NoError(t, err, "a fault must be reported via result, not a Go error")
			require.NotNil(t, result)

			assert.Equal(t, tc.wantStatus, result.Status)
			assertTimedOutPresent(t, result, tc.wantTimedOut)
		})
	}
}

// assertTimedOutPresent asserts the timed_out key is PRESENT with the given
// value — an absent key and a false key are different defects, so presence
// and value are checked separately via the two-value map lookup.
func assertTimedOutPresent(t *testing.T, result *common.StepResult, want bool) {
	t.Helper()
	got, present := result.Response[impair.TimedOutKey]
	require.True(t, present, "timed_out must be present on every send result, faulted or not")
	assert.Equal(t, want, got)
}

// TestAdapter_TimedOut_DropPathReportsFalse proves a deliberately dropped
// step — which never attempts a receive at all — still carries a present
// false, via the EnsureTimedOut funnel rather than finishSend's own
// classification (which never runs for a dropped step).
func TestAdapter_TimedOut_DropPathReportsFalse(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	adapter := diameter.NewAdapterWithTransport(tr, 300*time.Millisecond)
	defer adapter.Close()

	result, err := adapter.Execute("send", hss.Addr(), "UAR", timedOutUARFields(),
		map[string]string{"drop": "true"})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, common.StatusPassed, result.Status)
	assert.Equal(t, true, result.Response[impair.DroppedKey])
	assertTimedOutPresent(t, result, false)
}

// TestAdapter_TimedOut_ConnFaultReportsFalse proves a step that truncates its
// own send (a connection-lifecycle fault, not a receive-side outcome) still
// carries a present false timed_out alongside its connection_state.
func TestAdapter_TimedOut_ConnFaultReportsFalse(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	adapter := diameter.NewAdapterWithTransport(impair.Wrap(tr), 3*time.Second)
	defer adapter.Close()

	result, err := adapter.Execute("send", hss.Addr(), "UAR", timedOutUARFields(),
		map[string]string{"truncate": "true"})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, impair.ConnStateTruncated, result.Response[impair.ConnectionStateKey])
	assertTimedOutPresent(t, result, false)
}

// TestAdapter_TimedOut_HandshakeFailureReportsFalse proves a step that never
// gets past the CER/CEA handshake — no application request was ever sent —
// still carries a present false timed_out, via prepareRequest's failedStep
// return funnelled through Execute.
func TestAdapter_TimedOut_HandshakeFailureReportsFalse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	adapter := diameter.NewAdapterWithTransport(tr, 300*time.Millisecond)
	defer adapter.Close()

	result, err := adapter.Execute("send", addr, "UAR", timedOutUARFields(), nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, common.StatusFailed, result.Status)
	assertTimedOutPresent(t, result, false)
}

// TestAdapter_TimedOut_SendFunnelAudit is a source-level mechanical audit,
// the same genre as cmd/cellrift's TestAdapterConstruction_AlwaysWrapsWithImpair.
// This defect class is invisible at runtime: if a future edit removes the
// EnsureTimedOut wrapper from Execute, every existing test above keeps
// passing and timed_out simply goes missing from most results, since
// finishSend's own MarkTimedOut call only ever covers the one branch it
// classifies. Reading adapter.go's source directly is what catches that.
//
// Mutation-tested: temporarily deleting "impair.EnsureTimedOut(" from
// Execute in adapter.go made this test fail (both assertions), and restoring
// it made it pass again.
func TestAdapter_TimedOut_SendFunnelAudit(t *testing.T) {
	data, err := os.ReadFile("adapter.go")
	require.NoError(t, err, "this audit reads adapter.go directly; it must exist alongside this test file")

	src := string(data)
	assert.Contains(t, src, "impair.EnsureTimedOut(result), err",
		"Execute's send branch must route every result through the funnel — without it, response.timed_out "+
			"silently disappears from every StepResult except the one finishSend classifies, and no other test notices")
	assert.Equal(t, 1, strings.Count(src, "impair.MarkTimedOut"),
		"exactly one classification site may stamp timed_out; more than one means the classification has been "+
			"copied somewhere the typed error is no longer available")
}

// TestAdapter_StepTimeoutMs_HonouredOnSend pins that a step's own timeout_ms
// bounds the answer wait. The schema and validator accepted the key on any
// step, but this adapter never read it back, so a Diameter step declaring
// timeout_ms: 200 against a silent peer waited the full adapter default
// while SIP honoured the same key. With a 2 s adapter default and a 200 ms
// step budget, the step must fail well under 2 s and be stamped timed_out.
func TestAdapter_StepTimeoutMs_HonouredOnSend(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()
	hss.SetFault(diameter.HSSFaultSilence)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	start := time.Now()
	result, err := adapter.Execute("send", hss.Addr(), "UAR", timedOutUARFields(), map[string]string{"timeout_ms": "200"})
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, common.StatusFailed, result.Status)
	assertTimedOutPresent(t, result, true)
	assert.Less(t, elapsed, 1200*time.Millisecond, "timeout_ms: 200 must override the 2 s adapter default")
	assert.GreaterOrEqual(t, elapsed, 150*time.Millisecond, "the step must actually wait out its own budget")
}

// TestAdapter_StepTimeoutMs_FallsBackToAdapterDefault is the control: with
// no timeout_ms, and with a non-positive one, the adapter default still
// governs.
func TestAdapter_StepTimeoutMs_FallsBackToAdapterDefault(t *testing.T) {
	for _, cfg := range []map[string]string{nil, {"timeout_ms": "0"}, {"timeout_ms": "junk"}} {
		hss := diameter.NewMockHSS(t)
		hss.SetFault(diameter.HSSFaultSilence)
		tr, err := common.NewTCPTransport(":0")
		require.NoError(t, err)
		adapter := diameter.NewAdapterWithTransport(tr, 300*time.Millisecond)

		start := time.Now()
		result, err := adapter.Execute("send", hss.Addr(), "UAR", timedOutUARFields(), cfg)
		elapsed := time.Since(start)
		require.NoError(t, err)
		assert.Equal(t, common.StatusFailed, result.Status)
		assert.GreaterOrEqual(t, elapsed, 250*time.Millisecond, "cfg %v must fall back to the 300 ms adapter default", cfg)

		adapter.Close()
		hss.Close()
	}
}
