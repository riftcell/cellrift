package scenario_test

import (
	"path/filepath"
	"testing"

	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// parseFieldsDoc decodes a standalone `fields: ...` YAML document into a
// StepFields value, exercising the same UnmarshalYAML path a full scenario
// parse invokes on Step.Fields.
func parseFieldsDoc(t *testing.T, doc string) (scenario.StepFields, error) {
	t.Helper()
	var wrapper struct {
		Fields scenario.StepFields `yaml:"fields"`
	}
	err := yaml.Unmarshal([]byte(doc), &wrapper)
	return wrapper.Fields, err
}

func TestStepFields_FlattensNestedMapping(t *testing.T) {
	doc := `
fields:
  Public-Identity: "sip:a@b"
  SIP-Auth-Data-Item:
    SIP-Item-Number: 1
    SIP-Authentication-Scheme: "Digest-AKAv1-MD5"
`
	fields, err := parseFieldsDoc(t, doc)
	require.NoError(t, err)
	assert.Equal(t, scenario.StepFields{
		"Public-Identity":                              "sip:a@b",
		"SIP-Auth-Data-Item.SIP-Item-Number":           "1",
		"SIP-Auth-Data-Item.SIP-Authentication-Scheme": "Digest-AKAv1-MD5",
	}, fields)
}

func TestStepFields_FlatScenarioUnchanged(t *testing.T) {
	path := filepath.Join("..", "..", "scenarios", "diameter", "uar_uaa.yaml")
	s, err := scenario.ParseFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, s.Steps)

	step := s.Steps[0]
	assert.Equal(t, "{{ subscriber_msisdn }}", step.Fields["User-Name"])
	// Public-Identity's exact value is deliberately NOT pinned: this test is
	// about flat fields never becoming dotted keys, and an exact pin on
	// scenario content made an unrelated edit to uar_uaa.yaml fail here.
	// What matters is that the value arrives whole, with its templates
	// unexpanded by the parser.
	assert.Contains(t, step.Fields["Public-Identity"], "sip:{{ subscriber_msisdn }}@")
	for k := range step.Fields {
		assert.NotContains(t, k, ".", "flat scenario must not produce a dotted key")
	}
}

func TestStepFields_NestsMoreThanOneLevel(t *testing.T) {
	doc := `
fields:
  A:
    B:
      C: v
`
	fields, err := parseFieldsDoc(t, doc)
	require.NoError(t, err)
	assert.Equal(t, scenario.StepFields{"A.B.C": "v"}, fields)
}

func TestStepFields_RejectsSequence(t *testing.T) {
	doc := `
fields:
  Bad-Field:
    - one
    - two
`
	_, err := parseFieldsDoc(t, doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Bad-Field")
}

func TestStepFields_RejectsCollidingKeys(t *testing.T) {
	doc := `
fields:
  "A.B": x
  A:
    B: y
`
	_, err := parseFieldsDoc(t, doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "A.B")
}

func TestStepFields_PreservesVarReferences(t *testing.T) {
	doc := `
fields:
  SIP-Auth-Data-Item:
    SIP-Item-Number: "{{ subscriber_msisdn }}"
`
	fields, err := parseFieldsDoc(t, doc)
	require.NoError(t, err)
	assert.Equal(t, "{{ subscriber_msisdn }}", fields["SIP-Auth-Data-Item.SIP-Item-Number"])
}
