package sip_test

import (
	"strings"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdapter_SelfInflictedTransportFaults proves, for the SIP adapter, that a
// transport fault is a step outcome: a transport fault a real peer inflicts (reset, EOF, deadline
// exceeded, truncated frame) comes back as an assertable *common.StepResult
// with Status failed and a populated Error — never a Go error that would
// make the engine skip this step's assertions entirely.
//
// SIP dials per send and holds no connection state, so there is nothing to
// assert about a dropped or reconnected connection here — Status and Error
// are the whole observable surface, and the named connection-state
// vocabulary (response.connection_state) is tested separately.
func TestAdapter_SelfInflictedTransportFaults(t *testing.T) {
	tests := []struct {
		name              string
		fault             sip.TCPFault
		wantStatus        common.StepStatus
		wantErrSubstrings []string
		boundElapsed      bool
	}{
		{
			name:              "reset",
			fault:             sip.TCPFaultReset,
			wantStatus:        common.StatusFailed,
			wantErrSubstrings: []string{"reset"},
		},
		{
			name:              "eof",
			fault:             sip.TCPFaultEOF,
			wantStatus:        common.StatusFailed,
			wantErrSubstrings: []string{"EOF"},
		},
		{
			name:              "deadline exceeded (silence)",
			fault:             sip.TCPFaultSilence,
			wantStatus:        common.StatusFailed,
			wantErrSubstrings: []string{"i/o timeout"},
			boundElapsed:      true,
		},
		{
			name:              "truncated frame",
			fault:             sip.TCPFaultTruncate,
			wantStatus:        common.StatusFailed,
			wantErrSubstrings: []string{"parse response", "malformed start line"},
		},
		{
			name:       "control: no fault",
			fault:      sip.TCPFaultNone,
			wantStatus: common.StatusPassed,
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

			fields := map[string]string{"To": "sip:alice@example.test", "From": "sip:cellrift@example.test"}

			start := time.Now()
			result, err := adapter.Execute("send", mock.Addr(), "REGISTER", fields, nil)
			elapsed := time.Since(start)

			// The whole claim: a transport fault the peer inflicted is a
			// step outcome, not an infrastructure error. A Go error here would
			// make the engine skip this step's assertions entirely.
			require.NoError(t, err, "transport fault must be reported as a StepResult, not a Go error")
			require.NotNil(t, result)

			t.Logf("fault=%s status=%s error=%q elapsed=%s", tc.fault, result.Status, result.Error, elapsed)

			assert.Equal(t, tc.wantStatus, result.Status)
			for _, substr := range tc.wantErrSubstrings {
				assert.Contains(t, strings.ToLower(result.Error), strings.ToLower(substr))
			}
			if tc.boundElapsed {
				assert.Less(t, elapsed, 3*time.Second)
			}
		})
	}
}
