package sip_test

import (
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Raw SIP message fixtures ---

var registerRequest = []byte("REGISTER sip:ims.local SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK776\r\n" +
	"From: <sip:447700900001@ims.local>;tag=abc123\r\n" +
	"To: <sip:447700900001@ims.local>\r\n" +
	"Call-ID: reg-001@10.0.0.1\r\n" +
	"CSeq: 1 REGISTER\r\n" +
	"Contact: <sip:447700900001@10.0.0.1:5060>\r\n" +
	"Max-Forwards: 70\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

var inviteRequest = []byte("INVITE sip:447700900002@ims.local SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK999\r\n" +
	"From: <sip:447700900001@ims.local>;tag=def456\r\n" +
	"To: <sip:447700900002@ims.local>\r\n" +
	"Call-ID: call-001@10.0.0.1\r\n" +
	"CSeq: 1 INVITE\r\n" +
	"Contact: <sip:447700900001@10.0.0.1:5060>\r\n" +
	"Max-Forwards: 70\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

var okResponse = []byte("SIP/2.0 200 OK\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK776\r\n" +
	"From: <sip:447700900001@ims.local>;tag=abc123\r\n" +
	"To: <sip:447700900001@ims.local>;tag=srv789\r\n" +
	"Call-ID: reg-001@10.0.0.1\r\n" +
	"CSeq: 1 REGISTER\r\n" +
	"Contact: <sip:447700900001@10.0.0.1:5060>\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

var ringingResponse = []byte("SIP/2.0 180 Ringing\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK999\r\n" +
	"From: <sip:447700900001@ims.local>;tag=def456\r\n" +
	"To: <sip:447700900002@ims.local>;tag=ring001\r\n" +
	"Call-ID: call-001@10.0.0.1\r\n" +
	"CSeq: 1 INVITE\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

var busyResponse = []byte("SIP/2.0 486 Busy Here\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK999\r\n" +
	"From: <sip:447700900001@ims.local>;tag=def456\r\n" +
	"To: <sip:447700900002@ims.local>;tag=busy001\r\n" +
	"Call-ID: call-001@10.0.0.1\r\n" +
	"CSeq: 1 INVITE\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

var byeRequest = []byte("BYE sip:447700900002@10.0.0.2:5060 SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKbye\r\n" +
	"From: <sip:447700900001@ims.local>;tag=def456\r\n" +
	"To: <sip:447700900002@ims.local>;tag=ring001\r\n" +
	"Call-ID: call-001@10.0.0.1\r\n" +
	"CSeq: 2 BYE\r\n" +
	"Max-Forwards: 70\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

var optionsRequest = []byte("OPTIONS sip:ims.local SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKopt\r\n" +
	"From: <sip:447700900001@ims.local>;tag=opt001\r\n" +
	"To: <sip:ims.local>\r\n" +
	"Call-ID: opt-001@10.0.0.1\r\n" +
	"CSeq: 1 OPTIONS\r\n" +
	"Max-Forwards: 70\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

// --- Parse tests ---

func TestParseMessage_RegisterRequest(t *testing.T) {
	msg, err := sip.ParseMessage(registerRequest)
	require.NoError(t, err)

	assert.True(t, msg.IsRequest())
	assert.Equal(t, "REGISTER", msg.Method)
	assert.Equal(t, "sip:ims.local", msg.RequestURI)
	assert.Equal(t, "SIP/2.0", msg.Version)
	assert.Contains(t, msg.Headers["From"], "447700900001")
	assert.Contains(t, msg.Headers["To"], "447700900001")
	assert.Equal(t, "reg-001@10.0.0.1", msg.Headers["Call-ID"])
	assert.Equal(t, "1 REGISTER", msg.Headers["CSeq"])
	assert.Contains(t, msg.Headers["Via"], "z9hG4bK776")
	assert.Contains(t, msg.Headers["Contact"], "447700900001")
}

func TestParseMessage_InviteRequest(t *testing.T) {
	msg, err := sip.ParseMessage(inviteRequest)
	require.NoError(t, err)
	assert.Equal(t, "INVITE", msg.Method)
	assert.Equal(t, "sip:447700900002@ims.local", msg.RequestURI)
}

func TestParseMessage_ByeRequest(t *testing.T) {
	msg, err := sip.ParseMessage(byeRequest)
	require.NoError(t, err)
	assert.Equal(t, "BYE", msg.Method)
}

func TestParseMessage_OptionsRequest(t *testing.T) {
	msg, err := sip.ParseMessage(optionsRequest)
	require.NoError(t, err)
	assert.Equal(t, "OPTIONS", msg.Method)
}

func TestParseMessage_200OK(t *testing.T) {
	msg, err := sip.ParseMessage(okResponse)
	require.NoError(t, err)

	assert.False(t, msg.IsRequest())
	assert.Equal(t, 200, msg.StatusCode)
	assert.Equal(t, "OK", msg.ReasonPhrase)
	assert.Equal(t, "reg-001@10.0.0.1", msg.Headers["Call-ID"])
}

func TestParseMessage_180Ringing(t *testing.T) {
	msg, err := sip.ParseMessage(ringingResponse)
	require.NoError(t, err)
	assert.Equal(t, 180, msg.StatusCode)
	assert.Equal(t, "Ringing", msg.ReasonPhrase)
}

func TestParseMessage_486Busy(t *testing.T) {
	msg, err := sip.ParseMessage(busyResponse)
	require.NoError(t, err)
	assert.Equal(t, 486, msg.StatusCode)
	assert.Equal(t, "Busy Here", msg.ReasonPhrase)
}

func TestParseMessage_AllMethods(t *testing.T) {
	methods := [][]byte{registerRequest, inviteRequest, byeRequest, optionsRequest}
	expectedMethods := []string{"REGISTER", "INVITE", "BYE", "OPTIONS"}
	for i, raw := range methods {
		msg, err := sip.ParseMessage(raw)
		require.NoError(t, err, "failed parsing method %s", expectedMethods[i])
		assert.Equal(t, expectedMethods[i], msg.Method)
	}
}

func TestParseMessage_AllStatusCodes(t *testing.T) {
	responses := []struct {
		raw  []byte
		code int
	}{
		{okResponse, 200},
		{ringingResponse, 180},
		{busyResponse, 486},
	}
	for _, tt := range responses {
		msg, err := sip.ParseMessage(tt.raw)
		require.NoError(t, err, "failed parsing status %d", tt.code)
		assert.Equal(t, tt.code, msg.StatusCode)
	}
}

func TestParseMessage_AllHeaders(t *testing.T) {
	msg, err := sip.ParseMessage(registerRequest)
	require.NoError(t, err)

	expectedHeaders := []string{"From", "To", "Via", "Contact", "Call-ID", "CSeq", "Max-Forwards", "Content-Length"}
	for _, h := range expectedHeaders {
		assert.Contains(t, msg.Headers, h, "missing header %s", h)
	}
}

func TestParseMessage_AuthorizationHeader(t *testing.T) {
	raw := []byte("REGISTER sip:ims.local SIP/2.0\r\n" +
		"From: <sip:user@ims.local>;tag=x\r\n" +
		"To: <sip:user@ims.local>\r\n" +
		"Call-ID: auth-test\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		"Authorization: Digest username=\"user\"\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n")
	msg, err := sip.ParseMessage(raw)
	require.NoError(t, err)
	assert.Contains(t, msg.Headers, "Authorization")
}

func TestParseMessage_WWWAuthenticateHeader(t *testing.T) {
	raw := []byte("SIP/2.0 401 Unauthorized\r\n" +
		"From: <sip:user@ims.local>;tag=x\r\n" +
		"To: <sip:user@ims.local>;tag=y\r\n" +
		"Call-ID: auth-test\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		"WWW-Authenticate: Digest realm=\"ims.local\"\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n")
	msg, err := sip.ParseMessage(raw)
	require.NoError(t, err)
	assert.Equal(t, 401, msg.StatusCode)
	assert.Contains(t, msg.Headers, "WWW-Authenticate")
}

// --- Serialise tests ---

func TestSerialise_Request(t *testing.T) {
	msg, err := sip.ParseMessage(registerRequest)
	require.NoError(t, err)

	out := sip.Serialise(msg)
	assert.Contains(t, string(out), "REGISTER sip:ims.local SIP/2.0\r\n")
	assert.Contains(t, string(out), "Call-ID: reg-001@10.0.0.1\r\n")
	assert.Contains(t, string(out), "\r\n\r\n")
}

func TestSerialise_Response(t *testing.T) {
	msg, err := sip.ParseMessage(okResponse)
	require.NoError(t, err)

	out := sip.Serialise(msg)
	assert.Contains(t, string(out), "SIP/2.0 200 OK\r\n")
}

// --- Round-trip test ---

func TestRoundTrip_ParseSerialise(t *testing.T) {
	fixtures := [][]byte{registerRequest, inviteRequest, okResponse, ringingResponse, busyResponse, byeRequest, optionsRequest}

	for _, raw := range fixtures {
		msg1, err := sip.ParseMessage(raw)
		require.NoError(t, err)

		serialised := sip.Serialise(msg1)
		msg2, err := sip.ParseMessage(serialised)
		require.NoError(t, err)

		// Core fields must match.
		assert.Equal(t, msg1.Method, msg2.Method)
		assert.Equal(t, msg1.RequestURI, msg2.RequestURI)
		assert.Equal(t, msg1.StatusCode, msg2.StatusCode)
		assert.Equal(t, msg1.ReasonPhrase, msg2.ReasonPhrase)
		assert.Equal(t, msg1.Version, msg2.Version)
		assert.Equal(t, msg1.Headers["Call-ID"], msg2.Headers["Call-ID"])
		assert.Equal(t, msg1.Headers["CSeq"], msg2.Headers["CSeq"])
		assert.Equal(t, msg1.Headers["From"], msg2.Headers["From"])
		assert.Equal(t, msg1.Headers["To"], msg2.Headers["To"])
	}
}

// --- BuildRequest / BuildResponse ---

func TestBuildRequest(t *testing.T) {
	msg := sip.BuildRequest("REGISTER", "sip:ims.local", map[string]string{
		"From":    "<sip:user@ims.local>;tag=abc",
		"To":      "<sip:user@ims.local>",
		"Call-ID": "test-call-id",
		"CSeq":    "1 REGISTER",
	})
	assert.Equal(t, "REGISTER", msg.Method)
	assert.Equal(t, "70", msg.Headers["Max-Forwards"])
	assert.Equal(t, "0", msg.Headers["Content-Length"])
	assert.Contains(t, msg.Headers["User-Agent"], "cellrift")

	// Serialise and re-parse to validate wire format.
	raw := sip.Serialise(msg)
	parsed, err := sip.ParseMessage(raw)
	require.NoError(t, err)
	assert.Equal(t, "REGISTER", parsed.Method)
}

func TestBuildResponse(t *testing.T) {
	msg := sip.BuildResponse(200, "OK", map[string]string{
		"From":    "<sip:user@ims.local>;tag=abc",
		"To":      "<sip:user@ims.local>;tag=srv1",
		"Call-ID": "test-call-id",
		"CSeq":    "1 REGISTER",
	})
	assert.Equal(t, 200, msg.StatusCode)
	assert.Equal(t, "0", msg.Headers["Content-Length"])
}

// --- ResponseFieldsMap ---

func TestResponseFieldsMap(t *testing.T) {
	msg, err := sip.ParseMessage(okResponse)
	require.NoError(t, err)

	fields := sip.ResponseFieldsMap(msg)
	assert.Equal(t, 200, fields["status_code"])
	assert.Equal(t, "OK", fields["reason_phrase"])

	headers, ok := fields["header"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "reg-001@10.0.0.1", headers["Call-ID"])
}

// --- Additional status code fixtures ---

var forbiddenResponse = []byte("SIP/2.0 403 Forbidden\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK776\r\n" +
	"From: <sip:447700900001@ims.local>;tag=abc123\r\n" +
	"To: <sip:447700900001@ims.local>;tag=srv789\r\n" +
	"Call-ID: reg-001@10.0.0.1\r\n" +
	"CSeq: 1 REGISTER\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

var notFoundResponse = []byte("SIP/2.0 404 Not Found\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK999\r\n" +
	"From: <sip:447700900001@ims.local>;tag=def456\r\n" +
	"To: <sip:447700900002@ims.local>;tag=nf001\r\n" +
	"Call-ID: call-001@10.0.0.1\r\n" +
	"CSeq: 1 INVITE\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

var requestTerminatedResponse = []byte("SIP/2.0 487 Request Terminated\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK999\r\n" +
	"From: <sip:447700900001@ims.local>;tag=def456\r\n" +
	"To: <sip:447700900002@ims.local>;tag=term001\r\n" +
	"Call-ID: call-001@10.0.0.1\r\n" +
	"CSeq: 1 INVITE\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

var serviceUnavailableResponse = []byte("SIP/2.0 503 Service Unavailable\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK999\r\n" +
	"From: <sip:447700900001@ims.local>;tag=def456\r\n" +
	"To: <sip:447700900002@ims.local>;tag=su001\r\n" +
	"Call-ID: call-001@10.0.0.1\r\n" +
	"CSeq: 1 INVITE\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

func TestParseMessage_403Forbidden(t *testing.T) {
	msg, err := sip.ParseMessage(forbiddenResponse)
	require.NoError(t, err)
	assert.Equal(t, 403, msg.StatusCode)
	assert.Equal(t, "Forbidden", msg.ReasonPhrase)
}

func TestParseMessage_404NotFound(t *testing.T) {
	msg, err := sip.ParseMessage(notFoundResponse)
	require.NoError(t, err)
	assert.Equal(t, 404, msg.StatusCode)
	assert.Equal(t, "Not Found", msg.ReasonPhrase)
}

func TestParseMessage_487RequestTerminated(t *testing.T) {
	msg, err := sip.ParseMessage(requestTerminatedResponse)
	require.NoError(t, err)
	assert.Equal(t, 487, msg.StatusCode)
	assert.Equal(t, "Request Terminated", msg.ReasonPhrase)
}

func TestParseMessage_503ServiceUnavailable(t *testing.T) {
	msg, err := sip.ParseMessage(serviceUnavailableResponse)
	require.NoError(t, err)
	assert.Equal(t, 503, msg.StatusCode)
	assert.Equal(t, "Service Unavailable", msg.ReasonPhrase)
}

// --- 3GPP header round-trip tests ---

var registerWith3GPPHeaders = []byte("REGISTER sip:ims.local SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK776\r\n" +
	"From: <sip:447700900001@ims.local>;tag=abc123\r\n" +
	"To: <sip:447700900001@ims.local>\r\n" +
	"Call-ID: reg-3gpp@10.0.0.1\r\n" +
	"CSeq: 1 REGISTER\r\n" +
	"Contact: <sip:447700900001@10.0.0.1:5060>\r\n" +
	"P-Visited-Network-ID: ims.mnc001.mcc001.3gppnetwork.org\r\n" +
	"P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=00101c001234\r\n" +
	"P-Charging-Vector: icid-value=abc123;icid-generated-at=pcscf.ims.local\r\n" +
	"Path: <sip:pcscf.ims.local;lr>\r\n" +
	"Max-Forwards: 70\r\n" +
	"Content-Length: 0\r\n" +
	"\r\n")

func TestRoundTrip_3GPPHeaders(t *testing.T) {
	msg1, err := sip.ParseMessage(registerWith3GPPHeaders)
	require.NoError(t, err)

	// Verify headers are parsed.
	assert.Contains(t, msg1.Headers, "P-Visited-Network-ID")
	assert.Contains(t, msg1.Headers, "P-Access-Network-Info")
	assert.Contains(t, msg1.Headers, "P-Charging-Vector")
	assert.Contains(t, msg1.Headers, "Path")

	// Round-trip through serialise → parse.
	serialised := sip.Serialise(msg1)
	msg2, err := sip.ParseMessage(serialised)
	require.NoError(t, err)

	assert.Equal(t, msg1.Headers["P-Visited-Network-ID"], msg2.Headers["P-Visited-Network-ID"])
	assert.Equal(t, msg1.Headers["P-Access-Network-Info"], msg2.Headers["P-Access-Network-Info"])
	assert.Equal(t, msg1.Headers["P-Charging-Vector"], msg2.Headers["P-Charging-Vector"])
	assert.Equal(t, msg1.Headers["Path"], msg2.Headers["Path"])
}

func TestParseMessage_AllStatusCodes_Extended(t *testing.T) {
	responses := []struct {
		raw    []byte
		code   int
		reason string
	}{
		{okResponse, 200, "OK"},
		{ringingResponse, 180, "Ringing"},
		{forbiddenResponse, 403, "Forbidden"},
		{notFoundResponse, 404, "Not Found"},
		{busyResponse, 486, "Busy Here"},
		{requestTerminatedResponse, 487, "Request Terminated"},
		{serviceUnavailableResponse, 503, "Service Unavailable"},
	}
	for _, tt := range responses {
		msg, err := sip.ParseMessage(tt.raw)
		require.NoError(t, err, "failed parsing status %d", tt.code)
		assert.Equal(t, tt.code, msg.StatusCode)
		assert.Equal(t, tt.reason, msg.ReasonPhrase)
	}
}

// --- Error cases ---

func TestParseMessage_Empty(t *testing.T) {
	_, err := sip.ParseMessage([]byte{})
	assert.Error(t, err)
}

func TestParseMessage_Malformed(t *testing.T) {
	_, err := sip.ParseMessage([]byte("garbage"))
	assert.Error(t, err)
}
