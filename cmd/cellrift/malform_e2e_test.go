package main

// malform_e2e_test.go is the malformed-messages feature's production-path
// proof. Every adapter-level malform test builds its own hand-constructed
// adapter, never going through cmd/cellrift/main.go's own wiring. That gap has
// already produced two production-only defects elsewhere — adapters
// constructed without impair.Wrap, and --target-host failing to recurse into
// sub-steps — neither visible to any adapter test that existed at the time.
//
// The assertions below therefore target the mock peer's own RawReads()
// arrival history, not step-result keys. A result saying
// raw_bytes_sent == 20 proves the adapter set a map key; only a 20-byte
// entry in the peer's own read history proves those bytes actually left the
// socket.

import (
	"bytes"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Step IDs and Call-IDs, exactly as scenarios/ims/malformed_sip_headers.yaml
// assigns them. Named here once, mirroring conn_fault_e2e_test.go's own
// convention, so a future edit to the scenario file only has to be
// reconciled in one place.
const (
	malformMissingViaStepID = "register_missing_via"
	malformMissingViaCallID = "malform-missing-via@lab.local"

	malformDuplicateViaStepID = "register_duplicate_via"
	malformDuplicateViaCallID = "malform-duplicate-via@lab.local"

	malformOverwriteViaStepID = "register_overwrite_via"
	malformOverwriteViaCallID = "malform-overwrite-via@lab.local"

	malformCleanStepID = "register_clean"
	malformCleanCallID = "malform-clean@lab.local"

	malformInjectStepID = "inject_diameter_over_sip"
	malformReplayStepID = "replay_captured_wire"
)

// Step IDs and their own distinguishing Session-Id fragments, exactly as
// scenarios/diameter/malformed_diameter.yaml assigns them. Each step's own
// Session-Id value ends in a distinct suffix (";1", ";2") so a raw arrival
// can be identified without depending on the mock ever framing the message
// — the whole point of the corruption these steps declare is that framing
// may not succeed.
const (
	malformBadAVPLengthStepID  = "uar_bad_avp_length"
	malformBadAVPLengthNeedle  = "test.cellrift.local;447700900201;1"
	malformBadMsgLengthStepID  = "uar_bad_msg_length"
	malformBadMsgLengthNeedle  = "test.cellrift.local;447700900201;2"
	malformInjectSIPOverDiamID = "inject_sip_over_diameter"
	malformDeclaredBadLength   = 9999
	sipOverDiameterHexPayload  = "4f5054494f4e53207369703a6873732e6c61622e6c6f63616c205349502f322e300d0a0d0a"
)

// diameterCERHeader is the literal decode of the scenario's raw.hex value
// "0100001480000101000000001111111122222222": Version 1 (byte 0), Message
// Length 20 (bytes 1-3), R-flag set plus Command Code 257
// Capabilities-Exchange (bytes 4-7), Application-Id 0 (bytes 8-11),
// Hop-by-Hop Identifier 0x11111111 (bytes 12-15), and End-to-End Identifier
// 0x22222222 (bytes 16-19).
var diameterCERHeader = []byte{
	0x01, 0x00, 0x00, 0x14, 0x80, 0x00, 0x01, 0x01,
	0x00, 0x00, 0x00, 0x00, 0x11, 0x11, 0x11, 0x11,
	0x22, 0x22, 0x22, 0x22,
}

// malformSettleWindow mirrors internal/protocols/sip's own
// malformSettleWindow/rawSettleWindow discipline: a raw-action step performs
// no Receive, so nothing synchronises "the mock has read the bytes" with
// "Execute has returned" the way a send step's response wait does.
const malformSettleWindow = 200 * time.Millisecond

// readsContaining and readsOfLength are declared in conn_fault_e2e_test.go
// (same package) and are reused here rather than redeclared.

// viaLines returns every line of raw beginning "Via:" (case-insensitive),
// split on the CRLF line terminator Serialise/malform.go both use.
func viaLines(raw []byte) []string {
	var out []string
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) >= 4 && strings.EqualFold(line[:4], "Via:") {
			out = append(out, line)
		}
	}
	return out
}

// readsEqualTo returns the mock's raw reads that are byte-identical to want.
func readsEqualTo(reads [][]byte, want []byte) [][]byte {
	var out [][]byte
	for _, r := range reads {
		if bytes.Equal(r, want) {
			out = append(out, r)
		}
	}
	return out
}

// threeByteUint reads a 3-byte big-endian unsigned integer, the exact wire
// width both the Diameter AVP Length field and the Message Length header
// use (RFC 6733 §4.1, §3).
func threeByteUint(b []byte) int {
	return int(b[0])<<16 | int(b[1])<<8 | int(b[2])
}

// avpLengthFieldAt independently locates a non-vendor AVP's own 3-byte
// Length field by finding the ASCII bytes of its own data value in raw and
// walking back 8 bytes (Code(4) + Flags(1) + Length(3), the header width
// for an AVP with no Vendor-Id) — a separate, from-scratch computation from
// findAVPOffset/avpHeaderLen in internal/protocols/diameter, so a defect in
// the production AVP walk cannot hide behind a test that calls the same
// code to check itself. Session-Id (the only AVP this test targets) is
// declared vendor:false in the dictionary, so the 8-byte header width holds.
func avpLengthFieldAt(raw, dataNeedle []byte) []byte {
	idx := bytes.Index(raw, dataNeedle)
	if idx < 8 {
		return nil
	}
	hdrStart := idx - 8
	return raw[hdrStart+5 : hdrStart+8]
}

// requestReadsContaining narrows readsContaining to arrivals that are
// themselves a REGISTER request. This matters for malform-clean@lab.local
// specifically: replay_captured_wire later replays register_clean's own
// captured 200 OK response verbatim over a fresh connection to the same
// mock, so a bare Call-ID search sees TWO arrivals sharing that Call-ID —
// the original request and its own later-replayed response. Only the
// request is what this test's Via-line assertions are about.
func requestReadsContaining(reads [][]byte, needle string) [][]byte {
	var out [][]byte
	for _, r := range readsContaining(reads, needle) {
		if bytes.HasPrefix(r, []byte("REGISTER ")) {
			out = append(out, r)
		}
	}
	return out
}

// TestRunScenario_MalformedHeadersSurviveProductionWiring runs
// scenarios/ims/malformed_sip_headers.yaml through the real production run
// path (the same method cellrift run itself calls) and proves the
// three sip_header ops (missing, duplicate, overwrite) and action: raw's
// inline-hex cross-protocol injection all reach a real mock peer's socket
// exactly as declared, asserted against mock.RawReads() rather than step
// results.
func TestRunScenario_MalformedHeadersSurviveProductionWiring(t *testing.T) {
	mock := sip.NewTCPMockUAS(t, sip.TCPFaultNone)
	defer mock.Close()

	cfg := &runConfig{timeout: 5 * time.Second, targetHost: mock.Addr()}

	runStart := time.Now()
	result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", "malformed_sip_headers.yaml"))
	elapsed := time.Since(runStart)
	require.NotNil(t, result)
	assert.Less(t, elapsed, 5*time.Second,
		"every malformed step declares timeout_ms: 500 — the whole run must stay well inside the 5s budget")

	// Settle window before reading arrival history: some of these steps may
	// still be waiting on a response the mock cannot correlate against a
	// corrupted request when Execute returns.
	time.Sleep(malformSettleWindow)
	reads := mock.RawReads()

	missing := requestReadsContaining(reads, malformMissingViaCallID)
	require.Len(t, missing, 1, "exactly one arrival must carry the missing-Via request's Call-ID")
	assert.Empty(t, viaLines(missing[0]), "the missing op must leave zero Via lines on the wire")

	duplicate := requestReadsContaining(reads, malformDuplicateViaCallID)
	require.Len(t, duplicate, 1, "exactly one arrival must carry the duplicate-Via request's Call-ID")
	assert.Len(t, viaLines(duplicate[0]), 2, "the duplicate op must leave exactly two Via lines on the wire")

	overwrite := requestReadsContaining(reads, malformOverwriteViaCallID)
	require.Len(t, overwrite, 1, "exactly one arrival must carry the overwrite-Via request's Call-ID")
	overwriteVia := viaLines(overwrite[0])
	require.Len(t, overwriteVia, 1, "the overwrite op must leave exactly one Via line on the wire")
	assert.Equal(t, "Via: SIP/2.0/TCP not-a-real-via", overwriteVia[0])

	// The control: an ordinary REGISTER, no malform: block, proving the
	// three malformed steps above didn't somehow corrupt the connection or
	// the adapter's own state for subsequent steps.
	clean := requestReadsContaining(reads, malformCleanCallID)
	require.Len(t, clean, 1, "exactly one arrival must carry the clean control request's Call-ID")
	cleanVia := viaLines(clean[0])
	require.Len(t, cleanVia, 1, "the unimpaired control must carry exactly one well-formed Via line")
	assert.Contains(t, cleanVia[0], "SIP/2.0/TCP")
	assert.Contains(t, cleanVia[0], "branch=")

	// action: raw's inline-hex byte source: a real Diameter CER header
	// injected directly over this SIP connection, byte-identical.
	injected := readsEqualTo(reads, diameterCERHeader)
	require.Len(t, injected, 1, "exactly one arrival must be byte-identical to the injected Diameter CER header")

	injectStep := findStep(t, result, malformInjectStepID)
	require.NotNil(t, injectStep.Result)
	assert.Equal(t, common.StatusPassed, injectStep.Result.Status, "error: %v", injectStep.Result.Error)
	assert.Equal(t, 20, injectStep.Result.Response[malform.RawBytesSentKey])

	// Deliberately NOT asserted: result.Status for register_missing_via,
	// register_duplicate_via, or register_overwrite_via. All three steps
	// send bytes a mock parses differently than a hand-built response
	// builder was designed for, and this test makes no claim about how any
	// peer — mock or real — reacts to a header-malformed request. What
	// matters, and is asserted above, is that the declared corruption
	// genuinely reached the wire.
}

// TestRunScenario_ActionRawSurvivesProductionWiring drives the same
// scenario file against a fresh mock and proves action: raw's two byte
// sources end to end: the inline-hex Diameter injection (also proven above,
// re-confirmed here as this test's own control) and the captured
// response.__raw replay, which is the production-path proof that a
// response.__raw capture round-trips through state.Resolve into raw.ref
// with no byte lost. Kept separate from the header test so a failure names
// which half broke.
func TestRunScenario_ActionRawSurvivesProductionWiring(t *testing.T) {
	mock := sip.NewTCPMockUAS(t, sip.TCPFaultNone)
	defer mock.Close()

	cfg := &runConfig{timeout: 5 * time.Second, targetHost: mock.Addr()}

	result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", "malformed_sip_headers.yaml"))
	require.NotNil(t, result)

	time.Sleep(malformSettleWindow)
	reads := mock.RawReads()

	// Row 1: the inline-hex injection.
	injectStep := findStep(t, result, malformInjectStepID)
	require.NotNil(t, injectStep.Result)
	assert.Equal(t, common.StatusPassed, injectStep.Result.Status, "error: %v", injectStep.Result.Error)
	assert.Equal(t, 20, injectStep.Result.Response[malform.RawBytesSentKey])
	injected := readsEqualTo(reads, diameterCERHeader)
	require.Len(t, injected, 1, "exactly one arrival must be byte-identical to the injected Diameter CER header")

	// Row 2: the captured-response.__raw replay. register_clean's own
	// RawResponse is the mock's real 200 OK bytes for that step — captured
	// via response.__raw, resolved through {{ register_clean.clean_wire }},
	// and replayed verbatim by replay_captured_wire.
	cleanStep := findStep(t, result, malformCleanStepID)
	require.NotNil(t, cleanStep.Result)
	require.NotEmpty(t, cleanStep.Result.RawResponse,
		"register_clean must have captured the mock's real response bytes for step 6 to replay")

	replayStep := findStep(t, result, malformReplayStepID)
	require.NotNil(t, replayStep.Result)
	assert.Equal(t, common.StatusPassed, replayStep.Result.Status, "error: %v", replayStep.Result.Error)
	sentBytes, ok := replayStep.Result.Response[malform.RawBytesSentKey].(int)
	require.True(t, ok, "response.raw_bytes_sent must be present and an int")
	assert.Greater(t, sentBytes, 0)

	replayed := readsEqualTo(reads, cleanStep.Result.RawResponse)
	require.Len(t, replayed, 1,
		"exactly one arrival must be byte-identical to register_clean's own captured RawResponse")

	// Row 3 (the bypass row): neither raw step's own request-shaped headers
	// come from this adapter's own buildRequestMessage. Max-Forwards is
	// exclusively injected by a SIP send step's own request builder and
	// never appears on a 20-byte Diameter header or on a captured SIP
	// response — its absence from both raw arrivals is the wire-level proof
	// that action: raw never calls buildRequestMessage.
	assert.NotContains(t, string(injected[0]), "Max-Forwards")
	assert.NotContains(t, string(injected[0]), "Call-ID")
	assert.NotContains(t, string(replayed[0]), "Max-Forwards")

	// Deliberately NOT asserted:
	//   - the mock's own recorded transport/parse errors (Errors()). With
	//     TCPFaultNone the mock tries to parse every arrival it reads, and a
	//     deliberately malformed or cross-protocol request producing a
	//     mock-side parse error is a consequence of the feature working
	//     exactly as declared, not a defect in it.
	//   - any claim about how a real peer (a real P-CSCF or a real Diameter
	//     HSS) reacts to a header-malformed or cross-protocol message. This
	//     phase proves the bytes leave the socket exactly as declared; what
	//     a real peer does with them is explicitly out of scope, stated in
	//     the scenario file's own header comment.
}

// TestRunScenario_MalformedDiameterSurvivesProductionWiring runs
// scenarios/diameter/malformed_diameter.yaml through the real production run
// path and proves the AVP-length override and the
// Message-Length header override reach a real mock HSS's socket exactly
// as declared, plus action: raw's Diameter-side injection (a SIP OPTIONS
// request's wire bytes sent directly over the Diameter connection) — all
// three asserted against diameter.MockHSS.RawReads(), never the mock's
// framed request history, since a corrupted AVP Length or Message Length is
// exactly the kind of input that can break the mock's own read-framing
// before a request would ever reach that higher-level history.
//
// This test found a genuine production-only defect on first run: this
// scenario's three steps share ONE Diameter connection, exactly as a real
// scenario file does, and the AVP-length-corrupted first step makes the
// mock's own AVP decode overrun the buffer, so the mock closes the
// connection — a peer-initiated close, not a scenario-declared impair
// fault. Diameter's finishSend reported that as an ordinary receive failure
// without ever telling the adapter's OWN peer-state machine the connection
// was gone, so the SECOND step's ensureConnected saw peer state still Open,
// skipped reconnecting, and failed forever with "connection ... is gone,
// reconnect required" — never even reaching the wire. Fixed by exporting
// common.IsDeadConnErr (internal/protocols/common/connstate.go) and calling
// it from finishSend (internal/protocols/diameter/adapter.go) to reset peer
// state on a peer-initiated close, the same bookkeeping applyConnFault
// already keeps in sync for a self-inflicted fault (the reconnection desync fix) —
// this closes the identical gap for the case no scenario step declared.
// Undiscoverable at the adapter level: every existing adapter-level malform
// test builds one send per adapter instance, never two malformed sends
// sharing one connection the way a real multi-step scenario does.
//
// Diameter's finishSend honours a step's own timeout_ms (stepTimeout, pinned
// by TestAdapter_StepTimeoutMs_HonouredOnSend), so this scenario's
// uar_bad_msg_length step waits its declared 500ms. cfg.timeout is still kept
// small (2s) as a backstop, so a regression there cannot stall the suite.
func TestRunScenario_MalformedDiameterSurvivesProductionWiring(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	cfg := &runConfig{timeout: 2 * time.Second, targetHost: hss.Addr()}

	runStart := time.Now()
	result := cfg.runScenario(filepath.Join(scenarioDir(), "diameter", "malformed_diameter.yaml"))
	elapsed := time.Since(runStart)
	require.NotNil(t, result)
	assert.Less(t, elapsed, 10*time.Second,
		"uar_bad_msg_length pays this test's own 2s cfg.timeout in full (see the gap noted above); the whole run must still stay well inside cmd/cellrift's own test-suite budget")

	time.Sleep(malformSettleWindow)
	reads := hss.RawReads()

	// AVP-length override: only the Session-Id AVP's own Length field diverges from
	// correct encoding; the message's own Message Length header is
	// untouched (equal to the real, complete length of the arrival).
	require.NotNil(t, findStep(t, result, malformBadAVPLengthStepID).Result)
	avpLenReads := readsContaining(reads, malformBadAVPLengthNeedle)
	require.Len(t, avpLenReads, 1, "exactly one arrival must carry the bad-AVP-length UAR's own Session-Id value")
	avpLenRaw := avpLenReads[0]
	avpLenField := avpLengthFieldAt(avpLenRaw, []byte(malformBadAVPLengthNeedle))
	require.NotNil(t, avpLenField, "the Session-Id AVP's own Length field must be locatable in the arrival")
	assert.Equal(t, malformDeclaredBadLength, threeByteUint(avpLenField),
		"the Session-Id AVP's own Length field must carry the declared bad value")
	assert.Equal(t, len(avpLenRaw), threeByteUint(avpLenRaw[1:4]),
		"the Message Length header must be untouched by an avp_length-only override")

	// Message-Length override: only the Message Length header diverges; that same UAR's
	// own Session-Id AVP Length field is untouched (correctly encoded).
	require.NotNil(t, findStep(t, result, malformBadMsgLengthStepID).Result)
	msgLenReads := readsContaining(reads, malformBadMsgLengthNeedle)
	require.Len(t, msgLenReads, 1, "exactly one arrival must carry the bad-Message-Length UAR's own Session-Id value")
	msgLenRaw := msgLenReads[0]
	assert.Equal(t, malformDeclaredBadLength, threeByteUint(msgLenRaw[1:4]),
		"the Message Length header must carry the declared bad value")
	msgLenAVPField := avpLengthFieldAt(msgLenRaw, []byte(malformBadMsgLengthNeedle))
	require.NotNil(t, msgLenAVPField, "the Session-Id AVP's own Length field must be locatable in the arrival")
	assert.NotEqual(t, malformDeclaredBadLength, threeByteUint(msgLenAVPField),
		"a msg_length-only override must leave the Session-Id AVP's own Length field correctly encoded")

	// action: raw, the Diameter-side cross-protocol case: a SIP OPTIONS
	// request's raw wire bytes, injected directly over this Diameter
	// connection, byte-identical.
	sipPayload, err := hex.DecodeString(sipOverDiameterHexPayload)
	require.NoError(t, err)
	injected := readsEqualTo(reads, sipPayload)
	require.Len(t, injected, 1, "exactly one arrival must be byte-identical to the injected SIP OPTIONS request")

	injectStep := findStep(t, result, malformInjectSIPOverDiamID)
	require.NotNil(t, injectStep.Result)
	assert.Equal(t, common.StatusPassed, injectStep.Result.Status, "error: %v", injectStep.Result.Error)
	assert.Equal(t, len(sipPayload), injectStep.Result.Response[malform.RawBytesSentKey])

	// Deliberately NOT asserted: result.Status for uar_bad_avp_length or
	// uar_bad_msg_length. A corrupted AVP Length or Message Length can break
	// the mock's own read-framing entirely (the same reason RawReads(), not
	// ReceivedRequests(), is the correct assertion target here) — this
	// phase makes no claim about how any peer, mock or real, reacts to a
	// corrupted request. What matters, and is asserted above, is that the
	// declared corruption genuinely reached the wire.
}
