package diameter_test

import (
	"bytes"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reorderFields returns a distinct UAR field set for one labelled group
// member, carrying both its own Session-Id (echoed back by MockHSS's UAA)
// and its own User-Name (which MockHSS does NOT echo, but which reaches the
// wire on the REQUEST itself). Making every member genuinely distinguishable
// this way is what lets a test tell correct correlation apart from lucky
// ordering: a group where every member sent an identical request could never
// prove that.
func reorderFields(label string) map[string]string {
	return map[string]string{
		"Session-Id":      sessionIDFor(label),
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
		"User-Name":       userNameFor(label),
	}
}

// sessionIDFor and userNameFor are the two per-label distinct values every
// reorder test in this file keys its assertions on: Session-Id is echoed
// back by MockHSS's buildUAA, so it survives round-trip and proves
// answer-to-member correlation; User-Name is never echoed, but MockHSS
// records the exact request bytes it received, so decoding it back out of
// RecordedDiameterRequest.Raw proves arrival-order correlation instead.
func sessionIDFor(label string) string { return "cellrift;test;reorder;" + label }
func userNameFor(label string) string  { return "reorder-" + label + "@ims.local" }

// uarMember builds one common.ReorderMember sending a UAR with label's own
// distinct fields at the given SendOrder, optionally carrying its own
// impair: config (nil for none).
func uarMember(addr, label string, sendOrder int, config map[string]string) common.ReorderMember {
	return common.ReorderMember{
		Target:    addr,
		Message:   "UAR",
		Fields:    reorderFields(label),
		Config:    config,
		SendOrder: sendOrder,
	}
}

// recordedMember is one arrival decoded back into the label and Hop-by-Hop
// ID that produced it, so a test can assert on arrival ORDER (labels) and on
// the actual correlation KEY the implementation uses (Hop-by-Hop ID) rather
// than only on one or the other.
type recordedMember struct {
	label    string
	hopByHop uint32
}

// decodeRecorded parses each recorded request's raw bytes back into its
// User-Name field and Hop-by-Hop ID, in the SAME order MockHSS recorded
// them — arrival order. This is how a test observes wire order without the
// adapter needing to expose Hop-by-Hop IDs itself.
func decodeRecorded(t *testing.T, reqs []diameter.RecordedDiameterRequest) []recordedMember {
	t.Helper()
	out := make([]recordedMember, len(reqs))
	for i, r := range reqs {
		msg, err := diameter.ParseMessage(r.Raw)
		require.NoError(t, err)
		un := msg.FindAVP(diameter.AVPCodeUserName)
		require.NotNil(t, un, "every recorded UAR in this file must carry User-Name")
		require.Equal(t, r.HopByHopID, msg.HopByHopID, "RecordedDiameterRequest.HopByHopID must match the parsed message's own field")
		out[i] = recordedMember{label: un.StringVal, hopByHop: msg.HopByHopID}
	}
	return out
}

// labelsOf projects a []recordedMember down to just the labels, for a
// straightforward slice-equality assertion on arrival order.
func labelsOf(recorded []recordedMember) []string {
	labels := make([]string, len(recorded))
	for i, r := range recorded {
		labels[i] = r.label
	}
	return labels
}

// avpSessionID extracts response.avp["Session-Id"] from a passed StepResult,
// the value buildStepResult's responseFieldsMap populates from the parsed
// answer — the correlation proof every declaration-order test in this file
// relies on.
func avpSessionID(t *testing.T, result *common.StepResult) string {
	t.Helper()
	avpMap, ok := result.Response["avp"].(map[string]interface{})
	require.True(t, ok, "a passed result must carry a decoded avp map")
	sid, _ := avpMap["Session-Id"].(string)
	return sid
}

// TestAdapter_Reorder_PeerObservesSendOrderNotDeclarationOrder proves
// reorder's core Diameter claim: a 3-member group declared [A, B, C] with
// send_order [3, 1, 2] must reach MockHSS as B, then C, then A — the
// declared send_order, not the order the scenario lists the members in.
func TestAdapter_Reorder_PeerObservesSendOrderNotDeclarationOrder(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	members := common.ReorderMembers{
		uarMember(mock.Addr(), "A", 3, nil),
		uarMember(mock.Addr(), "B", 1, nil),
		uarMember(mock.Addr(), "C", 2, nil),
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.NoError(t, err)
	require.Len(t, results, 3)

	reqs := mock.RequestsOfCommand(diameter.CmdUserAuthorization)
	require.Len(t, reqs, 3)

	recorded := decodeRecorded(t, reqs)
	gotOrder := labelsOf(recorded)
	wantOrder := []string{userNameFor("B"), userNameFor("C"), userNameFor("A")}
	assert.Equal(t, wantOrder, gotOrder,
		"wire arrival order must follow send_order (B,C,A), which differs from declaration order (A,B,C)")

	assert.NotEqual(t, recorded[0].hopByHop, recorded[1].hopByHop, "each member's own Hop-by-Hop ID must be distinct")
	assert.NotEqual(t, recorded[1].hopByHop, recorded[2].hopByHop)
}

// TestAdapter_Reorder_ResultsReturnInDeclarationOrder is reorder's
// highest-value test: results must come back as [A, B, C] — declaration
// order — even though the wire saw B, C, A. Each result's own Session-Id
// (echoed by MockHSS's UAA) proves correlation survived the reorder, not
// just that three results of the right length were returned.
func TestAdapter_Reorder_ResultsReturnInDeclarationOrder(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	members := common.ReorderMembers{
		uarMember(mock.Addr(), "A", 3, nil),
		uarMember(mock.Addr(), "B", 1, nil),
		uarMember(mock.Addr(), "C", 2, nil),
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.NoError(t, err)
	require.Len(t, results, 3)

	for i, label := range []string{"A", "B", "C"} {
		require.Equal(t, common.StatusPassed, results[i].Status, "member %s should have passed: %s", label, results[i].Error)
		assert.Equal(t, sessionIDFor(label), avpSessionID(t, results[i]),
			"result %d must carry member %s's own Session-Id, proving correlation survived the reorder", i, label)
	}
}

// TestAdapter_Reorder_CorrelatesOutOfOrderAnswers proves correlation still
// holds when SetAnswerChunks forces every answer's framing to interleave
// across several physical reads — the case that would break a
// receiveAnswer-called-N-times implementation even before send_order enters
// the picture, since a split, out-of-order answer already exercises
// session.nextMessage's assembly loop under receiveAnswers' set-correlation.
func TestAdapter_Reorder_CorrelatesOutOfOrderAnswers(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()
	mock.SetAnswerChunks(2)

	members := common.ReorderMembers{
		uarMember(mock.Addr(), "A", 3, nil),
		uarMember(mock.Addr(), "B", 1, nil),
		uarMember(mock.Addr(), "C", 2, nil),
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.NoError(t, err)
	require.Len(t, results, 3)

	for i, label := range []string{"A", "B", "C"} {
		require.Equal(t, common.StatusPassed, results[i].Status,
			"member %s should have passed even with split answer framing: %s", label, results[i].Error)
		assert.Equal(t, sessionIDFor(label), avpSessionID(t, results[i]))
	}
}

// TestAdapter_Reorder_BuildFailureAbortsWholeGroup proves an unknown message
// type on ANY member aborts the whole group before a single byte reaches the
// wire. resolveCommand's failure is an unrecoverable Go error (not a
// protocol-level StepResult), so ExecuteReorder itself returns (nil, err) —
// read from prepareRequest's own documented contract, not assumed from this
// test's name.
func TestAdapter_Reorder_BuildFailureAbortsWholeGroup(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	members := common.ReorderMembers{
		uarMember(mock.Addr(), "A", 1, nil),
		{Target: mock.Addr(), Message: "NOPE", Fields: reorderFields("B"), SendOrder: 2},
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.Error(t, err, "an unknown message type must abort the whole group as a Go error")
	assert.Contains(t, err.Error(), "unknown diameter message type")
	assert.Nil(t, results)

	assert.Empty(t, mock.RequestsOfCommand(diameter.CmdUserAuthorization),
		"a group whose second member fails to build must send NOTHING, not just skip the failing member")
}

// TestAdapter_Reorder_MemberDropContributesNothingToTheWire proves reorder's
// composition with drop: the send_order:2 member never reaches the wire and
// never enters the group's Hop-by-Hop wait set, while the other two members
// — still written in THEIR OWN send_order — get real answers.
func TestAdapter_Reorder_MemberDropContributesNothingToTheWire(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	members := common.ReorderMembers{
		uarMember(mock.Addr(), "A", 3, nil),
		uarMember(mock.Addr(), "B", 1, nil),
		uarMember(mock.Addr(), "C", 2, map[string]string{"drop": "true"}),
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.NoError(t, err)
	require.Len(t, results, 3)

	reqs := mock.RequestsOfCommand(diameter.CmdUserAuthorization)
	require.Len(t, reqs, 2, "the dropped member must never reach the wire")
	gotOrder := labelsOf(decodeRecorded(t, reqs))
	assert.Equal(t, []string{userNameFor("B"), userNameFor("A")}, gotOrder,
		"the two surviving members must still arrive in their own send_order")

	assert.Equal(t, common.StatusPassed, results[2].Status, "the dropped member itself: %s", results[2].Error)
	dropped, ok := results[2].Response[impair.DroppedKey]
	require.True(t, ok, "the dropped member's own result must report response.dropped")
	assert.Equal(t, true, dropped)

	assert.Equal(t, common.StatusPassed, results[0].Status, "member A: %s", results[0].Error)
	assert.Equal(t, common.StatusPassed, results[1].Status, "member B: %s", results[1].Error)
	assert.Equal(t, sessionIDFor("A"), avpSessionID(t, results[0]))
	assert.Equal(t, sessionIDFor("B"), avpSessionID(t, results[1]))
}

// TestAdapter_Reorder_MemberDuplicateSendsTwiceAtItsPosition proves reorder's
// composition with duplicate: the send_order:1 member's bytes go out twice,
// back to back at its own position, before the send_order:2 member's single
// copy — three arrivals total, not two.
func TestAdapter_Reorder_MemberDuplicateSendsTwiceAtItsPosition(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	members := common.ReorderMembers{
		uarMember(mock.Addr(), "A", 2, nil),
		uarMember(mock.Addr(), "B", 1, map[string]string{"duplicate": "true"}),
	}

	results, err := adapter.ExecuteReorder(members, 0)
	require.NoError(t, err)
	require.Len(t, results, 2)

	reqs := mock.RequestsOfCommand(diameter.CmdUserAuthorization)
	require.Len(t, reqs, 3, "B's duplicated pair plus A's single send must total three arrivals")
	assert.True(t, bytes.Equal(reqs[0].Raw, reqs[1].Raw), "the first two arrivals must be B's byte-identical duplicate")
	assert.Equal(t, reqs[0].HopByHopID, reqs[1].HopByHopID)

	third := decodeRecorded(t, reqs[2:3])
	assert.Equal(t, userNameFor("A"), third[0].label, "the third arrival must be A, sent after B's duplicated pair")

	assert.Equal(t, common.StatusPassed, results[1].Status, "member B: %s", results[1].Error)
	duplicated, ok := results[1].Response[impair.DuplicatedKey]
	require.True(t, ok, "the duplicated member's own result must report response.duplicated")
	assert.Equal(t, true, duplicated)

	assert.Equal(t, common.StatusPassed, results[0].Status, "member A: %s", results[0].Error)
	_, hasDup := results[0].Response[impair.DuplicatedKey]
	assert.False(t, hasDup, "the non-duplicated member must not report response.duplicated")
}

// TestAdapter_Reorder_GroupTimeoutHonoured proves the group's own declared
// timeoutMs bounds the WHOLE wait, not the adapter's much longer default.
// MockHSS answers requests on one connection strictly one at a time
// (drainPending's loop), so SetAnswerChunks(61) — 60 sleeps of 5ms between
// chunks — makes each answer cost roughly 300ms of genuine wall-clock write
// time. A 450ms group budget sits deliberately between one answer's cost
// (~300ms, comfortably inside) and two answers' cumulative cost (~600ms,
// comfortably outside): member A's real answer always finishes first and
// well within budget; member B's answer never finishes arriving before the
// deadline, regardless of ordinary CI scheduling noise. The handshake is
// warmed up on an ordinary, un-chunked send first, so its own CEA (which
// SetAnswerChunks would otherwise also slow down) never eats into the
// group's own deadline, which is computed only once ExecuteReorder itself
// starts.
func TestAdapter_Reorder_GroupTimeoutHonoured(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 5*time.Second)
	defer cleanup()

	_, warm := executeUAR(t, adapter, mock.Addr(), nil)
	require.Equal(t, common.StatusPassed, warm.Status, "handshake warm-up must succeed before chunking is armed")

	mock.SetAnswerChunks(61)

	members := common.ReorderMembers{
		uarMember(mock.Addr(), "A", 1, nil),
		uarMember(mock.Addr(), "B", 2, nil),
	}

	callStart := time.Now()
	results, err := adapter.ExecuteReorder(members, 450)
	elapsed := time.Since(callStart)

	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Less(t, elapsed, 1*time.Second,
		"the group's own 450ms timeout must bound the call, not the adapter's 5s default")

	assert.Equal(t, common.StatusPassed, results[0].Status,
		"member A's answer must have finished arriving comfortably inside the deadline: %s", results[0].Error)
	assert.Equal(t, sessionIDFor("A"), avpSessionID(t, results[0]))

	assert.Equal(t, common.StatusFailed, results[1].Status,
		"member B's answer must not have finished arriving before the group deadline expired")

	// MockHSS is still slowly finishing B's chunked write on its own
	// goroutine after this test's own deadline gave up waiting for it.
	// Waiting here for that write to actually finish, before this test's
	// deferred cleanup closes the connection, avoids a pre-existing
	// self-deadlock in testing.go's writeResponse: it holds m.mu for the
	// whole write and then calls addError (which also locks m.mu) if that
	// write fails, which it would if the peer had already closed the
	// connection out from under it. This test avoids ever triggering that
	// path rather than changing testing.go.
	if remaining := time.Until(callStart.Add(900 * time.Millisecond)); remaining > 0 {
		time.Sleep(remaining)
	}
}

// TestAdapter_Reorder_AllMembersDroppedReturnsImmediately proves a group
// where every member declares drop never enters the group wait at all: with
// nothing written to the wire, there is nothing to wait for, and blocking
// the full declared timeout here would be the exact hang
// impair.DropRequested's single-step short-circuit already exists to avoid.
func TestAdapter_Reorder_AllMembersDroppedReturnsImmediately(t *testing.T) {
	mock, adapter, cleanup := newImpairTestAdapter(t, 2*time.Second)
	defer cleanup()

	drop := map[string]string{"drop": "true"}
	members := common.ReorderMembers{
		uarMember(mock.Addr(), "A", 1, drop),
		uarMember(mock.Addr(), "B", 2, drop),
	}

	start := time.Now()
	results, err := adapter.ExecuteReorder(members, 3000)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Less(t, elapsed, 500*time.Millisecond, "an all-dropped group must never enter the group wait")

	for i, r := range results {
		assert.Equal(t, common.StatusPassed, r.Status, "member %d must report passed/dropped, not failed", i)
		dropped, ok := r.Response[impair.DroppedKey]
		require.True(t, ok)
		assert.Equal(t, true, dropped)
	}

	assert.Empty(t, mock.RequestsOfCommand(diameter.CmdUserAuthorization))
}

// receiveCounter wraps a transport and records how many Receive calls are
// in flight at once. It exists to make the single-reader
// invariant a test result rather than a design claim: reordering is the
// first feature in this codebase to put more than one request in flight, so
// "no second goroutine is racing the adapter's reader" stops being obvious
// the moment anyone edits reorder.go.
//
// Connect is forwarded because Diameter's handshake requires a
// common.Connector; a wrapper that dropped it would fail the handshake
// before the invariant could be observed at all.
//
// This test proves this codebase's own reorder path holds the invariant on
// the scenarios exercised here; it is not a formal proof that no future
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

func (r *receiveCounter) Connect(addr string) error {
	return r.Transport.(common.Connector).Connect(addr)
}

// TestReorder_SingleGoroutineEverCallsReceive is the single-reader
// invariant: it wraps the real, impairment-decorated TCP transport in
// receiveCounter, runs a 3-member reorder group with send_order reversed
// against a real MockHSS, then asserts no more than one Receive call was
// ever in flight at once.
func TestReorder_SingleGoroutineEverCallsReceive(t *testing.T) {
	mock := diameter.NewMockHSS(t)
	defer mock.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	counter := &receiveCounter{Transport: impair.Wrap(tr)}
	adapter := diameter.NewAdapterWithTransport(counter, 2*time.Second)
	defer adapter.Close()

	members := common.ReorderMembers{
		uarMember(mock.Addr(), "A", 3, nil),
		uarMember(mock.Addr(), "B", 1, nil),
		uarMember(mock.Addr(), "C", 2, nil),
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
