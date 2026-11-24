package diameter_test

import (
	"sort"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time interface check.
var _ common.ProtocolAdapter = (*diameter.Adapter)(nil)

func TestAdapter_ImplementsProtocolAdapter(t *testing.T) {
	// Verified at compile time above.
}

func TestAdapter_SendUAR_MockHSS(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;uar",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}

	result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err)

	assert.Equal(t, common.StatusPassed, result.Status, "UAR should succeed: %s", result.Error)
	assert.NotNil(t, result.RawResponse)

	// Check response fields.
	avpMap, ok := result.Response["avp"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "hss.ims.local", avpMap["Origin-Host"])
	// A Cx UAA reports through Experimental-Result, not the base
	// Result-Code — see the mock's buildUAA and test/interop/EVIDENCE.md.
	// The grouped member is flattened into the same namespace a top-level
	// AVP occupies, which is what lets a scenario assert on it.
	assert.Equal(t, uint32(2001), result.Response["Experimental-Result-Code"])
	assert.Nil(t, result.Response["result_code"], "a UAA carries no base Result-Code")
}

// TestAdapter_SendUAR_EncodesPublicIdentityVendorScoped is the 3GPP
// vendor-scoping fix's client-side proof: asserted against the raw bytes MockHSS actually
// received (diameter.MockHSS.LastRequestRaw), not against the AVP struct
// buildAVPsFromFields builds before EncodeAVP ever runs — the struct was
// never what a real Open5GS peer rejected; the encoded flags byte was.
func TestAdapter_SendUAR_EncodesPublicIdentityVendorScoped(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;vendor-avp",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"User-Name":       "447700900001@ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}

	result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "UAR should succeed: %s", result.Error)

	raw := hss.LastRequestRaw()
	require.NotEmpty(t, raw, "mock must have captured the UAR's raw bytes")

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)
	require.Equal(t, diameter.CmdUserAuthorization, msg.CommandCode,
		"LastRequestRaw must be the UAR, not an earlier CER on the same connection")

	publicID := msg.FindAVP(diameter.AVPCodePublicID)
	require.NotNil(t, publicID, "UAR must carry Public-Identity")
	assert.Equal(t, byte(0xC0), publicID.Flags, "Public-Identity (601, 3GPP Cx) must be vendor-scoped: V+M")
	assert.Equal(t, diameter.VendorID3GPP, publicID.VendorID)

	userName := msg.FindAVP(diameter.AVPCodeUserName)
	require.NotNil(t, userName, "UAR must carry User-Name")
	assert.Equal(t, byte(0x40), userName.Flags, "User-Name (1, RFC 6733 base) must stay plain-M — no over-scoping")
	assert.Equal(t, uint32(0), userName.VendorID)
}

// TestAdapter_SendUAR_IncludesDestinationRealm is the Destination-Realm
// fix's client-side proof: MockHSS's CEA advertises Origin-Realm "ims.local" (testing.go's
// buildCEAWithResult), so the UAR that follows the automatic handshake must
// carry that value as Destination-Realm — derived, not hard-coded in any
// scenario field. Asserted against MockHSS.LastRequestRaw's decoded bytes,
// exactly like TestAdapter_SendUAR_EncodesPublicIdentityVendorScoped above.
func TestAdapter_SendUAR_IncludesDestinationRealm(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;dest-realm",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}

	result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "UAR should succeed: %s", result.Error)

	raw := hss.LastRequestRaw()
	require.NotEmpty(t, raw, "mock must have captured the UAR's raw bytes")

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)
	require.Equal(t, diameter.CmdUserAuthorization, msg.CommandCode,
		"LastRequestRaw must be the UAR, not an earlier CER on the same connection")

	destRealm := msg.FindAVP(diameter.AVPCodeDestinationRealm)
	require.NotNil(t, destRealm, "UAR must carry Destination-Realm")
	assert.Equal(t, "ims.local", destRealm.StringVal, "Destination-Realm must be derived from the CEA's Origin-Realm")
	assert.Equal(t, byte(0x40), destRealm.Flags, "Destination-Realm (283, RFC 6733 base) must be plain-M, not vendor-scoped")
	assert.Equal(t, uint32(0), destRealm.VendorID)
}

// TestAdapter_SendUAR_ExplicitDestinationRealmWins proves the override path:
// a step that supplies its own Destination-Realm field must win over the
// derived CEA value, and — the point of this test — the field path and the
// derived path must not BOTH fire, producing two AVP 283 entries on the wire.
func TestAdapter_SendUAR_ExplicitDestinationRealmWins(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":        "cellrift;test;dest-realm-explicit",
		"Origin-Host":       "cellrift.ims.local",
		"Origin-Realm":      "ims.local",
		"Public-Identity":   "sip:447700900001@ims.local",
		"Destination-Realm": "explicit.example",
	}

	result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "UAR should succeed: %s", result.Error)

	raw := hss.LastRequestRaw()
	require.NotEmpty(t, raw, "mock must have captured the UAR's raw bytes")

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)
	require.Equal(t, diameter.CmdUserAuthorization, msg.CommandCode)

	var destRealmCount int
	for _, a := range msg.AVPs {
		if a.Code == diameter.AVPCodeDestinationRealm {
			destRealmCount++
			assert.Equal(t, "explicit.example", a.StringVal, "the explicit field value must win, not the derived ims.local")
		}
	}
	assert.Equal(t, 1, destRealmCount, "exactly one Destination-Realm AVP must be present — the field path and the derived path must not both fire")
}

// TestAdapter_FailsAgainstStrictPeer — name retained deliberately (it is
// greppable history, and this exact name is referenced by the project's
// release-verification checklist as the proof that RFC 6733 §5.3 handshake
// enforcement is covered; renaming it silently drops that coverage) — used
// to document that Cellrift's Diameter support did NOT interoperate with an
// RFC-compliant peer. It no longer does.
//
// What changed: the adapter now performs the RFC 6733 §5.3 capabilities
// exchange (CER/CEA) automatically before the first application request,
// so a peer enforcing §5.3 — NewStrictMockHSS — no longer
// tears the connection down. The strict mock now succeeds because the
// handshake runs inside Connect: this test starts passing the moment the
// handshake works, so it was inverted rather than leaving the suite
// red.
//
// What this proves: the handshake is spec-shaped and self-consistent
// against Cellrift's OWN in-repo strict mock — CER/CEA completes, the peer
// reaches Open, and the application request that follows succeeds.
//
// What this does NOT prove: real-peer interoperability. NewStrictMockHSS is
// written against the same encoder/decoder this adapter uses, which is
// exactly the self-consistency weakness identified during v1.0 planning —
// a mock cannot be surprised by its own client. Third-party evidence (an
// Open5GS CEA and UAA, produced by software Cellrift did not write) is
// the interop CI job's job, not this test's. This test's claim is scoped
// to spec-shape self-consistency against the in-repo mock; real-peer
// evidence is explicitly outside its reach — do not read this green test as
// interop evidence.
//
// That job now exists (.github/workflows/interop.yml), and its captured
// evidence (test/interop/EVIDENCE.md) proves the RFC 6733 §5.3 handshake
// reaches a real gradiant/open5gs:2.8.0 peer's Open state. But the
// application-level UAR/UAA exchange that follows the handshake still fails
// against that same peer (Result-Code 5005, a missing
// Vendor-Specific-Application-Id AVP — a distinct, not-yet-closed gap).
// Three claims stay explicitly distinct, and a pass on one is not evidence
// for another: (1) this test's claim, mock self-consistency against
// NewStrictMockHSS; (2) real-peer handshake success, proven by EVIDENCE.md;
// (3) real-peer application-exchange success, not yet achieved by anything
// in this repository.
func TestAdapter_FailsAgainstStrictPeer(t *testing.T) {
	hss := diameter.NewStrictMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 500*time.Millisecond)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;strict",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}

	result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err, "transport-level send should not error")

	assert.Equal(t, common.StatusPassed, result.Status,
		"UAR must now succeed against a peer that enforces CER/CEA: %s", result.Error)
	assert.Equal(t, 0, hss.Rejected(),
		"nothing should have been torn down for skipping the handshake")
}

// TestAdapter_AnswersInboundDWR proves a DWR arriving
// WHILE a UAR is outstanding is answered by the client, and the UAR itself
// still gets its own answer — not the DWR mistaken for it. mockHSS's
// dwrBeforeAnswer option writes the DWR onto the wire immediately before the
// UAA, from the same goroutine that will write the UAA next, so the DWR is
// genuinely in flight during session.receiveAnswer's wait, deterministically
// — no sleep, no idle-timeout race, since a DWR answered on an idle
// connection would not exercise the demux loop at all.
func TestAdapter_AnswersInboundDWR(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()
	hss.SetDWRBeforeAnswer(true)

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;dwr",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}

	result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err)

	assert.Equal(t, common.StatusPassed, result.Status,
		"the UAR should still succeed once its own answer arrives: %s", result.Error)
	assert.Equal(t, uint32(300), result.Response["command_code"],
		"the captured response must be the UAA, not the DWR the client answered along the way")
}

// TestAdapter_RejectsUnsupportedApplication proves a step
// fails immediately, naming the missing application, when a successful CEA
// never advertised it — before any UAR is sent, not as a deferred, cryptic
// 3007 from the peer. NewMockHSSWithApps(AppIDCreditControl) answers the CER
// with a 2001 CEA (Credit-Control is a common application) that advertises
// only app 4, never app 16777216 (Cx) — exactly the case a UAR-only client
// wants to be told about clearly.
func TestAdapter_RejectsUnsupportedApplication(t *testing.T) {
	hss := diameter.NewMockHSSWithApps(t, diameter.AppIDCreditControl)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;unsupported-app",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}

	result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err, "an application mismatch is a protocol outcome, not a Go error")

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Contains(t, result.Error, "16777216",
		"the error must name the application (Cx) the step needed, not a raw 3007")
}

func TestAdapter_SendCCR_MockHSS(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":        "cellrift;test;ccr",
		"Origin-Host":       "cellrift.ims.local",
		"Origin-Realm":      "ims.local",
		"CC-Request-Type":   "1",
		"CC-Request-Number": "0",
	}

	result, err := adapter.Execute("send", hss.Addr(), "CCR", fields, nil)
	require.NoError(t, err)

	assert.Equal(t, common.StatusPassed, result.Status, "CCR should succeed: %s", result.Error)
	assert.Equal(t, uint32(2001), result.Response["result_code"])
}

// --- AIR/AIA — Authentication-Information-Request, command 318 ---

func TestAdapter_SendAIR_MockHSS(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;air",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"User-Name":       diameter.MockProvisionedUser,
		"Visited-PLMN-Id": "\x02\xf8\x10",
		"Requested-EUTRAN-Authentication-Info.Number-Of-Requested-Vectors":  "1",
		"Requested-EUTRAN-Authentication-Info.Immediate-Response-Preferred": "0",
	}

	result, err := adapter.Execute("send", hss.Addr(), "AIR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "AIR should succeed: %s", result.Error)

	assert.Equal(t, uint32(2001), result.Response["result_code"])

	avpMap, ok := result.Response["avp"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, avpMap, "Authentication-Info", "AIA must carry Authentication-Info")
}

// TestAdapter_SendAIR_EncodesRequestOnTheWire proves the AIR itself, not
// just the mock's answer, is spec-shaped: command 318 on Application-Id
// 16777251, Auth-Session-State auto-injected, and the
// Requested-EUTRAN-Authentication-Info Grouped AVP vendor-scoped with its
// Number-Of-Requested-Vectors member intact.
func TestAdapter_SendAIR_EncodesRequestOnTheWire(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;air-wire",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"User-Name":       diameter.MockProvisionedUser,
		"Visited-PLMN-Id": "\x02\xf8\x10",
		"Requested-EUTRAN-Authentication-Info.Number-Of-Requested-Vectors":  "1",
		"Requested-EUTRAN-Authentication-Info.Immediate-Response-Preferred": "0",
	}

	result, err := adapter.Execute("send", hss.Addr(), "AIR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "AIR should succeed: %s", result.Error)

	raw := hss.LastRequestRaw()
	require.NotEmpty(t, raw, "mock must have captured the AIR's raw bytes")

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)
	assert.Equal(t, diameter.CmdAuthenticationInformation, msg.CommandCode)
	assert.Equal(t, diameter.AppIDS6a, msg.AppID)

	authSessionState := msg.FindAVP(diameter.AVPCodeAuthSessionState)
	require.NotNil(t, authSessionState, "AIR must carry auto-injected Auth-Session-State")

	visitedPLMNDef, ok := diameter.LookupAVP("Visited-PLMN-Id")
	require.True(t, ok)
	visitedPLMN := msg.FindAVP(visitedPLMNDef.Code)
	require.NotNil(t, visitedPLMN, "AIR must carry Visited-PLMN-Id")
	assert.Equal(t, byte(0xC0), visitedPLMN.Flags)
	assert.Equal(t, diameter.VendorID3GPP, visitedPLMN.VendorID)

	reqInfoDef, ok := diameter.LookupAVP("Requested-EUTRAN-Authentication-Info")
	require.True(t, ok)
	reqInfo := msg.FindAVP(reqInfoDef.Code)
	require.NotNil(t, reqInfo, "AIR must carry Requested-EUTRAN-Authentication-Info")
	assert.Equal(t, byte(0xC0), reqInfo.Flags)
	assert.Equal(t, diameter.VendorID3GPP, reqInfo.VendorID)

	require.NoError(t, diameter.DecodeGroupedAVP(reqInfo))
	numVectorsDef, ok := diameter.LookupAVP("Number-Of-Requested-Vectors")
	require.True(t, ok)
	numVectors := diameter.FindAVP(reqInfo.GroupedAVPs, numVectorsDef.Code)
	require.NotNil(t, numVectors, "Requested-EUTRAN-Authentication-Info must carry Number-Of-Requested-Vectors")
}

// TestAdapter_SendAIR_UnknownUserRejected is AIR's distinct error path: a
// User-Name the mock has not "provisioned" draws 5003
// DIAMETER_AUTHORIZATION_REJECTED, not the happy path's 2001 — asserted on
// the specific code in result.Error so a future regression that fails for
// an unrelated reason (timeout, handshake) cannot silently satisfy this
// test.
func TestAdapter_SendAIR_UnknownUserRejected(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;air-unknown",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"User-Name":       "447700900999",
		"Visited-PLMN-Id": "\x02\xf8\x10",
	}

	result, err := adapter.Execute("send", hss.Addr(), "AIR", fields, nil)
	require.NoError(t, err)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Contains(t, result.Error, "Result-Code 5003")
}

// --- ULR/ULA — Update-Location-Request, command 316 ---

func TestAdapter_SendULR_MockHSS(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":                            "cellrift;test;ulr",
		"Origin-Host":                           "cellrift.ims.local",
		"Origin-Realm":                          "ims.local",
		"User-Name":                             diameter.MockProvisionedUser,
		"RAT-Type":                              "1004", // EUTRAN
		"ULR-Flags":                             "3",
		"Visited-PLMN-Id":                       "\x02\xf8\x10",
		"Terminal-Information.Software-Version": "01",
	}

	result, err := adapter.Execute("send", hss.Addr(), "ULR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "ULR should succeed: %s", result.Error)

	assert.Equal(t, uint32(2001), result.Response["result_code"])

	avpMap, ok := result.Response["avp"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, avpMap, "Subscription-Data", "ULA must carry Subscription-Data")
}

// TestAdapter_SendULR_EncodesRequestOnTheWire proves the ULR itself is
// spec-shaped: command 316 on Application-Id 16777251, Auth-Session-State
// auto-injected, and RAT-Type / ULR-Flags / Visited-PLMN-Id /
// Terminal-Information all vendor-scoped as TS 29.272 requires.
func TestAdapter_SendULR_EncodesRequestOnTheWire(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":                            "cellrift;test;ulr-wire",
		"Origin-Host":                           "cellrift.ims.local",
		"Origin-Realm":                          "ims.local",
		"User-Name":                             diameter.MockProvisionedUser,
		"RAT-Type":                              "1004", // EUTRAN
		"ULR-Flags":                             "3",
		"Visited-PLMN-Id":                       "\x02\xf8\x10",
		"Terminal-Information.Software-Version": "01",
	}

	result, err := adapter.Execute("send", hss.Addr(), "ULR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "ULR should succeed: %s", result.Error)

	raw := hss.LastRequestRaw()
	require.NotEmpty(t, raw, "mock must have captured the ULR's raw bytes")

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)
	assert.Equal(t, diameter.CmdUpdateLocation, msg.CommandCode)
	assert.Equal(t, diameter.AppIDS6a, msg.AppID)

	authSessionState := msg.FindAVP(diameter.AVPCodeAuthSessionState)
	require.NotNil(t, authSessionState, "ULR must carry auto-injected Auth-Session-State")

	ratTypeDef, ok := diameter.LookupAVP("RAT-Type")
	require.True(t, ok)
	ratType := msg.FindAVP(ratTypeDef.Code)
	require.NotNil(t, ratType, "ULR must carry RAT-Type")
	assert.Equal(t, uint32(1032), ratTypeDef.Code, "RAT-Type must be AVP code 1032")
	assert.Equal(t, byte(0xC0), ratType.Flags)
	assert.Equal(t, diameter.VendorID3GPP, ratType.VendorID)

	ulrFlagsDef, ok := diameter.LookupAVP("ULR-Flags")
	require.True(t, ok)
	ulrFlags := msg.FindAVP(ulrFlagsDef.Code)
	require.NotNil(t, ulrFlags, "ULR must carry ULR-Flags")

	visitedPLMNDef, ok := diameter.LookupAVP("Visited-PLMN-Id")
	require.True(t, ok)
	visitedPLMN := msg.FindAVP(visitedPLMNDef.Code)
	require.NotNil(t, visitedPLMN, "ULR must carry Visited-PLMN-Id")

	termInfoDef, ok := diameter.LookupAVP("Terminal-Information")
	require.True(t, ok)
	termInfo := msg.FindAVP(termInfoDef.Code)
	require.NotNil(t, termInfo, "ULR must carry Terminal-Information")
	assert.Equal(t, byte(0xC0), termInfo.Flags)
	assert.Equal(t, diameter.VendorID3GPP, termInfo.VendorID)
}

// TestAdapter_SendULR_UnknownUserRejected is the distinct error path for
// ULR, mirroring TestAdapter_SendAIR_UnknownUserRejected.
func TestAdapter_SendULR_UnknownUserRejected(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;ulr-unknown",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"User-Name":       "447700900999",
		"RAT-Type":        "1004",
		"ULR-Flags":       "3",
		"Visited-PLMN-Id": "\x02\xf8\x10",
	}

	result, err := adapter.Execute("send", hss.Addr(), "ULR", fields, nil)
	require.NoError(t, err)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Contains(t, result.Error, "Result-Code 5003")
}

// --- MAR/MAA — Multimedia-Auth-Request, command 303 ---

func TestAdapter_SendMAR_MockHSS(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":                         "cellrift;test;mar",
		"Origin-Host":                        "cellrift.ims.local",
		"Origin-Realm":                       "ims.local",
		"User-Name":                          diameter.MockProvisionedUser,
		"Public-Identity":                    "sip:447700900001@ims.local",
		"Server-Name":                        "sip:scscf.ims.local:5060",
		"SIP-Number-Auth-Items":              "1",
		"SIP-Auth-Data-Item.SIP-Item-Number": "1",
		"SIP-Auth-Data-Item.SIP-Authentication-Scheme": "Digest-AKAv1-MD5",
	}

	result, err := adapter.Execute("send", hss.Addr(), "MAR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "MAR should succeed: %s", result.Error)

	assert.Equal(t, uint32(2001), result.Response["result_code"])

	avpMap, ok := result.Response["avp"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, avpMap, "SIP-Auth-Data-Item", "MAA must carry SIP-Auth-Data-Item")
}

// TestAdapter_SendMAR_EncodesGroupedAuthDataItemOnTheWire is the nested
// Grouped AVP syntax's end-to-end proof: a scenario author writes SIP-Auth-Data-Item as a nested
// YAML map, StepFields flattens it to dotted keys, GroupDottedFields
// regroups it, and newScopedGroupedAVP encodes it with the V bit — this
// asserts the result on the bytes MockHSS actually received, not on the
// pre-encode AVP struct.
func TestAdapter_SendMAR_EncodesGroupedAuthDataItemOnTheWire(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":                         "cellrift;test;mar-wire",
		"Origin-Host":                        "cellrift.ims.local",
		"Origin-Realm":                       "ims.local",
		"User-Name":                          diameter.MockProvisionedUser,
		"Public-Identity":                    "sip:447700900001@ims.local",
		"Server-Name":                        "sip:scscf.ims.local:5060",
		"SIP-Number-Auth-Items":              "1",
		"SIP-Auth-Data-Item.SIP-Item-Number": "1",
		"SIP-Auth-Data-Item.SIP-Authentication-Scheme": "Digest-AKAv1-MD5",
	}

	result, err := adapter.Execute("send", hss.Addr(), "MAR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "MAR should succeed: %s", result.Error)

	raw := hss.LastRequestRaw()
	require.NotEmpty(t, raw, "mock must have captured the MAR's raw bytes")

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)
	assert.Equal(t, diameter.CmdMultimediaAuth, msg.CommandCode)
	assert.Equal(t, diameter.AppIDCx, msg.AppID)

	authDataItemDef, ok := diameter.LookupAVP("SIP-Auth-Data-Item")
	require.True(t, ok)
	authDataItem := msg.FindAVP(authDataItemDef.Code)
	require.NotNil(t, authDataItem, "MAR must carry SIP-Auth-Data-Item")
	assert.Equal(t, uint32(612), authDataItem.Code)
	assert.Equal(t, byte(0xC0), authDataItem.Flags, "SIP-Auth-Data-Item (612, 3GPP Cx) must be vendor-scoped: V+M")
	assert.Equal(t, diameter.VendorID3GPP, authDataItem.VendorID)

	require.NoError(t, diameter.DecodeGroupedAVP(authDataItem))

	itemNumberDef, ok := diameter.LookupAVP("SIP-Item-Number")
	require.True(t, ok)
	schemeDef, ok := diameter.LookupAVP("SIP-Authentication-Scheme")
	require.True(t, ok)

	var gotCodes []uint32
	for _, sub := range authDataItem.GroupedAVPs {
		gotCodes = append(gotCodes, sub.Code)
	}
	sort.Slice(gotCodes, func(i, j int) bool { return gotCodes[i] < gotCodes[j] })

	wantCodes := []uint32{itemNumberDef.Code, schemeDef.Code}
	sort.Slice(wantCodes, func(i, j int) bool { return wantCodes[i] < wantCodes[j] })

	assert.Equal(t, wantCodes, gotCodes,
		"SIP-Auth-Data-Item's sub-AVPs must be SIP-Item-Number (613) and SIP-Authentication-Scheme (608)")
}

// TestAdapter_SendMAR_UnknownUserRejected is the distinct error path for
// MAR, mirroring TestAdapter_SendAIR_UnknownUserRejected /
// TestAdapter_SendULR_UnknownUserRejected.
func TestAdapter_SendMAR_UnknownUserRejected(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;test;mar-unknown",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"User-Name":       "447700900999",
		"Public-Identity": "sip:447700900001@ims.local",
		"Server-Name":     "sip:scscf.ims.local:5060",
	}

	result, err := adapter.Execute("send", hss.Addr(), "MAR", fields, nil)
	require.NoError(t, err)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Contains(t, result.Error, "Result-Code 5003")
}

// --- SAR/SAA — Server-Assignment-Request, command 301 ---

func TestAdapter_SendSAR_MockHSS(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":                  "cellrift;test;sar",
		"Origin-Host":                 "cellrift.ims.local",
		"Origin-Realm":                "ims.local",
		"User-Name":                   diameter.MockProvisionedUser,
		"Public-Identity":             "sip:447700900001@ims.local",
		"Server-Name":                 "sip:scscf.ims.local:5060",
		"Server-Assignment-Type":      "1", // REGISTRATION
		"User-Data-Already-Available": "0", // USER_DATA_NOT_AVAILABLE
	}

	result, err := adapter.Execute("send", hss.Addr(), "SAR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "SAR should succeed: %s", result.Error)

	assert.Equal(t, uint32(2001), result.Response["result_code"])

	avpMap, ok := result.Response["avp"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, avpMap, "User-Data", "SAA must carry User-Data")
}

// TestAdapter_SendSAR_EncodesRequestOnTheWire proves the SAR itself is
// spec-shaped: command 301 on Application-Id 16777216, Auth-Session-State
// auto-injected, and Server-Name / Server-Assignment-Type /
// User-Data-Already-Available all present. SAR's happy path needs no
// Grouped field — its only Grouped AVP, SCSCF-Restoration-Info, is optional.
func TestAdapter_SendSAR_EncodesRequestOnTheWire(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":                  "cellrift;test;sar-wire",
		"Origin-Host":                 "cellrift.ims.local",
		"Origin-Realm":                "ims.local",
		"User-Name":                   diameter.MockProvisionedUser,
		"Public-Identity":             "sip:447700900001@ims.local",
		"Server-Name":                 "sip:scscf.ims.local:5060",
		"Server-Assignment-Type":      "1",
		"User-Data-Already-Available": "0",
	}

	result, err := adapter.Execute("send", hss.Addr(), "SAR", fields, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "SAR should succeed: %s", result.Error)

	raw := hss.LastRequestRaw()
	require.NotEmpty(t, raw, "mock must have captured the SAR's raw bytes")

	msg, err := diameter.ParseMessage(raw)
	require.NoError(t, err)
	assert.Equal(t, diameter.CmdServerAssignment, msg.CommandCode)
	assert.Equal(t, diameter.AppIDCx, msg.AppID)

	authSessionState := msg.FindAVP(diameter.AVPCodeAuthSessionState)
	require.NotNil(t, authSessionState, "SAR must carry auto-injected Auth-Session-State")

	serverNameDef, ok := diameter.LookupAVP("Server-Name")
	require.True(t, ok)
	require.NotNil(t, msg.FindAVP(serverNameDef.Code), "SAR must carry Server-Name")

	sat, ok := diameter.LookupAVP("Server-Assignment-Type")
	require.True(t, ok)
	require.NotNil(t, msg.FindAVP(sat.Code), "SAR must carry Server-Assignment-Type")

	uda, ok := diameter.LookupAVP("User-Data-Already-Available")
	require.True(t, ok)
	require.NotNil(t, msg.FindAVP(uda.Code), "SAR must carry User-Data-Already-Available")
}

// TestAdapter_SendSAR_UnknownUserRejected is the distinct error path for
// SAR, mirroring TestAdapter_SendMAR_UnknownUserRejected.
func TestAdapter_SendSAR_UnknownUserRejected(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":                  "cellrift;test;sar-unknown",
		"Origin-Host":                 "cellrift.ims.local",
		"Origin-Realm":                "ims.local",
		"User-Name":                   "447700900999",
		"Public-Identity":             "sip:447700900001@ims.local",
		"Server-Name":                 "sip:scscf.ims.local:5060",
		"Server-Assignment-Type":      "1",
		"User-Data-Already-Available": "0",
	}

	result, err := adapter.Execute("send", hss.Addr(), "SAR", fields, nil)
	require.NoError(t, err)

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Contains(t, result.Error, "Result-Code 5003")
}

func TestAdapter_UnsupportedAction(t *testing.T) {
	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, time.Second)
	defer adapter.Close()

	_, err = adapter.Execute("bogus", "127.0.0.1:3868", "UAR", nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported action")
}

func TestAdapter_UnknownMessageType(t *testing.T) {
	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, time.Second)
	defer adapter.Close()

	_, err = adapter.Execute("send", "127.0.0.1:3868", "XYZ", nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown diameter message type")
}

// TestNewAdapter_ReportsNoWarningsWhenTransportIsSupplied proves
// NewAdapterWithTransport never fabricates a substitution warning: that
// field is only ever populated by NewAdapter's own SCTP-fallback path.
func TestNewAdapter_ReportsNoWarningsWhenTransportIsSupplied(t *testing.T) {
	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, time.Second)
	defer adapter.Close()

	assert.Empty(t, adapter.Warnings())
}

func TestMockHSS_StartsAndStopsCleanly(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	assert.NotEmpty(t, hss.Addr())
	hss.Close()
	assert.Empty(t, hss.Errors())
}

func TestAdapter_EndToEnd_UAR_UAA(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":      "cellrift;e2e;uar",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"User-Name":       "447700900001@ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}

	result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status)
	assert.GreaterOrEqual(t, result.DurationMs, int64(0))

	// Verify the Server-Name AVP was returned by the mock HSS.
	avpMap := result.Response["avp"].(map[string]interface{})
	assert.Equal(t, "sip:scscf.ims.local:5060", avpMap["Server-Name"])
}

func TestAdapter_ConnectEstablishesHeldConnection(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	require.NoError(t, adapter.Connect(hss.Addr(), nil))

	fields := map[string]string{
		"Session-Id":      "cellrift;test;connect",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}

	result, err := adapter.Execute("send", hss.Addr(), "UAR", fields, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status, "UAR should still succeed after Connect: %s", result.Error)
}

func TestAdapter_ConnectIsIdempotent(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	require.NoError(t, adapter.Connect(hss.Addr(), nil))
	require.NoError(t, adapter.Connect(hss.Addr(), nil))
}

func TestAdapter_ConnectRejectsNonConnectorTransport(t *testing.T) {
	tr, err := common.NewUDPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, time.Second)
	defer adapter.Close()

	err = adapter.Connect("127.0.0.1:3868", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UDPTransport")
}

func TestAdapter_EndToEnd_CCR_CCA(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)

	adapter := diameter.NewAdapterWithTransport(tr, 2*time.Second)
	defer adapter.Close()

	fields := map[string]string{
		"Session-Id":        "cellrift;e2e;ccr",
		"Origin-Host":       "cellrift.ims.local",
		"Origin-Realm":      "ims.local",
		"CC-Request-Type":   "1",
		"CC-Request-Number": "0",
	}

	result, err := adapter.Execute("send", hss.Addr(), "CCR", fields, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status)
	assert.Equal(t, uint32(2001), result.Response["result_code"])

	// Verify echoed CC-Request-Type.
	avpMap := result.Response["avp"].(map[string]interface{})
	assert.Equal(t, uint32(1), avpMap["CC-Request-Type"])
}

// TestAdapter_IdentityPrecedence pins ResolveOriginIdentity's precedence
// chain: cfg wins over fields, fields win over the package default,
// asserted on the resolved values directly rather than on CER bytes, since
// building a CER is not this function's job.
func TestAdapter_IdentityPrecedence(t *testing.T) {
	fields := map[string]string{
		"Origin-Host":  "fields.ims.local",
		"Origin-Realm": "fields-realm.local",
	}

	t.Run("cfg wins over fields", func(t *testing.T) {
		cfg := map[string]string{
			"origin_host":  "cfg.ims.local",
			"origin_realm": "cfg-realm.local",
		}
		host, realm := diameter.ResolveOriginIdentity(cfg, fields)
		assert.Equal(t, "cfg.ims.local", host)
		assert.Equal(t, "cfg-realm.local", realm)
	})

	t.Run("fields used when cfg is nil", func(t *testing.T) {
		host, realm := diameter.ResolveOriginIdentity(nil, fields)
		assert.Equal(t, "fields.ims.local", host)
		assert.Equal(t, "fields-realm.local", realm)
	})

	t.Run("default used when neither cfg nor fields supply identity", func(t *testing.T) {
		host, realm := diameter.ResolveOriginIdentity(nil, nil)
		assert.Equal(t, diameter.DefaultOriginHost, host)
		assert.Equal(t, diameter.DefaultOriginRealm, realm)
	})
}
