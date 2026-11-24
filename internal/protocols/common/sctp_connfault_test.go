package common

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ishidawataru/sctp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sctpPeer is a minimal in-process SCTP listener for fault tests: it accepts
// one association, reads whatever arrives, and records it. It is not a
// protocol mock — no Diameter framing, no answers — so it observes exactly
// the bytes a fault put on the wire and nothing else.
type sctpPeer struct {
	ln   *sctp.SCTPListener
	addr string
	mu   sync.Mutex
	read [][]byte
}

// newSCTPPeer starts the listener, skipping the calling test when the kernel
// has no usable SCTP. It binds one explicit loopback IP rather than the
// wildcard: see sctp.go's dialLocalAddr doc comment — a wildcard-bound SCTP
// socket's Addr().String() is slash-joined across every interface and is not
// a dialable host:port.
func newSCTPPeer(t *testing.T) *sctpPeer {
	t.Helper()
	laddr, err := sctp.ResolveSCTPAddr("sctp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("sctp unavailable on this machine: %v", err)
	}
	ln, err := sctp.ListenSCTP("sctp", laddr)
	if err != nil {
		t.Skipf("sctp unavailable on this machine: %v", err)
	}
	p := &sctpPeer{ln: ln, addr: ln.Addr().String()}
	go p.acceptLoop()
	t.Cleanup(func() { ln.Close() })
	return p
}

// acceptLoop accepts every inbound association for the lifetime of the
// listener, handing each one to its own readLoop. It exits cleanly once the
// test's Cleanup closes the listener.
func (p *sctpPeer) acceptLoop() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.readLoop(conn)
	}
}

// readLoop appends every Read's bytes to p.read under the mutex, deep-copying
// each chunk so a later reused buffer can never corrupt an already-recorded
// read — the same discipline the protocol mocks follow.
func (p *sctpPeer) readLoop(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			p.mu.Lock()
			p.read = append(p.read, chunk)
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Reads returns a deep copy of every chunk observed so far.
func (p *sctpPeer) Reads() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.read))
	for i, r := range p.read {
		out[i] = append([]byte(nil), r...)
	}
	return out
}

// waitForPeerReads polls peer.Reads() until at least n chunks have arrived or
// the deadline expires, failing the test loudly on timeout rather than
// racing an unsynchronised assertion against the peer's own goroutine.
func waitForPeerReads(t *testing.T, peer *sctpPeer, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(peer.Reads()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("peer never observed %d read(s), got %d", n, len(peer.Reads()))
}

// newSCTPFaultTransport builds a *SCTPTransport via the unexported,
// non-falling-back constructor — NewSCTPOrFallbackTransport would silently
// hand back a TCP transport on a kernel without SCTP and invalidate every
// assertion here — and connects it to peer, skipping the calling test if
// SCTP itself is unavailable.
func newSCTPFaultTransport(t *testing.T, peer *sctpPeer) *SCTPTransport {
	t.Helper()
	tr, err := newSCTPTransport(":0")
	if err != nil {
		t.Skipf("sctp unavailable on this machine: %v", err)
	}
	sctpTr, ok := tr.(*SCTPTransport)
	require.True(t, ok, "newSCTPTransport must return *SCTPTransport")
	t.Cleanup(func() { sctpTr.Close() })
	require.NoError(t, sctpTr.Connect(peer.addr))
	return sctpTr
}

// TestSCTPTransport_AbortConn_NextSendReportsConnectionGone proves the
// bookkeeping guarantee AbortConn exists to provide: whatever the local
// kernel does with the SCTP_ABORT write itself, this transport's own
// connState is cleared directly (not left to isDeadConnErr classification),
// so the very next Send fails through the existing "is gone, reconnect
// required" path rather than surfacing a raw syscall error.
//
// It deliberately does not require AbortConn() itself to return nil. On the
// machine this was developed on, sc.SCTPWrite(nil,
// &sctp.SndRcvInfo{Flags: SCTP_ABORT}) returned syscall.EINVAL every time,
// even though the encoded sinfo_flags bit matched that machine's own kernel
// header definition exactly, and even though ordinary SCTP INIT, COOKIE
// ECHO/ACK, DATA and graceful SHUTDOWN all worked correctly on the same
// kernel (every other test in this package proves that). This has not yet
// been re-checked on GitHub Actions ubuntu-latest, this project's own
// reference SCTP environment, so it is recorded here rather than assumed
// resolved.
func TestSCTPTransport_AbortConn_NextSendReportsConnectionGone(t *testing.T) {
	peer := newSCTPPeer(t)
	tr := newSCTPFaultTransport(t, peer)

	require.NoError(t, tr.Send([]byte("first"), peer.addr))
	waitForPeerReads(t, peer, 1)

	abortErr := tr.AbortConn()
	t.Logf("AbortConn() returned: %v (see this test's doc comment for what a non-nil value here means on this machine)", abortErr)

	err := tr.Send([]byte("second"), peer.addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is gone, reconnect required")
}

func TestSCTPTransport_AbruptCloseConn_NextSendReportsConnectionGone(t *testing.T) {
	peer := newSCTPPeer(t)
	tr := newSCTPFaultTransport(t, peer)

	require.NoError(t, tr.Send([]byte("first"), peer.addr))
	waitForPeerReads(t, peer, 1)

	require.NoError(t, tr.AbruptCloseConn())

	err := tr.Send([]byte("second"), peer.addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is gone, reconnect required")
}

func TestSCTPTransport_ResetConn_RefusesAndLeavesAssociationUsable(t *testing.T) {
	peer := newSCTPPeer(t)
	tr := newSCTPFaultTransport(t, peer)

	err := tr.ResetConn()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sctp_abort")

	// The association was untouched — a Send right after ResetConn's refusal
	// still succeeds.
	require.NoError(t, tr.Send([]byte("still works"), peer.addr))
	waitForPeerReads(t, peer, 1)
}

func TestSCTPTransport_SendTruncated_PeerReadsShortFrame(t *testing.T) {
	peer := newSCTPPeer(t)
	tr := newSCTPFaultTransport(t, peer)

	msg := []byte("0123456789abcdef")
	require.NoError(t, tr.SendTruncated(msg, peer.addr, 8))

	waitForPeerReads(t, peer, 1)
	reads := peer.Reads()
	require.Len(t, reads, 1)
	assert.Equal(t, msg[:8], reads[0])
}

// TestIsDeadConnErr_DoesNotMatchClosedSCTPConn pins the finding that made
// the connection-fault code call dropConn() directly instead of relying on error
// classification: a closed *sctp.SCTPConn's next Write returns a raw
// syscall.EBADF (the vendored library swaps its internal fd to -1), which is
// neither io.EOF, io.ErrUnexpectedEOF nor net.ErrClosed. The TCP half of the
// same test is the contrast that makes this meaningful: the identical
// sequence on a *net.TCPConn DOES produce an error isDeadConnErr recognises.
//
// If this test ever starts failing on the SCTP half, the vendored library
// changed its close semantics and the direct dropConn() calls in
// connfault.go/sctp.go could in principle be simplified — but DO NOT
// simplify them on the strength of this test alone: the direct call is also
// what makes the three transports behave uniformly, which is worth keeping
// independently.
func TestIsDeadConnErr_DoesNotMatchClosedSCTPConn(t *testing.T) {
	peer := newSCTPPeer(t)

	laddr, err := sctp.ResolveSCTPAddr("sctp", "127.0.0.1:0")
	require.NoError(t, err)
	raddr, err := sctp.ResolveSCTPAddr("sctp", peer.addr)
	require.NoError(t, err)
	conn, err := sctp.DialSCTPExt("sctp", laddr, raddr, sctp.InitMsg{})
	require.NoError(t, err)

	require.NoError(t, conn.Close())
	_, writeErr := conn.Write([]byte("x"))
	require.Error(t, writeErr)
	assert.False(t, isDeadConnErr(writeErr),
		"a closed *sctp.SCTPConn's next Write must NOT be classified dead by isDeadConnErr")

	// TCP contrast: the identical close-then-write sequence on a
	// *net.TCPConn DOES produce an error isDeadConnErr recognises.
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer tcpLn.Close()
	tcpConn, err := net.Dial("tcp", tcpLn.Addr().String())
	require.NoError(t, err)
	require.NoError(t, tcpConn.Close())
	_, tcpWriteErr := tcpConn.Write([]byte("x"))
	require.Error(t, tcpWriteErr)
	assert.True(t, isDeadConnErr(tcpWriteErr),
		"a closed *net.TCPConn's next Write must be classified dead by isDeadConnErr — the contrast that makes the SCTP result meaningful")
}
