package common_test

import (
	"crypto/tls"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingTLSServer is the TLS twin of countingTCPServer. It reuses the
// existing selfSignedTLS(t) helper from tls_test.go — both live in package
// common_test, so it is directly callable without duplicating cert
// generation.
type countingTLSServer struct {
	ln        net.Listener
	acceptCnt atomic.Int32
	silent    bool

	mu    sync.Mutex
	conns []net.Conn
}

func newCountingTLSServer(t *testing.T) *countingTLSServer {
	t.Helper()
	serverCfg, _ := selfSignedTLS(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	require.NoError(t, err)
	s := &countingTLSServer{ln: ln}
	go s.serve()
	t.Cleanup(s.close)
	return s
}

func newSilentCountingTLSServer(t *testing.T) *countingTLSServer {
	t.Helper()
	serverCfg, _ := selfSignedTLS(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	require.NoError(t, err)
	s := &countingTLSServer{ln: ln, silent: true}
	go s.serve()
	t.Cleanup(s.close)
	return s
}

func (s *countingTLSServer) addr() string {
	return s.ln.Addr().String()
}

func (s *countingTLSServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.acceptCnt.Add(1)
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()

		if s.silent {
			// Force the handshake to complete server-side so Connect (the
			// dial) succeeds; go silent only after that, never reading or
			// writing application data.
			go func(c net.Conn) {
				if tlsConn, ok := c.(*tls.Conn); ok {
					_ = tlsConn.Handshake()
				}
			}(conn)
			continue
		}
		go s.echo(conn)
	}
}

func (s *countingTLSServer) echo(conn net.Conn) {
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

func (s *countingTLSServer) close() {
	_ = s.ln.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
}

func TestTLSTransport_ConnectReusesConnectionAcrossSendReceive(t *testing.T) {
	server := newCountingTLSServer(t)
	_, clientCfg := selfSignedTLS(t)

	tr, err := common.NewTLSTransport(":0", clientCfg)
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

func TestTLSTransport_NoConnectDialsPerSend(t *testing.T) {
	server := newCountingTLSServer(t)
	_, clientCfg := selfSignedTLS(t)

	tr, err := common.NewTLSTransport(":0", clientCfg)
	require.NoError(t, err)
	defer tr.Close()

	require.NoError(t, tr.Send([]byte("first"), server.addr()))
	_, _, err = tr.Receive(2 * time.Second)
	require.NoError(t, err)

	require.NoError(t, tr.Send([]byte("second"), server.addr()))
	_, _, err = tr.Receive(2 * time.Second)
	require.NoError(t, err)

	assert.Equal(t, int32(2), server.acceptCnt.Load(),
		"un-connected path must dial a fresh connection per Send")
}

func TestTLSTransport_SendAddrMismatch(t *testing.T) {
	server := newCountingTLSServer(t)
	_, clientCfg := selfSignedTLS(t)

	tr, err := common.NewTLSTransport(":0", clientCfg)
	require.NoError(t, err)
	defer tr.Close()

	require.NoError(t, tr.Connect(server.addr()))

	err = tr.Send([]byte("hello"), "127.0.0.1:1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), server.addr())
	assert.Contains(t, err.Error(), "127.0.0.1:1")

	assert.Equal(t, int32(1), server.acceptCnt.Load(),
		"a mismatched Send must not dial anything")
}

// TestTLSTransport_CloseUnblocksBlockedReceive guards against golang/go#23518,
// a real TLS-only footgun (fixed long before the pinned Go 1.26.6) where
// tls.Conn.Close did not reliably unblock a stuck Read. Run for TLS
// specifically — do not infer the result from the TCP test.
func TestTLSTransport_CloseUnblocksBlockedReceive(t *testing.T) {
	server := newSilentCountingTLSServer(t)
	_, clientCfg := selfSignedTLS(t)

	tr, err := common.NewTLSTransport(":0", clientCfg)
	require.NoError(t, err)

	require.NoError(t, tr.Connect(server.addr()))

	done := make(chan error, 1)
	go func() {
		_, _, err := tr.Receive(30 * time.Second)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	require.NoError(t, tr.Close())

	select {
	case err := <-done:
		assert.Error(t, err, "Close should unblock Receive with an error")
	case <-time.After(2 * time.Second):
		t.Fatal("Receive did not unblock within 2s of Close")
	}
}
