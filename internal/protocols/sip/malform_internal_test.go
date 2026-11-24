// malform_internal_test.go covers applyHeaderMalform, headerOps, and
// serialiseWithMalform -- the config-reading dispatch layer this file adds
// over the pure RemoveHeader/DuplicateHeader/OverwriteHeader
// functions. package sip (internal) because all three are unexported,
// mirroring this package's existing internal/external test split
// (adapter_internal_test.go vs adapter_test.go).
package sip

import (
	"strings"
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dispatchFixtureHeaders is this file's own header set, independent of
// malform_test.go's fixtureHeaders (that file lives in package sip_test and
// is not visible from here).
func dispatchFixtureHeaders() map[string]string {
	return map[string]string{
		HeaderVia:     "SIP/2.0/UDP 192.0.2.1:5060;branch=z9hG4bK776asdhds",
		HeaderFrom:    "<sip:alice@ims.local>;tag=1928301774",
		HeaderTo:      "<sip:bob@ims.local>",
		HeaderCallID:  "a84b4c76e66710@192.0.2.1",
		HeaderCSeq:    "1 REGISTER",
		HeaderContact: "<sip:alice@192.0.2.1:5060>",
	}
}

func dispatchFixtureMessage() *SIPMessage {
	return BuildRequest("REGISTER", "sip:ims.local", dispatchFixtureHeaders())
}

// countDispatchHeaderLines counts CRLF-delimited lines in raw beginning with
// "name:" (case-insensitive) -- a package-local twin of malform_test.go's
// countHeaderLines, needed here because that one lives in package sip_test.
func countDispatchHeaderLines(raw []byte, name string) int {
	prefix := strings.ToLower(name) + ":"
	count := 0
	for _, line := range strings.Split(string(raw), crlf) {
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			count++
		}
	}
	return count
}

func TestApplyHeaderMalform_EmptyConfigReturnsInputUnchanged(t *testing.T) {
	raw := Serialise(dispatchFixtureMessage())
	out, err := applyHeaderMalform(raw, map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, raw, out)
}

func TestApplyHeaderMalform_Missing_RemovesHeader(t *testing.T) {
	raw := Serialise(dispatchFixtureMessage())
	out, err := applyHeaderMalform(raw, map[string]string{
		malform.SIPHeaderNameKey: "Via",
		malform.SIPHeaderOpKey:   malform.OpMissing,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, countDispatchHeaderLines(out, "Via"))
}

func TestApplyHeaderMalform_Duplicate_AddsSecondLine(t *testing.T) {
	raw := Serialise(dispatchFixtureMessage())
	out, err := applyHeaderMalform(raw, map[string]string{
		malform.SIPHeaderNameKey: "Via",
		malform.SIPHeaderOpKey:   malform.OpDuplicate,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, countDispatchHeaderLines(out, "Via"))
}

func TestApplyHeaderMalform_Overwrite_ReplacesValue(t *testing.T) {
	raw := Serialise(dispatchFixtureMessage())
	out, err := applyHeaderMalform(raw, map[string]string{
		malform.SIPHeaderNameKey:  "Via",
		malform.SIPHeaderOpKey:    malform.OpOverwrite,
		malform.SIPHeaderValueKey: "SIP/2.0/TCP not-a-real-via",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, countDispatchHeaderLines(out, "Via"))
	assert.Contains(t, string(out), "Via: SIP/2.0/TCP not-a-real-via"+crlf)
}

func TestApplyHeaderMalform_UnknownOp_NamesOpAndListsAllowed(t *testing.T) {
	raw := Serialise(dispatchFixtureMessage())
	_, err := applyHeaderMalform(raw, map[string]string{
		malform.SIPHeaderNameKey: "Via",
		malform.SIPHeaderOpKey:   "bogus",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"bogus"`)
	assert.Contains(t, err.Error(), malform.OpMissing)
	assert.Contains(t, err.Error(), malform.OpDuplicate)
	assert.Contains(t, err.Error(), malform.OpOverwrite)
}

func TestSerialiseWithMalform_EmptyConfigMatchesSerialise(t *testing.T) {
	msg := dispatchFixtureMessage()
	want := Serialise(msg)
	got, err := serialiseWithMalform(msg, map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestSerialiseWithMalform_UnknownOpReturnsError(t *testing.T) {
	msg := dispatchFixtureMessage()
	_, err := serialiseWithMalform(msg, map[string]string{
		malform.SIPHeaderNameKey: "Via",
		malform.SIPHeaderOpKey:   "bogus",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}
