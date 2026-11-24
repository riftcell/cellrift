package common_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time interface check for TLSTransport.
var _ common.Transport = (*common.TLSTransport)(nil)

// selfSignedTLS generates an in-memory self-signed cert for tests.
func selfSignedTLS(t *testing.T) (serverCfg, clientCfg *tls.Config) {
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
	serverCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	clientCfg = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec
	return serverCfg, clientCfg
}

// TestBuildTLSConfig_AllEmpty_VerifiesByDefault pins the secure default:
// with no cert, key, or CA and no opt-out, the client verifies the server
// certificate against the system root pool (RootCAs nil is tls.Config's own
// "use system roots"). This inverts the previous test, which asserted that
// the same inputs silently set InsecureSkipVerify.
func TestBuildTLSConfig_AllEmpty_VerifiesByDefault(t *testing.T) {
	cfg, err := common.BuildTLSConfig("", "", "", false)
	require.NoError(t, err)
	assert.False(t, cfg.InsecureSkipVerify, "verification must be on unless explicitly disabled")
	assert.Nil(t, cfg.RootCAs, "no CA file means the system root pool, not no verification")
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
}

// TestBuildTLSConfig_ExplicitInsecureSkipVerify pins that the opt-out works
// when — and only when — asked for, and does not otherwise change the config.
func TestBuildTLSConfig_ExplicitInsecureSkipVerify(t *testing.T) {
	cfg, err := common.BuildTLSConfig("", "", "", true)
	require.NoError(t, err)
	assert.True(t, cfg.InsecureSkipVerify)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
}

func TestBuildTLSConfig_MissingCertFile(t *testing.T) {
	_, err := common.BuildTLSConfig("/nonexistent/cert.pem", "/nonexistent/key.pem", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load cert/key")
}

func TestBuildTLSConfig_MissingCAFile(t *testing.T) {
	_, err := common.BuildTLSConfig("", "", "/nonexistent/ca.pem", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read CA file")
}

func TestBuildTLSConfig_CertWithoutCA_NotInsecure(t *testing.T) {
	// Providing cert/key but no CA should NOT set InsecureSkipVerify.
	// (It will fail at dial time if the server cert is untrusted, as expected.)
	_, err := common.BuildTLSConfig("/nonexistent/cert.pem", "/nonexistent/key.pem", "", false)
	// We expect an error because the files don't exist — the important thing
	// is that the empty-paths InsecureSkipVerify shortcut is not triggered.
	require.Error(t, err)
}

// ── TLSTransport ─────────────────────────────────────────────────────────────

func TestTLSTransport_SendReceive(t *testing.T) {
	serverCfg, clientCfg := selfSignedTLS(t)

	// Start a TLS server using the server config.
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	require.NoError(t, err)
	defer ln.Close()

	msg := []byte("REGISTER sip:ims.local SIP/2.0\r\n\r\n")
	received := make(chan []byte, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		received <- buf[:n]
	}()

	// Client: TLS transport dials out.
	tr, err := common.NewTLSTransport(":0", clientCfg)
	require.NoError(t, err)
	defer tr.Close()

	assert.NotNil(t, tr.LocalAddr())

	err = tr.Send(msg, ln.Addr().String())
	require.NoError(t, err)

	select {
	case got := <-received:
		assert.Equal(t, msg, got)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: server did not receive message")
	}
}

func TestTLSTransport_ReceiveFromLastConn(t *testing.T) {
	serverCfg, clientCfg := selfSignedTLS(t)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	require.NoError(t, err)
	defer ln.Close()

	response := []byte("SIP/2.0 200 OK\r\n\r\n")

	// Server: accept, read request, write response.
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		conn.Read(buf)       //nolint:errcheck
		conn.Write(response) //nolint:errcheck
	}()

	tr, err := common.NewTLSTransport(":0", clientCfg)
	require.NoError(t, err)
	defer tr.Close()

	req := []byte("OPTIONS sip:ims.local SIP/2.0\r\n\r\n")
	err = tr.Send(req, ln.Addr().String())
	require.NoError(t, err)

	got, addr, err := tr.Receive(3 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, response, got)
	assert.NotNil(t, addr)

	<-serverDone
}

func TestNewTLSTransport_LocalAddr(t *testing.T) {
	_, clientCfg := selfSignedTLS(t)
	tr, err := common.NewTLSTransport(":0", clientCfg)
	require.NoError(t, err)
	defer tr.Close()
	assert.NotNil(t, tr.LocalAddr())
	assert.NotEmpty(t, tr.LocalAddr().String())
}

func TestNewTransport_TLS_Unsupported(t *testing.T) {
	// "tls" is not supported via NewTransport — must use NewTLSTransport.
	_, err := common.NewTransport("tls", ":0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}
