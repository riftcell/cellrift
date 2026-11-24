// Package diameter implements the Diameter base protocol (RFC 6733) and
// Diameter Credit-Control Application (RFC 4006).
package diameter

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
)

// mustCodeDict is a private, lazily-loaded copy of the embedded AVP
// dictionary, used only by mustCode below. It cannot simply read
// dictionary.go's package-level Dictionary var: per the Go spec, ALL
// package-level variable initializer expressions in a package run before
// ANY of that package's init() functions, and Dictionary is populated
// inside dictionary.go's init(), not in its own declaration's initializer.
// That means every AVPCode* var below — each a variable initializer calling
// mustCode — would unconditionally observe Dictionary as its nil zero value,
// not a race but a guaranteed ordering gap. Loading the dictionary directly
// here (memoized so it only parses once) sidesteps that gap; dictionary.go's
// init() then loads its own copy independently for LookupAVP/LookupAVPByCode
// callers, which is redundant but harmless since loadDictionary is pure.
var (
	mustCodeDictOnce sync.Once
	mustCodeDict     AVPDictionary
)

// mustCode resolves an AVP name to its code through the embedded dictionary
// at package-initialisation time. It panics on a name the dictionary does
// not define, because an incomplete or mistyped dictionary file must break
// `go build`/`go test` on a developer's machine, never surface as a
// malformed message on a customer's first run against a real HSS.
func mustCode(name string) uint32 {
	mustCodeDictOnce.Do(func() {
		mustCodeDict, _ = mustLoadDictionary()
	})
	def, ok := mustCodeDict[name]
	if !ok {
		panic(fmt.Sprintf("diameter: dictionary missing required AVP %q", name))
	}
	return def.Code
}

// AVP Code vars — RFC 6733 base AVPs. Resolved from the embedded dictionary
// (dictionary/base.yaml, dictionary/cx.yaml) rather than hardcoded, so each
// AVP's code/type/vendor-scoping facts live in exactly one place.
var (
	AVPCodeSessionID = mustCode("Session-Id")
	// AVPCodeExperimentalResult / ...Code address RFC 6733 §7.6's
	// application-scoped result, which is how every 3GPP Cx answer reports
	// its outcome.
	AVPCodeExperimentalResult     = mustCode("Experimental-Result")
	AVPCodeExperimentalResultCode = mustCode("Experimental-Result-Code")
	AVPCodeOriginHost             = mustCode("Origin-Host")
	AVPCodeOriginRealm            = mustCode("Origin-Realm")
	AVPCodeDestinationRealm       = mustCode("Destination-Realm") // RFC 6733 §6.6 — DiameterIdentity, base (NOT vendor-scoped)
	AVPCodeResultCode             = mustCode("Result-Code")
	AVPCodeUserName               = mustCode("User-Name")
	AVPCodePublicID               = mustCode("Public-Identity")    // 3GPP Cx
	AVPCodeServerName             = mustCode("Server-Name")        // 3GPP Cx
	AVPCodeSIPAuthDataItem        = mustCode("SIP-Auth-Data-Item") // 3GPP Cx — SIP-Auth-Data-Item (Grouped)
)

// AVP Code vars — RFC 6733 §5.3.1 capabilities-exchange AVPs (Host-IP-Address,
// Auth-Application-Id, Vendor-Specific-Application-Id, and related peer
// capability AVPs), sourced from dictionary/base.yaml. See RFC 6733 §5.3.1
// (https://www.rfc-editor.org/rfc/rfc6733#section-5.3.1) for the mandatory
// CER/CEA AVP set.
var (
	AVPCodeHostIPAddress               = mustCode("Host-IP-Address")
	AVPCodeAuthApplicationID           = mustCode("Auth-Application-Id")
	AVPCodeAcctApplicationID           = mustCode("Acct-Application-Id")
	AVPCodeVendorSpecificApplicationID = mustCode("Vendor-Specific-Application-Id")
	AVPCodeSupportedVendorID           = mustCode("Supported-Vendor-Id")
	AVPCodeVendorID                    = mustCode("Vendor-Id")
	AVPCodeFirmwareRevision            = mustCode("Firmware-Revision")
	AVPCodeProductName                 = mustCode("Product-Name")
	AVPCodeOriginStateID               = mustCode("Origin-State-Id")
)

// AVP Code vars — RFC 4006 credit-control AVPs, sourced from
// dictionary/base.yaml.
var (
	AVPCodeCCRequestType    = mustCode("CC-Request-Type")
	AVPCodeCCRequestNumber  = mustCode("CC-Request-Number")
	AVPCodeReqServiceUnit   = mustCode("Requested-Service-Unit") // Requested-Service-Unit
	AVPCodeGrantServiceUnit = mustCode("Granted-Service-Unit")   // Granted-Service-Unit
	AVPCodeRatingGroup      = mustCode("Rating-Group")
)

// AVP Flag bits — RFC 6733 §4.1.
const (
	AVPFlagVendor    byte = 0x80 // V bit
	AVPFlagMandatory byte = 0x40 // M bit
)

// Auth-Session-State values — RFC 6733 §8.11.
const (
	AuthSessionStateMaintained        uint32 = 0
	AuthSessionStateNoStateMaintained uint32 = 1
)

// AVP Code vars — RFC 6733 base and 3GPP S6a AVPs required by AIR/ULR,
// sourced from dictionary/base.yaml and dictionary/s6a.yaml respectively.
var (
	AVPCodeAuthSessionState = mustCode("Auth-Session-State")
	AVPCodeRATType          = mustCode("RAT-Type")
)

// AVP data types.
const (
	DataTypeOctetString = iota
	DataTypeUnsigned32
	DataTypeGrouped
)

// AVP represents a single Diameter Attribute-Value Pair.
type AVP struct {
	Code     uint32
	Flags    byte
	VendorID uint32 // only set when V flag is present
	Data     []byte // raw data payload

	// Parsed convenience fields — set by decode helpers.
	DataType int
	// For Unsigned32 AVPs.
	Uint32Val uint32
	// For OctetString/UTF8String AVPs.
	StringVal string
	// For Grouped AVPs.
	GroupedAVPs []AVP
}

// avpHeaderLen returns the header length based on the V flag.
func avpHeaderLen(flags byte) int {
	if flags&AVPFlagVendor != 0 {
		return 12 // Code(4) + Flags+Length(4) + VendorID(4)
	}
	return 8 // Code(4) + Flags+Length(4)
}

// padTo4 returns n rounded up to the next multiple of 4.
func padTo4(n int) int {
	return (n + 3) &^ 3
}

// EncodeAVP encodes a single AVP. The value can be:
//   - string: encoded as OctetString/UTF8String
//   - uint32: encoded as Unsigned32 (big-endian)
//   - []AVP: encoded as Grouped AVP (concatenated sub-AVPs)
//   - []byte: encoded as raw OctetString
func EncodeAVP(code uint32, flags byte, vendorID uint32, value interface{}) ([]byte, error) {
	var data []byte

	switch v := value.(type) {
	case string:
		data = []byte(v)
	case uint32:
		data = make([]byte, 4)
		binary.BigEndian.PutUint32(data, v)
	case []AVP:
		for _, sub := range v {
			encoded, err := EncodeAVP(sub.Code, sub.Flags, sub.VendorID, avpValue(sub))
			if err != nil {
				return nil, fmt.Errorf("encode grouped sub-AVP %d: %w", sub.Code, err)
			}
			data = append(data, encoded...)
		}
	case []byte:
		data = v
	default:
		return nil, fmt.Errorf("unsupported AVP value type %T", value)
	}

	hdrLen := avpHeaderLen(flags)
	avpLen := hdrLen + len(data)
	paddedLen := padTo4(avpLen)

	buf := make([]byte, paddedLen)

	// AVP Code.
	binary.BigEndian.PutUint32(buf[0:4], code)

	// Flags (1 byte) + Length (3 bytes).
	buf[4] = flags
	buf[5] = byte(avpLen >> 16)
	buf[6] = byte(avpLen >> 8)
	buf[7] = byte(avpLen)

	offset := 8
	if flags&AVPFlagVendor != 0 {
		binary.BigEndian.PutUint32(buf[8:12], vendorID)
		offset = 12
	}

	copy(buf[offset:], data)
	// Padding bytes are already zero from make().

	return buf, nil
}

// avpValue extracts the appropriate Go value from an AVP for re-encoding.
func avpValue(a AVP) interface{} {
	if len(a.GroupedAVPs) > 0 {
		return a.GroupedAVPs
	}
	if a.Data != nil {
		return a.Data
	}
	return a.StringVal
}

// DecodeAVP decodes a single AVP from a byte slice and returns the AVP
// along with the total bytes consumed (including padding).
func DecodeAVP(raw []byte) (*AVP, int, error) {
	if len(raw) < 8 {
		return nil, 0, fmt.Errorf("AVP too short: need at least 8 bytes, got %d", len(raw))
	}

	a := &AVP{}
	a.Code = binary.BigEndian.Uint32(raw[0:4])
	a.Flags = raw[4]

	// AVP Length is 3 bytes.
	avpLen := int(raw[5])<<16 | int(raw[6])<<8 | int(raw[7])
	hdrLen := avpHeaderLen(a.Flags)

	if avpLen < hdrLen {
		return nil, 0, fmt.Errorf("AVP %d: length %d is less than header %d", a.Code, avpLen, hdrLen)
	}

	paddedLen := padTo4(avpLen)
	if len(raw) < paddedLen {
		// Allow reading up to avpLen if padding extends past buffer (last AVP).
		if len(raw) < avpLen {
			return nil, 0, fmt.Errorf("AVP %d: need %d bytes, got %d", a.Code, avpLen, len(raw))
		}
		paddedLen = len(raw)
	}

	offset := 8
	if a.Flags&AVPFlagVendor != 0 {
		if avpLen < 12 {
			return nil, 0, fmt.Errorf("AVP %d: vendor-specific but length %d < 12", a.Code, avpLen)
		}
		a.VendorID = binary.BigEndian.Uint32(raw[8:12])
		offset = 12
	}

	dataLen := avpLen - hdrLen
	a.Data = make([]byte, dataLen)
	copy(a.Data, raw[offset:offset+dataLen])

	// Set typed convenience fields based on data length.
	if dataLen == 4 {
		a.DataType = DataTypeUnsigned32
		a.Uint32Val = binary.BigEndian.Uint32(a.Data)
	} else {
		a.DataType = DataTypeOctetString
		a.StringVal = string(a.Data)
	}

	return a, paddedLen, nil
}

// DecodeAVPs decodes a sequence of AVPs from the given byte slice.
func DecodeAVPs(raw []byte) ([]AVP, error) {
	var avps []AVP
	offset := 0
	for offset < len(raw) {
		a, consumed, err := DecodeAVP(raw[offset:])
		if err != nil {
			return avps, fmt.Errorf("decode AVP at offset %d: %w", offset, err)
		}
		avps = append(avps, *a)
		offset += consumed
	}
	return avps, nil
}

// DecodeGroupedAVP decodes the Data field of a Grouped AVP into sub-AVPs.
func DecodeGroupedAVP(a *AVP) error {
	if len(a.Data) == 0 {
		return nil
	}
	subs, err := DecodeAVPs(a.Data)
	if err != nil {
		return fmt.Errorf("decode grouped AVP %d: %w", a.Code, err)
	}
	a.GroupedAVPs = subs
	a.DataType = DataTypeGrouped
	return nil
}

// NewStringAVP creates an OctetString/UTF8String AVP, always with the
// Mandatory flag set. This does not consult AVPDefinition.Mandatory: that
// field is dictionary metadata recorded for completeness (two
// base AVPs, Firmware-Revision and Product-Name, are correctly recorded as
// non-mandatory), but wiring it through to the M-bit here would change the
// CER's encoded bytes and break the handshake tests that assert them. Same
// note applies to NewUint32AVP below.
func NewStringAVP(code uint32, value string) AVP {
	data := []byte(value)
	return AVP{
		Code:      code,
		Flags:     AVPFlagMandatory,
		Data:      data,
		DataType:  DataTypeOctetString,
		StringVal: value,
	}
}

// NewVendorStringAVP creates a vendor-scoped OctetString/UTF8String AVP: the
// Vendor-Specific (V) bit is set alongside Mandatory (flags 0xC0) and the
// 4-byte vendorID is carried in the AVP header, per RFC 6733 §4.4. This is
// required for any AVP defined by a vendor/3GPP specification rather than the
// Diameter base protocol: a peer's dictionary indexes AVPs by (code, vendor),
// so a 3GPP AVP (e.g. Public-Identity, code 601, Vendor-Id 10415) sent
// without the V bit lands in the (code, none) namespace, where that code does
// not exist — confirmed against a real Open5GS peer, which rejected exactly
// this shape with 5001 DIAMETER_AVP_UNSUPPORTED.
func NewVendorStringAVP(code, vendorID uint32, value string) AVP {
	data := []byte(value)
	return AVP{
		Code:      code,
		Flags:     AVPFlagVendor | AVPFlagMandatory,
		VendorID:  vendorID,
		Data:      data,
		DataType:  DataTypeOctetString,
		StringVal: value,
	}
}

// newScopedStringAVP builds a string AVP, consulting the AVP's own
// dictionary definition to decide whether it must be vendor-scoped, rather
// than a separate hand-maintained table. This is the ONLY place that
// decision is made — call sites in adapter.go and testing.go must route
// every string AVP through here rather than choosing NewStringAVP vs.
// NewVendorStringAVP themselves.
//
// The decision has to come from the dictionary rather than a short list of
// known codes: a peer indexes AVPs by (code, vendor-id), so an AVP defined
// by a vendor or 3GPP specification and sent without the V bit lands in the
// base (code, none) namespace, where that code does not exist — a real
// Open5GS peer rejected exactly that shape with 5001
// DIAMETER_AVP_UNSUPPORTED. The old hardcoded table listed three Cx codes
// by hand; every AVP in the 3GPP S6a set is equally vendor-scoped under
// Vendor-Id 10415, so a table that has to be extended by hand for each new
// AVP is a silent regression waiting for the first S6a request. Sourcing
// the decision from each AVP's own definition removes that whole class of
// bug rather than patching one instance of it.
func newScopedStringAVP(code uint32, value string) AVP {
	if def, ok := LookupAVPByCode(code); ok && def.Vendor {
		return NewVendorStringAVP(code, def.VendorID, value)
	}
	return NewStringAVP(code, value)
}

// NewUint32AVP creates an Unsigned32 AVP with the Mandatory flag.
func NewUint32AVP(code uint32, value uint32) AVP {
	data := make([]byte, 4)
	binary.BigEndian.PutUint32(data, value)
	return AVP{
		Code:      code,
		Flags:     AVPFlagMandatory,
		Data:      data,
		DataType:  DataTypeUnsigned32,
		Uint32Val: value,
	}
}

// newScopedUint32AVP builds a Unsigned32/Integer32/Enumerated AVP, consulting
// the AVP's own dictionary definition to decide whether it must be
// vendor-scoped — the same decision newScopedStringAVP makes for strings,
// and for the same reason: a peer indexes AVPs by (code, vendor-id), so a
// vendor AVP encoded plain-M lands in the base namespace where its code does
// not exist. Before the ULR builder landed, only string-typed S6a AVPs were
// routed through this check (3GPP vendor scoping) — RAT-Type and ULR-Flags being the first
// numeric S6a AVPs any scenario field ever sends is what surfaced this gap:
// encodeUint32Field called bare NewUint32AVP, so a vendor-scoped Enumerated
// or Unsigned32 AVP silently went out plain-M, the identical defect a real
// peer already rejected once for strings.
func newScopedUint32AVP(code uint32, value uint32) AVP {
	avp := NewUint32AVP(code, value)
	if def, ok := LookupAVPByCode(code); ok && def.Vendor {
		avp.Flags = AVPFlagVendor | AVPFlagMandatory
		avp.VendorID = def.VendorID
	}
	return avp
}

// NewGroupedAVP creates a Grouped AVP with the Mandatory flag.
func NewGroupedAVP(code uint32, subs []AVP) AVP {
	return AVP{
		Code:        code,
		Flags:       AVPFlagMandatory,
		DataType:    DataTypeGrouped,
		GroupedAVPs: subs,
	}
}

// IANA Address Family Numbers used by the RFC 6733 §4.3.1 Address format —
// only the two families Cellrift ever needs to send (IPv4 host addresses and
// IPv6 host addresses).
const (
	addressFamilyIPv4 uint16 = 1
	addressFamilyIPv6 uint16 = 2
)

// NewAddressAVP creates an Address-format AVP per RFC 6733 §4.3.1: a 2-octet
// Address Family (IANA AFN) followed by the raw address bytes. Used for
// Host-IP-Address in the CER — prefers the 4-byte IPv4 form when the address
// has one, falling back to the 16-byte IPv6 form otherwise.
func NewAddressAVP(code uint32, ip net.IP) AVP {
	family := addressFamilyIPv6
	addr := ip.To16()
	if v4 := ip.To4(); v4 != nil {
		family = addressFamilyIPv4
		addr = v4
	}

	data := make([]byte, 2+len(addr))
	binary.BigEndian.PutUint16(data[0:2], family)
	copy(data[2:], addr)

	return AVP{
		Code:     code,
		Flags:    AVPFlagMandatory,
		Data:     data,
		DataType: DataTypeOctetString,
	}
}

// FindAVP searches a slice of AVPs for the first one matching the given code.
// Returns nil if not found.
func FindAVP(avps []AVP, code uint32) *AVP {
	for i := range avps {
		if avps[i].Code == code {
			return &avps[i]
		}
	}
	return nil
}

// AVPCodeName returns a human-readable name for known AVP codes, resolved
// through the dictionary's code index (dictionary.go's dictionaryByCode)
// rather than a hand-maintained reverse table.
func AVPCodeName(code uint32) string {
	if def, ok := LookupAVPByCode(code); ok {
		return def.Name
	}
	return fmt.Sprintf("AVP-%d", code)
}
