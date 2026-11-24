package diameter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise resolveCommand and commandRegistry directly and
// therefore live in the internal (package diameter) test binary rather than
// registry_test.go, which is package diameter_test and has no access to
// either unexported identifier — the same split adapter_internal_test.go
// already uses for buildAVPsFromFields.

func TestCommandRegistry_ResolvesEveryType(t *testing.T) {
	cases := []struct {
		msgType string
		code    uint32
		appID   uint32
	}{
		{"UAR", CmdUserAuthorization, AppIDCx},
		{"CCR", CmdCreditControl, AppIDCreditControl},
		{"AIR", CmdAuthenticationInformation, AppIDS6a},
		{"ULR", CmdUpdateLocation, AppIDS6a},
		{"MAR", CmdMultimediaAuth, AppIDCx},
		{"SAR", CmdServerAssignment, AppIDCx},
	}

	for _, tc := range cases {
		t.Run(tc.msgType, func(t *testing.T) {
			def, err := resolveCommand(tc.msgType)
			require.NoError(t, err)
			assert.Equal(t, tc.code, def.Code)
			assert.Equal(t, tc.appID, def.AppID)
		})
	}
}

func TestCommandRegistry_IsCaseInsensitive(t *testing.T) {
	want, err := resolveCommand("UAR")
	require.NoError(t, err)

	for _, spelling := range []string{"uar", "Uar", "UAR"} {
		got, err := resolveCommand(spelling)
		require.NoError(t, err)
		assert.Equal(t, want, got, "spelling %q should resolve identically", spelling)
	}
}

func TestCommandRegistry_UnknownTypeErrors(t *testing.T) {
	_, err := resolveCommand("XYZ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown diameter message type")
}

// TestAppendVendorSpecificApplicationID_CoversEveryRegisteredCommand proves
// the fix applies uniformly to every command in commandRegistry, not just
// UAR: the four Cx/S6a-scoped commands (UAR/MAR/SAR under Cx, AIR/ULR under
// S6a) each get exactly one Vendor-Specific-Application-Id (AVP 260) with
// the correct {Vendor-Id, Auth-Application-Id} pair, and Credit-Control
// (CCR, a bare application with no such ABNF field) gets nothing added —
// closing the question of whether this is one shared fix or six separate
// ones without needing a live mock round-trip per command.
func TestAppendVendorSpecificApplicationID_CoversEveryRegisteredCommand(t *testing.T) {
	cases := []struct {
		msgType   string
		wantWrap  bool
		wantAppID uint32
	}{
		{"UAR", true, AppIDCx},
		{"MAR", true, AppIDCx},
		{"SAR", true, AppIDCx},
		{"AIR", true, AppIDS6a},
		{"ULR", true, AppIDS6a},
		{"CCR", false, 0},
	}

	for _, tc := range cases {
		t.Run(tc.msgType, func(t *testing.T) {
			def, err := resolveCommand(tc.msgType)
			require.NoError(t, err)

			got := appendVendorSpecificApplicationID(nil, def.AppID)

			if !tc.wantWrap {
				assert.Empty(t, got, "%s must not get a Vendor-Specific-Application-Id", tc.msgType)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, AVPCodeVendorSpecificApplicationID, got[0].Code)
			sub := got[0].GroupedAVPs
			require.Len(t, sub, 2)
			assert.Equal(t, VendorID3GPP, sub[0].Uint32Val)
			assert.Equal(t, tc.wantAppID, sub[1].Uint32Val)
		})
	}
}

// TestCommandRegistry_SelfConsistent mirrors the dictionary's
// self-consistency gate: no two registry entries should collide on the wire
// (same command code), and every entry's Application-Id must be one of the
// three applications this package actually knows how to advertise/handle.
func TestCommandRegistry_SelfConsistent(t *testing.T) {
	knownAppIDs := map[uint32]bool{
		AppIDCx:            true,
		AppIDCreditControl: true,
		AppIDS6a:           true,
	}

	seenCodes := make(map[uint32]string)
	for msgType, def := range commandRegistry {
		assert.True(t, knownAppIDs[def.AppID], "%s has unrecognized AppID %d", msgType, def.AppID)

		if prior, ok := seenCodes[def.Code]; ok {
			t.Errorf("command code %d shared by %s and %s", def.Code, prior, msgType)
		}
		seenCodes[def.Code] = msgType
	}
}
