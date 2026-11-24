package common_test

import (
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time interface checks.
var (
	_ common.Transport = (*common.UDPTransport)(nil)
	_ common.Transport = (*common.TCPTransport)(nil)
	_ common.Transport = (*common.TLSTransport)(nil)

	// Connection-oriented transports only. UDPTransport is absent by design —
	// enforced at runtime by TestUDPTransport_DoesNotImplementConnector.
	_ common.Connector = (*common.TCPTransport)(nil)
	_ common.Connector = (*common.TLSTransport)(nil)
)

// TestUDPTransport_DoesNotImplementConnector is a runtime negative
// assertion: Go has no compile-time negative-assertion syntax, but
// asserting through an any-typed variable compiles unconditionally and
// evaluates at runtime, making this a real mechanical gate rather than a
// comment. UDPTransport must NOT implement Connector — datagram transports
// have no connection to hold, so there is nothing for Connect/Close to
// manage.
func TestUDPTransport_DoesNotImplementConnector(t *testing.T) {
	var tr any = (*common.UDPTransport)(nil)
	_, ok := tr.(common.Connector)
	assert.False(t, ok, "UDPTransport must NOT implement Connector — datagram transports "+
		"have no connection to hold, as explained in this test's doc comment above")
}

// TestSCTPOrFallbackTransport_SatisfiesConnector proves the type assertion
// still works through NewSCTPOrFallbackTransport's Transport interface
// return value — whatever concrete transport the constructor yields, it must
// not break this seam.
func TestSCTPOrFallbackTransport_SatisfiesConnector(t *testing.T) {
	tr, _, err := common.NewSCTPOrFallbackTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	_, ok := tr.(common.Connector)
	assert.True(t, ok, "the transport returned by NewSCTPOrFallbackTransport must satisfy Connector")
}

func TestUDPTransport_SendReceive(t *testing.T) {
	// Start a receiver on a random port.
	receiver, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	defer receiver.Close()

	// Start a sender on a random port.
	sender, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	defer sender.Close()

	msg := []byte("REGISTER sip:ims.local SIP/2.0\r\n\r\n")
	err = sender.Send(msg, receiver.LocalAddr().String())
	require.NoError(t, err)

	data, addr, err := receiver.Receive(2 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, msg, data)
	assert.NotNil(t, addr)
}

func TestUDPTransport_ReceiveTimeout(t *testing.T) {
	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	defer tr.Close()

	_, _, err = tr.Receive(50 * time.Millisecond)
	assert.Error(t, err, "expected timeout error")
}

func TestNewTransport_UDP(t *testing.T) {
	tr, err := common.NewTransport("udp", ":0")
	require.NoError(t, err)
	defer tr.Close()
	assert.NotNil(t, tr.LocalAddr())
}

func TestNewTransport_TCP(t *testing.T) {
	tr, err := common.NewTransport("tcp", ":0")
	require.NoError(t, err)
	defer tr.Close()
	assert.NotNil(t, tr.LocalAddr())
}

func TestTCPTransport_SendReceive(t *testing.T) {
	// Start a TCP receiver (listener).
	receiver, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	defer receiver.Close()

	// Start a TCP sender.
	sender, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	defer sender.Close()

	msg := []byte("REGISTER sip:ims.local SIP/2.0\r\n\r\n")
	err = sender.Send(msg, receiver.LocalAddr().String())
	require.NoError(t, err)

	data, addr, err := receiver.Receive(2 * time.Second)
	require.NoError(t, err)
	assert.Equal(t, msg, data)
	assert.NotNil(t, addr)
}

func TestNewTransport_Unknown(t *testing.T) {
	_, err := common.NewTransport("sctp", ":0")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}
