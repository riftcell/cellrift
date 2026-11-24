package engine_test

import (
	"testing"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// mockAdapter is a test adapter that returns a configurable result.
type mockAdapter struct {
	result    *common.StepResult
	err       error
	lastCalls []mockCall
}

type mockCall struct {
	Action  string
	Target  string
	Message string
	Fields  map[string]string
	Config  map[string]string
}

func (m *mockAdapter) Execute(action, target, message string, fields map[string]string, config map[string]string) (*common.StepResult, error) {
	m.lastCalls = append(m.lastCalls, mockCall{
		Action:  action,
		Target:  target,
		Message: message,
		Fields:  fields,
		Config:  config,
	})
	return m.result, m.err
}

func (m *mockAdapter) Close() error { return nil }

func TestExecuteStep_CaptureStoresValueInState(t *testing.T) {
	state := engine.NewState()
	state.Set("host", "hss.lab.local")

	adapter := &mockAdapter{
		result: &common.StepResult{
			Status: common.StatusPassed,
			Response: map[string]interface{}{
				"Result-Code": uint32(2001),
				"Server-Name": "sip:scscf.ims.local:5060",
			},
		},
	}

	step := &scenario.Step{
		ID:       "diameter_auth",
		Protocol: "diameter",
		Action:   "send",
		Target:   "{{ host }}:3868",
		Message:  "UAR",
		Fields:   map[string]string{"User-Name": "test"},
		Capture: map[string]string{
			"server_name": "response.Server-Name",
			"result_code": "response.Result-Code",
		},
	}

	result, err := engine.ExecuteStep(step, state, adapter, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status)

	// Verify captured values in state.
	v, ok := state.Get("server_name")
	require.True(t, ok)
	assert.Equal(t, "sip:scscf.ims.local:5060", v)

	v, ok = state.Get("result_code")
	require.True(t, ok)
	assert.Equal(t, uint32(2001), v)

	// Also stored with step ID prefix.
	v, ok = state.Get("diameter_auth.server_name")
	require.True(t, ok)
	assert.Equal(t, "sip:scscf.ims.local:5060", v)
}

func TestExecuteStep_FoldsImpairIntoConfig(t *testing.T) {
	cases := []struct {
		name       string
		impair     *scenario.ImpairSpec
		wantConfig map[string]string
	}{
		{
			name:       "latency and jitter",
			impair:     &scenario.ImpairSpec{LatencyMs: 200, JitterMs: 50},
			wantConfig: map[string]string{"latency_ms": "200", "jitter_ms": "50"},
		},
		{
			name:       "delay only",
			impair:     &scenario.ImpairSpec{DelayMs: 500},
			wantConfig: map[string]string{"delay_ms": "500"},
		},
		{
			name:       "drop only",
			impair:     &scenario.ImpairSpec{Drop: true},
			wantConfig: map[string]string{"drop": "true"},
		},
		{
			name:       "duplicate and latency",
			impair:     &scenario.ImpairSpec{Duplicate: true, LatencyMs: 200},
			wantConfig: map[string]string{"duplicate": "true", "latency_ms": "200"},
		},
		{
			name:       "reset only",
			impair:     &scenario.ImpairSpec{Reset: true},
			wantConfig: map[string]string{"reset": "true"},
		},
		{
			name:       "truncate only",
			impair:     &scenario.ImpairSpec{Truncate: true},
			wantConfig: map[string]string{"truncate": "true"},
		},
		{
			name:       "abrupt_close only",
			impair:     &scenario.ImpairSpec{AbruptClose: true},
			wantConfig: map[string]string{"abrupt_close": "true"},
		},
		{
			name:       "sctp_abort only",
			impair:     &scenario.ImpairSpec{SCTPAbort: true},
			wantConfig: map[string]string{"sctp_abort": "true"},
		},
		{
			name:       "reset and latency",
			impair:     &scenario.ImpairSpec{Reset: true, LatencyMs: 200},
			wantConfig: map[string]string{"reset": "true", "latency_ms": "200"},
		},
		{
			name:       "nil impair",
			impair:     nil,
			wantConfig: map[string]string{},
		},
		{
			name:       "all-zero impair",
			impair:     &scenario.ImpairSpec{},
			wantConfig: map[string]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := engine.NewState()
			adapter := &mockAdapter{
				result: &common.StepResult{Status: common.StatusPassed, Response: map[string]interface{}{}},
			}
			step := &scenario.Step{
				ID:       "step1",
				Protocol: "sip",
				Action:   "send",
				Target:   "h:5060",
				Message:  "REGISTER",
				Check:    "registered",
				Impair:   tc.impair,
			}

			_, err := engine.ExecuteStep(step, state, adapter, nil)
			require.NoError(t, err)

			require.Len(t, adapter.lastCalls, 1)
			gotConfig := adapter.lastCalls[0].Config
			assert.Equal(t, "registered", gotConfig["check"], "existing check fold must be undisturbed")
			for k, v := range tc.wantConfig {
				assert.Equal(t, v, gotConfig[k], "config[%q]", k)
			}
			for _, k := range []string{"latency_ms", "jitter_ms", "delay_ms", "drop", "duplicate", "reset", "truncate", "abrupt_close", "sctp_abort"} {
				if _, want := tc.wantConfig[k]; !want {
					_, present := gotConfig[k]
					assert.False(t, present, "config[%q] must be absent", k)
				}
			}
		})
	}
}

// TestImpairSpec_YAMLRoundTrip proves every impair: boolean sub-key's YAML
// tag matches the snake_case name a scenario author writes. A wrong tag is
// silent at compile time and would only surface as "the fault never fires" —
// SCTPAbort in particular needs an explicit yaml:"sctp_abort" tag, since
// Go's default field-name lowering would otherwise produce "sctpabort".
func TestImpairSpec_YAMLRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want scenario.ImpairSpec
	}{
		{"drop", "drop: true\n", scenario.ImpairSpec{Drop: true}},
		{"duplicate", "duplicate: true\n", scenario.ImpairSpec{Duplicate: true}},
		{"reset", "reset: true\n", scenario.ImpairSpec{Reset: true}},
		{"truncate", "truncate: true\n", scenario.ImpairSpec{Truncate: true}},
		{"abrupt_close", "abrupt_close: true\n", scenario.ImpairSpec{AbruptClose: true}},
		{"sctp_abort", "sctp_abort: true\n", scenario.ImpairSpec{SCTPAbort: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got scenario.ImpairSpec
			require.NoError(t, yaml.Unmarshal([]byte(tc.yaml), &got))
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExecuteStep_AssertionFailure(t *testing.T) {
	state := engine.NewState()

	adapter := &mockAdapter{
		result: &common.StepResult{
			Status: common.StatusPassed,
			Response: map[string]interface{}{
				"status_code": 403,
			},
		},
	}

	step := &scenario.Step{
		ID:       "check_status",
		Protocol: "sip",
		Action:   "send",
		Target:   "target:5060",
		Message:  "REGISTER",
		Assert:   []string{"response.status_code == 200"},
	}

	result, err := engine.ExecuteStep(step, state, adapter, nil)
	require.Error(t, err)

	var ae *engine.AssertionError
	require.ErrorAs(t, err, &ae)
	assert.Contains(t, ae.Expression, "status_code == 200")
	assert.Equal(t, "403", ae.Actual)
	assert.Equal(t, common.StatusFailed, result.Status)
}

func TestExecuteStep_AssertNotNull_Passes(t *testing.T) {
	state := engine.NewState()

	adapter := &mockAdapter{
		result: &common.StepResult{
			Status: common.StatusPassed,
			Response: map[string]interface{}{
				"Server-Name": "sip:scscf.ims.local",
			},
		},
	}

	step := &scenario.Step{
		ID:       "check_notnull",
		Protocol: "diameter",
		Action:   "send",
		Target:   "target:3868",
		Message:  "UAR",
		Capture:  map[string]string{"server_name": "response.Server-Name"},
		Assert:   []string{"server_name != null"},
	}

	result, err := engine.ExecuteStep(step, state, adapter, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status)
}

func TestExecuteStep_AssertNotNull_Fails(t *testing.T) {
	state := engine.NewState()

	adapter := &mockAdapter{
		result: &common.StepResult{
			Status:   common.StatusPassed,
			Response: map[string]interface{}{},
		},
	}

	step := &scenario.Step{
		ID:       "check_notnull_fail",
		Protocol: "diameter",
		Action:   "send",
		Target:   "target:3868",
		Message:  "UAR",
		Assert:   []string{"missing_var != null"},
	}

	_, err := engine.ExecuteStep(step, state, adapter, nil)
	require.Error(t, err)
	var ae *engine.AssertionError
	require.ErrorAs(t, err, &ae)
}

func TestExecuteStep_ResolvesTemplateVariables(t *testing.T) {
	state := engine.NewState()
	state.Set("msisdn", "447700900001")
	state.Set("domain", "ims.local")

	adapter := &mockAdapter{
		result: &common.StepResult{
			Status:   common.StatusPassed,
			Response: map[string]interface{}{},
		},
	}

	step := &scenario.Step{
		ID:       "resolved_step",
		Protocol: "sip",
		Action:   "send",
		Target:   "pcscf:5060",
		Message:  "REGISTER",
		Fields: map[string]string{
			"From": "sip:{{ msisdn }}@{{ domain }}",
		},
	}

	_, err := engine.ExecuteStep(step, state, adapter, nil)
	require.NoError(t, err)

	// Verify the adapter received resolved fields.
	require.Len(t, adapter.lastCalls, 1)
	assert.Equal(t, "sip:447700900001@ims.local", adapter.lastCalls[0].Fields["From"])
}

func TestExecuteStep_UndefinedVariableReturnsError(t *testing.T) {
	state := engine.NewState()

	adapter := &mockAdapter{}

	step := &scenario.Step{
		ID:       "undef_step",
		Protocol: "sip",
		Action:   "send",
		Target:   "{{ unknown_host }}:5060",
		Message:  "REGISTER",
	}

	_, err := engine.ExecuteStep(step, state, adapter, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "undefined variable")
}

// ── response.__raw ───────────────────────────────────────────────────────────

// rawFixture is a raw protocol message deliberately containing a CRLF and a
// second-line body so the assertion would visibly fail if []byte ever
// rendered through fmt's decimal-list %v verb instead of its literal text.
var rawFixture = []byte("SIP/2.0 200 OK\r\n\r\nv=0\r\n")

func TestExecuteStep_CaptureRawResponse(t *testing.T) {
	state := engine.NewState()

	adapter := &mockAdapter{
		result: &common.StepResult{
			Status:      common.StatusPassed,
			Response:    map[string]interface{}{"status_code": 200},
			RawResponse: rawFixture,
		},
	}

	step := &scenario.Step{
		ID:       "step1",
		Protocol: "sip",
		Action:   "send",
		Target:   "h:5060",
		Message:  "REGISTER",
		Capture:  map[string]string{"raw": "response.__raw"},
	}

	_, err := engine.ExecuteStep(step, state, adapter, nil)
	require.NoError(t, err)

	v, ok := state.Get("raw")
	require.True(t, ok)
	assert.Equal(t, rawFixture, v)

	v, ok = state.Get("step1.raw")
	require.True(t, ok)
	assert.Equal(t, rawFixture, v)
}

func TestExecuteStep_CaptureRawResponse_NoParsedResponse(t *testing.T) {
	state := engine.NewState()

	adapter := &mockAdapter{
		result: &common.StepResult{
			Status:      common.StatusPassed,
			Response:    nil, // adapter could not parse a response at all
			RawResponse: rawFixture,
		},
	}

	step := &scenario.Step{
		ID:       "step1",
		Protocol: "sip",
		Action:   "send",
		Target:   "h:5060",
		Message:  "REGISTER",
		Capture:  map[string]string{"raw": "response.__raw"},
	}

	_, err := engine.ExecuteStep(step, state, adapter, nil)
	require.NoError(t, err)

	v, ok := state.Get("raw")
	require.True(t, ok, "capture must still succeed with no parsed response map")
	assert.Equal(t, rawFixture, v)
}

func TestExecuteStep_CaptureRawResponse_EmptyRawIsAbsent(t *testing.T) {
	state := engine.NewState()

	adapter := &mockAdapter{
		result: &common.StepResult{
			Status:      common.StatusPassed,
			Response:    map[string]interface{}{"status_code": 200},
			RawResponse: nil,
		},
	}

	step := &scenario.Step{
		ID:       "step1",
		Protocol: "sip",
		Action:   "send",
		Target:   "h:5060",
		Message:  "REGISTER",
		Capture:  map[string]string{"raw": "response.__raw"},
	}

	_, err := engine.ExecuteStep(step, state, adapter, nil)
	require.NoError(t, err)

	_, ok := state.Get("raw")
	assert.False(t, ok, "an empty raw response must not be stored")
}

// TestExecuteStep_AssertRawResponseNotNull pins a deliberate design choice:
// capture: and assert: reach RawResponse through the same lookup function,
// so an assertion on response.__raw is a documented, tested capability
// rather than an accident capture: alone happens to enable.
func TestExecuteStep_AssertRawResponseNotNull(t *testing.T) {
	state := engine.NewState()

	passAdapter := &mockAdapter{
		result: &common.StepResult{
			Status:      common.StatusPassed,
			Response:    map[string]interface{}{"status_code": 200},
			RawResponse: rawFixture,
		},
	}
	passStep := &scenario.Step{
		ID:       "step1",
		Protocol: "sip",
		Action:   "send",
		Target:   "h:5060",
		Message:  "REGISTER",
		Assert:   []string{"response.__raw != null"},
	}
	result, err := engine.ExecuteStep(passStep, state, passAdapter, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status)

	failAdapter := &mockAdapter{
		result: &common.StepResult{
			Status:      common.StatusPassed,
			Response:    map[string]interface{}{"status_code": 200},
			RawResponse: nil,
		},
	}
	failStep := &scenario.Step{
		ID:       "step2",
		Protocol: "sip",
		Action:   "send",
		Target:   "h:5060",
		Message:  "REGISTER",
		Assert:   []string{"response.__raw != null"},
	}
	failResult, err := engine.ExecuteStep(failStep, state, failAdapter, nil)
	require.Error(t, err)
	var ae *engine.AssertionError
	require.ErrorAs(t, err, &ae)
	assert.Contains(t, ae.Expression, "response.__raw != null")
	assert.Equal(t, common.StatusFailed, failResult.Status)
}

// TestRun_RawResponseCapturedThenReferencedInLaterStep proves the raw bytes
// captured in one step survive as literal text into the next step's
// template resolution, not a fmt %v decimal-list rendering.
func TestRun_RawResponseCapturedThenReferencedInLaterStep(t *testing.T) {
	adapter := &mockAdapter{
		result: &common.StepResult{
			Status:      common.StatusPassed,
			Response:    map[string]interface{}{"status_code": 200},
			RawResponse: rawFixture,
		},
	}

	s := &scenario.Scenario{
		Name: "raw-capture-cross-step",
		Steps: []scenario.Step{
			{ID: "step1", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", Capture: map[string]string{"raw": "response.__raw"}},
			{ID: "step2", Protocol: "sip", Action: "send", Target: "h:5060", Message: "{{ step1.raw }}"},
		},
	}

	result := engine.Run(s, common.AdapterMap{"sip": adapter}, engine.RunOptions{})
	require.Equal(t, common.StatusPassed, result.Status)

	require.Len(t, adapter.lastCalls, 2)
	assert.Equal(t, string(rawFixture), adapter.lastCalls[1].Message)
}

func TestExecuteStep_CaptureNonRawPathUnchanged(t *testing.T) {
	state := engine.NewState()

	adapter := &mockAdapter{
		result: &common.StepResult{
			Status:   common.StatusPassed,
			Response: map[string]interface{}{"Server-Name": "sip:scscf.ims.local"},
		},
	}

	step := &scenario.Step{
		ID:       "diameter_auth",
		Protocol: "diameter",
		Action:   "send",
		Target:   "target:3868",
		Message:  "UAR",
		Capture:  map[string]string{"server_name": "response.Server-Name"},
	}

	_, err := engine.ExecuteStep(step, state, adapter, nil)
	require.NoError(t, err)

	v, ok := state.Get("server_name")
	require.True(t, ok)
	assert.Equal(t, "sip:scscf.ims.local", v)
}

// ── EvaluateAssertionPublic ──────────────────────────────────────────────────

func TestEvaluateAssertionPublic_Equality(t *testing.T) {
	state := engine.NewState()
	state.Set("result_code", "2001")
	assert.NoError(t, engine.EvaluateAssertionPublic("result_code == 2001", state))
}

func TestEvaluateAssertionPublic_Inequality(t *testing.T) {
	state := engine.NewState()
	state.Set("status", "failed")
	assert.NoError(t, engine.EvaluateAssertionPublic("status != passed", state))
}

func TestEvaluateAssertionPublic_EqualityFails(t *testing.T) {
	state := engine.NewState()
	state.Set("result_code", "5001")
	err := engine.EvaluateAssertionPublic("result_code == 2001", state)
	require.Error(t, err)
	var ae *engine.AssertionError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "5001", ae.Actual)
}

func TestEvaluateAssertionPublic_NumericGreaterThan(t *testing.T) {
	state := engine.NewState()
	state.Set("latency", "150")
	assert.NoError(t, engine.EvaluateAssertionPublic("latency < 500", state))
}

func TestEvaluateAssertionPublic_NumericLessThanOrEqual(t *testing.T) {
	state := engine.NewState()
	state.Set("count", "10")
	assert.NoError(t, engine.EvaluateAssertionPublic("count <= 10", state))
}

func TestEvaluateAssertionPublic_NumericGreaterThanOrEqual(t *testing.T) {
	state := engine.NewState()
	state.Set("score", "100")
	assert.NoError(t, engine.EvaluateAssertionPublic("score >= 100", state))
}

func TestEvaluateAssertionPublic_NumericStrictGreater(t *testing.T) {
	state := engine.NewState()
	state.Set("val", "5")
	assert.NoError(t, engine.EvaluateAssertionPublic("val > 4", state))
}

func TestEvaluateAssertionPublic_NumericComparisonFails(t *testing.T) {
	state := engine.NewState()
	state.Set("latency", "999")
	err := engine.EvaluateAssertionPublic("latency < 500", state)
	require.Error(t, err)
	var ae *engine.AssertionError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "999", ae.Actual)
}

func TestEvaluateAssertionPublic_NullCheck_Null(t *testing.T) {
	state := engine.NewState()
	// key not set → null
	assert.NoError(t, engine.EvaluateAssertionPublic("missing_key == null", state))
}

func TestEvaluateAssertionPublic_NotNull_PresentValue(t *testing.T) {
	state := engine.NewState()
	state.Set("server_name", "sip:scscf.ims.local")
	assert.NoError(t, engine.EvaluateAssertionPublic("server_name != null", state))
}

func TestEvaluateAssertionPublic_UnsupportedSyntax(t *testing.T) {
	state := engine.NewState()
	err := engine.EvaluateAssertionPublic("no_operator_here", state)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported assertion syntax")
}
