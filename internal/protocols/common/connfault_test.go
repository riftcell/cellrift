package common

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// echoAndReadLoop accepts one connection from ln and reads every byte it
// sends into a channel of received chunks, closing done when the peer's
// connection is observed to end (EOF or a read error). It is the "real peer"
// half of these tests — a bare accept-and-read loop, not a mock framework.
func echoAndReadLoop(t *testing.T, ln net.Listener, received chan<- []byte, done chan<- struct{}) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		close(done)
		return
	}
	defer conn.Close()
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			received <- chunk
		}
		if err != nil {
			close(done)
			return
		}
	}
}

func TestTCPTransport_ResetConn_NextSendReportsConnectionGone(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	received := make(chan []byte, 8)
	done := make(chan struct{})
	go echoAndReadLoop(t, ln, received, done)

	tr, err := NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	addr := ln.Addr().String()
	require.NoError(t, tr.Connect(addr))
	require.NoError(t, tr.Send([]byte("first"), addr))

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("peer never observed the first write")
	}

	require.NoError(t, tr.ResetConn())

	err = tr.Send([]byte("second"), addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is gone, reconnect required")
}

func TestTCPTransport_ResetConn_DialPerSendStash(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	received := make(chan []byte, 8)
	done := make(chan struct{})
	go echoAndReadLoop(t, ln, received, done)

	tr, err := NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	addr := ln.Addr().String()
	// No Connect: dial-per-send path, addr == "".
	require.NoError(t, tr.Send([]byte("first"), addr))

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("peer never observed the first write")
	}

	require.NoError(t, tr.ResetConn())

	conn, connectedAddr := tr.conns.snapshot()
	assert.Nil(t, conn)
	assert.Equal(t, "", connectedAddr)
}

func TestTCPTransport_ResetConn_NoConnectionNamesFaultAndDoesNotPanic(t *testing.T) {
	tr, err := NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	assert.NotPanics(t, func() {
		err := tr.ResetConn()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no connection to fault")
	})
}

func TestTCPTransport_AbruptCloseConn_NextSendReportsConnectionGone(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	received := make(chan []byte, 8)
	done := make(chan struct{})
	go echoAndReadLoop(t, ln, received, done)

	tr, err := NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	addr := ln.Addr().String()
	require.NoError(t, tr.Connect(addr))
	require.NoError(t, tr.Send([]byte("first"), addr))

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("peer never observed the first write")
	}

	require.NoError(t, tr.AbruptCloseConn())

	err = tr.Send([]byte("second"), addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is gone, reconnect required")
}

func TestTCPTransport_AbortConn_RejectsNonSCTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	received := make(chan []byte, 8)
	done := make(chan struct{})
	go echoAndReadLoop(t, ln, received, done)

	tr, err := NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	addr := ln.Addr().String()
	require.NoError(t, tr.Connect(addr))
	require.NoError(t, tr.Send([]byte("first"), addr))

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("peer never observed the first write")
	}

	err = tr.AbortConn()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires an SCTP association")

	// Connection must still be usable — AbortConn never closed it.
	require.NoError(t, tr.Send([]byte("second"), addr))
}

func TestTCPTransport_SendTruncated_WritesExactNBytes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	received := make(chan []byte, 8)
	done := make(chan struct{})
	go echoAndReadLoop(t, ln, received, done)

	tr, err := NewTCPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	addr := ln.Addr().String()
	require.NoError(t, tr.Connect(addr))

	msg := []byte("0123456789abcdef")
	require.NoError(t, tr.SendTruncated(msg, addr, 8))

	var chunk []byte
	select {
	case chunk = <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("peer never observed the short write")
	}
	assert.Equal(t, msg[:8], chunk)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("peer never observed EOF after the short write")
	}
}

// selfSignedTLSConfig generates an in-memory self-signed cert/key for tests,
// following the same shape as common_test's own selfSignedTLS. This file
// lives in package common (not common_test) since it exercises connState
// directly, so it cannot import that helper without an import cycle and
// needs its own copy.
func selfSignedTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

func TestTLSTransport_ResetConn_SkipsCloseNotify(t *testing.T) {
	serverCfg := selfSignedTLSConfig(t)

	serverTr, err := NewTLSTransport(":0", serverCfg)
	require.NoError(t, err)
	defer serverTr.Close()

	readErrCh := make(chan error, 1)
	go func() {
		conn, acceptErr := serverTr.listener.Accept()
		if acceptErr != nil {
			readErrCh <- acceptErr
			return
		}
		defer conn.Close()
		tlsConn := tls.Server(conn, serverCfg)
		if hsErr := tlsConn.Handshake(); hsErr != nil {
			readErrCh <- hsErr
			return
		}
		buf := make([]byte, 64)
		if _, rerr := tlsConn.Read(buf); rerr != nil {
			readErrCh <- rerr
			return
		}
		// Second read observes the reset.
		_, rerr := tlsConn.Read(buf)
		readErrCh <- rerr
	}()

	clientTr, err := NewTLSTransport(":0", &tls.Config{InsecureSkipVerify: true}) //nolint:gosec
	require.NoError(t, err)
	defer clientTr.Close()

	addr := serverTr.listener.Addr().String()
	require.NoError(t, clientTr.Connect(addr))
	require.NoError(t, clientTr.Send([]byte("hello"), addr))

	require.NoError(t, clientTr.ResetConn())

	select {
	case rerr := <-readErrCh:
		require.Error(t, rerr)
		// A reset should not be a clean io.EOF following a well-formed
		// close_notify alert. If distinguishing a raw reset from a clean
		// TLS shutdown proves unstable across kernels, this assertion is
		// weakened to "not a clean io.EOF" rather than deleted.
		assert.NotEqual(t, io.EOF, rerr)
	case <-time.After(2 * time.Second):
		t.Fatal("server never observed the reset")
	}

	err = clientTr.Send([]byte("second"), addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is gone, reconnect required")
}

func TestUDPTransport_SendTruncated_WritesShortDatagramAndStaysUsable(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	defer server.Close()

	tr, err := NewUDPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	addr := server.LocalAddr().String()
	msg := []byte("0123456789abcdef")
	require.NoError(t, tr.SendTruncated(msg, addr, 8))

	buf := make([]byte, 64)
	require.NoError(t, server.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, _, err := server.ReadFromUDP(buf)
	require.NoError(t, err)
	assert.Equal(t, msg[:8], buf[:n])

	// Subsequent Send still works — nothing was closed.
	require.NoError(t, tr.Send([]byte("still works"), addr))
}

func TestSendTruncated_ClampsOversizedCount(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	defer server.Close()

	tr, err := NewUDPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	addr := server.LocalAddr().String()
	msg := []byte("short")
	require.NoError(t, tr.SendTruncated(msg, addr, 999))

	buf := make([]byte, 64)
	require.NoError(t, server.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, _, err := server.ReadFromUDP(buf)
	require.NoError(t, err)
	assert.Equal(t, msg, buf[:n])

	// n <= 0 writes zero bytes.
	require.NoError(t, tr.SendTruncated(msg, addr, 0))
	require.NoError(t, server.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, _, err = server.ReadFromUDP(buf)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestUDPTransport_DoesNotImplementConnFaultInjector(t *testing.T) {
	tr, err := NewUDPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	_, ok := any(tr).(ConnFaultInjector)
	assert.False(t, ok)
}
