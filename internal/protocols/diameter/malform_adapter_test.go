// malform_adapter_test.go proves the avp_length/msg_length overrides at the wire: a
// declared malform: block, folded into a step's config map by
// internal/engine/executor.go's foldMalformConfig, changes the exact bytes
// a real Diameter peer reads off a real TCP socket.
//
// Every assertion in this file reads MockHSS.RawReads(), the pre-framing
// capture, and never the mock's framed request history (LastRequestRaw /
// RequestsOfCommand / their shared underlying accessor). Why: extractMessage
// (testing.go) frames inbound bytes using the Message Length header at
// buf[1:4] — the exact field msg_length corrupts — and recordRequest runs
// only after that framing succeeds. A corrupted Message Length can
// therefore mean the framed history stays empty forever, or a corrupted AVP
// length can desynchronise DecodeAVPs inside ParseMessage and make the mock
// answer nothing at all. A passing test here proves the corrupted bytes
// reached the wire exactly as declared — NOT that the peer accepted them;
// the mock's reaction to a malformed message is not this file's claim.
package diameter_test

import (
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// malformStepTimeout is short so the cases where the mock never answers a
// corrupted message (a real, expected outcome, not a bug) finish fast.
const malformStepTimeout = 1500 * time.Millisecond

// malformSettleWindow gives a background write time to actually reach the
// mock's read loop before RawReads() is inspected, matching
// conn_fault_e2e_test.go's discipline of distinguishing "not observed yet"
// from "will never arrive".
const malformSettleWindow = 200 * time.Millisecond

// newMalformAdapter builds a Diameter adapter over a REAL TCP transport,
// wrapped exactly the way cmd/cellrift/main.go wraps it, pointed at a real
// in-process MockHSS. Copied from conn_fault_test.go's newFaultAdapter —
// nothing here fabricates a transport double (CLAUDE.md's no-mocks rule for
// protocol adapters in integration tests).
func newMalformAdapter(t *testing.T) (*diameter.Adapter, *diameter.MockHSS) {
	t.Helper()
	m := diameter.NewMockHSS(t)
	t.Cleanup(m.Close)
	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	a := diameter.NewAdapterWithTransport(impair.Wrap(tr), malformStepTimeout)
	t.Cleanup(func() { _ = a.Close() })
	return a, m
}

// malformFields is the same four-key UAR fixture conn_fault_test.go's
// faultFields() uses, duplicated locally per this package's own per-file
// fixture convention rather than exported across files.
func malformFields() map[string]string {
	return map[string]string{
		"Session-Id":      "cellrift;test;malform",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}
}

// lastApplicationRead returns the LAST entry of m.RawReads(), requiring the
// slice to be non-empty. The CER is itself a read, so the UAR under test is
// never entry 0 — mirroring TestAdapter_Truncate_PeerSeesShortRead's own
// note in conn_fault_test.go.
func lastApplicationRead(t *testing.T, m *diameter.MockHSS) []byte {
	t.Helper()
	reads := m.RawReads()
	require.NotEmpty(t, reads, "the mock must have read at least the CER")
	return reads[len(reads)-1]
}

// avpLengthAt walks raw from offset 20 (HeaderLength) exactly the way the
// production walk does, and returns the 3-byte Length field of the first
// AVP whose code matches. Written independently of the production
// findAVPOffset so a defect in that walk cannot hide behind itself.
func avpLengthAt(t *testing.T, raw []byte, code uint32) int {
	t.Helper()
	const headerLength = 20
	off := headerLength
	for off+8 <= len(raw) {
		avpCode := uint32(raw[off])<<24 | uint32(raw[off+1])<<16 | uint32(raw[off+2])<<8 | uint32(raw[off+3])
		flags := raw[off+4]
		length := int(raw[off+5])<<16 | int(raw[off+6])<<8 | int(raw[off+7])
		if avpCode == code {
			return length
		}
		off += avpAdvance(flags, length)
	}
	t.Fatalf("AVP code %d not found while walking %d raw bytes", code, len(raw))
	return 0
}

// avpAdvance computes how far to advance past one AVP given its own header
// flags and (uncorrupted) length field: 8-byte header, or 12 with the V bit
// set, padded up to the next 4-byte boundary — the same math avp.go's own
// avpHeaderLen/padTo4 implement, re-derived here rather than called so this
// test file does not depend on those unexported production helpers.
func avpAdvance(flags byte, length int) int {
	hdrLen := 8
	if flags&0x80 != 0 {
		hdrLen = 12
	}
	if length < hdrLen {
		length = hdrLen
	}
	if rem := length % 4; rem != 0 {
		length += 4 - rem
	}
	return length
}

// messageLengthOf decodes the 3-byte Message Length header at raw[1:4].
func messageLengthOf(raw []byte) int {
	return int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
}

func TestAdapter_Malform_AVPLength_ReachesTheWire(t *testing.T) {
	a, m := newMalformAdapter(t)

	result, err := a.Execute("send", m.Addr(), "UAR", malformFields(), map[string]string{
		malform.AVPLengthNameKey:  "Session-Id",
		malform.AVPLengthValueKey: "9999",
	})
	require.NoError(t, err)
	_ = result // result.Status is not asserted here — the mock's reaction to a corrupted AVP is not this test's claim.

	time.Sleep(malformSettleWindow)
	raw := lastApplicationRead(t, m)

	assert.Equal(t, 9999, avpLengthAt(t, raw, diameter.AVPCodeSessionID),
		"the targeted AVP's Length field must carry the declared wrong value")
	assert.Equal(t, len(raw), messageLengthOf(raw),
		"only the AVP field diverged; the Message Length header must still decode to the true byte length")
}

func TestAdapter_Malform_MessageLength_ReachesTheWire(t *testing.T) {
	a, m := newMalformAdapter(t)

	result, err := a.Execute("send", m.Addr(), "UAR", malformFields(), map[string]string{
		malform.MsgLengthKey: "9999",
	})
	require.NoError(t, err)
	_ = result

	time.Sleep(malformSettleWindow)
	raw := lastApplicationRead(t, m)

	assert.Equal(t, 9999, messageLengthOf(raw),
		"the Message Length header must carry the declared wrong value")
	assert.NotEqual(t, len(raw), messageLengthOf(raw),
		"the entry's actual byte length must be the true, uncorrupted length, distinct from the corrupted header")
}

func TestAdapter_Malform_Unimpaired_IsByteIdentical(t *testing.T) {
	a, m := newMalformAdapter(t)

	fields := malformFields()
	result, err := a.Execute("send", m.Addr(), "UAR", fields, map[string]string{})
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "an unimpaired UAR must succeed: %s", result.Error)

	time.Sleep(malformSettleWindow)
	raw := lastApplicationRead(t, m)

	assert.Equal(t, len(raw), messageLengthOf(raw),
		"an unimpaired step's Message Length header must equal its own byte length")

	const avpHeaderLen = 8 // Session-Id is not vendor-scoped: 4 (Code) + 1 (Flags) + 3 (Length)
	wantSessionIDLen := avpHeaderLen + len(fields["Session-Id"])
	assert.Equal(t, wantSessionIDLen, avpLengthAt(t, raw, diameter.AVPCodeSessionID),
		"an unimpaired step's Session-Id AVP Length must be correct — the control proving corruption is inert when undeclared")
}

func TestAdapter_Malform_BothOverrides(t *testing.T) {
	a, m := newMalformAdapter(t)

	result, err := a.Execute("send", m.Addr(), "UAR", malformFields(), map[string]string{
		malform.AVPLengthNameKey:  "Session-Id",
		malform.AVPLengthValueKey: "9999",
		malform.MsgLengthKey:      "8888",
	})
	require.NoError(t, err)
	_ = result

	time.Sleep(malformSettleWindow)
	raw := lastApplicationRead(t, m)

	assert.Equal(t, 9999, avpLengthAt(t, raw, diameter.AVPCodeSessionID),
		"the AVP Length override must land alongside the Message Length override")
	assert.Equal(t, 8888, messageLengthOf(raw),
		"the Message Length override must land alongside the AVP Length override")
}

// TestAdapter_Malform_UnknownAVPFailsLoudly declares an avp_length override
// naming CC-Request-Type — a real dictionary AVP, but one this UAR's own
// fields never carry — proving applyMalform's findAVPOffset failure
// propagates as a Go error, and that the failed build never reaches the
// wire at all.
func TestAdapter_Malform_UnknownAVPFailsLoudly(t *testing.T) {
	a, m := newMalformAdapter(t)

	before := len(m.RequestsOfCommand(diameter.CmdUserAuthorization))

	result, err := a.Execute("send", m.Addr(), "UAR", malformFields(), map[string]string{
		malform.AVPLengthNameKey:  "CC-Request-Type",
		malform.AVPLengthValueKey: "9999",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CC-Request-Type")
	assert.Nil(t, result)

	time.Sleep(malformSettleWindow)
	assert.Len(t, m.RequestsOfCommand(diameter.CmdUserAuthorization), before,
		"a malform declaration that fails to build must never put a request on the wire")
}

// TestAdapter_Malform_DoesNotUseFramedRequestHistory is the executable
// statement of why this file never asserts against the framed request
// history: a msg_length override corrupts the exact field extractMessage
// frames on, so RequestsOfCommand must stay empty for this request even
// though RawReads() shows the corrupted bytes genuinely reached the wire.
func TestAdapter_Malform_DoesNotUseFramedRequestHistory(t *testing.T) {
	a, m := newMalformAdapter(t)

	result, err := a.Execute("send", m.Addr(), "UAR", malformFields(), map[string]string{
		malform.MsgLengthKey: "9999",
	})
	require.NoError(t, err)
	_ = result

	time.Sleep(malformSettleWindow)

	assert.Empty(t, m.RequestsOfCommand(diameter.CmdUserAuthorization),
		"a corrupted Message Length must never frame into the mock's request history")

	raw := lastApplicationRead(t, m)
	assert.Equal(t, 9999, messageLengthOf(raw),
		"RawReads() must still show the corrupted request even though it never framed")
}
