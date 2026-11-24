package scenario_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Parser tests ---

func TestParseFile_ValidScenario(t *testing.T) {
	path := filepath.Join("..", "..", "scenarios", "ims", "register_basic.yaml")
	s, err := scenario.ParseFile(path)
	require.NoError(t, err)

	assert.Equal(t, "VoLTE basic registration", s.Name)
	assert.Equal(t, "1.0", s.Version)
	assert.Contains(t, s.Protocols, "sip")
	assert.Contains(t, s.Protocols, "diameter")
	assert.Equal(t, "udp", s.Protocols["sip"].Transport)
	assert.Equal(t, "sctp", s.Protocols["diameter"].Transport)
	assert.Len(t, s.Steps, 3)
	assert.Equal(t, "diameter_auth", s.Steps[0].ID)
	assert.Equal(t, "sip_register", s.Steps[1].ID)
}

func TestParseFile_MissingFile(t *testing.T) {
	_, err := scenario.ParseFile("nonexistent.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent.yaml")
}

func TestParse_MalformedYAML(t *testing.T) {
	bad := []byte("name: test\nsteps:\n  - id: [invalid")
	_, err := scenario.Parse(bad, "bad.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad.yaml")
}

// TestParse_RejectsUnknownKeys pins strict decoding. Before KnownFields was
// enabled, a typo'd key was silently dropped: `assertions:` instead of
// `assert:` validated clean and the step ran with no checks at all. Each row
// puts the typo at a different nesting level so a regression at any one
// struct is caught, and each asserts the error names the offending key so a
// user can find it.
func TestParse_RejectsUnknownKeys(t *testing.T) {
	const base = "version: \"1.0\"\nname: t\nprotocols:\n  sip:\n    transport: udp\nsteps:\n  - id: a\n    protocol: sip\n    action: send\n    target: h:5060\n    message: OPTIONS\n"

	cases := []struct {
		name   string
		yaml   string
		badKey string
	}{
		{"top-level typo", "version: \"1.0\"\nname: t\nprotocolz:\n  sip: {}\nsteps: []\n", "protocolz"},
		{"step-level typo of assert", base + "    assertions:\n      - x == 1\n", "assertions"},
		{"step-level typo of target", base + "    targett: h:5060\n", "targett"},
		{"nested typo inside impair", base + "    impair:\n      latency: 100\n", "latency"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := scenario.Parse([]byte(tc.yaml), "typo.yaml")
			require.Error(t, err, "an unknown key must be a parse error, not silently dropped")
			assert.Contains(t, err.Error(), tc.badKey, "the error must name the offending key")
			assert.Contains(t, err.Error(), "typo.yaml")
		})
	}
}

// TestParse_StrictDecodingAcceptsEveryDeclaredKey is the control: strictness
// must reject only genuinely unknown keys. It exercises the widest set of
// legitimate keys a step can carry, so a field accidentally dropped from a
// struct (which strict mode would then reject) fails here rather than
// breaking a shipped scenario.
func TestParse_StrictDecodingAcceptsEveryDeclaredKey(t *testing.T) {
	full := `version: "1.0"
name: everything
description: every declared step key at once
protocols:
  sip:
    transport: udp
    local_port: 5060
variables:
  peer: "h:5060"
steps:
  - id: a
    protocol: sip
    action: send
    target: "{{ peer }}"
    message: OPTIONS
    timeout_ms: 500
    fields:
      From: "<sip:a@b>"
    impair:
      latency_ms: 10
      jitter_ms: 5
      delay_ms: 1
    malform:
      sip_header:
        name: Via
        op: duplicate
    capture:
      code: response.status_code
    assert:
      - response.status_code == 200
`
	_, err := scenario.Parse([]byte(full), "full.yaml")
	require.NoError(t, err)
}

func TestParse_ImpairBlock(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want *scenario.ImpairSpec
	}{
		{
			name: "latency and jitter",
			yaml: "name: t\nversion: '1.0'\nsteps:\n  - id: s1\n    protocol: sip\n    action: send\n    target: h:5060\n    message: REGISTER\n    impair:\n      latency_ms: 200\n      jitter_ms: 50\n",
			want: &scenario.ImpairSpec{LatencyMs: 200, JitterMs: 50},
		},
		{
			name: "delay only",
			yaml: "name: t\nversion: '1.0'\nsteps:\n  - id: s1\n    protocol: sip\n    action: send\n    target: h:5060\n    message: REGISTER\n    impair:\n      delay_ms: 500\n",
			want: &scenario.ImpairSpec{DelayMs: 500},
		},
		{
			name: "no impair key",
			yaml: "name: t\nversion: '1.0'\nsteps:\n  - id: s1\n    protocol: sip\n    action: send\n    target: h:5060\n    message: REGISTER\n",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := scenario.Parse([]byte(tc.yaml), "t.yaml")
			require.NoError(t, err)
			require.Len(t, s.Steps, 1)
			assert.Equal(t, tc.want, s.Steps[0].Impair)
		})
	}
}

func TestMarshal_ImpairNilOmitsKey(t *testing.T) {
	s := scenario.Step{ID: "s1", Protocol: "sip", Action: "send"}
	data, err := json.Marshal(s)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "impair")
}

func TestParseFile_DiameterScenario(t *testing.T) {
	path := filepath.Join("..", "..", "scenarios", "diameter", "uar_uaa.yaml")
	s, err := scenario.ParseFile(path)
	require.NoError(t, err)

	assert.Equal(t, "Diameter UAR/UAA", s.Name)
	assert.Contains(t, s.Protocols, "diameter")
	assert.GreaterOrEqual(t, len(s.Steps), 1)
}

// --- Validator tests ---

func TestValidate_WellFormedScenario(t *testing.T) {
	s := validScenario()
	errs := scenario.Validate(s)
	assert.Empty(t, errs)
}

func TestValidate_MissingName(t *testing.T) {
	s := validScenario()
	s.Name = ""
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	assertHasFieldError(t, errs, "name")
}

func TestValidate_MissingVersion(t *testing.T) {
	s := validScenario()
	s.Version = ""
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	assertHasFieldError(t, errs, "version")
}

func TestValidate_UnknownProtocol(t *testing.T) {
	s := validScenario()
	s.Protocols["gtp"] = &scenario.ProtocolConfig{Transport: "udp"}
	s.Steps[0].Protocol = "gtp"
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	found := false
	for _, e := range errs {
		if e.Message != "" && contains(e.Message, "gtp") {
			found = true
		}
	}
	assert.True(t, found, "expected error mentioning unknown protocol gtp, got: %v", errs)
}

func TestValidate_DuplicateStepIDs(t *testing.T) {
	s := validScenario()
	s.Steps = append(s.Steps, scenario.Step{
		ID:       "step1",
		Protocol: "sip",
		Action:   "send",
		Target:   "host:5060",
		Message:  "REGISTER",
	})
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	found := false
	for _, e := range errs {
		if contains(e.Message, "duplicate") {
			found = true
		}
	}
	assert.True(t, found, "expected duplicate id error, got: %v", errs)
}

func TestValidate_UndefinedVariableReference(t *testing.T) {
	s := validScenario()
	s.Steps[0].Target = "{{ unknown_host }}:5060"
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	found := false
	for _, e := range errs {
		if contains(e.Message, "unknown_host") {
			found = true
		}
	}
	assert.True(t, found, "expected undefined variable error, got: %v", errs)
}

func TestValidate_MalformedAssertion(t *testing.T) {
	s := validScenario()
	s.Steps[0].Assert = []string{"this is not valid"}
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	found := false
	for _, e := range errs {
		if contains(e.Message, "malformed assertion") {
			found = true
		}
	}
	assert.True(t, found, "expected malformed assertion error, got: %v", errs)
}

func TestValidate_ValidAssertions(t *testing.T) {
	s := validScenario()
	s.Steps[0].Assert = []string{
		"result_code == 2001",
		"server_name != null",
		"latency_ms < 500",
		"count >= 1",
	}
	errs := scenario.Validate(s)
	assert.Empty(t, errs)
}

func TestValidate_DottedVariableReference(t *testing.T) {
	s := validScenario()
	// step1 is defined, so step1.field should resolve.
	s.Steps = append(s.Steps, scenario.Step{
		ID:       "step2",
		Protocol: "sip",
		Action:   "send",
		Target:   "host:5060",
		Message:  "REGISTER",
		Fields:   map[string]string{"Auth": "{{ step1.server_name }}"},
	})
	errs := scenario.Validate(s)
	assert.Empty(t, errs)
}

func TestValidate_WaitStepNeedsNoProtocol(t *testing.T) {
	s := validScenario()
	s.Steps = []scenario.Step{
		{ID: "pause", Action: "wait", Duration: 500},
	}
	errs := scenario.Validate(s)
	assert.Empty(t, errs)
}

func TestValidate_WaitStepRequiresPositiveDuration(t *testing.T) {
	cases := []struct {
		name     string
		duration int
	}{
		{"zero duration", 0},
		{"negative duration", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validScenario()
			s.Steps = []scenario.Step{
				{ID: "pause", Action: "wait", Duration: tc.duration},
			}
			errs := scenario.Validate(s)
			require.Len(t, errs, 1)
			assert.Equal(t, "duration_ms", errs[0].Field)
			assert.Contains(t, errs[0].Message, "duration_ms")
		})
	}
}

func TestValidate_NonWaitStepStillRequiresProtocol(t *testing.T) {
	s := validScenario()
	s.Steps = []scenario.Step{
		{ID: "step1", Action: "send", Target: "host:5060", Message: "REGISTER"},
	}
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	assertHasFieldError(t, errs, "protocol")
}

func TestValidate_ParallelStepStillRequiresSubSteps(t *testing.T) {
	s := validScenario()
	s.Steps = []scenario.Step{
		{ID: "parallel_group", Action: "parallel"},
	}
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	assertHasFieldError(t, errs, "steps")
}

func TestValidate_ImpairSendOnly(t *testing.T) {
	cases := []struct {
		action   string
		wantErrs bool
	}{
		{"send", false},
		{"wait", true},
		{"assert_state", true},
		{"parallel", true},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			s := validScenario()
			step := scenario.Step{
				ID:     "s1",
				Action: tc.action,
				Impair: &scenario.ImpairSpec{LatencyMs: 200},
			}
			switch tc.action {
			case "wait":
				step.Duration = 500
			case "parallel":
				step.Steps = []scenario.Step{{ID: "sub1", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER"}}
			case "assert_state", "send":
				step.Protocol = "sip"
				step.Target = "h:5060"
				step.Message = "REGISTER"
				step.Check = "registered"
			}
			s.Steps = []scenario.Step{step}
			errs := scenario.Validate(s)
			if !tc.wantErrs {
				for _, e := range errs {
					assert.NotEqual(t, "impair", e.Field, "action %q should not reject impair, got: %v", tc.action, errs)
				}
				return
			}
			found := false
			for _, e := range errs {
				if e.Field == "impair" {
					found = true
					assert.Contains(t, e.Message, "send")
					assert.Contains(t, e.Message, tc.action)
				}
			}
			assert.True(t, found, "expected impair error naming action %q, got: %v", tc.action, errs)
		})
	}
}

func TestValidate_ImpairRejectsNegative(t *testing.T) {
	cases := []struct {
		name  string
		spec  *scenario.ImpairSpec
		field string
	}{
		{"latency_ms", &scenario.ImpairSpec{LatencyMs: -1}, "impair.latency_ms"},
		{"jitter_ms", &scenario.ImpairSpec{JitterMs: -1}, "impair.jitter_ms"},
		{"delay_ms", &scenario.ImpairSpec{DelayMs: -1}, "impair.delay_ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validScenario()
			s.Steps[0].Impair = tc.spec
			errs := scenario.Validate(s)
			assertHasFieldError(t, errs, tc.field)
		})
	}
}

func TestValidate_ImpairEmptyBlockIsLegal(t *testing.T) {
	s := validScenario()
	s.Steps[0].Impair = &scenario.ImpairSpec{}
	errs := scenario.Validate(s)
	assert.Empty(t, errs)
}

// TestValidate_ImpairRejectsInParallelSubStep protects the deliberate
// descent into a parallel step's sub-steps in validateImpairTree. Validate
// does not recurse into parallel sub-steps for anything else (AVP fields,
// variable references, or step shape), so this rule's descent is easy to
// delete by accident — which would reinstate the "validated but does
// nothing" defect class one level of nesting deeper.
func TestValidate_ImpairRejectsInParallelSubStep(t *testing.T) {
	t.Run("non-send sub-step rejected, message matches top-level", func(t *testing.T) {
		topLevel := validScenario()
		topLevel.Steps = []scenario.Step{
			{ID: "pause", Action: "wait", Duration: 500, Impair: &scenario.ImpairSpec{LatencyMs: 200}},
		}
		topErrs := scenario.Validate(topLevel)
		require.Len(t, topErrs, 1)
		require.Equal(t, "impair", topErrs[0].Field)

		nested := validScenario()
		nested.Steps = []scenario.Step{
			{
				ID:     "parallel_group",
				Action: "parallel",
				Steps: []scenario.Step{
					{ID: "pause", Action: "wait", Duration: 500, Impair: &scenario.ImpairSpec{LatencyMs: 200}},
				},
			},
		}
		nestedErrs := scenario.Validate(nested)
		require.Len(t, nestedErrs, 1)
		assert.Equal(t, "impair", nestedErrs[0].Field)
		assert.Equal(t, topErrs[0].Message, nestedErrs[0].Message,
			"top-level and nested rejection must produce the identical message")
	})

	t.Run("send sub-step accepted", func(t *testing.T) {
		s := validScenario()
		s.Steps = []scenario.Step{
			{
				ID:     "parallel_group",
				Action: "parallel",
				Steps: []scenario.Step{
					{ID: "sub1", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", Impair: &scenario.ImpairSpec{LatencyMs: 200}},
				},
			},
		}
		errs := scenario.Validate(s)
		assert.Empty(t, errs)
	})

	t.Run("send sub-step with negative value rejected", func(t *testing.T) {
		s := validScenario()
		s.Steps = []scenario.Step{
			{
				ID:     "parallel_group",
				Action: "parallel",
				Steps: []scenario.Step{
					{ID: "sub1", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", Impair: &scenario.ImpairSpec{LatencyMs: -1}},
				},
			},
		}
		errs := scenario.Validate(s)
		assertHasFieldError(t, errs, "impair.latency_ms")
	})
}

func TestValidate_NoSteps(t *testing.T) {
	s := validScenario()
	s.Steps = nil
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	assertHasFieldError(t, errs, "steps")
}

// --- Integration: parse real fixture files and validate ---

func TestParseAndValidate_RegisterBasic(t *testing.T) {
	path := filepath.Join("..", "..", "scenarios", "ims", "register_basic.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("fixture not yet created")
	}
	s, err := scenario.ParseFile(path)
	require.NoError(t, err)
	errs := scenario.Validate(s)
	assert.Empty(t, errs, "register_basic.yaml should validate cleanly, got: %v", errs)
}

func TestParseAndValidate_UarUaa(t *testing.T) {
	path := filepath.Join("..", "..", "scenarios", "diameter", "uar_uaa.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("fixture not yet created")
	}
	s, err := scenario.ParseFile(path)
	require.NoError(t, err)
	errs := scenario.Validate(s)
	assert.Empty(t, errs, "uar_uaa.yaml should validate cleanly, got: %v", errs)
}

// --- ScanLibrary tests ---

func TestScanLibrary_FindsAllScenarios(t *testing.T) {
	dir := filepath.Join("..", "..", "scenarios")
	entries := scenario.ScanLibrary(dir)
	require.GreaterOrEqual(t, len(entries), 5, "expected at least 5 built-in scenarios")

	// Verify known scenarios are present.
	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
		assert.NotEmpty(t, e.Name)
		assert.NotEmpty(t, e.Protocols)
		assert.NotEmpty(t, e.Path)
	}
	assert.True(t, names["VoLTE basic registration"])
	assert.True(t, names["VoLTE basic call"])
	assert.True(t, names["Diameter credit control (CCR-I / CCA-I)"])
}

func TestScanLibrary_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	entries := scenario.ScanLibrary(dir)
	assert.Empty(t, entries)
}

func TestScanLibrary_NonexistentDir(t *testing.T) {
	entries := scenario.ScanLibrary("/nonexistent/path")
	assert.Empty(t, entries)
}

func TestScanLibrary_SkipsInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	// Write an invalid YAML file.
	os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("not: [valid yaml"), 0644)
	// Write a non-YAML file.
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a scenario"), 0644)
	entries := scenario.ScanLibrary(dir)
	assert.Empty(t, entries)
}

func TestScanLibrary_TruncatesLongDescriptions(t *testing.T) {
	dir := t.TempDir()
	longDesc := "name: test\nversion: '1.0'\ndescription: " +
		"\"This is a very long description that exceeds eighty characters and should be truncated by the library scanner.\"\n" +
		"protocols:\n  sip:\n    transport: udp\nsteps:\n  - id: s1\n    protocol: sip\n    action: send\n    target: h:5060\n    message: REGISTER\n"
	os.WriteFile(filepath.Join(dir, "long.yaml"), []byte(longDesc), 0644)
	entries := scenario.ScanLibrary(dir)
	require.Len(t, entries, 1)
	assert.LessOrEqual(t, len(entries[0].Description), 80)
}

// --- ValidationError.Error tests ---

func TestValidationError_ErrorWithStepID(t *testing.T) {
	e := scenario.ValidationError{Field: "target", StepID: "step1", Message: "is empty"}
	assert.Contains(t, e.Error(), "step1")
	assert.Contains(t, e.Error(), "target")
}

func TestValidationError_ErrorWithoutStepID(t *testing.T) {
	e := scenario.ValidationError{Field: "name", Message: "required"}
	s := e.Error()
	assert.Contains(t, s, "name")
	assert.Contains(t, s, "required")
	assert.NotContains(t, s, "step")
}

// --- Parse and validate all scenario files ---

func TestParseAndValidate_AllBuiltinScenarios(t *testing.T) {
	scenarioDir := filepath.Join("..", "..", "scenarios")
	files := []string{
		"ims/register_basic.yaml",
		"ims/register_deregister.yaml",
		"ims/basic_call.yaml",
		"ims/call_rejected.yaml",
		"diameter/credit_control.yaml",
		"diameter/uar_uaa.yaml",
	}
	for _, f := range files {
		path := filepath.Join(scenarioDir, f)
		t.Run(f, func(t *testing.T) {
			s, err := scenario.ParseFile(path)
			require.NoError(t, err, "parse %s", f)
			errs := scenario.Validate(s)
			assert.Empty(t, errs, "%s should validate cleanly, got: %v", f, errs)
			assert.NotEmpty(t, s.Name)
			assert.NotEmpty(t, s.Version)
			assert.NotEmpty(t, s.Description)
			assert.GreaterOrEqual(t, len(s.Steps), 2, "%s should have at least 2 steps", f)
		})
	}
}

// --- Helpers ---

func validScenario() *scenario.Scenario {
	return &scenario.Scenario{
		Name:    "Test scenario",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip": {Transport: "udp"},
		},
		Variables: map[string]string{
			"host": "lab.local",
		},
		Steps: []scenario.Step{
			{
				ID:       "step1",
				Protocol: "sip",
				Action:   "send",
				Target:   "{{ host }}:5060",
				Message:  "REGISTER",
			},
		},
	}
}

func assertHasFieldError(t *testing.T, errs []scenario.ValidationError, field string) {
	t.Helper()
	for _, e := range errs {
		if e.Field == field {
			return
		}
	}
	t.Errorf("expected error on field %q, got: %v", field, errs)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
