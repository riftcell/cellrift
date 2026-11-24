package sip_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the SIP status gate: a step is passed only when the peer's
// final response is a 2xx, exactly as Diameter's buildStepResult passes only
// a Result-Code of 2001. They exist because before the gate landed,
// finishSend stamped StatusPassed unconditionally — a peer answering
// "500 Server Internal Error" produced a green step and exit 0, which a
// review only caught by running a hostile peer against the real binary. The
// default mock never exercised this path since it answers every valid
// request with a success; ErrorResponder is the sad-path hook that makes the
// case testable in-process.

func newGateFixture(t *testing.T) (*sip.MockUASServer, *sip.Adapter) {
	t.Helper()
	mock := sip.NewMockUAS(t)
	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	adapter := sip.NewAdapterWithTransport(impair.Wrap(tr), "udp", 2*time.Second)
	t.Cleanup(func() {
		adapter.Close()
		mock.Close()
	})
	return mock, adapter
}

func sendOptions(t *testing.T, adapter *sip.Adapter, addr string) *common.StepResult {
	t.Helper()
	result, err := adapter.Execute("send", addr, "OPTIONS", map[string]string{
		"From": "<sip:447700900001@ims.local>;tag=gate001",
		"To":   "<sip:ims.local>",
	}, nil)
	require.NoError(t, err, "a protocol-level failure must be a result, never a Go error")
	return result
}

// TestAdapter_StatusGate_NonSuccessFinalFailsTheStep covers every non-2xx
// final class. The 3xx row is deliberate: a redirect is not the success the
// step asked for, and the gate is strict in the same way Diameter's is (any
// Result-Code other than 2001 fails) rather than inventing a SIP-only
// leniency a scenario author would have to learn separately.
func TestAdapter_StatusGate_NonSuccessFinalFailsTheStep(t *testing.T) {
	cases := []struct {
		code   int
		reason string
	}{
		{301, "Moved Permanently"},
		{404, "Not Found"},
		{403, "Forbidden"},
		{500, "Server Internal Error"},
		{503, "Service Unavailable"},
		{603, "Decline"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			mock, adapter := newGateFixture(t)
			mock.SetResponder(sip.ErrorResponder(tc.code, tc.reason))

			result := sendOptions(t, adapter, mock.Addr())

			assert.Equal(t, common.StatusFailed, result.Status)
			assert.Contains(t, result.Error, "expected 2xx")
			assert.Contains(t, result.Error, tc.reason)
			assert.Equal(t, tc.code, result.Response["status_code"],
				"the real status must still be captured so an assertion can inspect it")
			assert.NotEmpty(t, result.RawResponse, "the raw response is kept for response.__raw")
		})
	}
}

// TestAdapter_StatusGate_SuccessFinalPasses is the control: the gate must not
// have made ordinary success stricter than it was.
func TestAdapter_StatusGate_SuccessFinalPasses(t *testing.T) {
	for _, code := range []int{200, 202} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			mock, adapter := newGateFixture(t)
			mock.SetResponder(sip.ErrorResponder(code, "OK"))

			result := sendOptions(t, adapter, mock.Addr())

			assert.Equal(t, common.StatusPassed, result.Status)
			assert.Empty(t, result.Error)
		})
	}
}

// TestAdapter_StatusGate_DefaultMockStillPasses proves the hook is inert
// when unset, so every pre-existing test that relies on the default success
// responses keeps its meaning.
func TestAdapter_StatusGate_DefaultMockStillPasses(t *testing.T) {
	mock, adapter := newGateFixture(t)
	result := sendOptions(t, adapter, mock.Addr())
	assert.Equal(t, common.StatusPassed, result.Status)
}
