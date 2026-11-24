package diameter

import (
	"strconv"
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise applyMalform directly and therefore live in the
// internal (package diameter) test binary rather than
// malform_adapter_test.go, which is package diameter_test and asserts wire
// bytes reaching a real mock peer — a different concern (see that file's own
// doc comment). This file cannot reuse malform_test.go's fixture helpers
// (buildFixtureMessage, diffPositions, ...) since those live in the
// external diameter_test package and are not visible here; it builds its
// own copies against the same production encoder instead.

// applyMalformFixture builds a real, correctly-encoded UAR through the
// production encoder, mirroring malform_test.go's buildFixtureMessage: a
// Session-Id AVP first, an Origin-Host AVP last, so neither override target
// in this file's tests is the message's only AVP.
func applyMalformFixture(t *testing.T) []byte {
	t.Helper()
	avps := []AVP{
		NewStringAVP(AVPCodeSessionID, "session;1;2;3"),
		NewStringAVP(AVPCodeOriginHost, "host.example.com"),
	}
	raw, _, err := BuildRequest(CmdUserAuthorization, AppIDCx, avps)
	require.NoError(t, err)
	return raw
}

// applyMalformDiffPositions mirrors malform_test.go's diffPositions.
func applyMalformDiffPositions(a, b []byte) []int {
	var positions []int
	for i := range a {
		if a[i] != b[i] {
			positions = append(positions, i)
		}
	}
	return positions
}

// applyMalformDecode3ByteLength mirrors malform_test.go's decode3ByteLength.
func applyMalformDecode3ByteLength(b []byte) int {
	return int(b[0])<<16 | int(b[1])<<8 | int(b[2])
}

// applyMalformComplement mirrors malform_test.go's complementOf3ByteLength:
// XORing the all-ones 24-bit mask guarantees the result differs from the
// original in all 3 byte positions, unlike a fixed small literal.
func applyMalformComplement(b []byte) int {
	return applyMalformDecode3ByteLength(b) ^ 0xFFFFFF
}

// applyMalformFindOffset walks an uncorrupted, correctly-encoded message to
// locate the byte offset of the AVP with the given code, using the same
// production walk primitives findAVPOffset itself uses (this file has
// direct access to them, being package diameter).
func applyMalformFindOffset(t *testing.T, raw []byte, code uint32) int {
	t.Helper()
	off, err := findAVPOffset(raw, code)
	require.NoError(t, err)
	return off
}

func TestApplyMalform_EmptyConfigReturnsInputUnchanged(t *testing.T) {
	original := applyMalformFixture(t)
	originalCopy := append([]byte(nil), original...)

	out, err := applyMalform(original, map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, originalCopy, out)
}

func TestApplyMalform_AVPLengthOnly(t *testing.T) {
	original := applyMalformFixture(t)
	off := applyMalformFindOffset(t, original, AVPCodeSessionID)
	bad := applyMalformComplement(original[off+5 : off+8])

	config := map[string]string{
		malform.AVPLengthNameKey:  "Session-Id",
		malform.AVPLengthValueKey: strconv.Itoa(bad),
	}

	out, err := applyMalform(original, config)
	require.NoError(t, err)
	assert.Equal(t, []int{off + 5, off + 6, off + 7}, applyMalformDiffPositions(original, out))
}

func TestApplyMalform_MsgLengthOnly(t *testing.T) {
	original := applyMalformFixture(t)
	bad := applyMalformComplement(original[1:4])

	config := map[string]string{
		malform.MsgLengthKey: strconv.Itoa(bad),
	}

	out, err := applyMalform(original, config)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3}, applyMalformDiffPositions(original, out))
}

func TestApplyMalform_BothOverrides(t *testing.T) {
	original := applyMalformFixture(t)
	off := applyMalformFindOffset(t, original, AVPCodeSessionID)
	badAVP := applyMalformComplement(original[off+5 : off+8])
	badMsg := applyMalformComplement(original[1:4])

	config := map[string]string{
		malform.AVPLengthNameKey:  "Session-Id",
		malform.AVPLengthValueKey: strconv.Itoa(badAVP),
		malform.MsgLengthKey:      strconv.Itoa(badMsg),
	}

	out, err := applyMalform(original, config)
	require.NoError(t, err)
	assert.Len(t, applyMalformDiffPositions(original, out), 6)
}

func TestApplyMalform_UnknownDictionaryNameFailsLoudly(t *testing.T) {
	original := applyMalformFixture(t)

	config := map[string]string{
		malform.AVPLengthNameKey:  "Not-A-Real-AVP",
		malform.AVPLengthValueKey: "9999",
	}

	_, err := applyMalform(original, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Not-A-Real-AVP")
}

func TestApplyMalform_DictionaryKnownAVPAbsentFromMessageFailsLoudly(t *testing.T) {
	original := applyMalformFixture(t)

	config := map[string]string{
		malform.AVPLengthNameKey:  "Result-Code",
		malform.AVPLengthValueKey: "9999",
	}

	_, err := applyMalform(original, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not present")
}

// TestApplyMalform_AVPLengthRunsBeforeMsgLength proves the order documented
// on applyMalform: with both overrides declared, the AVP walk that resolves
// avp_length must still succeed and produce the correct offset even though
// msg_length's declared value is set far smaller than the real message
// length — the AVP walk runs against the message before the Message Length
// header has been touched, and both fields' final values must reflect
// exactly what was declared, independently of each other.
func TestApplyMalform_AVPLengthRunsBeforeMsgLength(t *testing.T) {
	original := applyMalformFixture(t)
	off := applyMalformFindOffset(t, original, AVPCodeOriginHost)
	badAVP := applyMalformComplement(original[off+5 : off+8])
	badMsg := 1 // far smaller than the real message length

	config := map[string]string{
		malform.AVPLengthNameKey:  "Origin-Host",
		malform.AVPLengthValueKey: strconv.Itoa(badAVP),
		malform.MsgLengthKey:      strconv.Itoa(badMsg),
	}

	out, err := applyMalform(original, config)
	require.NoError(t, err)
	assert.Equal(t, badAVP, applyMalformDecode3ByteLength(out[off+5:off+8]))
	assert.Equal(t, badMsg, applyMalformDecode3ByteLength(out[1:4]))
}
