// Package malform declares the shared vocabulary for a scenario step's
// deliberate, post-encode message corruption: the config-map keys the
// engine writes and both the Diameter and SIP adapters read, the SIP header
// operation names, and the one shared size bound both protocols' 3-byte
// length fields must respect.
package malform

import "strconv"

// AVPLengthNameKey, AVPLengthValueKey, MsgLengthKey, SIPHeaderNameKey,
// SIPHeaderOpKey and SIPHeaderValueKey are the keys the engine's
// foldMalformConfig writes into an adapter's config map from a step's
// malform: block. They are the DECLARATION side of the vocabulary, read
// back out by AVPLengthOverride/MessageLengthOverride/SIPHeaderOverride
// below. They are deliberately prefixed "malform_" so they can never
// collide with impair:'s own flat keys ("latency_ms", "drop", ...) in the
// same config map. This prefix is a plain string, not the "__" prefix —
// that one is reserved for fields: body injection from executor to
// adapter, a different channel entirely.
const (
	AVPLengthNameKey  = "malform_avp_length_name"
	AVPLengthValueKey = "malform_avp_length_value"
	MsgLengthKey      = "malform_msg_length"
	SIPHeaderNameKey  = "malform_sip_header_name"
	SIPHeaderOpKey    = "malform_sip_header_op"
	SIPHeaderValueKey = "malform_sip_header_value"
)

// OpMissing, OpDuplicate and OpOverwrite are the three SIP header
// corruption operations a malform.sip_header block may declare. This is the
// single source of truth shared by the scenario validator (which rejects
// any other op value), the published JSON schema (whose sip_header.op enum
// must list exactly these three strings), and the SIP adapter's own op
// dispatch table.
const (
	OpMissing   = "missing"
	OpDuplicate = "duplicate"
	OpOverwrite = "overwrite"
)

// MaxThreeByteLength is the largest value that fits in a 3-byte unsigned
// field: 0xFFFFFF, 16777215. Both the Diameter AVP Length field (3 bytes at
// AVP header offset [5:8]) and the Diameter Message Length field (3 bytes
// at message offset [1:4]) are three bytes wide, so a declared override
// above this bound cannot be written to the wire at all — it is a
// scenario-authoring error, not a malformation the wire can even represent.
const MaxThreeByteLength = 0xFFFFFF

// HeaderSpec is one step's declared SIP header corruption, read back out of
// the config map by the SIP adapter via SIPHeaderOverride.
type HeaderSpec struct {
	Name  string
	Op    string
	Value string
}

// AVPLengthOverride reads a declared avp_length override back out of cfg.
// Both AVPLengthNameKey and AVPLengthValueKey must be present; ok is false
// otherwise. AVPLengthValueKey is parsed with strconv.Atoi: the engine
// writes it via strconv.Itoa, so a parse failure can only be a bug, and an
// exact-parse contract keeps that loud rather than silently returning 0.
func AVPLengthOverride(cfg map[string]string) (name string, value int, ok bool) {
	name, hasName := cfg[AVPLengthNameKey]
	rawValue, hasValue := cfg[AVPLengthValueKey]
	if !hasName || !hasValue {
		return "", 0, false
	}
	value, err := strconv.Atoi(rawValue)
	if err != nil {
		return "", 0, false
	}
	return name, value, true
}

// MessageLengthOverride reads a declared msg_length override back out of
// cfg. A present-but-unparseable value is ok == false, for the same reason
// AVPLengthOverride treats it as a bug rather than a silent zero. Zero
// itself is a legitimate, deliberately-wrong length — distinct from
// "absent" — and is returned as (0, true) when declared.
func MessageLengthOverride(cfg map[string]string) (value int, ok bool) {
	rawValue, has := cfg[MsgLengthKey]
	if !has {
		return 0, false
	}
	value, err := strconv.Atoi(rawValue)
	if err != nil {
		return 0, false
	}
	return value, true
}

// SIPHeaderOverride reads a declared sip_header override back out of cfg.
// Name and Op must both be present; ok is false otherwise. Value may be
// empty (legitimate for op: missing, which never reads it).
func SIPHeaderOverride(cfg map[string]string) (HeaderSpec, bool) {
	name, hasName := cfg[SIPHeaderNameKey]
	op, hasOp := cfg[SIPHeaderOpKey]
	if !hasName || !hasOp {
		return HeaderSpec{}, false
	}
	return HeaderSpec{Name: name, Op: op, Value: cfg[SIPHeaderValueKey]}, true
}
