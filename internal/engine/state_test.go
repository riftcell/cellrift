package engine_test

import (
	"testing"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestState_SetGet(t *testing.T) {
	s := engine.NewState()

	s.Set("name", "cellrift")
	s.Set("count", 42)
	s.Set("enabled", true)

	v, ok := s.Get("name")
	assert.True(t, ok)
	assert.Equal(t, "cellrift", v)

	v, ok = s.Get("count")
	assert.True(t, ok)
	assert.Equal(t, 42, v)

	v, ok = s.Get("enabled")
	assert.True(t, ok)
	assert.Equal(t, true, v)

	_, ok = s.Get("missing")
	assert.False(t, ok)
}

func TestState_GetString(t *testing.T) {
	s := engine.NewState()
	s.Set("host", "ims.local")
	s.Set("port", 5060)

	assert.Equal(t, "ims.local", s.GetString("host"))
	assert.Equal(t, "5060", s.GetString("port"))
	assert.Equal(t, "", s.GetString("missing"))
}

func TestState_Resolve_SingleVariable(t *testing.T) {
	s := engine.NewState()
	s.Set("host", "pcscf.lab.local")

	result, err := s.Resolve("sip:{{ host }}:5060")
	require.NoError(t, err)
	assert.Equal(t, "sip:pcscf.lab.local:5060", result)
}

func TestState_Resolve_MultipleVariables(t *testing.T) {
	s := engine.NewState()
	s.Set("user", "447700900001")
	s.Set("domain", "ims.local")

	result, err := s.Resolve("sip:{{ user }}@{{ domain }}")
	require.NoError(t, err)
	assert.Equal(t, "sip:447700900001@ims.local", result)
}

func TestState_Resolve_DottedVariable(t *testing.T) {
	s := engine.NewState()
	s.Set("diameter_auth.server_name", "sip:scscf.ims.local:5060")

	result, err := s.Resolve("{{ diameter_auth.server_name }}")
	require.NoError(t, err)
	assert.Equal(t, "sip:scscf.ims.local:5060", result)
}

func TestState_Resolve_UndefinedVariable(t *testing.T) {
	s := engine.NewState()
	_, err := s.Resolve("{{ undefined_var }}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "undefined variable")
	assert.Contains(t, err.Error(), "undefined_var")
}

func TestState_Resolve_NoTemplates(t *testing.T) {
	s := engine.NewState()
	result, err := s.Resolve("plain text")
	require.NoError(t, err)
	assert.Equal(t, "plain text", result)
}

func TestState_ResolveMap(t *testing.T) {
	s := engine.NewState()
	s.Set("msisdn", "447700900001")
	s.Set("domain", "ims.local")

	fields := map[string]string{
		"From": "sip:{{ msisdn }}@{{ domain }}",
		"To":   "sip:{{ msisdn }}@{{ domain }}",
	}

	resolved, err := s.ResolveMap(fields)
	require.NoError(t, err)
	assert.Equal(t, "sip:447700900001@ims.local", resolved["From"])
	assert.Equal(t, "sip:447700900001@ims.local", resolved["To"])
}

func TestState_LoadVariables(t *testing.T) {
	s := engine.NewState()
	s.LoadVariables(map[string]string{
		"host":   "hss.lab.local",
		"msisdn": "447700900001",
	})

	assert.Equal(t, "hss.lab.local", s.GetString("host"))
	assert.Equal(t, "447700900001", s.GetString("msisdn"))
}

func TestState_LoadOverrides(t *testing.T) {
	s := engine.NewState()
	s.Set("msisdn", "447700900001")

	s.LoadOverrides([]string{"msisdn=447700900002", "new_var=hello"})

	assert.Equal(t, "447700900002", s.GetString("msisdn"))
	assert.Equal(t, "hello", s.GetString("new_var"))
}

func TestState_LoadEnvOverrides(t *testing.T) {
	s := engine.NewState()

	environ := []string{
		"HOME=/home/user",
		"CELLRIFT_VAR_SUBSCRIBER_MSISDN=447700900003",
		"CELLRIFT_VAR_HSS_HOST=hss.env.local",
		"PATH=/usr/bin",
	}
	s.LoadEnvOverrides(environ)

	assert.Equal(t, "447700900003", s.GetString("subscriber_msisdn"))
	assert.Equal(t, "hss.env.local", s.GetString("hss_host"))

	// Non-CELLRIFT_VAR_ env vars should not be loaded.
	_, ok := s.Get("home")
	assert.False(t, ok)
}

func TestState_OverridePrecedence(t *testing.T) {
	s := engine.NewState()

	// 1. Scenario variables (lowest).
	s.LoadVariables(map[string]string{"msisdn": "from_scenario"})

	// 2. Environment overrides.
	s.LoadEnvOverrides([]string{"CELLRIFT_VAR_MSISDN=from_env"})

	// 3. CLI overrides (highest).
	s.LoadOverrides([]string{"msisdn=from_cli"})

	assert.Equal(t, "from_cli", s.GetString("msisdn"))
}

func TestState_ResolveRendersByteSliceAsText(t *testing.T) {
	s := engine.NewState()
	s.Set("raw", []byte("SIP/2.0 200 OK"))

	result, err := s.Resolve("{{ raw }}")
	require.NoError(t, err)
	assert.Equal(t, "SIP/2.0 200 OK", result)

	// Non-[]byte values still render exactly as before.
	s.Set("code", uint32(2001))
	result, err = s.Resolve("{{ code }}")
	require.NoError(t, err)
	assert.Equal(t, "2001", result)
}

func TestState_GetStringRendersByteSliceAsText(t *testing.T) {
	s := engine.NewState()
	s.Set("raw", []byte("SIP/2.0 200 OK"))

	assert.Equal(t, "SIP/2.0 200 OK", s.GetString("raw"))

	// A plain string is unchanged.
	s.Set("name", "cellrift")
	assert.Equal(t, "cellrift", s.GetString("name"))
}

func TestState_All(t *testing.T) {
	s := engine.NewState()
	s.Set("a", "1")
	s.Set("b", "2")

	all := s.All()
	assert.Len(t, all, 2)
	assert.Equal(t, "1", all["a"])
	assert.Equal(t, "2", all["b"])

	// Modifying the returned map should not affect state.
	all["c"] = "3"
	_, ok := s.Get("c")
	assert.False(t, ok)
}
