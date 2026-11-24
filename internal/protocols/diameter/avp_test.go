package diameter

import (
	"net"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- RFC 6733 base AVP round-trip tests ---

func TestEncodeDecodeAVP_String_SessionID(t *testing.T) {
	val := "cellrift;1234;5678"
	encoded, err := EncodeAVP(AVPCodeSessionID, AVPFlagMandatory, 0, val)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeSessionID, decoded.Code)
	assert.Equal(t, val, decoded.StringVal)
}

func TestEncodeDecodeAVP_String_OriginHost(t *testing.T) {
	val := "cellrift.ims.local"
	encoded, err := EncodeAVP(AVPCodeOriginHost, AVPFlagMandatory, 0, val)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeOriginHost, decoded.Code)
	assert.Equal(t, val, decoded.StringVal)
}

func TestEncodeDecodeAVP_String_OriginRealm(t *testing.T) {
	val := "ims.local"
	encoded, err := EncodeAVP(AVPCodeOriginRealm, AVPFlagMandatory, 0, val)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeOriginRealm, decoded.Code)
	assert.Equal(t, val, decoded.StringVal)
}

func TestEncodeDecodeAVP_Uint32_ResultCode(t *testing.T) {
	encoded, err := EncodeAVP(AVPCodeResultCode, AVPFlagMandatory, 0, uint32(2001))
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeResultCode, decoded.Code)
	assert.Equal(t, uint32(2001), decoded.Uint32Val)
}

func TestEncodeDecodeAVP_String_UserName(t *testing.T) {
	val := "447700900001@ims.local"
	encoded, err := EncodeAVP(AVPCodeUserName, AVPFlagMandatory, 0, val)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeUserName, decoded.Code)
	assert.Equal(t, val, decoded.StringVal)
}

func TestEncodeDecodeAVP_String_PublicIdentity(t *testing.T) {
	val := "sip:447700900001@ims.local"
	encoded, err := EncodeAVP(AVPCodePublicID, AVPFlagMandatory, 0, val)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodePublicID, decoded.Code)
	assert.Equal(t, val, decoded.StringVal)
}

func TestEncodeDecodeAVP_String_ServerName(t *testing.T) {
	val := "sip:scscf.ims.local:5060"
	encoded, err := EncodeAVP(AVPCodeServerName, AVPFlagMandatory, 0, val)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeServerName, decoded.Code)
	assert.Equal(t, val, decoded.StringVal)
}

// --- RFC 4006 credit-control AVP round-trip tests ---

func TestEncodeDecodeAVP_Uint32_CCRequestType(t *testing.T) {
	encoded, err := EncodeAVP(AVPCodeCCRequestType, AVPFlagMandatory, 0, uint32(1))
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeCCRequestType, decoded.Code)
	assert.Equal(t, uint32(1), decoded.Uint32Val)
}

func TestEncodeDecodeAVP_Uint32_CCRequestNumber(t *testing.T) {
	encoded, err := EncodeAVP(AVPCodeCCRequestNumber, AVPFlagMandatory, 0, uint32(0))
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeCCRequestNumber, decoded.Code)
	assert.Equal(t, uint32(0), decoded.Uint32Val)
}

func TestEncodeDecodeAVP_Uint32_RatingGroup(t *testing.T) {
	encoded, err := EncodeAVP(AVPCodeRatingGroup, AVPFlagMandatory, 0, uint32(100))
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeRatingGroup, decoded.Code)
	assert.Equal(t, uint32(100), decoded.Uint32Val)
}

func TestEncodeDecodeAVP_Grouped_ReqServiceUnit(t *testing.T) {
	// Requested-Service-Unit is a Grouped AVP containing Rating-Group.
	sub := NewUint32AVP(AVPCodeRatingGroup, 42)
	grouped := NewGroupedAVP(AVPCodeReqServiceUnit, []AVP{sub})

	encoded, err := EncodeAVP(grouped.Code, grouped.Flags, 0, grouped.GroupedAVPs)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeReqServiceUnit, decoded.Code)

	// Decode the grouped content.
	err = DecodeGroupedAVP(decoded)
	require.NoError(t, err)
	require.Len(t, decoded.GroupedAVPs, 1)
	assert.Equal(t, AVPCodeRatingGroup, decoded.GroupedAVPs[0].Code)
	assert.Equal(t, uint32(42), decoded.GroupedAVPs[0].Uint32Val)
}

func TestEncodeDecodeAVP_Grouped_GrantedServiceUnit(t *testing.T) {
	sub := NewUint32AVP(AVPCodeRatingGroup, 99)
	grouped := NewGroupedAVP(AVPCodeGrantServiceUnit, []AVP{sub})

	encoded, err := EncodeAVP(grouped.Code, grouped.Flags, 0, grouped.GroupedAVPs)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeGrantServiceUnit, decoded.Code)
}

// --- 3GPP Cx SIP-Auth-Data-Item AVP (v2) ---

func TestEncodeDecodeAVP_Grouped_SIPAuthDataItem(t *testing.T) {
	// SIP-Auth-Data-Item (612) is a Grouped AVP in the Cx interface. It
	// typically contains sub-AVPs like User-Name for the auth context.
	sub := NewStringAVP(AVPCodeUserName, "447700900001@ims.local")
	grouped := NewGroupedAVP(AVPCodeSIPAuthDataItem, []AVP{sub})

	encoded, err := EncodeAVP(grouped.Code, grouped.Flags, 0, grouped.GroupedAVPs)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeSIPAuthDataItem, decoded.Code)
	assert.Equal(t, "SIP-Auth-Data-Item", AVPCodeName(decoded.Code))

	err = DecodeGroupedAVP(decoded)
	require.NoError(t, err)
	require.Len(t, decoded.GroupedAVPs, 1)
	assert.Equal(t, AVPCodeUserName, decoded.GroupedAVPs[0].Code)
	assert.Equal(t, "447700900001@ims.local", decoded.GroupedAVPs[0].StringVal)
}

// --- Padding tests ---

func TestEncodeAVP_Padding(t *testing.T) {
	// A 3-byte string "abc" should be padded to 4-byte boundary.
	// Header (8) + data (3) = 11 bytes → padded to 12.
	encoded, err := EncodeAVP(AVPCodeOriginHost, AVPFlagMandatory, 0, "abc")
	require.NoError(t, err)
	assert.Equal(t, 12, len(encoded), "3-byte data should pad to 12 total")
	assert.Equal(t, byte(0), encoded[11], "padding byte should be zero")
}

func TestEncodeAVP_NoPaddingNeeded(t *testing.T) {
	// A 4-byte uint32 needs no padding. Header (8) + data (4) = 12.
	encoded, err := EncodeAVP(AVPCodeResultCode, AVPFlagMandatory, 0, uint32(2001))
	require.NoError(t, err)
	assert.Equal(t, 12, len(encoded))
}

// --- Multiple AVP decode test ---

func TestDecodeAVPs_MultipleAVPs(t *testing.T) {
	// Encode two AVPs and concatenate them.
	a1, err := EncodeAVP(AVPCodeOriginHost, AVPFlagMandatory, 0, "host.local")
	require.NoError(t, err)
	a2, err := EncodeAVP(AVPCodeResultCode, AVPFlagMandatory, 0, uint32(2001))
	require.NoError(t, err)

	combined := append(a1, a2...)
	avps, err := DecodeAVPs(combined)
	require.NoError(t, err)
	require.Len(t, avps, 2)
	assert.Equal(t, AVPCodeOriginHost, avps[0].Code)
	assert.Equal(t, AVPCodeResultCode, avps[1].Code)
}

// --- Error cases ---

func TestDecodeAVP_TooShort(t *testing.T) {
	_, _, err := DecodeAVP([]byte{0, 0, 0})
	assert.Error(t, err)
}

func TestEncodeAVP_UnsupportedType(t *testing.T) {
	_, err := EncodeAVP(1, 0, 0, 3.14) // float64 not supported
	assert.Error(t, err)
}

// --- Raw Diameter frame fixture ---

func TestDecodeAVP_RawFixture_UAA(t *testing.T) {
	// Hand-crafted UAA AVP: Result-Code = 2001 (DIAMETER_SUCCESS).
	// AVP Code: 268 (0x0000010C)
	// Flags: 0x40 (M bit)
	// Length: 12 (0x00000C)
	// Value: 2001 (0x000007D1)
	raw := []byte{
		0x00, 0x00, 0x01, 0x0C, // AVP Code 268
		0x40, 0x00, 0x00, 0x0C, // Flags=M, Length=12
		0x00, 0x00, 0x07, 0xD1, // Value 2001
	}

	decoded, consumed, err := DecodeAVP(raw)
	require.NoError(t, err)
	assert.Equal(t, 12, consumed)
	assert.Equal(t, AVPCodeResultCode, decoded.Code)
	assert.Equal(t, uint32(2001), decoded.Uint32Val)
}

func TestDecodeAVP_RawFixture_OriginHost(t *testing.T) {
	// Hand-crafted Origin-Host AVP: "hss.ims.local"
	// AVP Code: 264 (0x00000108)
	// Flags: 0x40 (M bit)
	// Length: 21 (8 + 13) (0x000015)
	// Value: "hss.ims.local" (13 bytes) + 3 padding bytes
	raw := []byte{
		0x00, 0x00, 0x01, 0x08, // AVP Code 264
		0x40, 0x00, 0x00, 0x15, // Flags=M, Length=21
		'h', 's', 's', '.', 'i', 'm', 's', '.', 'l', 'o', 'c', 'a', 'l',
		0x00, 0x00, 0x00, // padding to 24
	}

	decoded, consumed, err := DecodeAVP(raw)
	require.NoError(t, err)
	assert.Equal(t, 24, consumed)
	assert.Equal(t, AVPCodeOriginHost, decoded.Code)
	assert.Equal(t, "hss.ims.local", decoded.StringVal)
}

// --- FindAVP ---

func TestFindAVP(t *testing.T) {
	avps := []AVP{
		NewStringAVP(AVPCodeOriginHost, "host.local"),
		NewUint32AVP(AVPCodeResultCode, 2001),
	}
	found := FindAVP(avps, AVPCodeResultCode)
	require.NotNil(t, found)
	assert.Equal(t, uint32(2001), found.Uint32Val)

	missing := FindAVP(avps, 99999)
	assert.Nil(t, missing)
}

// --- AVPCodeName ---

func TestAVPCodeName(t *testing.T) {
	assert.Equal(t, "Session-Id", AVPCodeName(AVPCodeSessionID))
	assert.Equal(t, "Result-Code", AVPCodeName(AVPCodeResultCode))
	assert.Equal(t, "CC-Request-Type", AVPCodeName(AVPCodeCCRequestType))
	assert.Equal(t, "AVP-12345", AVPCodeName(12345))

	// RFC 6733 §5.3.1 capabilities-exchange AVPs.
	assert.Equal(t, "Host-IP-Address", AVPCodeName(AVPCodeHostIPAddress))
	assert.Equal(t, "Auth-Application-Id", AVPCodeName(AVPCodeAuthApplicationID))
	assert.Equal(t, "Vendor-Id", AVPCodeName(AVPCodeVendorID))
	assert.Equal(t, "Product-Name", AVPCodeName(AVPCodeProductName))
	assert.Equal(t, "Vendor-Specific-Application-Id", AVPCodeName(AVPCodeVendorSpecificApplicationID))
	assert.Equal(t, "Firmware-Revision", AVPCodeName(AVPCodeFirmwareRevision))
}

// --- NewAddressAVP — RFC 6733 §4.3.1 Address format ---

func TestNewAddressAVP_IPv4(t *testing.T) {
	ip := net.ParseIP("192.0.2.10")
	avp := NewAddressAVP(AVPCodeHostIPAddress, ip)

	require.Len(t, avp.Data, 6)
	assert.Equal(t, []byte{0x00, 0x01}, avp.Data[0:2], "IANA address family 1 = IPv4")
	assert.Equal(t, ip.To4(), net.IP(avp.Data[2:]))
}

func TestNewAddressAVP_IPv6(t *testing.T) {
	ip := net.ParseIP("2001:db8::1")
	avp := NewAddressAVP(AVPCodeHostIPAddress, ip)

	require.Len(t, avp.Data, 18)
	assert.Equal(t, []byte{0x00, 0x02}, avp.Data[0:2], "IANA address family 2 = IPv6")
	assert.Equal(t, ip.To16(), net.IP(avp.Data[2:]))
}

func TestNewAddressAVP_RoundTrip(t *testing.T) {
	avp := NewAddressAVP(AVPCodeHostIPAddress, net.ParseIP("10.0.0.5"))

	encoded, err := EncodeAVP(avp.Code, avp.Flags, avp.VendorID, avp.Data)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, avp.Data, decoded.Data)
}

// --- NewVendorStringAVP / newScopedStringAVP — 3GPP vendor-scoped AVP encoding ---
//
// RFC 6733 §4.4: a peer's dictionary indexes AVPs by (code, vendor). A 3GPP
// AVP (Public-Identity 601, Server-Name 602, SIP-Auth-Data-Item 612)
// sent without the V bit and Vendor-Id 10415 lands in the base (code, none)
// namespace, where it does not exist — this is the exact byte-for-byte defect
// a real Open5GS peer rejected with 5001 DIAMETER_AVP_UNSUPPORTED.

// TestNewVendorStringAVP_SetsVBitAndVendorID — do not rename. This exact
// test name is referenced by the project's release-verification checklist
// as the proof that vendor-scoped AVP encoding is covered; renaming it
// silently drops that coverage.
func TestNewVendorStringAVP_SetsVBitAndVendorID(t *testing.T) {
	value := "sip:x@y"
	avp := NewVendorStringAVP(AVPCodePublicID, VendorID3GPP, value)

	assert.Equal(t, AVPFlagVendor|AVPFlagMandatory, avp.Flags, "flags must be V+M (0xC0)")
	assert.Equal(t, VendorID3GPP, avp.VendorID)

	encoded, err := EncodeAVP(avp.Code, avp.Flags, avp.VendorID, avp.StringVal)
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(encoded), 12)
	assert.Equal(t, byte(0xC0), encoded[4], "byte[4] must be flags 0xC0")

	vendorID := uint32(encoded[8])<<24 | uint32(encoded[9])<<16 | uint32(encoded[10])<<8 | uint32(encoded[11])
	assert.Equal(t, uint32(10415), vendorID, "bytes[8:12] must decode to Vendor-Id 10415")

	wantLen := 12 + len(value)
	gotLen := int(encoded[5])<<16 | int(encoded[6])<<8 | int(encoded[7])
	assert.Equal(t, wantLen, gotLen, "3-byte length field must equal 12 + len(value)")
}

func TestNewScopedStringAVP_VendorScopesCxAVPs(t *testing.T) {
	avp := newScopedStringAVP(AVPCodePublicID, "sip:447700900001@ims.local")
	assert.Equal(t, AVPFlagVendor|AVPFlagMandatory, avp.Flags, "Public-Identity must be vendor-scoped")
	assert.Equal(t, VendorID3GPP, avp.VendorID)
}

func TestNewScopedStringAVP_BaseAVPsStayPlain(t *testing.T) {
	baseCodes := []uint32{AVPCodeOriginHost, AVPCodeSessionID, AVPCodeUserName}
	for _, code := range baseCodes {
		avp := newScopedStringAVP(code, "some-value")
		assert.Equal(t, AVPFlagMandatory, avp.Flags, "base AVP %d must not be vendor-scoped", code)
		assert.Equal(t, uint32(0), avp.VendorID, "base AVP %d must carry no Vendor-Id", code)
	}
}

// TestNewScopedStringAVP_DestinationRealmIsBaseScoped.
// Destination-Realm (283) is an RFC 6733 base AVP, unrelated to the 3GPP
// vendor-scoping decision 601/602/613 need. Asserted as two SEPARATE
// properties (Flags == 0x40 AND VendorID == 0, i.e. the V bit absent) rather
// than only checking Flags == 0x40 — a test that only checks Flags could pass
// even if VendorID drifted nonzero by accident.
func TestNewScopedStringAVP_DestinationRealmIsBaseScoped(t *testing.T) {
	avp := newScopedStringAVP(AVPCodeDestinationRealm, "gradiant")

	assert.Equal(t, AVPFlagMandatory, avp.Flags, "Destination-Realm must be plain-M (0x40), not vendor-scoped")
	assert.Equal(t, uint32(0), avp.VendorID, "Destination-Realm must carry no Vendor-Id")
	assert.Equal(t, byte(0), avp.Flags&AVPFlagVendor, "Destination-Realm's V bit must be clear")
	assert.Equal(t, "Destination-Realm", AVPCodeName(AVPCodeDestinationRealm))
}

func TestNewVendorStringAVP_RoundTrip(t *testing.T) {
	value := "sip:447700900001@ims.local"
	avp := NewVendorStringAVP(AVPCodePublicID, VendorID3GPP, value)

	encoded, err := EncodeAVP(avp.Code, avp.Flags, avp.VendorID, avp.StringVal)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodePublicID, decoded.Code)
	assert.Equal(t, VendorID3GPP, decoded.VendorID)
	assert.Equal(t, value, decoded.StringVal)
}

// --- Dictionary-derived AVPCode* vars ---

// avpCodeVarToDictName maps each AVPCode* Go identifier to the dictionary
// name it must resolve through mustCode, mirroring avp.go's own var block.
// Used only by the two tests below; not a production table.
var avpCodeVarToDictName = map[string]uint32{
	"Session-Id":                     AVPCodeSessionID,
	"Origin-Host":                    AVPCodeOriginHost,
	"Origin-Realm":                   AVPCodeOriginRealm,
	"Destination-Realm":              AVPCodeDestinationRealm,
	"Result-Code":                    AVPCodeResultCode,
	"User-Name":                      AVPCodeUserName,
	"Public-Identity":                AVPCodePublicID,
	"Server-Name":                    AVPCodeServerName,
	"SIP-Auth-Data-Item":             AVPCodeSIPAuthDataItem,
	"Host-IP-Address":                AVPCodeHostIPAddress,
	"Auth-Application-Id":            AVPCodeAuthApplicationID,
	"Acct-Application-Id":            AVPCodeAcctApplicationID,
	"Vendor-Specific-Application-Id": AVPCodeVendorSpecificApplicationID,
	"Supported-Vendor-Id":            AVPCodeSupportedVendorID,
	"Vendor-Id":                      AVPCodeVendorID,
	"Firmware-Revision":              AVPCodeFirmwareRevision,
	"Product-Name":                   AVPCodeProductName,
	"Origin-State-Id":                AVPCodeOriginStateID,
	"CC-Request-Type":                AVPCodeCCRequestType,
	"CC-Request-Number":              AVPCodeCCRequestNumber,
	"Requested-Service-Unit":         AVPCodeReqServiceUnit,
	"Granted-Service-Unit":           AVPCodeGrantServiceUnit,
	"Rating-Group":                   AVPCodeRatingGroup,
}

func TestAVPCode_ValuesMatchDictionary(t *testing.T) {
	require.Len(t, avpCodeVarToDictName, 23, "expected all 23 AVPCode* identifiers covered")
	for dictName, gotCode := range avpCodeVarToDictName {
		def, ok := LookupAVP(dictName)
		require.True(t, ok, "dictionary must define %q", dictName)
		assert.Equal(t, def.Code, gotCode, "AVPCode var for %q must equal Dictionary[%q].Code", dictName, dictName)
	}
}

func TestAVPCodeName_ResolvesEveryDictionaryEntry(t *testing.T) {
	names := make([]string, 0, len(Dictionary))
	for name := range Dictionary {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		def := Dictionary[name]
		assert.Equal(t, name, AVPCodeName(def.Code), "AVPCodeName(%d) must resolve to dictionary key %q", def.Code, name)
	}
}

// --- newScopedStringAVP whole-dictionary vendor-scoping property ---

// TestNewScopedStringAVP_MatchesDictionaryForEveryAVP iterates the whole
// dictionary (sorted for stable failure output) and asserts newScopedStringAVP
// encodes exactly what each AVP's own definition says: V+M (0xC0) and the
// definition's VendorID when Vendor is true, plain M (0x40) and VendorID 0
// otherwise. This is a property over the whole dictionary rather than a
// per-AVP test, so it automatically covers all ~175 S6a AVPs the moment
// s6a.yaml is transcribed, with no test-file maintenance required.
func TestNewScopedStringAVP_MatchesDictionaryForEveryAVP(t *testing.T) {
	names := make([]string, 0, len(Dictionary))
	for name := range Dictionary {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		def := Dictionary[name]
		avp := newScopedStringAVP(def.Code, "x")
		if def.Vendor {
			assert.Equal(t, AVPFlagVendor|AVPFlagMandatory, avp.Flags, "%q (vendor-scoped) must carry flags 0xC0", name)
			assert.Equal(t, def.VendorID, avp.VendorID, "%q (vendor-scoped) must carry its own VendorID", name)
		} else {
			assert.Equal(t, AVPFlagMandatory, avp.Flags, "%q (base) must carry flags 0x40", name)
			assert.Equal(t, uint32(0), avp.VendorID, "%q (base) must carry no VendorID", name)
		}
	}
}

// TestNewScopedStringAVP_UnknownCodeStaysPlain preserves today's behaviour
// for a code a caller constructs directly, with no dictionary entry: it must
// still produce a plain mandatory AVP rather than panicking or defaulting to
// vendor-scoped.
func TestNewScopedStringAVP_UnknownCodeStaysPlain(t *testing.T) {
	const unknownCode = 999999
	_, ok := LookupAVPByCode(unknownCode)
	require.False(t, ok, "test fixture code must not collide with a real dictionary entry")

	avp := newScopedStringAVP(unknownCode, "some-value")
	assert.Equal(t, AVPFlagMandatory, avp.Flags, "unknown code must stay plain-M")
	assert.Equal(t, uint32(0), avp.VendorID, "unknown code must carry no VendorID")
}
