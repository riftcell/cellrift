package reporter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- MaskPII (stateless convenience wrapper) ---

func TestMaskPII_E164WithPlus(t *testing.T) {
	assert.Equal(t, "[REDACTED-1]", MaskPII("+447700900001"))
}

func TestMaskPII_E164WithoutPlus(t *testing.T) {
	assert.Equal(t, "[REDACTED-1]", MaskPII("447700900001"))
}

func TestMaskPII_E164InSentence(t *testing.T) {
	in := "assertion failed: User-Name 447700900001 not found in HSS"
	out := MaskPII(in)
	assert.NotContains(t, out, "447700900001")
	assert.Contains(t, out, "[REDACTED-1]")
}

// TestMaskPII_AdjacentNumbersSeparatedByOneChar pins the leak the review
// found: with a consuming trailing-context group, the separator after the
// first number was swallowed by its match, so the second number had no
// preceding character to satisfy the leading group and went out unmasked.
// It affected every pair of numbers separated by exactly one character —
// the ordinary shape of a comma- or space-joined MSISDN list — in all three
// reporters at once, since they share Masker.Mask.
func TestMaskPII_AdjacentNumbersSeparatedByOneChar(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"comma", "+447700900001,+447700900002", "[REDACTED-1],[REDACTED-2]"},
		{"space", "+447700900001 +447700900002", "[REDACTED-1] [REDACTED-2]"},
		{"semicolon", "447700900001;447700900002", "[REDACTED-1];[REDACTED-2]"},
		{"three in a row", "+447700900001,+447700900002,+447700900003", "[REDACTED-1],[REDACTED-2],[REDACTED-3]"},
		{"same number twice keeps one token", "+447700900001,+447700900001", "[REDACTED-1],[REDACTED-1]"},
		{"two separators still works", "from=+447700900001;to=+447700900002", "from=[REDACTED-1];to=[REDACTED-2]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, MaskPII(tc.in))
		})
	}
}

// TestMaskPII_EmbeddedDigitRunsStillNotMasked is the control for moving the
// trailing check out of the regex: a candidate immediately followed by a
// digit is the prefix of a longer run, not a number, and must be left alone
// — exactly what the old consuming group guaranteed.
func TestMaskPII_EmbeddedDigitRunsStillNotMasked(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"16-digit run", "1234567890123456"},
		{"20-digit run", "12345678901234567890"},
		{"number glued to trailing digits", "id=4477009000019999999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.in, MaskPII(tc.in), "a longer digit run is not an E.164 number")
		})
	}
}

func TestMaskPII_MultipleNumbers(t *testing.T) {
	in := "caller=447700900001 callee=+447700900002"
	out := MaskPII(in)
	assert.NotContains(t, out, "447700900001")
	assert.NotContains(t, out, "447700900002")
	// Two distinct numbers get two distinct tokens within the same string.
	assert.Contains(t, out, "[REDACTED-1]")
	assert.Contains(t, out, "[REDACTED-2]")
}

func TestMaskPII_ShortCodesNotMasked(t *testing.T) {
	cases := []string{
		"result_code == 2001",
		"DIAMETER_SUCCESS: 2001",
		"status 200",
		"error code 404",
	}
	for _, c := range cases {
		assert.Equal(t, c, MaskPII(c), "should not mask: %s", c)
	}
}

func TestMaskPII_NoNumbers(t *testing.T) {
	s := "assertion failed: response.status_code == ok"
	assert.Equal(t, s, MaskPII(s))
}

func TestMaskPII_EmptyString(t *testing.T) {
	assert.Equal(t, "", MaskPII(""))
}

func TestMaskPII_USNumber(t *testing.T) {
	assert.Equal(t, "[REDACTED-1]", MaskPII("+12125550100"))
}

func TestMaskPII_NumberAtStartOfString(t *testing.T) {
	out := MaskPII("447700900001 failed to register")
	assert.NotContains(t, out, "447700900001")
	assert.Contains(t, out, "[REDACTED-1]")
}

func TestMaskPII_NumberAtEndOfString(t *testing.T) {
	out := MaskPII("failed to reach 447700900001")
	assert.NotContains(t, out, "447700900001")
	assert.Contains(t, out, "[REDACTED-1]")
}

// --- Masker (stateful, cross-string correlation) ---

func TestMasker_SameNumberSameToken(t *testing.T) {
	m := NewMasker()
	first := m.Mask("caller 447700900001 dialled")
	second := m.Mask("callee rejected 447700900001")
	assert.Contains(t, first, "[REDACTED-1]")
	assert.Contains(t, second, "[REDACTED-1]")
	assert.NotContains(t, second, "[REDACTED-2]")
}

func TestMasker_DifferentNumbersDifferentTokens(t *testing.T) {
	m := NewMasker()
	out1 := m.Mask("caller=447700900001")
	out2 := m.Mask("callee=447700900002")
	assert.Contains(t, out1, "[REDACTED-1]")
	assert.Contains(t, out2, "[REDACTED-2]")
}

func TestMasker_TokensAreStable(t *testing.T) {
	m := NewMasker()
	// Establish token for first number.
	m.Mask("447700900001")
	// Second number gets token 2.
	m.Mask("447700900002")
	// First number seen again — must still be token 1.
	out := m.Mask("447700900001 called 447700900002")
	assert.Contains(t, out, "[REDACTED-1]")
	assert.Contains(t, out, "[REDACTED-2]")
}

func TestMasker_CrossScenarioCorrelation(t *testing.T) {
	// Simulate two scenario steps both referencing the same subscriber.
	m := NewMasker()
	step1 := m.Mask("assertion failed: From header 447700900001 rejected by P-CSCF")
	step2 := m.Mask("timeout waiting for 200 OK from 447700900001")
	assert.Equal(t, countOccurrences(step1, "[REDACTED-1]"), 1)
	assert.Equal(t, countOccurrences(step2, "[REDACTED-1]"), 1)
	// No second token should appear — only one unique number.
	assert.NotContains(t, step1, "[REDACTED-2]")
	assert.NotContains(t, step2, "[REDACTED-2]")
}

// countOccurrences counts non-overlapping occurrences of sub in s.
func countOccurrences(s, sub string) int {
	count := 0
	i := 0
	for i < len(s) {
		idx := indexOf(s[i:], sub)
		if idx < 0 {
			break
		}
		count++
		i += idx + len(sub)
	}
	return count
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
