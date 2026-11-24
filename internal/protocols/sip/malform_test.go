package sip_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countHeaderLines counts CRLF-delimited lines in raw beginning with
// "name:" (case-insensitive). Both the duplicate and remove assertions in
// this file read out of it.
func countHeaderLines(raw []byte, name string) int {
	prefix := strings.ToLower(name) + ":"
	count := 0
	for _, line := range bytes.Split(raw, []byte("\r\n")) {
		if strings.HasPrefix(strings.ToLower(string(line)), prefix) {
			count++
		}
	}
	return count
}

// splitLines is an independent (non-implementation) way of turning a
// serialised message into its CRLF-delimited lines, used to assert on
// exact line content and position without going through this package's own
// splitHeaderBlock/rebuildMessage helpers.
func splitLines(raw []byte) []string {
	return strings.Split(string(raw), "\r\n")
}

func fixtureHeaders() map[string]string {
	return map[string]string{
		sip.HeaderVia:     "SIP/2.0/UDP 192.0.2.1:5060;branch=z9hG4bK776asdhds",
		sip.HeaderFrom:    "<sip:alice@ims.local>;tag=1928301774",
		sip.HeaderTo:      "<sip:bob@ims.local>",
		sip.HeaderCallID:  "a84b4c76e66710@192.0.2.1",
		sip.HeaderCSeq:    "1 REGISTER",
		sip.HeaderContact: "<sip:alice@192.0.2.1:5060>",
	}
}

func fixtureRaw() []byte {
	return sip.Serialise(sip.BuildRequest("REGISTER", "sip:ims.local", fixtureHeaders()))
}

// fixtureRawWithSDPBody returns a serialised INVITE carrying a real SDP body
// and a correctly computed Content-Length, so the "does not repair framing"
// behavior has real body bytes and a real length to diverge from.
func fixtureRawWithSDPBody() []byte {
	headers := fixtureHeaders()
	headers[sip.HeaderContentType] = "application/sdp"
	body := []byte("v=0\r\no=- 0 0 IN IP4 192.0.2.1\r\ns=-\r\n" +
		"c=IN IP4 192.0.2.1\r\nt=0 0\r\nm=audio 49170 RTP/AVP 0\r\n")
	msg := sip.BuildRequest("INVITE", "sip:bob@ims.local", headers)
	msg.Body = body
	msg.Headers[sip.HeaderContentLength] = strconv.Itoa(len(body))
	return sip.Serialise(msg)
}

func TestMalform_SIPHeaderRemove_DeletesOnlyTargetLine(t *testing.T) {
	raw := fixtureRaw()
	out, err := sip.RemoveHeader(raw, "Via")
	require.NoError(t, err)

	assert.Equal(t, 0, countHeaderLines(out, "Via"))

	rawLines := splitLines(raw)
	outLines := splitLines(out)
	var expected []string
	removed := false
	for _, l := range rawLines {
		if !removed && strings.HasPrefix(l, "Via:") {
			removed = true
			continue
		}
		expected = append(expected, l)
	}
	require.True(t, removed, "fixture must actually contain a Via line")
	assert.Equal(t, expected, outLines, "every other line must stay byte-identical")
	assert.Equal(t, rawLines[0], outLines[0], "start-line must be unchanged")
}

func TestMalform_SIPHeaderRemove_IsCaseInsensitive(t *testing.T) {
	raw := fixtureRaw()

	viaVariant, err := sip.RemoveHeader(raw, "via")
	require.NoError(t, err)
	viaUpper, err := sip.RemoveHeader(raw, "VIA")
	require.NoError(t, err)
	viaCanonical, err := sip.RemoveHeader(raw, "Via")
	require.NoError(t, err)

	assert.Equal(t, viaCanonical, viaVariant)
	assert.Equal(t, viaCanonical, viaUpper)
}

func TestMalform_SIPHeaderDuplicate_EmptyValueCopiesOriginalLineAdjacently(t *testing.T) {
	raw := fixtureRaw()
	out, err := sip.DuplicateHeader(raw, "Via", "")
	require.NoError(t, err)

	assert.Equal(t, 2, countHeaderLines(out, "Via"))

	rawLines := splitLines(raw)
	outLines := splitLines(out)
	viaIdx := indexOfLinePrefix(t, rawLines, "Via:")

	require.Equal(t, rawLines[viaIdx], outLines[viaIdx], "original Via line stays at its position")
	assert.Equal(t, outLines[viaIdx], outLines[viaIdx+1], "duplicate must be byte-identical and adjacent")
}

func TestMalform_SIPHeaderDuplicate_ExplicitValueAppendsSecondLine(t *testing.T) {
	raw := fixtureRaw()
	value := "SIP/2.0/TCP bogus.invalid;branch=z9hG4bKfake"
	out, err := sip.DuplicateHeader(raw, "Via", value)
	require.NoError(t, err)

	assert.Equal(t, 2, countHeaderLines(out, "Via"))

	rawLines := splitLines(raw)
	outLines := splitLines(out)
	viaIdx := indexOfLinePrefix(t, rawLines, "Via:")

	assert.Equal(t, rawLines[viaIdx], outLines[viaIdx], "the first Via line is the untouched original")
	assert.Equal(t, "Via: "+value, outLines[viaIdx+1], "the second Via line carries the supplied value")
}

func TestMalform_SIPHeaderOverwrite_ReplacesValueAtSamePosition(t *testing.T) {
	raw := fixtureRaw()
	out, err := sip.OverwriteHeader(raw, "Via", "GARBAGE-NOT-A-VIA")
	require.NoError(t, err)

	assert.Equal(t, 1, countHeaderLines(out, "Via"))

	rawLines := splitLines(raw)
	outLines := splitLines(out)
	require.Equal(t, len(rawLines), len(outLines), "overwrite must not add or remove lines")

	viaIdx := indexOfLinePrefix(t, rawLines, "Via:")
	assert.Equal(t, "Via: GARBAGE-NOT-A-VIA", outLines[viaIdx])
}

func TestMalform_SIPHeaderOps_ErrorOnAbsentHeader(t *testing.T) {
	raw := fixtureRaw()
	wantErr := `sip malform: header "X-Not-Present" is not present in the serialised message`

	_, err := sip.RemoveHeader(raw, "X-Not-Present")
	require.EqualError(t, err, wantErr)

	_, err = sip.DuplicateHeader(raw, "X-Not-Present", "")
	require.EqualError(t, err, wantErr)

	_, err = sip.OverwriteHeader(raw, "X-Not-Present", "value")
	require.EqualError(t, err, wantErr)
}

func TestMalform_SIPHeaderOverwrite_ContentLengthDoesNotRepairFraming(t *testing.T) {
	raw := fixtureRawWithSDPBody()
	parts := bytes.SplitN(raw, []byte("\r\n\r\n"), 2)
	require.Len(t, parts, 2)
	originalBody := parts[1]

	out, err := sip.OverwriteHeader(raw, "Content-Length", "9999")
	require.NoError(t, err)

	outParts := bytes.SplitN(out, []byte("\r\n\r\n"), 2)
	require.Len(t, outParts, 2)
	assert.True(t, bytes.Equal(originalBody, outParts[1]), "body bytes must be untouched")

	outLines := splitLines(out)
	clIdx := indexOfLinePrefix(t, outLines, "Content-Length:")
	assert.Equal(t, "Content-Length: 9999", outLines[clIdx], "the corrupted length is written verbatim, never recomputed")
}

func TestMalform_SIPHeaderOps_DoNotMutateCallerInput(t *testing.T) {
	raw := fixtureRaw()
	rawCopy := append([]byte(nil), raw...)

	_, err := sip.RemoveHeader(raw, "Via")
	require.NoError(t, err)
	assert.Equal(t, rawCopy, raw)

	_, err = sip.DuplicateHeader(raw, "Via", "")
	require.NoError(t, err)
	assert.Equal(t, rawCopy, raw)

	_, err = sip.OverwriteHeader(raw, "Via", "x")
	require.NoError(t, err)
	assert.Equal(t, rawCopy, raw)
}

func TestMalform_SIPHeaderOps_RoundTripWithNoBody(t *testing.T) {
	raw := fixtureRaw()
	require.True(t, bytes.HasSuffix(raw, []byte("\r\n\r\n")), "fixture must have no body for this test to be meaningful")

	_, err := sip.RemoveHeader(raw, "Via")
	assert.NoError(t, err)
	_, err = sip.DuplicateHeader(raw, "Via", "")
	assert.NoError(t, err)
	_, err = sip.OverwriteHeader(raw, "Via", "x")
	assert.NoError(t, err)
}

// TestMalform_SIPHeaderDuplicate_IsInvisibleToParseMessage pins the trap
// this file's whole design exists to route around: ParseMessage coalesces
// two same-named header lines into one ", "-joined value, silently hiding
// the duplicate. This is why every duplicate assertion in this file reads
// raw bytes directly instead of re-parsing the result.
func TestMalform_SIPHeaderDuplicate_IsInvisibleToParseMessage(t *testing.T) {
	raw := fixtureRaw()
	dup, err := sip.DuplicateHeader(raw, "Via", "")
	require.NoError(t, err)
	require.Equal(t, 2, countHeaderLines(dup, "Via"))

	msg, err := sip.ParseMessage(dup)
	require.NoError(t, err)

	via := msg.Headers[sip.HeaderVia]
	assert.Equal(t, 1, strings.Count(via, ", "),
		"ParseMessage coalesces the two duplicated lines into one comma-joined value")
}

func indexOfLinePrefix(t *testing.T, lines []string, prefix string) int {
	t.Helper()
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	require.Fail(t, "no line found with prefix", prefix)
	return -1
}
