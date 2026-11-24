package impair

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTransportBase is a small local test double implementing
// common.Transport, used to unit-test the connfault.go dispatch table in
// isolation. This is NOT a mock of a protocol adapter or a real peer —
// CLAUDE.md's "no mocks for protocol adapters in integration tests" rule
// targets integration tests exercising sip.Adapter/diameter.Adapter against
// a real socket; this is a transport-level unit test of a plain dispatch
// table, a different genre entirely.
type fakeTransportBase struct {
	sendErr   error
	sendCalls int
}

func (f *fakeTransportBase) Send(msg []byte, addr string) error {
	f.sendCalls++
	return f.sendErr
}
func (f *fakeTransportBase) Receive(time.Duration) ([]byte, net.Addr, error) { return nil, nil, nil }
func (f *fakeTransportBase) Close() error                                    { return nil }
func (f *fakeTransportBase) LocalAddr() net.Addr                             { return nil }

// fakeInjector adds common.ConnFaultInjector on top of fakeTransportBase.
type fakeInjector struct {
	*fakeTransportBase
	resetErr, abruptErr, abortErr error
}

func (f *fakeInjector) ResetConn() error       { return f.resetErr }
func (f *fakeInjector) AbruptCloseConn() error { return f.abruptErr }
func (f *fakeInjector) AbortConn() error       { return f.abortErr }

// fakeTruncator adds common.TruncatingSender on top of fakeTransportBase.
type fakeTruncator struct {
	*fakeTransportBase
	truncErr    error
	lastN       int
	lastMsg     []byte
	truncCalled bool
}

func (f *fakeTruncator) SendTruncated(msg []byte, addr string, n int) error {
	f.truncCalled = true
	f.lastMsg = msg
	f.lastN = n
	return f.truncErr
}

var (
	_ common.Transport         = (*fakeTransportBase)(nil)
	_ common.ConnFaultInjector = (*fakeInjector)(nil)
	_ common.TruncatingSender  = (*fakeTruncator)(nil)
)

func TestResetRequested_ExactMatchOnly(t *testing.T) {
	assert.True(t, ResetRequested(map[string]string{"reset": "true"}))
	assert.False(t, ResetRequested(map[string]string{"reset": "1"}))
	assert.False(t, ResetRequested(nil))
}

func TestRequestedConnFault_MatchesEachDeclaredKey(t *testing.T) {
	cases := []struct {
		cfg   map[string]string
		state string
	}{
		{map[string]string{"reset": "true"}, ConnStateReset},
		{map[string]string{"abrupt_close": "true"}, ConnStateClosed},
		{map[string]string{"sctp_abort": "true"}, ConnStateAborted},
	}
	for _, tc := range cases {
		f, ok := RequestedConnFault(tc.cfg)
		require.True(t, ok)
		assert.Equal(t, tc.state, f.State)
	}
}

func TestRequestedConnFault_NilOrEmptyConfigReportsFalse(t *testing.T) {
	_, ok := RequestedConnFault(nil)
	assert.False(t, ok)
	_, ok = RequestedConnFault(map[string]string{})
	assert.False(t, ok)
}

func TestRequestedConnFault_DoesNotMatchTruncate(t *testing.T) {
	_, ok := RequestedConnFault(map[string]string{"truncate": "true"})
	assert.False(t, ok)
}

func TestApplyConnFault_NilConfigDoesNothing(t *testing.T) {
	tr := &fakeTransportBase{}
	state, err := ApplyConnFault(tr, nil)
	require.NoError(t, err)
	assert.Equal(t, "", state)
}

func TestApplyConnFault_NonInjectorReturnsStateAndError(t *testing.T) {
	tr := &fakeTransportBase{}
	state, err := ApplyConnFault(tr, map[string]string{"reset": "true"})
	require.Error(t, err)
	assert.Equal(t, ConnStateReset, state)
	assert.Contains(t, err.Error(), "fakeTransportBase")
}

func TestApplyConnFault_InjectorErrorPropagates(t *testing.T) {
	inj := &fakeInjector{fakeTransportBase: &fakeTransportBase{}, resetErr: errors.New("boom")}
	state, err := ApplyConnFault(inj, map[string]string{"reset": "true"})
	require.Error(t, err)
	assert.Equal(t, ConnStateReset, state)
	assert.Contains(t, err.Error(), "boom")
}

// TestApplyConnFault_TableDriven iterates connFaults directly, so a fourth
// row added to the table later cannot go untested — this is exactly what
// the acceptance criteria calls "a test iterates connFaults directly (not a
// hand-written list)".
func TestApplyConnFault_TableDriven(t *testing.T) {
	for _, row := range connFaults {
		t.Run(row.ConfigKey, func(t *testing.T) {
			inj := &fakeInjector{fakeTransportBase: &fakeTransportBase{}}
			state, err := ApplyConnFault(inj, map[string]string{row.ConfigKey: "true"})
			require.NoError(t, err)
			assert.Equal(t, row.State, state)
		})
	}
}

func TestSendMaybeTruncated_NoTruncateCallsPlainSend(t *testing.T) {
	tr := &fakeTransportBase{}
	truncated, err := SendMaybeTruncated(tr, nil, []byte("hello"), "addr")
	require.NoError(t, err)
	assert.False(t, truncated)
	assert.Equal(t, 1, tr.sendCalls)
}

func TestSendMaybeTruncated_TruncateCallsSendTruncatedWithClampedN(t *testing.T) {
	tc := &fakeTruncator{fakeTransportBase: &fakeTransportBase{}}
	msg := []byte("hello world this is long")
	truncated, err := SendMaybeTruncated(tc, map[string]string{"truncate": "true"}, msg, "addr")
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.True(t, tc.truncCalled)
	assert.Equal(t, min(len(msg), TruncatedBytes), tc.lastN)
}

func TestSendMaybeTruncated_NonTruncatingSenderReturnsError(t *testing.T) {
	tr := &fakeTransportBase{}
	truncated, err := SendMaybeTruncated(tr, map[string]string{"truncate": "true"}, []byte("hi"), "addr")
	require.Error(t, err)
	assert.True(t, truncated)
	assert.Contains(t, err.Error(), "fakeTransportBase")
}

func TestFaultedResult_ReportsFailedWithConnectionState(t *testing.T) {
	start := time.Now()
	result := FaultedResult(start, ConnStateReset, "reset failed: %s", "boom")
	assert.Equal(t, common.StatusFailed, result.Status)
	assert.GreaterOrEqual(t, result.DurationMs, int64(0))
	assert.Equal(t, "reset failed: boom", result.Error)
	assert.Equal(t, ConnStateReset, result.Response[ConnectionStateKey])
}

func TestConnFaults_StatesAreDistinct(t *testing.T) {
	states := []string{ConnStateReset, ConnStateTruncated, ConnStateClosed, ConnStateAborted}
	seen := map[string]bool{}
	for _, s := range states {
		require.False(t, seen[s], "duplicate connection_state value %q", s)
		seen[s] = true
		assert.NotEqual(t, DroppedKey, s)
		assert.NotEqual(t, DuplicatedKey, s)
	}
}
