package diameter_test

import (
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// diffPositions returns the indices where two equal-length byte buffers
// differ. It is the central assertion primitive for this file's tests: a
// correct malform override must diverge from the original at exactly the
// byte positions the target field occupies, and nowhere else.
func diffPositions(a, b []byte) []int {
	var positions []int
	for i := range a {
		if a[i] != b[i] {
			positions = append(positions, i)
		}
	}
	return positions
}

// buildFixtureMessage builds a real, correctly-encoded Diameter request with
// Session-Id, a vendor-scoped Visited-PLMN-Id, and Origin-Host (in that
// order, so Session-Id is neither first nor last and Origin-Host is last),
// entirely through the production encoder — never a hand-built byte literal.
func buildFixtureMessage(t *testing.T) []byte {
	t.Helper()

	plmnDef, ok := diameter.LookupAVP("Visited-PLMN-Id")
	require.True(t, ok, "dictionary must define Visited-PLMN-Id")

	avps := []diameter.AVP{
		diameter.NewStringAVP(diameter.AVPCodeSessionID, "session;1;2;3"),
		diameter.NewVendorStringAVP(plmnDef.Code, plmnDef.VendorID, "\x02\xf8\x10"),
		diameter.NewStringAVP(diameter.AVPCodeOriginHost, "host.example.com"),
	}

	raw, _, err := diameter.BuildRequest(diameter.CmdUserAuthorization, diameter.AppIDCx, avps)
	require.NoError(t, err)
	return raw
}

func decode3ByteLength(b []byte) int {
	return int(b[0])<<16 | int(b[1])<<8 | int(b[2])
}

// complementOf3ByteLength returns the bitwise complement, within the 24-bit
// range every AVP Length/Message Length field occupies, of the 3-byte value
// at b. XORing with the all-ones 24-bit mask flips every bit of every byte,
// so the result is guaranteed to differ from the original in ALL 3 byte
// positions — unlike a fixed literal such as 9999, whose high byte happens
// to equal a small fixture message's own high byte (0), which would make a
// byte-diff assertion fail by coincidence rather than by a real bug.
func complementOf3ByteLength(b []byte) int {
	return decode3ByteLength(b) ^ 0xFFFFFF
}

func TestMalform_AVPLength(t *testing.T) {
	t.Run("changes exactly the 3 length bytes of the targeted AVP", func(t *testing.T) {
		original := buildFixtureMessage(t)
		originalCopy := append([]byte(nil), original...)

		off, findErr := findFixtureAVPOffset(t, original, diameter.AVPCodeSessionID)
		require.NoError(t, findErr)
		bad := complementOf3ByteLength(original[off+5 : off+8])

		out, err := diameter.OverrideAVPLength(original, diameter.AVPCodeSessionID, bad)
		require.NoError(t, err)
		require.Equal(t, len(original), len(out))

		positions := diffPositions(original, out)
		assert.Equal(t, []int{off + 5, off + 6, off + 7}, positions)
		assert.Equal(t, bad, decode3ByteLength(out[off+5:off+8]))

		assert.Equal(t, originalCopy, original, "input slice must not be mutated")
	})

	t.Run("other AVPs still decode cleanly after the override", func(t *testing.T) {
		original := buildFixtureMessage(t)
		out, err := diameter.OverrideAVPLength(original, diameter.AVPCodeSessionID, 9999)
		require.NoError(t, err)

		plmnDef, _ := diameter.LookupAVP("Visited-PLMN-Id")
		plmnOff, err := findFixtureAVPOffset(t, original, plmnDef.Code)
		require.NoError(t, err)
		plmnAVP, _, err := diameter.DecodeAVP(out[plmnOff:])
		require.NoError(t, err)
		assert.Equal(t, plmnDef.Code, plmnAVP.Code)
		assert.Equal(t, plmnDef.VendorID, plmnAVP.VendorID)

		hostOff, err := findFixtureAVPOffset(t, original, diameter.AVPCodeOriginHost)
		require.NoError(t, err)
		hostAVP, _, err := diameter.DecodeAVP(out[hostOff:])
		require.NoError(t, err)
		assert.Equal(t, "host.example.com", hostAVP.StringVal)
	})

	t.Run("targeting the last AVP works", func(t *testing.T) {
		original := buildFixtureMessage(t)
		off, err := findFixtureAVPOffset(t, original, diameter.AVPCodeOriginHost)
		require.NoError(t, err)
		bad := complementOf3ByteLength(original[off+5 : off+8])

		out, err := diameter.OverrideAVPLength(original, diameter.AVPCodeOriginHost, bad)
		require.NoError(t, err)
		assert.Equal(t, []int{off + 5, off + 6, off + 7}, diffPositions(original, out))
	})

	t.Run("vendor-specific AVP: VendorID bytes untouched", func(t *testing.T) {
		original := buildFixtureMessage(t)
		plmnDef, _ := diameter.LookupAVP("Visited-PLMN-Id")

		off, err := findFixtureAVPOffset(t, original, plmnDef.Code)
		require.NoError(t, err)
		bad := complementOf3ByteLength(original[off+5 : off+8])

		out, err := diameter.OverrideAVPLength(original, plmnDef.Code, bad)
		require.NoError(t, err)
		assert.Equal(t, []int{off + 5, off + 6, off + 7}, diffPositions(original, out))
		assert.Equal(t, original[off+8:off+12], out[off+8:off+12], "VendorID bytes must be untouched")
	})

	t.Run("badLength 0 succeeds", func(t *testing.T) {
		original := buildFixtureMessage(t)
		out, err := diameter.OverrideAVPLength(original, diameter.AVPCodeSessionID, 0)
		require.NoError(t, err)
		off, _ := findFixtureAVPOffset(t, original, diameter.AVPCodeSessionID)
		assert.Equal(t, 0, decode3ByteLength(out[off+5:off+8]))
	})

	t.Run("out-of-range badLength is a named error", func(t *testing.T) {
		original := buildFixtureMessage(t)

		_, err := diameter.OverrideAVPLength(original, diameter.AVPCodeSessionID, -1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "0..16777215")

		_, err = diameter.OverrideAVPLength(original, diameter.AVPCodeSessionID, 16777216)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "0..16777215")
	})

	t.Run("missing code is a named error", func(t *testing.T) {
		original := buildFixtureMessage(t)
		_, err := diameter.OverrideAVPLength(original, diameter.AVPCodeResultCode, 9999)
		require.Error(t, err)
		assert.Contains(t, err.Error(), diameter.AVPCodeName(diameter.AVPCodeResultCode))
		assert.Contains(t, err.Error(), "not present")
	})

	t.Run("too-short buffer is a named error", func(t *testing.T) {
		_, err := diameter.OverrideAVPLength([]byte{1, 2, 3}, diameter.AVPCodeSessionID, 9999)
		require.Error(t, err)
	})
}

func TestMalform_MessageLength(t *testing.T) {
	t.Run("changes exactly bytes 1, 2, 3", func(t *testing.T) {
		original := buildFixtureMessage(t)
		originalCopy := append([]byte(nil), original...)
		bad := complementOf3ByteLength(original[1:4])

		out, err := diameter.OverrideMessageLength(original, bad)
		require.NoError(t, err)
		require.Equal(t, len(original), len(out))

		assert.Equal(t, []int{1, 2, 3}, diffPositions(original, out))
		assert.Equal(t, bad, decode3ByteLength(out[1:4]))
		assert.Equal(t, originalCopy, original, "input slice must not be mutated")
	})

	t.Run("out-of-range badLength is a named error", func(t *testing.T) {
		original := buildFixtureMessage(t)

		_, err := diameter.OverrideMessageLength(original, -1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "0..16777215")

		_, err = diameter.OverrideMessageLength(original, 16777216)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "0..16777215")
	})

	t.Run("too-short buffer is a named error", func(t *testing.T) {
		_, err := diameter.OverrideMessageLength([]byte{1, 2, 3}, 9999)
		require.Error(t, err)
	})
}

func TestMalform_BothOverridesTogether(t *testing.T) {
	original := buildFixtureMessage(t)
	off, err := findFixtureAVPOffset(t, original, diameter.AVPCodeSessionID)
	require.NoError(t, err)
	badAVP := complementOf3ByteLength(original[off+5 : off+8])
	badMsg := complementOf3ByteLength(original[1:4])

	withAVP, err := diameter.OverrideAVPLength(original, diameter.AVPCodeSessionID, badAVP)
	require.NoError(t, err)
	withBoth, err := diameter.OverrideMessageLength(withAVP, badMsg)
	require.NoError(t, err)

	assert.Len(t, diffPositions(original, withBoth), 6)
}

// findFixtureAVPOffset walks an uncorrupted, correctly-encoded message
// (mirroring malform.go's own findAVPOffset, but re-derived here so the test
// file does not depend on that unexported helper) to locate the byte offset
// of the AVP with the given code, for asserting exactly which bytes an
// override touched.
func findFixtureAVPOffset(t *testing.T, raw []byte, code uint32) (int, error) {
	t.Helper()
	off := diameter.HeaderLength
	for off < len(raw) {
		avp, consumed, err := diameter.DecodeAVP(raw[off:])
		if err != nil {
			return 0, err
		}
		if avp.Code == code {
			return off, nil
		}
		off += consumed
	}
	t.Fatalf("AVP code %d not found in fixture", code)
	return 0, nil
}
