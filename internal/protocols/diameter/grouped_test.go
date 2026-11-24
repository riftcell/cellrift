package diameter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is package diameter (not diameter_test) because it exercises
// GroupDottedFields, newScopedGroupedAVP, encodeGroupedField and
// buildAVPsFromFields directly — all unexported.

func TestGroupDottedFields_SplitsFlatAndGrouped(t *testing.T) {
	flat, grouped := GroupDottedFields(map[string]string{
		"Origin-Host":                                  "h",
		"SIP-Auth-Data-Item.SIP-Item-Number":           "1",
		"SIP-Auth-Data-Item.SIP-Authentication-Scheme": "Digest-AKAv1-MD5",
	})

	assert.Equal(t, map[string]string{"Origin-Host": "h"}, flat)
	assert.Equal(t, DottedFieldGroups{
		"SIP-Auth-Data-Item": {
			"SIP-Item-Number":           "1",
			"SIP-Authentication-Scheme": "Digest-AKAv1-MD5",
		},
	}, grouped)
}

func TestGroupDottedFields_ReservedKeysStayFlat(t *testing.T) {
	flat, grouped := GroupDottedFields(map[string]string{
		"__body_content.type": "application/sdp",
	})

	assert.Equal(t, map[string]string{"__body_content.type": "application/sdp"}, flat)
	assert.Empty(t, grouped)
}

func TestBuildAVPs_EncodesGroupedSIPAuthDataItem(t *testing.T) {
	avps, err := buildAVPsFromFields(map[string]string{
		"SIP-Auth-Data-Item.SIP-Item-Number":           "1",
		"SIP-Auth-Data-Item.SIP-Authentication-Scheme": "Digest-AKAv1-MD5",
	})
	require.NoError(t, err)
	require.Len(t, avps, 1)

	grouped := avps[0]
	assert.Equal(t, AVPCodeSIPAuthDataItem, grouped.Code)
	assert.Equal(t, byte(0xC0), grouped.Flags, "must be vendor-scoped (V+M)")
	assert.Equal(t, VendorID3GPP, grouped.VendorID)

	encoded, err := EncodeAVP(grouped.Code, grouped.Flags, grouped.VendorID, grouped.GroupedAVPs)
	require.NoError(t, err)

	decoded, _, err := DecodeAVP(encoded)
	require.NoError(t, err)
	assert.Equal(t, AVPCodeSIPAuthDataItem, decoded.Code)
	assert.Equal(t, byte(0xC0), decoded.Flags)
	assert.Equal(t, uint32(10415), decoded.VendorID)

	require.NoError(t, DecodeGroupedAVP(decoded))
	require.Len(t, decoded.GroupedAVPs, 2)

	codes := map[uint32]bool{}
	for _, sub := range decoded.GroupedAVPs {
		codes[sub.Code] = true
	}
	assert.True(t, codes[613], "expected SIP-Item-Number (613) among sub-AVPs")
	assert.True(t, codes[608], "expected SIP-Authentication-Scheme (608) among sub-AVPs")
}

func TestBuildAVPs_EncodesGroupedRequestedEUTRANAuthInfo(t *testing.T) {
	avps, err := buildAVPsFromFields(map[string]string{
		"Requested-EUTRAN-Authentication-Info.Number-Of-Requested-Vectors": "1",
	})
	require.NoError(t, err)
	require.Len(t, avps, 1)

	grouped := avps[0]
	assert.Equal(t, uint32(1408), grouped.Code)
	assert.Equal(t, byte(0xC0), grouped.Flags)
	assert.Equal(t, VendorID3GPP, grouped.VendorID)
	require.Len(t, grouped.GroupedAVPs, 1)
	assert.Equal(t, uint32(1410), grouped.GroupedAVPs[0].Code)
	assert.Equal(t, uint32(1), grouped.GroupedAVPs[0].Uint32Val)
}

func TestBuildAVPs_UnknownGroupedPrefixErrors(t *testing.T) {
	_, err := buildAVPsFromFields(map[string]string{"Not-An-AVP.X": "1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown Diameter AVP "Not-An-AVP"`)
}

func TestBuildAVPs_NonGroupedPrefixErrors(t *testing.T) {
	_, err := buildAVPsFromFields(map[string]string{"Origin-Host.X": "1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Origin-Host")
	assert.Contains(t, err.Error(), "not a Grouped AVP")
}

func TestBuildAVPs_UnknownGroupedMemberErrors(t *testing.T) {
	_, err := buildAVPsFromFields(map[string]string{"SIP-Auth-Data-Item.Nope": "1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SIP-Auth-Data-Item")
	assert.Contains(t, err.Error(), "Nope")
}
