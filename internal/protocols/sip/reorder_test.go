package sip_test

import (
	"bytes"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reorderCallID is the one per-label distinct value every reorder test in
// this file keys its assertions on. It is set explicitly on each member's
// own Fields, never adapter-generated, and the mock echoes it back
// unchanged in every response — which is what lets a test tell correct
// positional correlation apart from coincidental ordering.
func reorderCallID(label string) string { return "cellrift-reorder-" + label }

// reorderFields returns a distinct field set for one labelled reorder-group
// member, carrying that member's own explicit Call-ID.
func reorderFields(callID string) map[string]string {
	return map[string]string{
		"To":      "sip:alice@example.test",
		"From":    "sip:cellrift@example.test",
		"Call-ID": callID,
	}
}

// reorderMember builds one common.ReorderMember sending a REGISTER with
// label's own distinct Call-ID at the given SendOrder, optionally carrying
// its own impair: config (nil for none). REGISTER is used throughout so
// mockResponses produces exactly one response per request and the response
// arithmetic in these tests is unambiguous.
func reorderMember(addr, label string, sendOrder int, config map[string]string) common.ReorderMember {
	return common.ReorderMember{
		Target:    addr,
		Message:   "REGISTER",
		Fields:    reorderFields(reorderCallID(label)),
		Config:    config,
		SendOrder: sendOrder,
	}
}

// resultCallID extracts the echoed Call-ID from a passed StepResult's own
// response headers — the correlation proof every test below relies on.
func resultCallID(t *testing.T, result *common.StepResult) string {
	t.Helper()
	headers, ok := result.Response["header"].(map[string]interface{})
	require.True(t, ok, "a passed result must carry a decoded header map")
	callID, _ := headers[sip.HeaderCallID].(string)
	return callID
}

// TestAdapter_Reorder_PeerObservesSendOrderNotDeclarationOrder proves
// reorder's core SIP claim: a 3-member group declared [A, B, C] with
// send_order [3, 1, 2] must reach MockUASServer as B, then C, then A — the
// declared send_order, not the order the scenario lists the members in.
func TestAdapter_Reorder_PeerObservesSendOrderNotDeclarationOrder(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	members := common.ReorderMembers{
		reorderMember(mock.Addr(), "A", 3, nil),
		reorderMember(mock.Addr(), "B", 1, nil),
		reorderMember(mock.Addr(), "C", 2, nil),
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.NoError(t, err)
	require.Len(t, results, 3)

	reqs := mock.ReceivedRequests()
	require.Len(t, reqs, 3)
	gotOrder := []string{reqs[0].CallID, reqs[1].CallID, reqs[2].CallID}
	wantOrder := []string{reorderCallID("B"), reorderCallID("C"), reorderCallID("A")}
	assert.Equal(t, wantOrder, gotOrder,
		"wire arrival order must follow send_order (B,C,A), which differs from declaration order (A,B,C)")
}

// TestAdapter_Reorder_ResultsReturnInDeclarationOrder is reorder's
// highest-value test: results must come back as [A, B, C] — declaration
// order — even though the wire saw B, C, A. Each result's own echoed
// Call-ID proves the send-order read was remapped correctly, not just that
// three passed results of the right length were returned.
func TestAdapter_Reorder_ResultsReturnInDeclarationOrder(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	members := common.ReorderMembers{
		reorderMember(mock.Addr(), "A", 3, nil),
		reorderMember(mock.Addr(), "B", 1, nil),
		reorderMember(mock.Addr(), "C", 2, nil),
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.NoError(t, err)
	require.Len(t, results, 3)

	for i, label := range []string{"A", "B", "C"} {
		require.Equal(t, common.StatusPassed, results[i].Status, "member %s should have passed: %s", label, results[i].Error)
		assert.Equal(t, reorderCallID(label), resultCallID(t, results[i]),
			"result %d must carry member %s's own Call-ID, proving the send-order read was remapped correctly", i, label)
	}
}

// TestAdapter_Reorder_BuildFailureAbortsWholeGroup proves a build failure on
// ANY member aborts the whole group before a single byte reaches the wire.
// SIP's own convention (adapters return (result, nil) on protocol-level
// failure, never (nil, err), for anything short of an infrastructure
// error) holds here too: an unresolvable body template is a StepResult
// failure, not a Go error.
func TestAdapter_Reorder_BuildFailureAbortsWholeGroup(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	badFields := reorderFields(reorderCallID("B"))
	badFields["__body_template"] = "does-not-exist"

	members := common.ReorderMembers{
		reorderMember(mock.Addr(), "A", 1, nil),
		{Target: mock.Addr(), Message: "REGISTER", Fields: badFields, SendOrder: 2},
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.NoError(t, err, "a SIP build failure is a StepResult, not a Go error")
	require.Len(t, results, 2)
	for i, r := range results {
		assert.Equal(t, common.StatusFailed, r.Status, "member %d must report the aborted group's failure", i)
		assert.Contains(t, r.Error, "body template")
	}

	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, mock.ReceivedRequests(),
		"a group whose second member fails to build must send NOTHING, not just skip the failing member")
}

// TestAdapter_Reorder_MemberDropDoesNotShiftTheRemap proves reorder's
// composition with drop, and is the test that catches an off-by-one in the
// read mapping: the send_order:2 member never reaches the wire, while the
// other two members — still written in THEIR OWN send_order — get their own
// real responses correctly attributed, not shifted by the gap the dropped
// member leaves behind.
func TestAdapter_Reorder_MemberDropDoesNotShiftTheRemap(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	members := common.ReorderMembers{
		reorderMember(mock.Addr(), "A", 3, nil),
		reorderMember(mock.Addr(), "B", 1, nil),
		reorderMember(mock.Addr(), "C", 2, map[string]string{"drop": "true"}),
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.NoError(t, err)
	require.Len(t, results, 3)

	reqs := mock.ReceivedRequests()
	require.Len(t, reqs, 2, "the dropped member must never reach the wire")
	gotOrder := []string{reqs[0].CallID, reqs[1].CallID}
	assert.Equal(t, []string{reorderCallID("B"), reorderCallID("A")}, gotOrder,
		"the two surviving members must still arrive in their own send_order")

	require.Equal(t, common.StatusPassed, results[2].Status, "the dropped member itself: %s", results[2].Error)
	dropped, ok := results[2].Response[impair.DroppedKey]
	require.True(t, ok, "the dropped member's own result must report response.dropped")
	assert.Equal(t, true, dropped)

	require.Equal(t, common.StatusPassed, results[0].Status, "member A: %s", results[0].Error)
	require.Equal(t, common.StatusPassed, results[1].Status, "member B: %s", results[1].Error)
	assert.Equal(t, reorderCallID("A"), resultCallID(t, results[0]),
		"member A's own result must carry its own Call-ID, not shifted by the dropped member in between")
	assert.Equal(t, reorderCallID("B"), resultCallID(t, results[1]))
}

// TestAdapter_Reorder_MemberDuplicateSendsTwiceAtItsPosition proves reorder's
// composition with duplicate: the send_order:1 member's bytes go out twice,
// back to back at its own position, before the send_order:2 member's single
// copy — three arrivals total — and the OTHER member's own result carries
// its own Call-ID, not B's drained duplicate echo.
func TestAdapter_Reorder_MemberDuplicateSendsTwiceAtItsPosition(t *testing.T) {
	mock, adapter := newMockAndAdapter(t, 2*time.Second)
	defer mock.Close()
	defer adapter.Close()

	members := common.ReorderMembers{
		reorderMember(mock.Addr(), "A", 2, nil),
		reorderMember(mock.Addr(), "B", 1, map[string]string{"duplicate": "true"}),
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.NoError(t, err)
	require.Len(t, results, 2)

	reqs := mock.ReceivedRequests()
	require.Len(t, reqs, 3, "B's duplicated pair plus A's single send must total three arrivals")
	assert.True(t, bytes.Equal(reqs[0].Raw, reqs[1].Raw), "the first two arrivals must be B's byte-identical duplicate")
	assert.Equal(t, reqs[0].CallID, reqs[1].CallID)
	assert.Equal(t, reqs[0].CSeq, reqs[1].CSeq)
	assert.Equal(t, reorderCallID("A"), reqs[2].CallID, "the third arrival must be A, sent after B's duplicated pair")

	require.Equal(t, common.StatusPassed, results[1].Status, "member B: %s", results[1].Error)
	duplicated, ok := results[1].Response[impair.DuplicatedKey]
	require.True(t, ok, "the duplicated member's own result must report response.duplicated")
	assert.Equal(t, true, duplicated)
	assert.Equal(t, reorderCallID("B"), resultCallID(t, results[1]))

	require.Equal(t, common.StatusPassed, results[0].Status, "member A: %s", results[0].Error)
	_, hasDup := results[0].Response[impair.DuplicatedKey]
	assert.False(t, hasDup, "the non-duplicated member must not report response.duplicated")
	assert.Equal(t, reorderCallID("A"), resultCallID(t, results[0]),
		"member A's own result must carry its own Call-ID, not B's drained duplicate echo")
}

// unreachableUDPAddr returns a UDP address that briefly had a real listener
// and now has none: a datagram sent to it goes nowhere, deterministically,
// without depending on some fixed well-known port being closed in every
// environment this suite happens to run in.
func unreachableUDPAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	addr := conn.LocalAddr().String()
	require.NoError(t, conn.Close())
	return addr
}

// TestAdapter_Reorder_GroupTimeoutHonoured proves the group's own declared
// timeoutMs bounds the whole call: with nothing ever answering, the group
// must not wait once per member for the adapter's own, much longer default
// timeout, and every member must report its own failure.
func TestAdapter_Reorder_GroupTimeoutHonoured(t *testing.T) {
	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	adapter := sip.NewAdapterWithTransport(impair.Wrap(tr), "udp", 500*time.Millisecond)
	defer adapter.Close()

	addr := unreachableUDPAddr(t)
	members := common.ReorderMembers{
		reorderMember(addr, "A", 1, nil),
		reorderMember(addr, "B", 2, nil),
	}

	callStart := time.Now()
	results, callErr := adapter.ExecuteReorder(members, 400)
	elapsed := time.Since(callStart)

	require.NoError(t, callErr)
	require.Len(t, results, 2)
	assert.Less(t, elapsed, 2*time.Second,
		"the group's own 400ms timeout must bound the call, not two full adapter default timeouts")

	for i, r := range results {
		assert.Equal(t, common.StatusFailed, r.Status, "member %d must report its own failure: nothing ever answered", i)
	}
}

// receiveCounter wraps a transport and records how many Receive calls are in
// flight at once. It exists to make the single-reader
// invariant a test result rather than a design claim, mirroring the
// receiveCounter in package diameter — duplicated here, not
// shared, since neither package exports test helpers and a shared helper
// would have to move into production code to be importable.
//
// Unlike the Diameter version, this wrapper does NOT declare a Connect
// method. SIP's UDP transport does not implement common.Connector, and
// declaring Connect here unconditionally would make the wrapped UDP
// transport falsely satisfy common.Connector — exactly the trap
// impair.Wrap's two-concrete-types design (connectorTransport vs Transport)
// exists to avoid. Do not "fix" this omission.
//
// This test proves this codebase's own SIP reorder path holds the invariant
// on the scenarios exercised here; it is not a formal proof that no future
// edit can break it, which is exactly why it runs on every
// `go test -race ./...`.
type receiveCounter struct {
	common.Transport
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	total       atomic.Int32
	violations  atomic.Int32
}

func (r *receiveCounter) Receive(timeout time.Duration) ([]byte, net.Addr, error) {
	n := r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	r.total.Add(1)
	if n > 1 {
		r.violations.Add(1)
	}
	for {
		m := r.maxInFlight.Load()
		if n <= m || r.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	return r.Transport.Receive(timeout)
}

// TestReorder_SingleGoroutineEverCallsReceive is the single-reader
// invariant: it wraps the real, impairment-decorated UDP transport in
// receiveCounter, runs a 3-member reorder group with send_order reversed
// against a real MockUASServer, then asserts no more than one Receive call
// was ever in flight at once.
func TestReorder_SingleGoroutineEverCallsReceive(t *testing.T) {
	mock := sip.NewMockUAS(t)
	defer mock.Close()

	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)

	counter := &receiveCounter{Transport: impair.Wrap(tr)}
	adapter := sip.NewAdapterWithTransport(counter, "udp", 2*time.Second)
	defer adapter.Close()

	members := common.ReorderMembers{
		reorderMember(mock.Addr(), "A", 3, nil),
		reorderMember(mock.Addr(), "B", 1, nil),
		reorderMember(mock.Addr(), "C", 2, nil),
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.NoError(t, err)
	for i, r := range results {
		require.Equal(t, common.StatusPassed, r.Status, "member %d: %s", i, r.Error)
	}

	assert.Greater(t, counter.total.Load(), int32(0),
		"the group never read from the transport at all — the invariant assertion below would be vacuous")
	assert.Equal(t, int32(1), counter.maxInFlight.Load(),
		"more than one goroutine called Receive concurrently during a reorder group")
	assert.Equal(t, int32(0), counter.violations.Load())
}
