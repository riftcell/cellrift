package scenario_test

import (
	"testing"

	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const credentialsYAML = `name: n
version: "1"
protocols:
  sip:
    username: impi-user
    password: pw
    credentials_file: creds.yaml
steps:
  - id: s1
    protocol: sip
    action: send
    target: "127.0.0.1:5060"
    message: REGISTER
`

// TestParse_AcceptsCredentialKeysUnderStrictDecoding pins that the three
// credential keys are part of the strictly decoded vocabulary.
func TestParse_AcceptsCredentialKeysUnderStrictDecoding(t *testing.T) {
	s, err := scenario.Parse([]byte(credentialsYAML), "c.yaml")
	require.NoError(t, err)
	sip := s.Protocols["sip"]
	assert.Equal(t, "impi-user", sip.Username)
	assert.Equal(t, "pw", sip.Password)
	assert.Equal(t, "creds.yaml", sip.CredentialsFile)
}

func scenarioWithProtocol(name string, cfg *scenario.ProtocolConfig) *scenario.Scenario {
	s := validScenario()
	s.Protocols = map[string]*scenario.ProtocolConfig{name: cfg}
	return s
}

func credentialErrors(errs []scenario.ValidationError) []scenario.ValidationError {
	var out []scenario.ValidationError
	for _, e := range errs {
		if len(e.Field) > 10 && e.Field[:10] == "protocols." {
			out = append(out, e)
		}
	}
	return out
}

func TestValidateCredentialScope_DiameterPasswordIsOneError(t *testing.T) {
	errs := credentialErrors(scenario.Validate(scenarioWithProtocol("diameter",
		&scenario.ProtocolConfig{Password: "SECRET-VALUE"})))
	require.Len(t, errs, 1)
	assert.Equal(t, "protocols.diameter.password", errs[0].Field)
	assert.NotContains(t, errs[0].Message, "SECRET-VALUE")
	assert.Contains(t, errs[0].Message, "sip only")
}

func TestValidateCredentialScope_DiameterTwoKeysStableOrder(t *testing.T) {
	cfg := &scenario.ProtocolConfig{Username: "u", CredentialsFile: "f"}
	for i := 0; i < 20; i++ {
		errs := credentialErrors(scenario.Validate(scenarioWithProtocol("diameter", cfg)))
		require.Len(t, errs, 2)
		assert.Equal(t, "protocols.diameter.username", errs[0].Field)
		assert.Equal(t, "protocols.diameter.credentials_file", errs[1].Field)
	}
}

func TestValidateCredentialScope_SIPCarryingAllKeysIsClean(t *testing.T) {
	cfg := &scenario.ProtocolConfig{Username: "u", Password: "p", CredentialsFile: "f"}
	assert.Empty(t, credentialErrors(scenario.Validate(scenarioWithProtocol("sip", cfg))))
}
