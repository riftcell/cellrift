package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is package scenario (not scenario_test) because
// TestEditDistance_Basics and TestClosestAVPName_Deterministic exercise
// editDistance/closestAVPName directly, and both are unexported.

func diameterStep(id string, fields map[string]string) Step {
	return Step{ID: id, Protocol: "diameter", Action: "send", Target: "peer:3868", Message: "UAR", Fields: fields}
}

func sipStep(id string, fields map[string]string) Step {
	return Step{ID: id, Protocol: "sip", Action: "send", Target: "peer:5060", Message: "REGISTER", Fields: fields}
}

func minimalScenario(steps ...Step) *Scenario {
	return &Scenario{Name: "t", Version: "1.0", Steps: steps}
}

func TestValidate_UnknownDiameterAVPField(t *testing.T) {
	s := minimalScenario(diameterStep("s1", map[string]string{"Public-Identitty": "x"}))
	errs := Validate(s)

	var found *ValidationError
	for i := range errs {
		if errs[i].Field == "fields.Public-Identitty" {
			found = &errs[i]
		}
	}
	require.NotNil(t, found, "expected a ValidationError for fields.Public-Identitty")
	assert.Equal(t, "s1", found.StepID)
	assert.Equal(t, `unknown Diameter AVP "Public-Identitty" (did you mean "Public-Identity"?)`, found.Message)
}

func TestValidate_UnknownDiameterAVPFieldNoCloseMatch(t *testing.T) {
	s := minimalScenario(diameterStep("s1", map[string]string{"Zzzzzzzz": "x"}))
	errs := Validate(s)

	var found *ValidationError
	for i := range errs {
		if errs[i].Field == "fields.Zzzzzzzz" {
			found = &errs[i]
		}
	}
	require.NotNil(t, found, "expected a ValidationError for fields.Zzzzzzzz")
	assert.Equal(t, `unknown Diameter AVP "Zzzzzzzz"`, found.Message)
}

func TestValidate_KnownDiameterAVPFieldsAccepted(t *testing.T) {
	s := minimalScenario(diameterStep("s1", map[string]string{
		"Session-Id":      "a;b;c",
		"Origin-Host":     "h",
		"User-Name":       "u",
		"Public-Identity": "sip:a@b",
		"CC-Request-Type": "1",
	}))
	errs := Validate(s)
	for _, e := range errs {
		assert.NotContains(t, e.Message, "unknown Diameter AVP")
	}
}

func TestValidate_SipFieldsNotCheckedAgainstAVPs(t *testing.T) {
	s := minimalScenario(sipStep("s1", map[string]string{
		"From": "a", "To": "b", "Contact": "c", "Expires": "3600",
	}))
	errs := Validate(s)
	for _, e := range errs {
		assert.NotContains(t, e.Message, "unknown Diameter AVP")
	}
}

func TestValidate_ReservedBodyKeysNotCheckedAsAVPs(t *testing.T) {
	s := minimalScenario(diameterStep("s1", map[string]string{
		"Session-Id":     "a;b;c",
		"__body_content": "v=0",
	}))
	errs := Validate(s)
	for _, e := range errs {
		assert.NotContains(t, e.Message, "unknown Diameter AVP")
	}
}

func TestValidate_NestedGroupedFieldAccepted(t *testing.T) {
	s := minimalScenario(diameterStep("s1", map[string]string{
		"SIP-Auth-Data-Item.SIP-Authentication-Scheme": "Digest-AKAv1-MD5",
	}))
	errs := Validate(s)
	for _, e := range errs {
		assert.NotContains(t, e.Message, "Grouped")
		assert.NotContains(t, e.Message, "unknown Diameter AVP")
	}
}

func TestValidate_UnknownGroupedPrefix(t *testing.T) {
	s := minimalScenario(diameterStep("s1", map[string]string{"SIP-Auth-Data-Itm.X": "1"}))
	errs := Validate(s)

	var found *ValidationError
	for i := range errs {
		if errs[i].Field == "fields.SIP-Auth-Data-Itm.X" {
			found = &errs[i]
		}
	}
	require.NotNil(t, found, "expected a ValidationError for fields.SIP-Auth-Data-Itm.X")
	assert.Equal(t, `unknown Diameter AVP "SIP-Auth-Data-Itm" (did you mean "SIP-Auth-Data-Item"?)`, found.Message)
}

func TestValidate_NonGroupedPrefix(t *testing.T) {
	s := minimalScenario(diameterStep("s1", map[string]string{"Origin-Host.X": "1"}))
	errs := Validate(s)

	var found *ValidationError
	for i := range errs {
		if errs[i].Field == "fields.Origin-Host.X" {
			found = &errs[i]
		}
	}
	require.NotNil(t, found, "expected a ValidationError for fields.Origin-Host.X")
	assert.Equal(t, `AVP "Origin-Host" is not a Grouped AVP and cannot carry members`, found.Message)
}

func TestValidate_UnknownGroupedMember(t *testing.T) {
	s := minimalScenario(diameterStep("s1", map[string]string{
		"SIP-Auth-Data-Item.SIP-Item-Numbr": "1",
	}))
	errs := Validate(s)

	var found *ValidationError
	for i := range errs {
		if errs[i].Field == "fields.SIP-Auth-Data-Item.SIP-Item-Numbr" {
			found = &errs[i]
		}
	}
	require.NotNil(t, found, "expected a ValidationError for fields.SIP-Auth-Data-Item.SIP-Item-Numbr")
	assert.Equal(t, `Grouped AVP "SIP-Auth-Data-Item" has no member "SIP-Item-Numbr" (did you mean "SIP-Item-Number"?)`, found.Message)
}

func TestEditDistance_Basics(t *testing.T) {
	assert.Equal(t, 3, editDistance("kitten", "sitting"))
	assert.Equal(t, 0, editDistance("a", "a"))
	assert.Equal(t, 3, editDistance("", "abc"))
}

func TestClosestAVPName_Deterministic(t *testing.T) {
	first := closestAVPName("Public-Identitty")
	require.NotEmpty(t, first)
	for i := 0; i < 20; i++ {
		assert.Equal(t, first, closestAVPName("Public-Identitty"))
	}
}
