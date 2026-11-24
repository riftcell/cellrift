package diameter

import (
	"encoding/binary"
	"fmt"
	"math/rand"
)

// Diameter header is always 20 bytes — RFC 6733 §3.
const HeaderLength = 20

// Diameter protocol version — RFC 6733 §3.
const Version byte = 1

// Command flags — RFC 6733 §3.
const (
	FlagRequest    byte = 0x80 // R bit
	FlagProxiable  byte = 0x40 // P bit
	FlagError      byte = 0x20 // E bit
	FlagRetransmit byte = 0x10 // T bit
)

// Command codes — RFC 6733 base.
//
// CmdCapabilitiesExchange (CER/CEA, §5.3) and CmdDeviceWatchdog (DWR/DWA,
// §5.5) are implemented: the adapter performs the handshake
// automatically on the first Diameter step (handshake.go, session.go) and
// answers inbound DWR while a request is outstanding. This is verified
// against Cellrift's own in-repo strict mock HSS — see
// TestAdapter_FailsAgainstStrictPeer in adapter_test.go, which now asserts
// success. It is NOT yet verified against a third-party Diameter
// implementation (freeDiameter, Open5GS, a production HSS); that evidence is
// the interop CI job (.github/workflows/interop.yml).
const (
	CmdCapabilitiesExchange uint32 = 257 // CER/CEA
	CmdDeviceWatchdog       uint32 = 280 // DWR/DWA
	CmdUserAuthorization    uint32 = 300 // UAR/UAA (3GPP Cx)
)

// Command codes — RFC 4006 credit-control.
const (
	CmdCreditControl uint32 = 272 // CCR/CCA
)

// Command codes — 3GPP TS 29.229 (Cx) §6.1.
const (
	CmdServerAssignment uint32 = 301 // SAR/SAA
	CmdMultimediaAuth   uint32 = 303 // MAR/MAA
)

// Command codes — 3GPP TS 29.272 (S6a) §7.2.
const (
	CmdUpdateLocation            uint32 = 316 // ULR/ULA
	CmdAuthenticationInformation uint32 = 318 // AIR/AIA
)

// Application IDs.
const (
	AppIDCx            uint32 = 16777216 // 3GPP Cx
	AppIDCreditControl uint32 = 4        // RFC 4006
)

// Result codes — RFC 6733 §7.1.
const (
	ResultSuccess               uint32 = 2001
	ResultCommandUnsupported    uint32 = 3001
	ResultAuthorizationRejected uint32 = 5003 // DIAMETER_AUTHORIZATION_REJECTED
	ResultMissingAVP            uint32 = 5005 // DIAMETER_MISSING_AVP
)

// CC-Request-Type values — RFC 4006 §8.3.
const (
	CCRequestTypeInitial     uint32 = 1
	CCRequestTypeUpdate      uint32 = 2
	CCRequestTypeTermination uint32 = 3
	CCRequestTypeEvent       uint32 = 4
)

// Message represents a Diameter message (header + AVP list) per RFC 6733 §3.
type Message struct {
	Version     byte
	Flags       byte
	CommandCode uint32
	AppID       uint32
	HopByHopID  uint32
	EndToEndID  uint32
	AVPs        []AVP
}

// IsRequest returns true if the R flag is set.
func (m *Message) IsRequest() bool {
	return m.Flags&FlagRequest != 0
}

// BuildMessage constructs a Diameter message with the given command code,
// application ID, flags, and AVP list. Hop-by-Hop and End-to-End IDs are
// auto-generated; the generated Hop-by-Hop ID is returned so a caller can
// later validate an answer's correlation against the ID actually sent (RFC
// 6733 §3 uses Hop-by-Hop for single-connection transaction correlation).
// Only the Hop-by-Hop ID is returned, not End-to-End: End-to-End is for
// duplicate detection across retransmission/failover, which is out of scope
// until a future phase adds it — this return tuple is the seam to extend
// then.
func BuildMessage(cmdCode uint32, appID uint32, flags byte, avps []AVP) ([]byte, uint32, error) {
	// Encode all AVPs.
	var avpBytes []byte
	for _, a := range avps {
		encoded, err := EncodeAVP(a.Code, a.Flags, a.VendorID, avpValue(a))
		if err != nil {
			return nil, 0, fmt.Errorf("encode AVP %s: %w", AVPCodeName(a.Code), err)
		}
		avpBytes = append(avpBytes, encoded...)
	}

	msgLen := HeaderLength + len(avpBytes)

	buf := make([]byte, msgLen)

	// Version (1 byte) + Message Length (3 bytes).
	buf[0] = Version
	buf[1] = byte(msgLen >> 16)
	buf[2] = byte(msgLen >> 8)
	buf[3] = byte(msgLen)

	// Command Flags (1 byte) + Command Code (3 bytes).
	buf[4] = flags
	buf[5] = byte(cmdCode >> 16)
	buf[6] = byte(cmdCode >> 8)
	buf[7] = byte(cmdCode)

	// Application-ID.
	binary.BigEndian.PutUint32(buf[8:12], appID)

	// Hop-by-Hop Identifier.
	hopByHop := rand.Uint32()
	binary.BigEndian.PutUint32(buf[12:16], hopByHop)

	// End-to-End Identifier.
	binary.BigEndian.PutUint32(buf[16:20], rand.Uint32())

	// AVPs.
	copy(buf[HeaderLength:], avpBytes)

	return buf, hopByHop, nil
}

// BuildRequest is a convenience for building a request message (R flag set).
// It forwards BuildMessage's Hop-by-Hop ID so a caller can correlate the
// answer it later receives.
func BuildRequest(cmdCode uint32, appID uint32, avps []AVP) ([]byte, uint32, error) {
	return BuildMessage(cmdCode, appID, FlagRequest, avps)
}

// BuildAnswer is a convenience for building an answer message (R flag
// cleared). Deliberately keeps the two-value ([]byte, error) signature and
// discards the generated Hop-by-Hop ID: an answer's Hop-by-Hop/End-to-End
// identifiers are overwritten with the request's by the caller (RFC 6733
// requires the echo), so a generated ID for an answer is never used. Keeping
// this signature stable also keeps this change out of every test file that
// builds an answer and has nothing to do with it.
func BuildAnswer(cmdCode uint32, appID uint32, avps []AVP) ([]byte, error) {
	raw, _, err := BuildMessage(cmdCode, appID, 0, avps)
	return raw, err
}

// ParseMessage parses a Diameter message from raw bytes.
func ParseMessage(raw []byte) (*Message, error) {
	if len(raw) < HeaderLength {
		return nil, fmt.Errorf("diameter message too short: need %d bytes, got %d", HeaderLength, len(raw))
	}

	m := &Message{}
	m.Version = raw[0]
	if m.Version != Version {
		return nil, fmt.Errorf("unsupported diameter version: %d", m.Version)
	}

	msgLen := int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
	if msgLen < HeaderLength {
		return nil, fmt.Errorf("invalid message length: %d", msgLen)
	}
	if len(raw) < msgLen {
		return nil, fmt.Errorf("message truncated: header says %d bytes, got %d", msgLen, len(raw))
	}

	m.Flags = raw[4]
	m.CommandCode = uint32(raw[5])<<16 | uint32(raw[6])<<8 | uint32(raw[7])
	m.AppID = binary.BigEndian.Uint32(raw[8:12])
	m.HopByHopID = binary.BigEndian.Uint32(raw[12:16])
	m.EndToEndID = binary.BigEndian.Uint32(raw[16:20])

	// Parse AVPs from body.
	avpData := raw[HeaderLength:msgLen]
	if len(avpData) > 0 {
		avps, err := DecodeAVPs(avpData)
		if err != nil {
			return nil, fmt.Errorf("decode AVPs: %w", err)
		}
		m.AVPs = avps
	}

	return m, nil
}

// FindAVPValue searches the message's AVP list for the given code.
func (m *Message) FindAVP(code uint32) *AVP {
	return FindAVP(m.AVPs, code)
}

// ResultCode returns the Result-Code AVP value, or 0 if not present.
func (m *Message) ResultCode() uint32 {
	a := m.FindAVP(AVPCodeResultCode)
	if a == nil {
		return 0
	}
	return a.Uint32Val
}
