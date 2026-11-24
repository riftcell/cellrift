package diameter_test

import (
	"encoding/binary"
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildParseMessage_UAR(t *testing.T) {
	avps := []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeSessionID, "cellrift;uar;001"),
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "cellrift.ims.local"),
		diameter.NewStringAVP(diameter.AVPCodeOriginRealm, "ims.local"),
		diameter.NewStringAVP(diameter.AVPCodePublicID, "sip:447700900001@ims.local"),
	}

	raw, _, err := diameter.BuildRequest(diameter.CmdUserAuthorization, diameter.AppIDCx, avps)
	require.NoError(t, err)
	require.True(t, len(raw) >= diameter.HeaderLength)

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)

	assert.Equal(t, diameter.Version, msg.Version)
	assert.Equal(t, diameter.CmdUserAuthorization, msg.CommandCode)
	assert.Equal(t, diameter.AppIDCx, msg.AppID)
	assert.True(t, msg.IsRequest())

	// Verify AVPs.
	sid := msg.FindAVP(diameter.AVPCodeSessionID)
	require.NotNil(t, sid)
	assert.Equal(t, "cellrift;uar;001", sid.StringVal)

	pub := msg.FindAVP(diameter.AVPCodePublicID)
	require.NotNil(t, pub)
	assert.Equal(t, "sip:447700900001@ims.local", pub.StringVal)
}

func TestBuildParseMessage_CCR(t *testing.T) {
	avps := []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeSessionID, "cellrift;ccr;001"),
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "cellrift.ims.local"),
		diameter.NewStringAVP(diameter.AVPCodeOriginRealm, "ims.local"),
		diameter.NewUint32AVP(diameter.AVPCodeCCRequestType, diameter.CCRequestTypeInitial),
		diameter.NewUint32AVP(diameter.AVPCodeCCRequestNumber, 0),
	}

	raw, _, err := diameter.BuildRequest(diameter.CmdCreditControl, diameter.AppIDCreditControl, avps)
	require.NoError(t, err)

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)

	assert.Equal(t, diameter.CmdCreditControl, msg.CommandCode)
	assert.Equal(t, diameter.AppIDCreditControl, msg.AppID)
	assert.True(t, msg.IsRequest())

	// Verify credit-control AVPs.
	ccrt := msg.FindAVP(diameter.AVPCodeCCRequestType)
	require.NotNil(t, ccrt)
	assert.Equal(t, diameter.CCRequestTypeInitial, ccrt.Uint32Val)

	ccrn := msg.FindAVP(diameter.AVPCodeCCRequestNumber)
	require.NotNil(t, ccrn)
	assert.Equal(t, uint32(0), ccrn.Uint32Val)
}

func TestBuildParseMessage_UAA_Answer(t *testing.T) {
	avps := []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeSessionID, "cellrift;uar;001"),
		diameter.NewUint32AVP(diameter.AVPCodeResultCode, diameter.ResultSuccess),
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "hss.ims.local"),
		diameter.NewStringAVP(diameter.AVPCodeOriginRealm, "ims.local"),
		diameter.NewStringAVP(diameter.AVPCodeServerName, "sip:scscf.ims.local:5060"),
	}

	raw, err := diameter.BuildAnswer(diameter.CmdUserAuthorization, diameter.AppIDCx, avps)
	require.NoError(t, err)

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)

	assert.Equal(t, diameter.CmdUserAuthorization, msg.CommandCode)
	assert.False(t, msg.IsRequest())
	assert.Equal(t, diameter.ResultSuccess, msg.ResultCode())

	sn := msg.FindAVP(diameter.AVPCodeServerName)
	require.NotNil(t, sn)
	assert.Equal(t, "sip:scscf.ims.local:5060", sn.StringVal)
}

func TestMessageLengthField(t *testing.T) {
	avps := []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "test"),
	}
	raw, _, err := diameter.BuildRequest(diameter.CmdUserAuthorization, diameter.AppIDCx, avps)
	require.NoError(t, err)

	// Message length is bytes 1-3 in the header.
	msgLen := int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
	assert.Equal(t, len(raw), msgLen, "header length field must match actual message size")
}

func TestParseMessage_TooShort(t *testing.T) {
	_, err := diameter.ParseMessage([]byte{0, 0, 0})
	assert.Error(t, err)
}

func TestParseMessage_BadVersion(t *testing.T) {
	raw := make([]byte, 20)
	raw[0] = 2 // wrong version
	raw[1] = 0
	raw[2] = 0
	raw[3] = 20
	_, err := diameter.ParseMessage(raw)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}

func TestResultCode_NotPresent(t *testing.T) {
	avps := []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "test"),
	}
	raw, _, err := diameter.BuildRequest(diameter.CmdUserAuthorization, diameter.AppIDCx, avps)
	require.NoError(t, err)

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), msg.ResultCode())
}

// --- BuildRequest Hop-by-Hop ID ---

func TestBuildRequest_ReturnsHopByHopID(t *testing.T) {
	avps := []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "test"),
	}
	raw, hopByHop, err := diameter.BuildRequest(diameter.CmdUserAuthorization, diameter.AppIDCx, avps)
	require.NoError(t, err)

	assert.Equal(t, hopByHop, binary.BigEndian.Uint32(raw[12:16]),
		"returned Hop-by-Hop ID must match the bytes actually written into the header")

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)
	assert.Equal(t, hopByHop, msg.HopByHopID)
}

func TestBuildRequest_HopByHopIDsDiffer(t *testing.T) {
	avps := []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "test"),
	}
	_, first, err := diameter.BuildRequest(diameter.CmdUserAuthorization, diameter.AppIDCx, avps)
	require.NoError(t, err)

	_, second, err := diameter.BuildRequest(diameter.CmdUserAuthorization, diameter.AppIDCx, avps)
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "successive BuildRequest calls must generate different Hop-by-Hop IDs")
}

// --- Command code constants ---

func TestCommandCodeConstants(t *testing.T) {
	assert.Equal(t, uint32(300), diameter.CmdUserAuthorization)
	assert.Equal(t, uint32(272), diameter.CmdCreditControl)
}
