package sip_test

import (
	"strings"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- SDPTemplate ---

func TestSDPTemplate_BasicAudioOffer(t *testing.T) {
	body, err := sip.SDPTemplate("basic_audio_offer")
	require.NoError(t, err)
	assert.Contains(t, body, "v=0")
	assert.Contains(t, body, "m=audio")
	assert.Contains(t, body, "a=rtpmap:8 PCMA/8000")
	assert.Contains(t, body, "a=rtpmap:0 PCMU/8000")
	assert.Contains(t, body, "a=rtpmap:101 telephone-event/8000")
	assert.Contains(t, body, "a=sendrecv")
	// Lines must be CRLF-terminated per RFC 4566 §5.
	assert.True(t, strings.Contains(body, "\r\n"), "SDP lines must use CRLF")
}

func TestSDPTemplate_UnknownTemplate(t *testing.T) {
	_, err := sip.SDPTemplate("unknown_template")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown SDP template")
}

// --- ParseSDP ---

func TestParseSDP_ExtractsPortAndCodec(t *testing.T) {
	sdp := "v=0\r\no=mock 0 0 IN IP4 127.0.0.1\r\ns=mock\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 49170 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\na=sendrecv\r\n"
	result := sip.ParseSDP([]byte(sdp))
	assert.Equal(t, 49170, result["port"])
	assert.Equal(t, "PCMA", result["codec"])
}

func TestParseSDP_FirstRtpmapWins(t *testing.T) {
	sdp := "v=0\r\nm=audio 5000 RTP/AVP 8 0\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:0 PCMU/8000\r\n"
	result := sip.ParseSDP([]byte(sdp))
	assert.Equal(t, "PCMA", result["codec"])
}

func TestParseSDP_EmptyBody(t *testing.T) {
	result := sip.ParseSDP([]byte{})
	assert.Equal(t, 0, result["port"])
	assert.Equal(t, "", result["codec"])
}

func TestParseSDP_NoAudioLine(t *testing.T) {
	sdp := "v=0\r\no=cellrift 0 0 IN IP4 0.0.0.0\r\ns=cellrift\r\nt=0 0\r\n"
	result := sip.ParseSDP([]byte(sdp))
	assert.Equal(t, 0, result["port"])
	assert.Equal(t, "", result["codec"])
}

// --- Adapter: INVITE with SDP body ---

func TestAdapter_SendInvite_WithSDP(t *testing.T) {
	uas, err := NewMockUAS()
	require.NoError(t, err)
	defer uas.Close()

	adapter, err := sip.NewAdapter("udp", ":0", 3*time.Second)
	require.NoError(t, err)
	defer adapter.Close()

	result, err := adapter.Execute("send", uas.Addr(), "INVITE", map[string]string{
		"From":                "<sip:447700900001@ims.local>;tag=sdp001",
		"To":                  "<sip:447700900002@ims.local>",
		"Contact":             "<sip:447700900001@10.0.0.1:5060>",
		"__body_content_type": "application/sdp",
		"__body_template":     "basic_audio_offer",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status, "error: %s", result.Error)

	// Response must include parsed SDP body.
	body, ok := result.Response["body"].(map[string]interface{})
	require.True(t, ok, "response.body should be a map")
	sdp, ok := body["sdp"].(map[string]interface{})
	require.True(t, ok, "response.body.sdp should be a map")
	assert.Equal(t, 49170, sdp["port"])
	assert.Equal(t, "PCMA", sdp["codec"])
}
