package sip_test

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the SIP receive loop's deadline semantics. Before the fix,
// waitForFinalResponse handed the FULL step timeout to every Receive call
// afresh, so a peer trickling 180 Ringing could hold one step for
// SIPMaxProvisional × timeout, and when the provisional budget ran out it
// fell through returning the last 1xx with a nil error — which finishSend
// stamped passed. A peer stuck in a provisional loop was reported healthy.
// Both hazards need a peer that keeps sending provisionals and never a final,
// which the synchronous mock responder cannot express, so tricklingPeer is a
// minimal UDP peer that does exactly that.

// tricklingPeer answers every request with a stream of 180 Ringing sent every
// interval, forever, and never a final response.
type tricklingPeer struct {
	conn     *net.UDPConn
	interval time.Duration
	done     chan struct{}
	wg       sync.WaitGroup
}

func newTricklingPeer(t *testing.T, interval time.Duration) *tricklingPeer {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	require.NoError(t, err)
	conn, err := net.ListenUDP("udp", addr)
	require.NoError(t, err)
	p := &tricklingPeer{conn: conn, interval: interval, done: make(chan struct{})}
	p.wg.Add(1)
	go p.serve()
	t.Cleanup(func() {
		close(p.done)
		conn.Close()
		p.wg.Wait()
	})
	return p
}

func (p *tricklingPeer) addr() string { return p.conn.LocalAddr().String() }

func (p *tricklingPeer) serve() {
	defer p.wg.Done()
	buf := make([]byte, 65535)
	n, remote, err := p.conn.ReadFromUDP(buf)
	if err != nil {
		return
	}
	req, err := sip.ParseMessage(buf[:n])
	if err != nil {
		return
	}
	ringing := sip.Serialise(sip.BuildResponse(180, "Ringing", map[string]string{
		"From": req.Headers["From"], "To": req.Headers["To"] + ";tag=trickle",
		"Call-ID": req.Headers["Call-ID"], "CSeq": req.Headers["CSeq"], "Via": req.Headers["Via"],
	}))
	tick := time.NewTicker(p.interval)
	defer tick.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-tick.C:
			if _, err := p.conn.WriteToUDP(ringing, remote); err != nil {
				return
			}
		}
	}
}

func newDeadlineAdapter(t *testing.T, timeout time.Duration) *sip.Adapter {
	t.Helper()
	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	adapter := sip.NewAdapterWithTransport(impair.Wrap(tr), "udp", timeout)
	t.Cleanup(func() { adapter.Close() })
	return adapter
}

func sendInvite(t *testing.T, adapter *sip.Adapter, addr string, config map[string]string) (*common.StepResult, time.Duration) {
	t.Helper()
	start := time.Now()
	result, err := adapter.Execute("send", addr, "INVITE", map[string]string{
		"From": "<sip:447700900001@ims.local>;tag=dl001",
		"To":   "<sip:447700900002@ims.local>",
	}, config)
	require.NoError(t, err)
	return result, time.Since(start)
}

// TestAdapter_ProvisionalTrickle_FailsWithinOneTimeout is the headline
// regression. A peer sends 180 Ringing every 50 ms and never a final. With a
// 300 ms step timeout the old loop never timed out (each 50 ms gap was well
// inside a fresh 300 ms budget), ran all 50 provisional iterations — 2.5 s —
// and then reported the last 180 as a PASS. The step must now fail within
// roughly one timeout, and must be stamped timed_out.
func TestAdapter_ProvisionalTrickle_FailsWithinOneTimeout(t *testing.T) {
	peer := newTricklingPeer(t, 50*time.Millisecond)
	adapter := newDeadlineAdapter(t, 300*time.Millisecond)

	result, elapsed := sendInvite(t, adapter, peer.addr(), nil)

	assert.Equal(t, common.StatusFailed, result.Status, "a step that never receives a final response must not pass")
	assert.Equal(t, true, result.Response[impair.TimedOutKey], "exhausting the wait is a timeout by the documented contract")
	assert.Contains(t, result.Error, "timeout")
	assert.Less(t, elapsed, 1200*time.Millisecond,
		"the deadline is absolute: total wait must be ~1× timeout, not re-armed per provisional (old behaviour was ~2.5 s here)")
	assert.GreaterOrEqual(t, elapsed, 250*time.Millisecond, "the step must actually wait out its budget, not give up early")
}

// TestAdapter_ProvisionalBudgetExhausted_FailsAsTimeout covers the other
// exit: the provisional budget (CELLRIFT_SIP_MAX_PROVISIONAL) runs out before
// the deadline does. The peer answers each request with a burst of more
// provisionals than the budget allows, and no final. This is the exact path
// that used to fall out of the loop and return a 1xx with a nil error.
func TestAdapter_ProvisionalBudgetExhausted_FailsAsTimeout(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_MAX_PROVISIONAL", "3")
	mock := sip.NewMockUAS(t)
	t.Cleanup(mock.Close)
	mock.SetResponder(func(req *sip.SIPMessage) []*sip.SIPMessage {
		var out []*sip.SIPMessage
		for range 6 {
			out = append(out, sip.BuildResponse(180, "Ringing", map[string]string{
				"From": req.Headers["From"], "To": req.Headers["To"] + ";tag=burst",
				"Call-ID": req.Headers["Call-ID"], "CSeq": req.Headers["CSeq"], "Via": req.Headers["Via"],
			}))
		}
		return out
	})
	adapter := newDeadlineAdapter(t, 2*time.Second)

	result, elapsed := sendInvite(t, adapter, mock.Addr(), nil)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, true, result.Response[impair.TimedOutKey])
	assert.Contains(t, result.Error, "3 provisional responses")
	assert.Less(t, elapsed, 1500*time.Millisecond, "budget exhaustion returns as soon as the 4th provisional would be read")
	_, hasStatus := result.Response["status_code"]
	assert.False(t, hasStatus, "no final response was received, so no status must be reported as if one were")
}

// TestAdapter_StepTimeoutMs_HonouredOnSend pins that a step's own timeout_ms
// bounds the send path's receive. Only assert_state used to read it; the
// send path used the adapter default regardless, so a scenario declaring
// timeout_ms: 200 against a silent peer still waited the full adapter
// timeout. With a 2 s adapter and a 200 ms step timeout, the step must fail
// well under 2 s.
func TestAdapter_StepTimeoutMs_HonouredOnSend(t *testing.T) {
	mock := sip.NewMockUAS(t)
	t.Cleanup(mock.Close)
	mock.SetResponder(func(*sip.SIPMessage) []*sip.SIPMessage { return nil }) // silent peer
	adapter := newDeadlineAdapter(t, 2*time.Second)

	result, elapsed := sendInvite(t, adapter, mock.Addr(), map[string]string{"timeout_ms": "200"})

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, true, result.Response[impair.TimedOutKey])
	assert.Less(t, elapsed, 1000*time.Millisecond, "timeout_ms: 200 must override the 2 s adapter default")
	assert.GreaterOrEqual(t, elapsed, 150*time.Millisecond)
}

// TestAdapter_ProvisionalThenFinal_StillPasses is the control: a normal
// exchange with provisionals followed by a prompt final response must be
// unaffected by the deadline change. The default mock's INVITE handling
// (180 then 200) is exactly this shape.
func TestAdapter_ProvisionalThenFinal_StillPasses(t *testing.T) {
	mock := sip.NewMockUAS(t)
	t.Cleanup(mock.Close)
	adapter := newDeadlineAdapter(t, 2*time.Second)

	result, _ := sendInvite(t, adapter, mock.Addr(), nil)

	assert.Equal(t, common.StatusPassed, result.Status)
	assert.Equal(t, 200, result.Response["status_code"])
	assert.Equal(t, false, result.Response[impair.TimedOutKey])
}
