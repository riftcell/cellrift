package common_test

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingTCPServer is an in-process TCP peer that counts Accept calls and
// echoes every message it reads back on the SAME connection, prefixed with
// "ACK:". Persistent-socket reuse is observable only if the server both
// counts accepts and keeps serving the same conn across multiple
// request/response pairs — a server that accepted once and replied once
// would not distinguish reuse from redial.
type countingTCPServer struct {
	ln         net.Listener
	acceptCnt  atomic.Int32
	silent     bool
	closeEarly bool

	mu    sync.Mutex
	conns []net.Conn
}

// newCountingTCPServer starts a counting TCP peer on 127.0.0.1:0 and
// registers its teardown with t.Cleanup.
func newCountingTCPServer(t *testing.T) *countingTCPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &countingTCPServer{ln: ln}
	go s.serve()
	t.Cleanup(s.close)
	return s
}

// newSilentCountingTCPServer accepts connections (so acceptCnt is still
// meaningful) but never reads or writes, simulating a peer that has gone
// quiet without closing the socket.
func newSilentCountingTCPServer(t *testing.T) *countingTCPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &countingTCPServer{ln: ln, silent: true}
	go s.serve()
	t.Cleanup(s.close)
	return s
}

// newCloseEarlyTCPServer accepts a connection, counts it, then immediately
// closes its side — simulating a peer that has gone away.
func newCloseEarlyTCPServer(t *testing.T) *countingTCPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &countingTCPServer{ln: ln, closeEarly: true}
	go s.serve()
	t.Cleanup(s.close)
	return s
}

func (s *countingTCPServer) addr() string {
	return s.ln.Addr().String()
}

func (s *countingTCPServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed — expected at test teardown
		}
		s.acceptCnt.Add(1)
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()

		if s.closeEarly {
			conn.Close()
			continue
		}
		if s.silent {
			continue // accept but never read/write
		}
		go s.echo(conn)
	}
}

// echo reads one message and writes a fixed reply, repeatedly, on the same
// connection — this loop is what makes "persistent socket" observable: a
// second Send/Receive pair that hits the same acceptCnt proves reuse.
func (s *countingTCPServer) echo(conn net.Conn) {
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		if _, err := conn.Write(append([]byte("ACK:"), buf[:n]...)); err != nil {
			return
		}
	}
}

func (s *countingTCPServer) close() {
	_ = s.ln.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
}

func TestTCPTransport_ConnectReusesConnectionAcrossSendReceive(t *testing.T) {
	server := newCountingTCPServer(t)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	require.NoError(t, tr.Connect(server.addr()))

	require.NoError(t, tr.Send([]byte("first"), server.addr()))
	got, _, err := tr.Receive(2 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, "ACK:first", string(got))

	require.NoError(t, tr.Send([]byte("second"), server.addr()))
	got, _, err = tr.Receive(2 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, "ACK:second", string(got))

	assert.Equal(t, int32(1), server.acceptCnt.Load(),
		"connection must be reused, not redialled, across two Send/Receive pairs")
}

func TestTCPTransport_NoConnectDialsPerSend(t *testing.T) {
	server := newCountingTCPServer(t)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	// No Connect call — this is SIP's path.
	require.NoError(t, tr.Send([]byte("first"), server.addr()))
	_, _, err = tr.Receive(2 * time.Second)
	require.NoError(t, err)

	require.NoError(t, tr.Send([]byte("second"), server.addr()))
	_, _, err = tr.Receive(2 * time.Second)
	require.NoError(t, err)

	assert.Equal(t, int32(2), server.acceptCnt.Load(),
		"un-connected path must dial a fresh connection per Send")
}

func TestTCPTransport_SendAddrMismatch(t *testing.T) {
	server := newCountingTCPServer(t)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	require.NoError(t, tr.Connect(server.addr()))

	// Round-trip once before touching acceptCnt. A TCP connect completes in the
	// kernel without the server's accept goroutine necessarily having been
	// scheduled, so asserting on acceptCnt straight after Connect races that
	// goroutine. Receiving the ACK proves the server accepted and served, which
	// is the happens-before barrier the assertion below needs.
	//
	// The TLS sibling needs no such barrier: a TLS handshake requires the server
	// to participate, so by the time Connect returns the accept has happened.
	require.NoError(t, tr.Send([]byte("warmup"), server.addr()))
	got, _, err := tr.Receive(2 * time.Second)
	require.NoError(t, err)
	require.Equal(t, "ACK:warmup", string(got))
	require.Equal(t, int32(1), server.acceptCnt.Load(), "baseline: one accept from Connect")

	err = tr.Send([]byte("hello"), "127.0.0.1:1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), server.addr())
	assert.Contains(t, err.Error(), "127.0.0.1:1")

	assert.Equal(t, int32(1), server.acceptCnt.Load(),
		"a mismatched Send must not dial anything")
}

func TestTCPTransport_ReceiveTimeoutKeepsConnectionUsable(t *testing.T) {
	server := newSilentCountingTCPServer(t)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	require.NoError(t, tr.Connect(server.addr()))
	require.NoError(t, tr.Send([]byte("ping"), server.addr()))

	_, _, err = tr.Receive(50 * time.Millisecond)
	require.Error(t, err, "silent server should cause a read timeout")

	// The connection must still be usable: switch the server to answer and
	// retry on the SAME socket. Since newSilentCountingTCPServer never reads
	// or writes, we can't get an ACK from it — but we CAN prove no redial
	// happened by checking acceptCnt stays 1 after the timeout.
	assert.Equal(t, int32(1), server.acceptCnt.Load(),
		"a read timeout must not cause a redial")

	// A further Send on the same held connection must not trigger a new dial.
	require.NoError(t, tr.Send([]byte("ping-again"), server.addr()))
	assert.Equal(t, int32(1), server.acceptCnt.Load(),
		"acceptCnt must remain 1 after retrying on the same held connection")
}

func TestTCPTransport_PeerCloseMakesSendFailLoudly(t *testing.T) {
	server := newCloseEarlyTCPServer(t)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	require.NoError(t, tr.Connect(server.addr()))

	_, _, err = tr.Receive(2 * time.Second)
	require.Error(t, err, "peer closed its side; Receive must surface an error")

	err = tr.Send([]byte("after-close"), server.addr())
	require.Error(t, err, "Send after the held connection died must fail loudly, not silently redial")
	assert.Contains(t, err.Error(), "reconnect")

	assert.Equal(t, int32(1), server.acceptCnt.Load(),
		"a dead held connection must not be silently replaced by a fresh dial")
}

// TestTCPTransport_LocalAddrAfterConnect pins the fix for a pre-existing
// defect: LocalAddr() must report the connected outbound socket's address
// once Connect has succeeded, not the transport's own listener (which is
// bound to ":0" and would resolve to the wildcard address, unusable as a
// CER's Host-IP-Address). Asserting mere non-emptiness would pass today —
// the bar is "differs from the pre-Connect value AND is not the unspecified
// address".
func TestTCPTransport_LocalAddrAfterConnect(t *testing.T) {
	server := newCountingTCPServer(t)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	before := tr.LocalAddr()

	require.NoError(t, tr.Connect(server.addr()))

	after := tr.LocalAddr()

	assert.NotEqual(t, before.String(), after.String(),
		"LocalAddr after Connect must differ from the pre-Connect listener address")

	host, _, err := net.SplitHostPort(after.String())
	require.NoError(t, err)
	ip := net.ParseIP(host)
	require.NotNil(t, ip, "LocalAddr after Connect must be a parseable IP")
	assert.False(t, ip.IsUnspecified(), "LocalAddr after Connect must not be the wildcard address")
}

// TestTLSTransport_LocalAddrAfterConnect is TCP's parity case for
// TLSTransport, using the countingTLSServer/selfSignedTLS helpers already
// defined in connect_tls_test.go (same package, no duplication needed).
func TestTLSTransport_LocalAddrAfterConnect(t *testing.T) {
	server := newCountingTLSServer(t)
	_, clientCfg := selfSignedTLS(t)

	tr, err := common.NewTLSTransport(":0", clientCfg)
	require.NoError(t, err)
	defer tr.Close()

	before := tr.LocalAddr()

	require.NoError(t, tr.Connect(server.addr()))

	after := tr.LocalAddr()

	assert.NotEqual(t, before.String(), after.String(),
		"LocalAddr after Connect must differ from the pre-Connect listener address")

	host, _, err := net.SplitHostPort(after.String())
	require.NoError(t, err)
	ip := net.ParseIP(host)
	require.NotNil(t, ip, "LocalAddr after Connect must be a parseable IP")
	assert.False(t, ip.IsUnspecified(), "LocalAddr after Connect must not be the wildcard address")
}

// TestTCPTransport_LocalAddrUnconnectedIsListener pins SIP's behaviour: a
// transport that has never called Connect must keep reporting the listener's
// address, INCLUDING after a dial-per-send Send has stashed a reply socket
// (connState.stash sets conn but leaves addr ""). This is the assertion that
// would catch a fix that keyed on `conn != nil` instead of `addr != ""` —
// SIP's dial-per-send path stashes a conn too, and must not be affected.
func TestTCPTransport_LocalAddrUnconnectedIsListener(t *testing.T) {
	server := newCountingTCPServer(t)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	listenerAddr := tr.LocalAddr().String()

	// No Connect call — dial-per-send stashes a reply socket (SIP's path).
	require.NoError(t, tr.Send([]byte("first"), server.addr()))
	_, _, err = tr.Receive(2 * time.Second)
	require.NoError(t, err)

	assert.Equal(t, listenerAddr, tr.LocalAddr().String(),
		"LocalAddr must still report the listener after a dial-per-send stash, since Connect was never called")
}

func TestTCPTransport_CloseUnblocksBlockedReceive(t *testing.T) {
	server := newSilentCountingTCPServer(t)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	require.NoError(t, tr.Connect(server.addr()))

	done := make(chan error, 1)
	go func() {
		_, _, err := tr.Receive(30 * time.Second)
		done <- err
	}()

	// Give the goroutine time to enter Receive/Read before closing.
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, tr.Close())

	select {
	case err := <-done:
		assert.Error(t, err, "Close should unblock Receive with an error")
	case <-time.After(2 * time.Second):
		t.Fatal("Receive did not unblock within 2s of Close")
	}
}
