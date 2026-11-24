package diameter

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wireAVPs round-trips avps through EncodeAVP/DecodeAVPs so the resulting
// AVPs match what a real decode produces: a Vendor-Specific-Application-Id
// arrives as DataTypeOctetString with raw bytes in Data, GroupedAVPs is NOT
// pre-populated. Constructing a Message directly from NewGroupedAVP would
// skip that and hide bugs in the decode path advertisedApplications relies
// on.
func wireAVPs(t *testing.T, avps []AVP) []AVP {
	t.Helper()
	var raw []byte
	for _, a := range avps {
		encoded, err := EncodeAVP(a.Code, a.Flags, a.VendorID, avpValue(a))
		require.NoError(t, err)
		raw = append(raw, encoded...)
	}
	decoded, err := DecodeAVPs(raw)
	require.NoError(t, err)
	return decoded
}

func vendorSpecificAVP(vendorID, appID uint32) AVP {
	return NewGroupedAVP(AVPCodeVendorSpecificApplicationID, []AVP{
		NewUint32AVP(AVPCodeVendorID, vendorID),
		NewUint32AVP(AVPCodeAuthApplicationID, appID),
	})
}

func TestAdvertisedApplications_BareAuthApplicationID(t *testing.T) {
	msg := &Message{AVPs: wireAVPs(t, []AVP{
		NewUint32AVP(AVPCodeAuthApplicationID, AppIDCreditControl),
	})}

	assert.Equal(t, []uint32{AppIDCreditControl}, advertisedApplications(msg))
}

func TestAdvertisedApplications_VendorSpecific(t *testing.T) {
	msg := &Message{AVPs: wireAVPs(t, []AVP{
		vendorSpecificAVP(VendorID3GPP, AppIDCx),
	})}

	assert.Equal(t, []uint32{AppIDCx}, advertisedApplications(msg))
}

func TestAdvertisedApplications_Both(t *testing.T) {
	msg := &Message{AVPs: wireAVPs(t, []AVP{
		NewUint32AVP(AVPCodeAuthApplicationID, AppIDCreditControl),
		vendorSpecificAVP(VendorID3GPP, AppIDCx),
	})}

	assert.ElementsMatch(t, []uint32{AppIDCreditControl, AppIDCx}, advertisedApplications(msg))
}

func TestAdvertisedApplications_IgnoresUnrelatedAVPs(t *testing.T) {
	msg := &Message{AVPs: wireAVPs(t, []AVP{
		NewStringAVP(AVPCodeOriginHost, "cellrift.test.local"),
		NewStringAVP(AVPCodeSessionID, "cellrift;1;2"),
		NewUint32AVP(AVPCodeAuthApplicationID, AppIDCreditControl),
	})}

	assert.Equal(t, []uint32{AppIDCreditControl}, advertisedApplications(msg))
}

func TestHasCommonApplication_TrueOnIntersection(t *testing.T) {
	msg := &Message{AVPs: wireAVPs(t, []AVP{
		vendorSpecificAVP(VendorID3GPP, AppIDCx),
	})}

	assert.True(t, hasCommonApplication(msg, []uint32{AppIDCx, AppIDS6a}))
}

func TestHasCommonApplication_FalseOnDisjoint(t *testing.T) {
	msg := &Message{AVPs: wireAVPs(t, []AVP{
		NewUint32AVP(AVPCodeAuthApplicationID, AppIDCreditControl),
	})}

	assert.False(t, hasCommonApplication(msg, []uint32{AppIDCx, AppIDS6a}))
}

func TestHasCommonApplication_FalseOnEmptySupported(t *testing.T) {
	msg := &Message{AVPs: wireAVPs(t, []AVP{
		vendorSpecificAVP(VendorID3GPP, AppIDCx),
	})}

	assert.False(t, hasCommonApplication(msg, nil))
}

func TestSupportedVendorApps_CxIsVendorScoped(t *testing.T) {
	// supportedVendorApps is the only place Cx is described as
	// vendor-scoped — the CER builder looks Cx up here rather than
	// hardcoding {VendorID3GPP, AppIDCx} at the call site.
	cx, ok := supportedVendorApps["Cx"]
	require.True(t, ok)
	assert.Equal(t, VendorSpecificApp{VendorID: VendorID3GPP, AppID: AppIDCx}, cx)
}

func TestSupportedVendorApps_S6aIsVendorScoped(t *testing.T) {
	// supportedVendorApps is the only place S6a is described as
	// vendor-scoped — buildCER's Vendor-Specific-Application-Id loop looks
	// S6a up here rather than hardcoding {VendorID3GPP, AppIDS6a} at the call
	// site, and AppIDS6a must never appear as a bare Auth-Application-Id.
	s6a, ok := supportedVendorApps["S6a"]
	require.True(t, ok)
	assert.Equal(t, VendorSpecificApp{VendorID: VendorID3GPP, AppID: AppIDS6a}, s6a)
}

func TestMockHSS_CEAAdvertisesS6a(t *testing.T) {
	// MockHSS's CEA must advertise S6a independently of supportedVendorApps:
	// executeSend's post-handshake application gate checks what the PEER's
	// CEA advertised, which this mock derives from defaultSupportedApps, not
	// from the CER it received.
	m := NewMockHSS(t)
	defer m.Close()

	cerRaw, _, err := buildCER("cellrift.test.example.com", "test.example.com", net.ParseIP("192.0.2.10"))
	require.NoError(t, err)
	cer, err := ParseMessage(cerRaw)
	require.NoError(t, err)

	ceaRaw, err := m.buildCEA(cer)
	require.NoError(t, err)
	cea, err := ParseMessage(ceaRaw)
	require.NoError(t, err)

	assert.Contains(t, advertisedApplications(cea), AppIDS6a)
}

func TestResultCodeName(t *testing.T) {
	assert.Equal(t, "DIAMETER_SUCCESS", resultCodeName(ResultSuccess))
	assert.Equal(t, "DIAMETER_NO_COMMON_APPLICATION", resultCodeName(ResultNoCommonApplication))
	assert.Equal(t, "DIAMETER_APPLICATION_UNSUPPORTED", resultCodeName(ResultApplicationUnsupported))
	assert.Equal(t, "DIAMETER_AUTHORIZATION_REJECTED", resultCodeName(ResultAuthorizationRejected))

	unknown := resultCodeName(9999)
	assert.NotEmpty(t, unknown)
	assert.NotPanics(t, func() { resultCodeName(9999) })
}
