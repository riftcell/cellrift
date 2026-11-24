package sip_test

import (
	"net"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time interface check.
var _ common.ProtocolAdapter = (*sip.Adapter)(nil)

func TestAdapter_SendRegister_MockUAS(t *testing.T) {
	uas, err := NewMockUAS()
	require.NoError(t, err)
	defer uas.Close()

	adapter, err := sip.NewAdapter("udp", ":0", 3*time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	result, err := adapter.Execute("send", uas.Addr(), "REGISTER", map[string]string{
		"From":    "<sip:447700900001@ims.local>;tag=test001",
		"To":      "<sip:447700900001@ims.local>",
		"Contact": "<sip:447700900001@10.0.0.1:5060>",
	}, nil)
	require.NoError(t, err)

	assert.Equal(t, common.StatusPassed, result.Status)
	assert.GreaterOrEqual(t, result.DurationMs, int64(0))
	assert.NotNil(t, result.Response)
	assert.NotNil(t, result.RawResponse)

	// Check response fields.
	assert.Equal(t, 200, result.Response["status_code"])
	assert.Equal(t, "OK", result.Response["reason_phrase"])

	headers, ok := result.Response["header"].(map[string]interface{})
	require.True(t, ok)
	assert.NotEmpty(t, headers["Contact"])

	// No errors on mock UAS.
	assert.Empty(t, uas.Errors())
}

func TestAdapter_SendInvite_MockUAS(t *testing.T) {
	uas, err := NewMockUAS()
	require.NoError(t, err)
	defer uas.Close()

	adapter, err := sip.NewAdapter("udp", ":0", 3*time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	result, err := adapter.Execute("send", uas.Addr(), "INVITE", map[string]string{
		"From":    "<sip:447700900001@ims.local>;tag=inv001",
		"To":      "<sip:447700900002@ims.local>",
		"Contact": "<sip:447700900001@10.0.0.1:5060>",
	}, nil)
	require.NoError(t, err)

	// The adapter consumes 180 Ringing then returns the final 200 OK.
	assert.Equal(t, common.StatusPassed, result.Status, "error: %s", result.Error)
	assert.NotNil(t, result.Response)
}

func TestAdapter_SendBye_MockUAS(t *testing.T) {
	uas, err := NewMockUAS()
	require.NoError(t, err)
	defer uas.Close()

	adapter, err := sip.NewAdapter("udp", ":0", 3*time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	result, err := adapter.Execute("send", uas.Addr(), "BYE", map[string]string{
		"From": "<sip:447700900001@ims.local>;tag=bye001",
		"To":   "<sip:447700900002@ims.local>;tag=srv001",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status)
	assert.Equal(t, 200, result.Response["status_code"])
}

func TestAdapter_SendOptions_MockUAS(t *testing.T) {
	uas, err := NewMockUAS()
	require.NoError(t, err)
	defer uas.Close()

	adapter, err := sip.NewAdapter("udp", ":0", 3*time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	result, err := adapter.Execute("send", uas.Addr(), "OPTIONS", map[string]string{
		"From": "<sip:447700900001@ims.local>;tag=opt001",
		"To":   "<sip:ims.local>",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status)
}

func TestAdapter_AssertState_Registered(t *testing.T) {
	uas, err := NewMockUAS()
	require.NoError(t, err)
	defer uas.Close()

	adapter, err := sip.NewAdapter("udp", ":0", 3*time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	// First register.
	_, err = adapter.Execute("send", uas.Addr(), "REGISTER", map[string]string{
		"From":    "<sip:447700900001@ims.local>;tag=st001",
		"To":      "<sip:447700900001@ims.local>",
		"Contact": "<sip:447700900001@10.0.0.1:5060>",
	}, nil)
	require.NoError(t, err)

	// Now assert state. A 2xx to REGISTER lands in "registered", never
	// "established" — the latter is a call, and this test pinned the wrong
	// state for a long time while the shipped register_basic.yaml waited
	// on a "registered" state that did not exist.
	result, err := adapter.Execute("assert_state", "", "", nil, map[string]string{
		"check":      "registered",
		"timeout_ms": "1000",
	})
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status)
}

func TestAdapter_AssertState_Timeout(t *testing.T) {
	adapter, err := sip.NewAdapter("udp", ":0", 100*time.Millisecond)
	require.NoError(t, err)
	defer adapter.Close()

	result, err := adapter.Execute("assert_state", "", "", nil, map[string]string{
		"check":      "established",
		"timeout_ms": "100",
	})
	require.NoError(t, err)
	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Contains(t, result.Error, "no dialog reached state")
}

func TestAdapter_UnsupportedAction(t *testing.T) {
	adapter, err := sip.NewAdapter("udp", ":0", time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	_, err = adapter.Execute("invalid_action", "", "", nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported action")
}

func TestAdapter_SendProducesValidWireFormat(t *testing.T) {
	uas, err := NewMockUAS()
	require.NoError(t, err)
	defer uas.Close()

	adapter, err := sip.NewAdapter("udp", ":0", 3*time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	result, err := adapter.Execute("send", uas.Addr(), "REGISTER", map[string]string{
		"From":    "<sip:447700900001@ims.local>;tag=wire001",
		"To":      "<sip:447700900001@ims.local>",
		"Contact": "<sip:447700900001@10.0.0.1:5060>",
	}, nil)
	require.NoError(t, err)

	// Parse the raw response to verify it's valid SIP.
	resp, err := sip.ParseMessage(result.RawResponse)
	require.NoError(t, err)
	assert.False(t, resp.IsRequest())
	assert.Equal(t, "SIP/2.0", resp.Version)
	assert.NotEmpty(t, resp.Headers["Call-ID"])
}

func TestMockUAS_StartsAndStopsCleanly(t *testing.T) {
	uas, err := NewMockUAS()
	require.NoError(t, err)
	assert.NotEmpty(t, uas.Addr())

	uas.Close()
	// Calling Close again should not panic.
}

// TestAdapter_BYEAfterINVITE_InjectsDialogHeaders verifies that a BYE sent
// after an established INVITE dialog reuses the dialog's Call-ID, To-tag,
// and CSeq. This exercises injectDialogHeaders when EstablishedDialog != nil.
func TestAdapter_BYEAfterINVITE_InjectsDialogHeaders(t *testing.T) {
	uas, err := NewMockUAS()
	require.NoError(t, err)
	defer uas.Close()

	adapter, err := sip.NewAdapter("udp", ":0", 3*time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	// Step 1: send INVITE — this establishes the dialog on 200 OK.
	inviteResult, err := adapter.Execute("send", uas.Addr(), "INVITE", map[string]string{
		"From":    "<sip:447700900001@ims.local>;tag=dlg001",
		"To":      "<sip:447700900002@ims.local>",
		"Contact": "<sip:447700900001@10.0.0.1:5060>",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, inviteResult.Status, "INVITE should pass: %s", inviteResult.Error)

	// Step 2: send BYE without Call-ID or CSeq — adapter should inject from dialog.
	byeResult, err := adapter.Execute("send", uas.Addr(), "BYE", map[string]string{
		"From": "<sip:447700900001@ims.local>;tag=dlg001",
		"To":   "<sip:447700900002@ims.local>", // no tag — adapter adds it from dialog
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, byeResult.Status, "BYE should pass after dialog established: %s", byeResult.Error)
	assert.Equal(t, 200, byeResult.Response["status_code"], "BYE should receive 200 OK")

	// The UAS echoes Via back; the Via should reflect UDP transport.
	headers, ok := byeResult.Response["header"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, headers["Via"].(string), "SIP/2.0/UDP")
}

// TestNewAdapterWithTransport_ViaTokenMatchesTransportType is the regression
// guard for the exact defect NewAdapterWithTransport's transportType
// parameter was added to prevent: before this change, every externally
// supplied transport was hardcoded to "udp", so a caller (e.g. the
// impairment-decorated transport main.go wires in) would have
// silently downgraded every TCP and TLS scenario's Via header to UDP.
//
// Each row drives the adapter over a real TCP socket via TCPMockUASServer.
// mockResponses echoes the request's own Via header back on the 200 OK
// (see mockResponses in testing.go), so the response headers this test
// asserts on are exactly what the adapter put on the wire — not what the
// mock invented. Using one TCP-backed mock for every row, including the
// unrecognised "quic" one, keeps the test to a single mock server type per
// the plan's constraint.
func TestNewAdapterWithTransport_ViaTokenMatchesTransportType(t *testing.T) {
	tests := []struct {
		name          string
		transportType string
		wantToken     string
	}{
		{name: "tcp", transportType: "tcp", wantToken: "SIP/2.0/TCP"},
		{name: "tls", transportType: "tls", wantToken: "SIP/2.0/TLS"},
		{name: "udp", transportType: "udp", wantToken: "SIP/2.0/UDP"},
		{name: "unrecognised falls back to udp", transportType: "quic", wantToken: "SIP/2.0/UDP"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := sip.NewTCPMockUAS(t, sip.TCPFaultNone)
			defer mock.Close()

			tr, err := common.NewTCPTransport(":0")
			require.NoError(t, err)
			adapter := sip.NewAdapterWithTransport(tr, tc.transportType, 2*time.Second)
			defer adapter.Close()

			result, err := adapter.Execute("send", mock.Addr(), "OPTIONS", map[string]string{
				"From": "<sip:447700900001@ims.local>;tag=via001",
				"To":   "<sip:ims.local>",
			}, nil)
			require.NoError(t, err)
			require.Equal(t, common.StatusPassed, result.Status, "error: %s", result.Error)

			headers, ok := result.Response["header"].(map[string]interface{})
			require.True(t, ok)
			assert.Contains(t, headers["Via"].(string), tc.wantToken)
			assert.Empty(t, mock.Errors())
		})
	}
}

// TestAdapter_TCPTransport_ViaHeader verifies the Via header uses SIP/2.0/TCP.
// A raw TCP listener captures the request bytes so we can inspect the Via
// without needing a full SIP UAS.
func TestAdapter_TCPTransport_ViaHeader(t *testing.T) {
	// Raw TCP listener — reads the SIP request and sends back a minimal 200 OK.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	captured := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		captured <- string(buf[:n])
		// Send minimal 200 OK so the adapter's Receive doesn't time out.
		resp := "SIP/2.0 200 OK\r\nVia: SIP/2.0/TCP 127.0.0.1\r\nCSeq: 1 OPTIONS\r\nContent-Length: 0\r\n\r\n"
		conn.Write([]byte(resp)) //nolint:errcheck
	}()

	adapter, err := sip.NewAdapter("tcp", ":0", 3*time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	_, err = adapter.Execute("send", ln.Addr().String(), "OPTIONS", map[string]string{
		"From": "<sip:447700900001@ims.local>;tag=tcp001",
		"To":   "<sip:ims.local>",
	}, nil)
	// The adapter may or may not parse the raw response correctly; we only care
	// about the request bytes that were captured.
	_ = err

	select {
	case raw := <-captured:
		assert.Contains(t, raw, "SIP/2.0/TCP", "TCP adapter should send Via with TCP token")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: TCP listener did not receive request")
	}
}
