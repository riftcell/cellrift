package engine

import (
	"testing"

	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func credStep() *scenario.Step { return &scenario.Step{ID: "reg", Protocol: "sip"} }

func TestBuildProtocolConfig_CarriesCredentialKeysAndOmitsEmpty(t *testing.T) {
	m := buildProtocolConfig(&scenario.ProtocolConfig{Username: "u", Password: "p", CredentialsFile: "f"})
	assert.Equal(t, "u", m["username"])
	assert.Equal(t, "p", m["password"])
	assert.Equal(t, "f", m["credentials_file"])

	empty := buildProtocolConfig(&scenario.ProtocolConfig{Transport: "udp"})
	for _, k := range []string{"username", "password", "credentials_file"} {
		assert.NotContains(t, empty, k)
	}
}

func TestBuildStepConfig_ResolvesCredentialTemplateFromVar(t *testing.T) {
	state := NewState()
	state.Set("sip_password", "from-cli")
	cfg, err := buildStepConfig(credStep(), map[string]string{"password": "{{ sip_password }}"}, "", state)
	require.NoError(t, err)
	assert.Equal(t, "from-cli", cfg["password"])
}

func TestBuildStepConfig_ResolvesCredentialTemplateFromEnvOverride(t *testing.T) {
	state := NewState()
	state.LoadEnvOverrides([]string{"CELLRIFT_VAR_SIP_PASSWORD=from-env"})
	cfg, err := buildStepConfig(credStep(), map[string]string{"password": "{{ sip_password }}"}, "", state)
	require.NoError(t, err)
	assert.Equal(t, "from-env", cfg["password"])
}

// TestBuildStepConfig_UnresolvableCredentialTemplateDoesNotFallThrough is the
// test this whole layer exists for. A typo'd {{ sip_password }} must stop the
// step. If it instead falls through, the run authenticates with whatever
// CELLRIFT_SIP_PASSWORD happens to hold: a different secret than the scenario
// named, with no message saying so.
func TestBuildStepConfig_UnresolvableCredentialTemplateDoesNotFallThrough(t *testing.T) {
	t.Setenv("CELLRIFT_SIP_PASSWORD", "AMBIENT-SECRET")
	state := NewState()
	cfg, err := buildStepConfig(credStep(), map[string]string{"password": "{{ nope }}"}, "", state)
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), `resolve protocol credential "password"`)
	assert.NotContains(t, err.Error(), "AMBIENT-SECRET")
}

func TestBuildStepConfig_OnlyNamedKeysAreTemplated(t *testing.T) {
	state := NewState()
	in := map[string]string{
		"cert_file": "/certs/{{ weird }}.pem", "transport": "{{ t }}",
		"local_port": "{{ p }}", "key_file": "{{x}}", "ca_file": "{{y}}",
	}
	cfg, err := buildStepConfig(credStep(), in, "", state)
	require.NoError(t, err)
	for k, v := range in {
		assert.Equal(t, v, cfg[k], k)
	}
}
