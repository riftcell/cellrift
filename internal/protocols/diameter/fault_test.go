package diameter_test

import (
	"strings"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdapter_SelfInflictedTransportFaults proves, for Diameter, that a
// transport fault the peer inflicts on an ESTABLISHED session (the RFC 6733
// §5.3 CER/CEA handshake completes normally first) must reach the engine as
// a failed *common.StepResult with a populated Error, never as a Go error
// that would skip assertion evaluation. All four fault classes plus a
// no-fault control are exercised by this one table-driven test over real
// sockets, mirroring the SIP adapter's proof of the same convention.
func TestAdapter_SelfInflictedTransportFaults(t *testing.T) {
	tests := []struct {
		name             string
		fault            diameter.HSSFault
		wantStatus       common.StepStatus
		wantErrSubstring string
		wantNotErrSubstr string
	}{
		{
			name:             "reset",
			fault:            diameter.HSSFaultReset,
			wantStatus:       common.StatusFailed,
			wantErrSubstring: "reset",
		},
		{
			name:             "eof",
			fault:            diameter.HSSFaultEOF,
			wantStatus:       common.StatusFailed,
			wantErrSubstring: "EOF",
			wantNotErrSubstr: "unexpected EOF",
		},
		{
			// Diameter's length-prefixed framing cannot extract a message
			// from 8 bytes (shorter than the 20-byte header), so the
			// client's nextMessage loop reads again and gets the peer's
			// close — this is an EOF, not a codec error.
			name:             "truncate",
			fault:            diameter.HSSFaultTruncate,
			wantStatus:       common.StatusFailed,
			wantErrSubstring: "EOF",
			wantNotErrSubstr: "unexpected EOF",
		},
		{
			// Empirically observed on this machine: the underlying TCP
			// transport's own read deadline expires and returns "i/o
			// timeout" before receiveAnswer's own deadline-exceeded check
			// (errAnswerTimeout, "timed out waiting for answer") ever gets a
			// chance to fire — the deadline arithmetic hands the timeout to
			// the transport layer first.
			name:             "silence",
			fault:            diameter.HSSFaultSilence,
			wantStatus:       common.StatusFailed,
			wantErrSubstring: "i/o timeout",
		},
		{
			name:       "control-no-fault",
			fault:      diameter.HSSFaultNone,
			wantStatus: common.StatusPassed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hss := diameter.NewMockHSS(t)
			defer hss.Close()
			hss.SetFault(tc.fault) // applies to the UAR only; the CER/CEA still succeeds

			tr, err := common.NewTCPTransport(":0")
			require.NoError(t, err)
			adapter := diameter.NewAdapterWithTransport(tr, 300*time.Millisecond)
			defer adapter.Close()

			fields := map[string]string{
				"Session-Id":      "cellrift;test;uar",
				"Origin-Host":     "cellrift.ims.local",
				"Origin-Realm":    "ims.local",
				"Public-Identity": "sip:447700900001@ims.local",
			}

			start := time.Now()
			result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
			elapsed := time.Since(start)

			// The whole claim: a transport fault the peer inflicted is
			// a step outcome, not an infrastructure error. A Go error here
			// would make the engine skip this step's assertions entirely.
			require.NoError(t, err, "transport fault must be reported as a StepResult, not a Go error")
			require.NotNil(t, result)
			t.Logf("fault=%s status=%s elapsed=%s error=%q", tc.fault, result.Status, elapsed, result.Error)

			assert.Equal(t, tc.wantStatus, result.Status)
			if tc.wantErrSubstring != "" {
				assert.Contains(t, strings.ToLower(result.Error), strings.ToLower(tc.wantErrSubstring))
			}
			if tc.wantNotErrSubstr != "" {
				assert.NotContains(t, result.Error, tc.wantNotErrSubstr)
			}
			if tc.fault == diameter.HSSFaultNone {
				assert.NotNil(t, result.RawResponse)
			}
			if tc.fault == diameter.HSSFaultSilence {
				assert.Less(t, elapsed, 3*time.Second)
			}

			// The handshake completed on every row, faulted or not: the
			// fault landed on the application request, not on CER/CEA.
			assert.Zero(t, hss.Rejected())

			// Deliberately not asserted: whether the held connection was
			// dropped or re-handshaked after a reset. The dead-connection
			// classifier matches io.EOF, io.ErrUnexpectedEOF and
			// net.ErrClosed but not ECONNRESET, so a real RST does not
			// currently clear the held connection. That is a known,
			// carried-forward gap for the connection-fault and assertion
			// phases, not something this test should pretend is fixed.
			//
			// Also deliberately not asserted: the mock's Errors() slice is
			// not required to be empty on faulted rows — closing a
			// connection mid-exchange is the behaviour under test, not a
			// mock malfunction.
		})
	}
}
