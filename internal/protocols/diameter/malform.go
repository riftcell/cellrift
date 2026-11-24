package diameter

import (
	"fmt"

	"github.com/riftcell/cellrift/internal/protocols/common/malform"
)

// This file holds deliberate, post-encode corruption of already-correct
// Diameter wire bytes. It never re-derives what EncodeAVP/BuildMessage
// already do correctly, and those two functions must never grow a "corrupt
// mode" branch of their own — every function below takes the bytes the real
// encoder already produced and returns a same-length copy with exactly one
// field changed. This keeps the encoder itself the single source of correct
// wire format, and makes "only the targeted field diverges" provable by a
// plain byte diff between the real encoder's output and this file's output.

// malformMaxLength is the largest value a 3-byte big-endian length field
// (AVP Length or Message Length) can hold, per RFC 6733 §4.1/§3.
const malformMaxLength = 0xFFFFFF

// threeByteLengthError is the shared error constructor for both length
// fields this file overrides — AVP Length (avp.go's EncodeAVP, buf[5:8])
// and Message Length (message.go's BuildMessage, buf[1:4]) are each exactly
// 3 bytes wide, so both range checks report the identical bound through one
// message shape instead of two copy-pasted error strings.
func threeByteLengthError(field string, v int) error {
	return fmt.Errorf("%s override %d is out of range: the %s field is 3 bytes wide (0..16777215)", field, v, field)
}

// findAVPOffset walks the AVP sequence in raw starting at HeaderLength,
// using DecodeAVP's own header-length/padding math (the same walk primitive
// avp.go already implements) to advance past each AVP, and returns the byte
// offset of the first AVP whose Code equals code. The walk runs against the
// UNCORRUPTED input, which is why it is safe to trust each AVP's own length
// field to advance the offset: this function must only ever be called
// before any override has been applied to raw.
func findAVPOffset(raw []byte, code uint32) (int, error) {
	off := HeaderLength
	for off < len(raw) {
		avp, consumed, err := DecodeAVP(raw[off:])
		if err != nil {
			return 0, fmt.Errorf("walk AVPs at offset %d: %w", off, err)
		}
		if avp.Code == code {
			return off, nil
		}
		off += consumed
	}
	return 0, fmt.Errorf("AVP %s (code %d) is not present in this message", AVPCodeName(code), code)
}

// OverrideAVPLength takes raw, an already-correctly-encoded Diameter message
// (e.g. BuildRequest's output), and returns a same-length copy in which only
// the target AVP's 3-byte Length field (the exact bytes EncodeAVP writes at
// offset+5..offset+7) has been replaced with badLength. The AVP's Code,
// Flags, VendorID, Data, and padding are left untouched, as is every other
// AVP in the message and the message's own Message Length header.
func OverrideAVPLength(raw []byte, code uint32, badLength int) ([]byte, error) {
	if badLength < 0 || badLength > malformMaxLength {
		return nil, threeByteLengthError("avp_length", badLength)
	}
	if len(raw) < HeaderLength {
		return nil, fmt.Errorf("diameter message too short to malform: need %d bytes, got %d", HeaderLength, len(raw))
	}

	off, err := findAVPOffset(raw, code)
	if err != nil {
		return nil, err
	}

	out := append([]byte(nil), raw...)
	out[off+5] = byte(badLength >> 16)
	out[off+6] = byte(badLength >> 8)
	out[off+7] = byte(badLength)
	return out, nil
}

// OverrideMessageLength takes raw, an already-correctly-encoded Diameter
// message, and returns a same-length copy in which only the 3-byte Message
// Length header field (the exact bytes BuildMessage writes at raw[1:4]) has
// been replaced with badLength. These are the identical 3 bytes session.go's
// messageLength and testing.go's extractMessage both read to decide whether
// a complete message is buffered yet — a test proving this override must
// therefore assert against a mock's pre-framing raw-read capture, not its
// framed request history, since a bad length can stop the mock's own reader
// from ever framing the message at all.
func OverrideMessageLength(raw []byte, badLength int) ([]byte, error) {
	if badLength < 0 || badLength > malformMaxLength {
		return nil, threeByteLengthError("msg_length", badLength)
	}
	if len(raw) < HeaderLength {
		return nil, fmt.Errorf("diameter message too short to malform: need %d bytes, got %d", HeaderLength, len(raw))
	}

	out := append([]byte(nil), raw...)
	out[1] = byte(badLength >> 16)
	out[2] = byte(badLength >> 8)
	out[3] = byte(badLength)
	return out, nil
}

// applyMalform reads a step's declared malform: overrides back out of
// config (the shape internal/engine/executor.go's foldMalformConfig writes
// from a step's malform: block) and applies them, in order, to raw —
// BuildRequest's already-correct output. Returns raw unchanged with a nil
// error when config declares neither override.
//
// Order is load-bearing: the AVP-length override runs FIRST, while raw's
// own Message Length header is still correct, because OverrideAVPLength's
// findAVPOffset walks the AVP sequence starting at HeaderLength using each
// AVP's own already-correct length field to advance past it — a walk that
// has nothing to do with the Message Length header, but running it after a
// Message Length corruption would still mean walking a message whose own
// framing already lies about its extent. The two overrides are otherwise
// independent and may both be declared on one step: unlike impair:'s
// boolean faults, there is no mutual exclusion here, because they corrupt
// two different, non-overlapping fields.
func applyMalform(raw []byte, config map[string]string) ([]byte, error) {
	var err error

	if name, value, ok := malform.AVPLengthOverride(config); ok {
		def, found := LookupAVP(name)
		if !found {
			return nil, fmt.Errorf("avp_length names unknown AVP %q", name)
		}
		if raw, err = OverrideAVPLength(raw, def.Code, value); err != nil {
			return nil, err
		}
	}

	if value, ok := malform.MessageLengthOverride(config); ok {
		if raw, err = OverrideMessageLength(raw, value); err != nil {
			return nil, err
		}
	}

	return raw, nil
}
