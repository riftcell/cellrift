// raw_test.go proves action: raw's Diameter half in isolation: action: raw
// writes an arbitrary byte payload directly onto this adapter's own
// connection, bypassing the Diameter encoder entirely, and reaches a real
// mock peer byte-for-byte. No scenario wiring exists yet — this file proves
// only that the adapter's own dispatch works.
package diameter_test

import (
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawSettleWindow gives a raw step's background write time to actually reach
// the mock's read loop before RawReads() is inspected. A raw step performs no
// Receive by design (it never waits for a peer's answer), so — unlike an
// ordinary send step, where seeing the answer already proves the request was
// read — there is no synchronisation point that guarantees the mock's read
// loop has recorded the write yet at the instant Execute returns. Mirrors
// malform_adapter_test.go's identical malformSettleWindow discipline.
const rawSettleWindow = 200 * time.Millisecond

// crossProtocolSIPPayload builds a real, wire-serialised SIP REGISTER
// request — the exact shape a raw step declares to prove one protocol's
// bytes reach the wire over another protocol's connection. Built with the
// real SIP encoder, not a hand-typed literal, so the payload is genuinely
// SIP-shaped rather than merely SIP-looking.
func crossProtocolSIPPayload() []byte {
	msg := sip.BuildRequest("REGISTER", "sip:ims.local", map[string]string{
		"Via":     "SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK-raw-test",
		"From":    "<sip:447700900001@ims.local>;tag=raw-test",
		"To":      "<sip:447700900001@ims.local>",
		"Call-ID": "raw-test-call-id@127.0.0.1",
		"CSeq":    "1 REGISTER",
	})
	return sip.Serialise(msg)
}

func TestAdapter_ActionRaw_ReachesMockHSSWire(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	payload := crossProtocolSIPPayload()
	config := map[string]string{malform.RawPayloadKey: string(payload)}

	result, err := adapter.Execute("raw", hss.Addr(), "", nil, config)
	require.NoError(t, err)

	require.Equal(t, common.StatusPassed, result.Status, "raw send should pass: %s", result.Error)
	assert.Equal(t, len(payload), result.Response[malform.RawBytesSentKey])
	assert.Nil(t, result.RawResponse, "a raw step never attempts to parse a response")
	_, hasAVP := result.Response["avp"]
	assert.False(t, hasAVP, "a raw step's result must carry no parsed Diameter fields")

	time.Sleep(rawSettleWindow)
	reads := hss.RawReads()
	require.NotEmpty(t, reads)
	assert.Equal(t, payload, reads[len(reads)-1],
		"the mock's last raw read must be byte-identical to the injected SIP text")
}

func TestAdapter_ActionRaw_MissingPayloadIsGoError(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	result, err := adapter.Execute("raw", hss.Addr(), "", nil, nil)
	require.Error(t, err)
	require.Nil(t, result)
	assert.Contains(t, err.Error(), "no payload")
}

// TestAdapter_ActionRaw_ReusesHandshakedConnection proves a raw step
// following a normal UAR send step writes onto the SAME connection: the
// mock records exactly one CER across both steps.
func TestAdapter_ActionRaw_ReusesHandshakedConnection(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;raw",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}
	sendResult, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, sendResult.Status)

	payload := crossProtocolSIPPayload()
	config := map[string]string{malform.RawPayloadKey: string(payload)}
	rawResult, err := adapter.Execute("raw", hss.Addr(), "", nil, config)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, rawResult.Status)

	cers := hss.RequestsOfCommand(diameter.CmdCapabilitiesExchange)
	assert.Len(t, cers, 1, "a raw step after a normal send must reuse the same handshaked connection, not re-handshake")
}
