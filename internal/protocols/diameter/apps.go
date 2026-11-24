package diameter

import "fmt"

// apps.go is the single source of truth for application advertisement on
// BOTH sides of the CER/CEA exchange: which Diameter applications Cellrift
// advertises, how they are encoded (bare Auth-Application-Id vs.
// vendor-wrapped Vendor-Specific-Application-Id), and how a peer's
// advertisement is checked for a common application. The mock CEA
// builder and the client's unsupported-application gate both call hasCommonApplication
// rather than re-deriving the intersection themselves, since hand-rolling it
// twice would let the two sides of the handshake silently disagree about
// what "common application" means.

// AppIDS6a is the 3GPP S6a/S6d Application-Id (TS 29.272).
const AppIDS6a uint32 = 16777251

// VendorID3GPP is the IANA SMI Enterprise (Vendor-Id) assigned to 3GPP.
const VendorID3GPP uint32 = 10415

// Result codes relevant to application advertisement — RFC 6733 §7.1.
// NOT 5001 (DIAMETER_AVP_UNSUPPORTED), which is a different failure mode.
const (
	ResultNoCommonApplication    uint32 = 5010
	ResultApplicationUnsupported uint32 = 3007
)

// VendorSpecificApp is the {Vendor-Id, Auth-Application-Id} pair a
// vendor-scoped application is advertised with. These — and ONLY these —
// are sent wrapped in Vendor-Specific-Application-Id (AVP 260). Sending a
// 3GPP application as a bare top-level Auth-Application-Id instead is a
// known interop failure: a 3GPP-profiled HSS matches vendor-scoped
// applications against AVP 260, not the bare Auth-Application-Id list.
// Credit-Control (AppIDCreditControl, RFC 4006) must never appear here — it
// is advertised as a bare Auth-Application-Id.
type VendorSpecificApp struct {
	VendorID uint32
	AppID    uint32
}

// vendorSpecificApps maps a 3GPP interface name to the VendorSpecificApp
// content its CER must advertise. Looked up by name when building the CER
// (buildCER), not derived from scenario config — ProtocolConfig has no
// applications field: the handshake fires automatically on the first
// Diameter step and no new scenario syntax is introduced for it, so
// advertised applications are compiled in here rather than made
// scenario-configurable.
type vendorSpecificApps map[string]VendorSpecificApp

// supportedVendorApps is the set of 3GPP interfaces Cellrift's CER
// advertises inside Vendor-Specific-Application-Id (AVP 260), each wrapped
// with Vendor-Id 10415: both Cx and S6a. Credit-Control (RFC 4006) stays a
// bare Auth-Application-Id and must never be added here — see the
// VendorSpecificApp doc comment above.
var supportedVendorApps = vendorSpecificApps{
	"Cx":  {VendorID: VendorID3GPP, AppID: AppIDCx},
	"S6a": {VendorID: VendorID3GPP, AppID: AppIDS6a},
}

// vendorSpecificAppFor reports whether appID is one of the vendor-scoped
// applications a command can be sent under, and if so, the {Vendor-Id,
// Auth-Application-Id} pair to wrap it in. Credit-Control's bare AppID
// correctly reports false here — supportedVendorApps never contains it (see
// its own doc comment above) — so a CCR is never given a
// Vendor-Specific-Application-Id it has no ABNF field for.
func vendorSpecificAppFor(appID uint32) (VendorSpecificApp, bool) {
	for _, vsa := range supportedVendorApps {
		if vsa.AppID == appID {
			return vsa, true
		}
	}
	return VendorSpecificApp{}, false
}

// advertisedApplications walks msg's top-level Auth-Application-Id AVPs
// (bare applications, e.g. Credit-Control) and its
// Vendor-Specific-Application-Id AVPs (vendor-scoped applications, e.g.
// Cx/S6a), returning every advertised Application-Id. Both AVP shapes are
// decoded by this one function so the client and the mock peer can never
// disagree about what was advertised.
func advertisedApplications(msg *Message) []uint32 {
	var apps []uint32
	for i := range msg.AVPs {
		a := &msg.AVPs[i]
		switch a.Code {
		case AVPCodeAuthApplicationID:
			apps = append(apps, a.Uint32Val)
		case AVPCodeVendorSpecificApplicationID:
			if appID, ok := vendorScopedAppID(a); ok {
				apps = append(apps, appID)
			}
		}
	}
	return apps
}

// vendorScopedAppID decodes a Vendor-Specific-Application-Id grouped AVP
// (mutating it in place — see Message.FindAVP's decode note) and returns
// its nested Auth-Application-Id, if present.
func vendorScopedAppID(a *AVP) (uint32, bool) {
	if err := DecodeGroupedAVP(a); err != nil {
		return 0, false
	}
	sub := FindAVP(a.GroupedAVPs, AVPCodeAuthApplicationID)
	if sub == nil {
		return 0, false
	}
	return sub.Uint32Val, true
}

// hasCommonApplication reports whether msg advertises at least one
// application in supported. This is the ONLY intersection check in the
// repo — the mock CEA builder and the client's unsupported-application
// gate both call it.
func hasCommonApplication(msg *Message, supported []uint32) bool {
	advertised := advertisedApplications(msg)
	for _, want := range supported {
		for _, have := range advertised {
			if want == have {
				return true
			}
		}
	}
	return false
}

// resultCodeNames maps a Diameter Result-Code AVP value to its RFC/3GPP
// name, used to make a rejection say what it means rather than printing a
// bare number.
type resultCodeNames map[uint32]string

var resultCodes = resultCodeNames{
	ResultSuccess:                "DIAMETER_SUCCESS",
	ResultNoCommonApplication:    "DIAMETER_NO_COMMON_APPLICATION",
	ResultApplicationUnsupported: "DIAMETER_APPLICATION_UNSUPPORTED",
	ResultAuthorizationRejected:  "DIAMETER_AUTHORIZATION_REJECTED",
	ResultMissingAVP:             "DIAMETER_MISSING_AVP",
}

// resultCodeName returns a human-readable name for a Diameter Result-Code.
// Unknown codes return a non-panicking fallback, matching AVPCodeName's
// AVP-<n> convention.
func resultCodeName(code uint32) string {
	if name, ok := resultCodes[code]; ok {
		return name
	}
	return fmt.Sprintf("RESULT-%d", code)
}
