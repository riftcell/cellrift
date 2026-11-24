package diameter

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise buildAVPsFromFields directly and therefore live in
// the internal (package diameter) test binary rather than adapter_test.go,
// which is package diameter_test and has no access to an unexported
// function.

func TestBuildAVPsFromFields_KnownStringField(t *testing.T) {
	avps, err := buildAVPsFromFields(map[string]string{"Origin-Host": "h.example"})
	require.NoError(t, err)
	require.Len(t, avps, 1)
	assert.Equal(t, AVPCodeOriginHost, avps[0].Code)
	assert.Equal(t, byte(0x40), avps[0].Flags)
	assert.Equal(t, uint32(0), avps[0].VendorID)
}

func TestBuildAVPsFromFields_VendorScopedStringField(t *testing.T) {
	avps, err := buildAVPsFromFields(map[string]string{"Public-Identity": "sip:a@b"})
	require.NoError(t, err)
	require.Len(t, avps, 1)
	assert.Equal(t, AVPCodePublicID, avps[0].Code)
	assert.Equal(t, byte(0xC0), avps[0].Flags)
	assert.Equal(t, uint32(10415), avps[0].VendorID)
}

func TestBuildAVPsFromFields_Uint32Field(t *testing.T) {
	avps, err := buildAVPsFromFields(map[string]string{"CC-Request-Type": "1"})
	require.NoError(t, err)
	require.Len(t, avps, 1)
	assert.Equal(t, AVPCodeCCRequestType, avps[0].Code)
	assert.Equal(t, uint32(1), avps[0].Uint32Val)
}

func TestBuildAVPsFromFields_UnknownFieldErrors(t *testing.T) {
	avps, err := buildAVPsFromFields(map[string]string{"Public-Identitty": "x"})
	assert.Nil(t, avps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown Diameter AVP "Public-Identitty"`)
}

func TestBuildAVPsFromFields_SkipsBodyInjectionKeys(t *testing.T) {
	avps, err := buildAVPsFromFields(map[string]string{
		"__body_content_type": "application/sdp",
		"__body_content":      "v=0",
	})
	require.NoError(t, err)
	assert.Empty(t, avps)
}

func TestBuildAVPsFromFields_UnencodableTypeErrors(t *testing.T) {
	// SIP-Auth-Data-Item (3GPP Cx) is Grouped — no encoder exists for it yet.
	avps, err := buildAVPsFromFields(map[string]string{"SIP-Auth-Data-Item": "x"})
	assert.Nil(t, avps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SIP-Auth-Data-Item")
	assert.Contains(t, err.Error(), "Grouped")
	assert.Contains(t, err.Error(), "cannot yet be built from a scenario field")
}

func TestBuildAVPsFromFields_InvalidUint32StillErrors(t *testing.T) {
	avps, err := buildAVPsFromFields(map[string]string{"CC-Request-Type": "abc"})
	assert.Nil(t, avps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CC-Request-Type")
}

// --- S6a vendor-scoping proof through the real send path ---
//
// Originally, one three-entry hardcoded table (vendorScopedAVPs)
// decided which AVPs were vendor-scoped, and it named only three Cx codes
// (601, 602, 613). Every AVP in the 3GPP TS 29.272 S6a set is equally
// vendor-scoped under Vendor-Id 10415, but no command sent at the time was
// an S6a one, so nothing else exercised that path. If vendor-scoping ever
// silently regressed back to a hand-maintained table that forgot to list an
// S6a code, an S6a field would encode without the V bit, land in the base
// (code, no-vendor) namespace where that code does not exist, and draw a
// 5001 DIAMETER_AVP_UNSUPPORTED from any real peer -- the same defect
// already fixed once for Cx (3GPP vendor scoping), silently reintroduced for S6a.
// That would not surface until the command-coverage phase actually sends
// one. These three tests assert it explicitly, by name and by wire byte,
// now.
//
// Visited-PLMN-Id (code 1407, OctetString) is named explicitly rather than
// picked dynamically: a reviewer can see at a glance that a genuine S6a AVP,
// not a synthetic fixture, produced flags 0xC0 and Vendor-Id 10415 on the
// wire.

func TestBuildAVPsFromFields_S6aFieldIsVendorScoped(t *testing.T) {
	avps, err := buildAVPsFromFields(map[string]string{"Visited-PLMN-Id": "\x02\xf8\x10"})
	require.NoError(t, err)
	require.Len(t, avps, 1)

	// Asserted as two SEPARATE properties, following this package's existing
	// precedent (TestNewScopedStringAVP_DestinationRealmIsBaseScoped): a test
	// that only checks Flags could pass while VendorID drifted independently.
	assert.Equal(t, byte(0xC0), avps[0].Flags, "Visited-PLMN-Id must be vendor-scoped (V+M)")
	assert.Equal(t, VendorID3GPP, avps[0].VendorID, "Visited-PLMN-Id must carry Vendor-Id 10415")
}

func TestBuildAVPsFromFields_S6aFieldEncodesVBitOnTheWire(t *testing.T) {
	avps, err := buildAVPsFromFields(map[string]string{"Visited-PLMN-Id": "\x02\xf8\x10"})
	require.NoError(t, err)
	require.Len(t, avps, 1)

	// The byte-level form matters because the Flags field on the Go struct
	// and the flags byte on the wire are two different things, and only the
	// second one is what a real peer actually sees.
	encoded, err := EncodeAVP(avps[0].Code, avps[0].Flags, avps[0].VendorID, avps[0].StringVal)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(encoded), 12)

	assert.Equal(t, byte(0xC0), encoded[4], "encoded byte[4] must be flags 0xC0 (V+M)")

	vendorID := uint32(encoded[8])<<24 | uint32(encoded[9])<<16 | uint32(encoded[10])<<8 | uint32(encoded[11])
	assert.Equal(t, uint32(10415), vendorID, "encoded bytes[8:12] must decode to Vendor-Id 10415")
}

// stringEncodedS6aKinds is the set of AVPKind values that route through
// avpFieldEncoders' string encoder (encodeStringField). Kept local to this
// test rather than exported: it exists only to select which S6a dictionary
// entries the whole-set property test below can legitimately exercise
// through buildAVPsFromFields today.
var stringEncodedS6aKinds = map[AVPKind]bool{
	AVPKindOctetString:      true,
	AVPKindUTF8String:       true,
	AVPKindDiameterIdentity: true,
	AVPKindDiameterURI:      true,
}

// --- command-scoped Auth-Session-State injection ---

func TestAppendAuthSessionState_OnlyNewCommands(t *testing.T) {
	cases := []struct {
		msgType string
		want    bool
	}{
		{"AIR", true},
		{"ULR", true},
		{"MAR", true},
		{"SAR", true},
		// UAR flipped to true: TS 29.229 §6.1.1's UAR ABNF marks
		// Auth-Session-State mandatory, and this case previously asserted
		// the opposite — pinning a defect rather than a decision. CCR stays
		// false: RFC 4006 §3.1's CCR ABNF does not require it.
		{"UAR", true},
		{"CCR", false},
	}

	for _, tc := range cases {
		t.Run(tc.msgType, func(t *testing.T) {
			cmd, err := resolveCommand(tc.msgType)
			require.NoError(t, err)

			avps := appendAuthSessionState(nil, cmd, map[string]string{})
			if tc.want {
				require.Len(t, avps, 1, "%s must get Auth-Session-State injected", tc.msgType)
				assert.Equal(t, AVPCodeAuthSessionState, avps[0].Code)
			} else {
				assert.Empty(t, avps, "%s must NOT get Auth-Session-State injected", tc.msgType)
			}
		})
	}
}

func TestAppendAuthSessionState_ScenarioValueWins(t *testing.T) {
	cmd, err := resolveCommand("AIR")
	require.NoError(t, err)

	avps := appendAuthSessionState(nil, cmd, map[string]string{"Auth-Session-State": "0"})
	assert.Empty(t, avps, "a step-supplied Auth-Session-State must not be duplicated by injection")
}

// TestAnswerBuilders_CoversEveryRegisteredCommand requires FULL coverage:
// every entry in commandRegistry must have an applicationAnswers entry, with
// no exceptions list. MAR/SAR's builders closed the last gap.
func TestAnswerBuilders_CoversEveryRegisteredCommand(t *testing.T) {
	for msgType, cmd := range commandRegistry {
		_, ok := applicationAnswers[cmd.Code]
		assert.True(t, ok, "%s (command code %d) has no entry in applicationAnswers", msgType, cmd.Code)
	}
}

// TestBuildAVPsFromFields_S6aEnumeratedFieldIsVendorScoped guards the
// numeric counterpart of TestBuildAVPsFromFields_S6aFieldIsVendorScoped:
// RAT-Type (Enumerated) is the first numeric S6a AVP any scenario field
// sends (the ULR's), and it surfaced a defect encodeUint32Field had
// carried since the S6a dictionary landed — bare NewUint32AVP with no vendor-scoping check,
// unlike encodeStringField's newScopedStringAVP routing.
func TestBuildAVPsFromFields_S6aEnumeratedFieldIsVendorScoped(t *testing.T) {
	avps, err := buildAVPsFromFields(map[string]string{"RAT-Type": "1004"})
	require.NoError(t, err)
	require.Len(t, avps, 1)

	assert.Equal(t, byte(0xC0), avps[0].Flags, "RAT-Type must be vendor-scoped (V+M)")
	assert.Equal(t, VendorID3GPP, avps[0].VendorID, "RAT-Type must carry Vendor-Id 10415")
}

// numericEncodedS6aKinds mirrors stringEncodedS6aKinds above but for the
// numeric encoder (encodeUint32Field): Unsigned32 and Enumerated are the two
// S6a-relevant kinds routed through it.
var numericEncodedS6aKinds = map[AVPKind]bool{
	AVPKindUnsigned32: true,
	AVPKindEnumerated: true,
}

// TestBuildAVPsFromFields_EveryS6aNumericAVPIsVendorScoped is the numeric
// counterpart of TestBuildAVPsFromFields_EveryS6aStringAVPIsVendorScoped: it
// fails the moment any Unsigned32/Enumerated S6a AVP loses its vendor flag,
// rather than relying on RAT-Type alone to stand in for the whole set.
func TestBuildAVPsFromFields_EveryS6aNumericAVPIsVendorScoped(t *testing.T) {
	var names []string
	for name, def := range Dictionary {
		if def.Application == "S6a" && numericEncodedS6aKinds[def.Kind] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	require.NotEmpty(t, names, "expected at least one numeric-typed S6a AVP")

	for _, name := range names {
		avps, err := buildAVPsFromFields(map[string]string{name: "0"})
		require.NoError(t, err, "%s: buildAVPsFromFields", name)
		require.Len(t, avps, 1, "%s: expected exactly one AVP", name)
		assert.Equal(t, byte(0xC0), avps[0].Flags, "%s: must be vendor-scoped (V+M)", name)
		assert.Equal(t, VendorID3GPP, avps[0].VendorID, "%s: must carry Vendor-Id 10415", name)
	}
}

// TestBuildAVPsFromFields_EveryS6aStringAVPIsVendorScoped is the check that
// scales: it fails the moment any single S6a entry is transcribed (or
// hand-edited later) without the vendor flag, rather than relying on one
// named example to stand in for the whole 175-entry set.
func TestBuildAVPsFromFields_EveryS6aStringAVPIsVendorScoped(t *testing.T) {
	var names []string
	for name, def := range Dictionary {
		if def.Application == "S6a" && stringEncodedS6aKinds[def.Kind] {
			names = append(names, name)
		}
	}
	sort.Strings(names) // deterministic, diffable failure output
	require.NotEmpty(t, names, "expected at least one string-typed S6a AVP")

	for _, name := range names {
		avps, err := buildAVPsFromFields(map[string]string{name: "x"})
		require.NoError(t, err, "%s: buildAVPsFromFields", name)
		require.Len(t, avps, 1, "%s: expected exactly one AVP", name)
		assert.Equal(t, byte(0xC0), avps[0].Flags, "%s: must be vendor-scoped (V+M)", name)
		assert.Equal(t, VendorID3GPP, avps[0].VendorID, "%s: must carry Vendor-Id 10415", name)
	}
}
