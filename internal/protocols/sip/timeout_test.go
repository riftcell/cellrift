package sip_test

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timedOutFields mirrors fault_test.go's own REGISTER fixture — a distinct
// copy (not a shared package var) since this file's concern is the
// timed_out key, not the fault vocabulary itself.
func timedOutFields() map[string]string {
	return map[string]string{"To": "sip:alice@example.test", "From": "sip:cellrift@example.test"}
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

// TestAdapter_TimedOut_AcrossFaultOutcomes is timed_out's SIP half:
// response.timed_out must be present on every send result, true only when
// the peer genuinely stayed silent, false (present, not absent) on every
// other outcome — success, a non-timeout receive failure, or a parse error.
func TestAdapter_TimedOut_AcrossFaultOutcomes(t *testing.T) {
	tests := []struct {
		name         string
		fault        sip.TCPFault
		wantStatus   common.StepStatus
		wantTimedOut bool
	}{
		{
			name:         "silence",
			fault:        sip.TCPFaultSilence,
			wantStatus:   common.StatusFailed,
			wantTimedOut: true,
		},
		{
			name:         "no-fault",
			fault:        sip.TCPFaultNone,
			wantStatus:   common.StatusPassed,
			wantTimedOut: false,
		},
		{
			name:         "eof",
			fault:        sip.TCPFaultEOF,
			wantStatus:   common.StatusFailed,
			wantTimedOut: false,
		},
		{
			name:         "reset",
			fault:        sip.TCPFaultReset,
			wantStatus:   common.StatusFailed,
			wantTimedOut: false,
		},
		{
			name:         "truncate",
			fault:        sip.TCPFaultTruncate,
			wantStatus:   common.StatusFailed,
			wantTimedOut: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := sip.NewTCPMockUAS(t, tc.fault)
			defer mock.Close()

			tr, err := common.NewTCPTransport(":0")
			require.NoError(t, err)
			adapter := sip.NewAdapterWithTransport(tr, "tcp", 200*time.Millisecond)
			defer adapter.Close()

			result, err := adapter.Execute("send", mock.Addr(), "REGISTER", timedOutFields(), nil)
			require.NoError(t, err, "a fault must be reported via result, not a Go error")
			require.NotNil(t, result)

			assert.Equal(t, tc.wantStatus, result.Status)
			assertTimedOutPresent(t, result, tc.wantTimedOut)
		})
	}
}

// TestAdapter_TimedOut_UDPSilenceReportsTrue proves the classifier is not
// TCP-specific: common.IsTimeoutErr(err) alone is SIP's whole classifier, and
// UDP hands back the same unwrapped netpoller deadline error TCP does.
func TestAdapter_TimedOut_UDPSilenceReportsTrue(t *testing.T) {
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	adapter := sip.NewAdapterWithTransport(tr, "udp", 200*time.Millisecond)
	defer adapter.Close()

	result, err := adapter.Execute("send", ln.LocalAddr().String(), "REGISTER", timedOutFields(), nil)
	require.NoError(t, err, "a fault must be reported via result, not a Go error")
	require.NotNil(t, result)

	assert.Equal(t, common.StatusFailed, result.Status)
	assertTimedOutPresent(t, result, true)
}

// TestAdapter_TimedOut_DropPathReportsFalse proves a deliberately dropped
// step — which never attempts a receive at all — still carries a present
// false, via the EnsureTimedOut funnel rather than waitForFinalResponse's own
// classification (which never runs for a dropped step).
func TestAdapter_TimedOut_DropPathReportsFalse(t *testing.T) {
	mock := sip.NewTCPMockUAS(t, sip.TCPFaultSilence)
	defer mock.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	adapter := sip.NewAdapterWithTransport(tr, "tcp", 200*time.Millisecond)
	defer adapter.Close()

	result, err := adapter.Execute("send", mock.Addr(), "REGISTER", timedOutFields(),
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
	mock := sip.NewTCPMockUAS(t, sip.TCPFaultSilence)
	defer mock.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	adapter := sip.NewAdapterWithTransport(impair.Wrap(tr), "tcp", 200*time.Millisecond)
	defer adapter.Close()

	result, err := adapter.Execute("send", mock.Addr(), "REGISTER", timedOutFields(),
		map[string]string{"truncate": "true"})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, impair.ConnStateTruncated, result.Response[impair.ConnectionStateKey])
	assertTimedOutPresent(t, result, false)
}

// TestAdapter_AssertState_HasNoTimedOutKey is the inverse assertion: assert_state
// polls in-process dialog state and never waits on a peer, so it must never
// gain the timed_out key at all — not even a present false.
func TestAdapter_AssertState_HasNoTimedOutKey(t *testing.T) {
	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	adapter := sip.NewAdapterWithTransport(tr, "udp", 50*time.Millisecond)
	defer adapter.Close()

	result, err := adapter.Execute("assert_state", "", "", nil, map[string]string{
		"check":      "confirmed",
		"timeout_ms": "50",
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	_, present := result.Response[impair.TimedOutKey]
	assert.False(t, present,
		"assert_state polls in-process dialog state and never waits on a peer; giving it the same key would make response.timed_out mean two different things depending on the action")
}

// TestAdapter_TimedOut_SendFunnelAudit is a source-level mechanical audit,
// mirroring the Diameter adapter's audit so both adapters are guarded the
// same way. This defect class is invisible at runtime: if a future edit
// removes the EnsureTimedOut wrapper from Execute, every existing test above
// keeps passing and timed_out simply goes missing from most results, since
// waitForFinalResponse's own MarkTimedOut call only ever covers the one
// branch it classifies. Reading adapter.go's source directly is what catches
// that.
//
// Mutation-tested: temporarily deleting "impair.EnsureTimedOut(" from
// Execute in adapter.go made this test fail (both Contains assertions), and
// restoring it made it pass again.
func TestAdapter_TimedOut_SendFunnelAudit(t *testing.T) {
	data, err := os.ReadFile("adapter.go")
	require.NoError(t, err, "this audit reads adapter.go directly; it must exist alongside this test file")

	src := string(data)
	assert.Contains(t, src, "impair.EnsureTimedOut(result), err",
		"Execute's send branch must route every result through the funnel — without it, response.timed_out "+
			"silently disappears from every StepResult except the one waitForFinalResponse classifies, and no other test notices")
	assert.Equal(t, 1, strings.Count(src, "impair.MarkTimedOut"),
		"exactly one classification site may stamp timed_out; more than one means the classification has been "+
			"copied somewhere the typed error is no longer available")
	assert.NotContains(t, src, "return impair.EnsureTimedOut(a.executeAssertState",
		"assert_state must stay outside the funnel: its timeout is a dialog-state poll deadline, not a peer-silence event")
}
