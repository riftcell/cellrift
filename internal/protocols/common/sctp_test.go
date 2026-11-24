package common_test

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSCTPOrFallback_ForceTCPFallback(t *testing.T) {
	os.Setenv("CELLRIFT_TRANSPORT_FALLBACK", "tcp")
	defer os.Unsetenv("CELLRIFT_TRANSPORT_FALLBACK")

	tr, warnings, err := common.NewSCTPOrFallbackTransport(":0")
	require.NoError(t, err)
	defer tr.Close()
	assert.NotNil(t, tr.LocalAddr())
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "forced by CELLRIFT_TRANSPORT_FALLBACK=tcp")
}

// TestSCTPOrFallback_UsesRealSCTPWhenAvailable replaces the old
// TestSCTPOrFallback_AutoFallback, whose name/behaviour would otherwise
// silently diverge now that real SCTP support has landed: on any
// SCTP-capable machine (ordinary Linux dev boxes and GitHub Actions
// ubuntu-latest included) it exercises the real-SCTP branch while its old
// name claimed "fallback". This test instead states, explicitly, which
// branch this machine took, so a green run is never ambiguous about what it
// proved.
func TestSCTPOrFallback_UsesRealSCTPWhenAvailable(t *testing.T) {
	os.Unsetenv("CELLRIFT_TRANSPORT_FALLBACK")

	tr, warnings, err := common.NewSCTPOrFallbackTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	switch tr.(type) {
	case *common.SCTPTransport:
		assert.Empty(t, warnings, "real SCTP transport must report no warnings")
	case *common.TCPTransport:
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], "sctp unavailable")
		t.Skip("kernel SCTP unavailable on this machine — TCP fallback branch exercised instead")
	default:
		t.Fatalf("unexpected transport type %T", tr)
	}
}

func TestSCTPFallback_SendReceive(t *testing.T) {
	os.Setenv("CELLRIFT_TRANSPORT_FALLBACK", "tcp")
	defer os.Unsetenv("CELLRIFT_TRANSPORT_FALLBACK")

	// Create a receiver via TCP.
	receiver, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	defer receiver.Close()

	// Create a sender via SCTP-or-fallback (will be TCP).
	sender, _, err := common.NewSCTPOrFallbackTransport(":0")
	require.NoError(t, err)
	defer sender.Close()

	msg := []byte{0x01, 0x00, 0x00, 0x14, 0x80, 0x00, 0x01, 0x2C} // partial Diameter header
	err = sender.Send(msg, receiver.LocalAddr().String())
	require.NoError(t, err)

	data, addr, err := receiver.Receive(2 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, msg, data)
	assert.NotNil(t, addr)
}

// TestSCTPTransport_LoopbackSendReceive proves a real SCTP association
// completes a byte-identical round-trip over loopback, and that the
// connected association's local address is single-homed and parseable — the
// loopback-level half of dialLocalAddr's multi-homed-address guard.
func TestSCTPTransport_LoopbackSendReceive(t *testing.T) {
	os.Unsetenv("CELLRIFT_TRANSPORT_FALLBACK")

	server, _, err := common.NewSCTPOrFallbackTransport("127.0.0.1:0")
	require.NoError(t, err)
	defer server.Close()
	if _, ok := server.(*common.SCTPTransport); !ok {
		t.Skip("kernel SCTP unavailable on this machine")
	}

	client, _, err := common.NewSCTPOrFallbackTransport("127.0.0.1:0")
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, client.(common.Connector).Connect(server.LocalAddr().String()))

	msg := []byte{0x01, 0x00, 0x00, 0x14, 0x80, 0x00, 0x01, 0x2C} // partial Diameter header
	require.NoError(t, client.Send(msg, server.LocalAddr().String()))

	data, _, err := server.Receive(2 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, msg, data)

	assert.NotContains(t, client.LocalAddr().String(), "/",
		"a real SCTP LocalAddr() must be single-homed, not every local interface slash-joined")
	host, _, err := net.SplitHostPort(client.LocalAddr().String())
	require.NoError(t, err)
	require.NotNil(t, net.ParseIP(host))
}

// TestSCTPTransport_ConnectTimesOut proves Connect to a non-routable address
// returns to the caller within the configured bound instead of hanging the
// run. It deliberately does not assert on the error text: on some networks
// the kernel answers immediately with a routing error rather than reaching
// the timeout, and the behaviour being pinned is "bounded, not hanging"
// either way — tightening this into a text match on the error would flake
// on such networks. This test also knowingly abandons a goroutine parked in
// the dial syscall when the timeout branch is taken; that is the
// documented, accepted limitation of dialWithTimeout (see its doc comment),
// and this repo runs no goroutine-leak detector, so it does not fail -race.
func TestSCTPTransport_ConnectTimesOut(t *testing.T) {
	t.Setenv("CELLRIFT_SCTP_CONNECT_TIMEOUT_MS", "300")
	os.Unsetenv("CELLRIFT_TRANSPORT_FALLBACK")

	tr, _, err := common.NewSCTPOrFallbackTransport("127.0.0.1:0")
	require.NoError(t, err)
	defer tr.Close()
	if _, ok := tr.(*common.SCTPTransport); !ok {
		t.Skip("kernel SCTP unavailable on this machine")
	}

	start := time.Now()
	// 192.0.2.1 is RFC 5737 TEST-NET-1, guaranteed non-routable.
	err = tr.(common.Connector).Connect("192.0.2.1:3868")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
}
