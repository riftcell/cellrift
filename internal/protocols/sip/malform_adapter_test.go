// malform_adapter_test.go proves malform: sip_header at the wire: a declared
// malform: sip_header block on a real send step changes the bytes a real
// in-process mock UAS reads off a real TCP socket.
//
// Every assertion in this file reads the mock's RawReads() pre-parse byte
// history, never a re-parsed sip.SIPMessage. SIPMessage.Headers is a
// map[string]string, and ParseMessage coalesces two same-named header lines
// into one ", "-joined value -- so a test that re-parsed a duplicated
// header would see exactly what an uncorrupted message produces and pass
// while proving nothing about the corruption this file exists to verify.
package sip_test

import (
	"strings"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// malformStepTimeout is short enough that a step the silent mock never
// answers still finishes fast, and long enough that a real loopback
// round-trip is never mistaken for a hang.
const malformStepTimeout = 1500 * time.Millisecond

// newMalformAdapter builds a SIP adapter over a real TCP transport wrapped
// exactly the way cmd/cellrift/main.go wraps it, pointed at a real
// in-process TCP mock UAS that reads one request and answers nothing.
// TCPFaultSilence is the right peer here for the same reason
// conn_fault_test.go's newTCPFaultAdapter uses it: it never calls
// ParseMessage, so a deliberately malformed request produces no spurious
// mock-side parse error to explain away.
func newMalformAdapter(t *testing.T) (*sip.Adapter, *sip.TCPMockUASServer) {
	t.Helper()
	u := sip.NewTCPMockUAS(t, sip.TCPFaultSilence)
	t.Cleanup(u.Close)
	tr, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	a := sip.NewAdapterWithTransport(impair.Wrap(tr), "tcp", malformStepTimeout)
	t.Cleanup(func() { _ = a.Close() })
	return a, u
}

// malformFields is the minimal REGISTER field set every case in this file
// sends, matching conn_fault_test.go's faultFields() shape.
func malformFields() map[string]string {
	return map[string]string{
		"From":    "<sip:447700900001@ims.local>;tag=malform001",
		"To":      "<sip:447700900001@ims.local>",
		"Contact": "<sip:447700900001@10.0.0.1:5060>",
	}
}

// countLinesStartingWith splits raw on CRLF and counts lines whose start
// matches prefix case-insensitively. Every header-count assertion in this
// file reads out of this one helper.
func countLinesStartingWith(raw []byte, prefix string) int {
	prefix = strings.ToLower(prefix)
	count := 0
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			count++
		}
	}
	return count
}

// lastRead returns the last RawReads() entry after a settle window, so "not
// observed yet" and "never arrived" are distinguishable, mirroring
// conn_fault_e2e_test.go's discipline for the same distinction.
func lastRead(t *testing.T, u *sip.TCPMockUASServer) []byte {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	reads := u.RawReads()
	require.NotEmpty(t, reads, "mock must have observed at least one read")
	return reads[len(reads)-1]
}

func TestAdapter_MalformSIPHeader_Missing_ReachesTheWire(t *testing.T) {
	a, u := newMalformAdapter(t)

	result, err := a.Execute("send", u.Addr(), "REGISTER", malformFields(), map[string]string{
		malform.SIPHeaderNameKey: "Via",
		malform.SIPHeaderOpKey:   malform.OpMissing,
	})
	require.NoError(t, err)
	assert.LessOrEqual(t, result.DurationMs, malformStepTimeout.Milliseconds()+300,
		"a step against a silent peer must not exceed its own step timeout")

	raw := lastRead(t, u)
	assert.Equal(t, 0, countLinesStartingWith(raw, "Via:"), "the Via line must be entirely absent")
	assert.Equal(t, 1, countLinesStartingWith(raw, "REGISTER "), "the start-line survives untouched")
	assert.Equal(t, 1, countLinesStartingWith(raw, "From:"))
	assert.Equal(t, 1, countLinesStartingWith(raw, "To:"))
	assert.Equal(t, 1, countLinesStartingWith(raw, "Call-ID:"))
}

func TestAdapter_MalformSIPHeader_Duplicate_ReachesTheWire(t *testing.T) {
	a, u := newMalformAdapter(t)

	result, err := a.Execute("send", u.Addr(), "REGISTER", malformFields(), map[string]string{
		malform.SIPHeaderNameKey: "Via",
		malform.SIPHeaderOpKey:   malform.OpDuplicate,
	})
	require.NoError(t, err)
	assert.LessOrEqual(t, result.DurationMs, malformStepTimeout.Milliseconds()+300)

	raw := lastRead(t, u)
	assert.Equal(t, 2, countLinesStartingWith(raw, "Via:"), "the peer must observe two separate Via lines")
}

func TestAdapter_MalformSIPHeader_Overwrite_ReachesTheWire(t *testing.T) {
	a, u := newMalformAdapter(t)
	const badVia = "SIP/2.0/TCP not-a-real-via"

	result, err := a.Execute("send", u.Addr(), "REGISTER", malformFields(), map[string]string{
		malform.SIPHeaderNameKey:  "Via",
		malform.SIPHeaderOpKey:    malform.OpOverwrite,
		malform.SIPHeaderValueKey: badVia,
	})
	require.NoError(t, err)
	assert.LessOrEqual(t, result.DurationMs, malformStepTimeout.Milliseconds()+300)

	raw := lastRead(t, u)
	assert.Equal(t, 1, countLinesStartingWith(raw, "Via:"))
	assert.Contains(t, string(raw), "Via: "+badVia+"\r\n")
}

// TestAdapter_MalformSIPHeader_Unimpaired_IsUncorrupted is the control: with
// no malform: block declared, the corruption path must be entirely inert.
func TestAdapter_MalformSIPHeader_Unimpaired_IsUncorrupted(t *testing.T) {
	a, u := newMalformAdapter(t)

	result, err := a.Execute("send", u.Addr(), "REGISTER", malformFields(), map[string]string{})
	require.NoError(t, err)
	assert.LessOrEqual(t, result.DurationMs, malformStepTimeout.Milliseconds()+300)

	raw := lastRead(t, u)
	assert.Equal(t, 1, countLinesStartingWith(raw, "Via:"), "exactly one, well-formed Via line")
}

// TestAdapter_MalformSIPHeader_UnknownHeaderFailsLoudly declares op missing
// on a header the REGISTER never carries. That is a scenario-authoring
// mistake, not peer behaviour, so it surfaces as a Go error rather than a
// failed StepResult -- matching the sibling impair.Configure error two
// lines above the malform call site in executeSend, and Diameter's
// equivalent malform error path.
func TestAdapter_MalformSIPHeader_UnknownHeaderFailsLoudly(t *testing.T) {
	a, u := newMalformAdapter(t)

	result, err := a.Execute("send", u.Addr(), "REGISTER", malformFields(), map[string]string{
		malform.SIPHeaderNameKey: "X-Not-Present",
		malform.SIPHeaderOpKey:   malform.OpMissing,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "X-Not-Present")
	assert.Nil(t, result)
	assert.Empty(t, u.RawReads(), "a step that fails before Send must never reach the wire")
}

// TestAdapter_MalformSIPHeader_DuplicateIsInvisibleWhenReparsed is the
// executable statement of why this whole file asserts bytes: take the
// duplicate case's RawReads() entry, run the real ParseMessage on it, and
// confirm the parsed Headers["Via"] holds exactly one comma-joined value
// while the raw bytes still carry two distinct Via lines.
func TestAdapter_MalformSIPHeader_DuplicateIsInvisibleWhenReparsed(t *testing.T) {
	a, u := newMalformAdapter(t)

	_, err := a.Execute("send", u.Addr(), "REGISTER", malformFields(), map[string]string{
		malform.SIPHeaderNameKey: "Via",
		malform.SIPHeaderOpKey:   malform.OpDuplicate,
	})
	require.NoError(t, err)

	raw := lastRead(t, u)
	require.Equal(t, 2, countLinesStartingWith(raw, "Via:"), "sanity: the raw bytes carry two Via lines")

	msg, err := sip.ParseMessage(raw)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(msg.Headers["Via"], ", "),
		"ParseMessage coalesces the two duplicated lines into one comma-joined value, hiding the corruption")
}
