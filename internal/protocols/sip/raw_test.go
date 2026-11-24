// raw_test.go proves action: raw's SIP half in isolation: action: raw writes
// an arbitrary byte payload directly onto this adapter's own transport,
// bypassing the SIP encoder entirely, and reaches a real mock peer
// byte-for-byte. No scenario wiring exists yet — this file proves only that
// the adapter's own dispatch works.
package sip_test

import (
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawSettleWindow gives a raw step's background write time to actually reach
// the mock's read loop before RawReads() is inspected. A raw step performs no
// Receive by design, so unlike an ordinary send step — where seeing the
// answer already proves the request was read — there is no synchronisation
// point that guarantees the mock has recorded the write yet the instant
// Execute returns. Mirrors malform_adapter_test.go's identical
// malformSettleWindow discipline.
const rawSettleWindow = 200 * time.Millisecond

// crossProtocolDiameterHeader is a 20-byte Diameter CER header — Version,
// Message Length (20), R-flag + Command Code 257 (Capabilities-Exchange),
// Application-Id 0, Hop-by-Hop 0x11111111, End-to-End 0x22222222 — used as
// the cross-protocol payload a raw step over a SIP connection declares.
// Hand-built rather than via diameter.BuildRequest: this package cannot
// import internal/protocols/diameter without an import cycle risk this
// plan's own file-scope leaves unverified, and a fixed 20-byte literal is
// simpler than adding that dependency for one fixture.
var crossProtocolDiameterHeader = []byte{
	0x01, 0x00, 0x00, 0x14, // Version=1, Message Length=20
	0x80, 0x00, 0x01, 0x01, // R-flag set, Command Code=257 (CER)
	0x00, 0x00, 0x00, 0x00, // Application-Id=0
	0x11, 0x11, 0x11, 0x11, // Hop-by-Hop ID
	0x22, 0x22, 0x22, 0x22, // End-to-End ID
}

func TestAdapter_ActionRaw_ReachesMockUASWire(t *testing.T) {
	uas := sip.NewTCPMockUAS(t, sip.TCPFaultSilence)
	defer uas.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := sip.NewAdapterWithTransport(tr, "tcp", 2*time.Second)
	defer adapter.Close()

	config := map[string]string{malform.RawPayloadKey: string(crossProtocolDiameterHeader)}
	result, err := adapter.Execute("raw", uas.Addr(), "", nil, config)
	require.NoError(t, err)

	require.Equal(t, common.StatusPassed, result.Status, "raw send should pass: %s", result.Error)
	assert.Equal(t, len(crossProtocolDiameterHeader), result.Response[malform.RawBytesSentKey])
	assert.Nil(t, result.RawResponse, "a raw step never attempts to parse a response")

	time.Sleep(rawSettleWindow)
	reads := uas.RawReads()
	require.NotEmpty(t, reads)
	assert.Equal(t, crossProtocolDiameterHeader, reads[len(reads)-1],
		"the mock's raw read must be byte-identical to the injected Diameter header")
}

func TestAdapter_ActionRaw_MissingPayloadIsGoError(t *testing.T) {
	uas := sip.NewTCPMockUAS(t, sip.TCPFaultSilence)
	defer uas.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := sip.NewAdapterWithTransport(tr, "tcp", 2*time.Second)
	defer adapter.Close()

	result, err := adapter.Execute("raw", uas.Addr(), "", nil, nil)
	require.Error(t, err)
	require.Nil(t, result)
	assert.Contains(t, err.Error(), "no payload")
}

// TestAdapter_ActionRaw_NonUTF8PayloadSurvivesUnchanged proves a payload
// containing bytes that are not valid UTF-8 round-trips byte-for-byte —
// Go strings hold arbitrary bytes, so no adapter-level conversion may
// sanitise or reject them.
func TestAdapter_ActionRaw_NonUTF8PayloadSurvivesUnchanged(t *testing.T) {
	uas := sip.NewTCPMockUAS(t, sip.TCPFaultSilence)
	defer uas.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := sip.NewAdapterWithTransport(tr, "tcp", 2*time.Second)
	defer adapter.Close()

	payload := []byte{0xFF, 0xFE, 0x00, 0x80, 0x81, 0xC0, 0xC1}
	config := map[string]string{malform.RawPayloadKey: string(payload)}
	result, err := adapter.Execute("raw", uas.Addr(), "", nil, config)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status)

	time.Sleep(rawSettleWindow)
	reads := uas.RawReads()
	require.NotEmpty(t, reads)
	assert.Equal(t, payload, reads[len(reads)-1])
}
