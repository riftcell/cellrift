package sip_test

import (
	"net"
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// truncatedRegister is a deliberately short, unparseable SIP datagram: eight
// bytes of "REGISTER" with no start line, no headers, and no CRLF. It cannot
// survive ParseMessage by construction, which is exactly what makes it the
// right fixture for proving RawReads() sees what ReceivedRequests() cannot.
var truncatedRegister = []byte("REGISTER")

// dialUDPTo opens a raw UDP socket connected to addr, for a test that needs
// to write bytes a real sip.Adapter would never construct (a truncated
// message), bypassing the adapter entirely.
func dialUDPTo(t *testing.T, addr string) *net.UDPConn {
	t.Helper()
	raddr, err := net.ResolveUDPAddr("udp", addr)
	require.NoError(t, err)
	conn, err := net.DialUDP("udp", nil, raddr)
	require.NoError(t, err)
	return conn
}

// TestMockUAS_RawReadsCapturesUnparseableDatagram proves the core claim this
// plan exists to correct: a truncated datagram is observable through
// RawReads() while being provably absent from ReceivedRequests(), since
// serve()'s parse-failure branch never records into the latter.
func TestMockUAS_RawReadsCapturesUnparseableDatagram(t *testing.T) {
	mock := sip.NewMockUAS(t)
	defer mock.Close()

	conn := dialUDPTo(t, mock.Addr())
	defer conn.Close()

	_, err := conn.Write(truncatedRegister)
	require.NoError(t, err)

	mock.WaitForRawRead(t, "mock must record the raw datagram even though it cannot parse", func(reads [][]byte) bool { return len(reads) == 1 })

	reads := mock.RawReads()
	require.Len(t, reads, 1)
	assert.Equal(t, len(truncatedRegister), len(reads[0]))
	assert.Equal(t, truncatedRegister, reads[0])

	assert.Empty(t, mock.ReceivedRequests(), "an unparseable datagram must never reach ReceivedRequests")
}

// TestMockUAS_RawReadsCopySemantics proves mutating a slice returned by
// RawReads(), or a returned entry, can never change what a later RawReads()
// call returns — the same deep-copy discipline ReceivedRequests uses.
func TestMockUAS_RawReadsCopySemantics(t *testing.T) {
	mock := sip.NewMockUAS(t)
	defer mock.Close()

	conn := dialUDPTo(t, mock.Addr())
	defer conn.Close()

	_, err := conn.Write(truncatedRegister)
	require.NoError(t, err)

	mock.WaitForRawRead(t, "the peer to record 1 raw read(s)", func(reads [][]byte) bool { return len(reads) == 1 })

	first := mock.RawReads()
	require.Len(t, first, 1)
	first[0][0] = 0xFF
	_ = append(first, []byte("extra"))

	second := mock.RawReads()
	require.Len(t, second, 1, "appending to a returned slice must not grow the mock's own history")
	assert.NotEqual(t, byte(0xFF), second[0][0], "mutating a returned entry must not affect a later call's result")
}

// TestMockUAS_RawReadsEmptyBeforeAnySend proves RawReads() on a mock that has
// received nothing returns an empty, non-nil-panicking slice.
func TestMockUAS_RawReadsEmptyBeforeAnySend(t *testing.T) {
	mock := sip.NewMockUAS(t)
	defer mock.Close()

	assert.Empty(t, mock.RawReads())
	assert.Len(t, mock.RawReads(), 0)
}

// TestTCPMockUAS_RawReadsCapturesShortRead proves the TCP mock's RawReads()
// sees a short write even though TCPFaultSilence never calls ParseMessage —
// TCPFaultSilence is the right fault here because it holds the connection
// open (rather than immediately closing it) and appends no spurious entry to
// Errors(), unlike TCPFaultNone's answerNormally would on a parse failure.
func TestTCPMockUAS_RawReadsCapturesShortRead(t *testing.T) {
	mock := sip.NewTCPMockUAS(t, sip.TCPFaultSilence)
	defer mock.Close()

	conn, err := net.Dial("tcp", mock.Addr())
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write(truncatedRegister)
	require.NoError(t, err)

	mock.WaitForRawRead(t, "mock must record the short read even though it never parses", func(reads [][]byte) bool { return len(reads) == 1 })

	reads := mock.RawReads()
	require.Len(t, reads, 1)
	assert.Equal(t, len(truncatedRegister), len(reads[0]))
	assert.Equal(t, truncatedRegister, reads[0])
	assert.Empty(t, mock.Errors(), "TCPFaultSilence must never append a spurious parse error")
}
