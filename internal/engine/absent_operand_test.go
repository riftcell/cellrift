package engine_test

import (
	"testing"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the absent-operand rule: an assertion whose left-hand side
// exists nowhere (not in the response, not in captured state) is an
// authoring error, never a verdict. Before the rule, an absent operand was
// compared as the string "<nil>", so `missing != 503` passed — a typo'd
// field name or a capture that found nothing produced a permanently green
// assertion. This is the same defect class as the responseFieldsMap bug
// (every Diameter scenario's response.Result-Code silently resolving to nil)
// that went undetected for several phases; the rule closes the class rather
// than the instance.

func newAbsentFixture(assertExpr string, response common.ResponseFields) (*scenario.Step, *mockAdapter) {
	return &scenario.Step{
			ID:       "s",
			Protocol: "sip",
			Action:   "send",
			Target:   "h:5060",
			Message:  "OPTIONS",
			Assert:   []string{assertExpr},
		}, &mockAdapter{result: &common.StepResult{
			Status:   common.StatusPassed,
			Response: response,
		}}
}

// TestAssertion_AbsentOperandIsAnError covers every operator against a
// field the response does not contain. The != row is the one that used to
// pass vacuously; the others used to fail with a misleading actual of
// "<nil>". All must now surface as an error that names the missing operand.
func TestAssertion_AbsentOperandIsAnError(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"not-equal used to pass vacuously", "response.missing != 503"},
		{"equal used to fail confusingly", "response.missing == 200"},
		{"greater-or-equal", "response.missing >= 1"},
		{"less-than", "response.missing < 10"},
		{"typo'd namespace", "responce.status_code == 200"},
		{"bare captured name never captured", "granted_units != 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step, adapter := newAbsentFixture(tc.expr, common.ResponseFields{"status_code": 200})
			result, err := engine.ExecuteStep(step, engine.NewState(), adapter, nil)

			require.Error(t, err, "an absent operand must be an error, not a pass")
			assert.Contains(t, err.Error(), "is not present", "the error must say the operand is missing")
			assert.Contains(t, err.Error(), "== null", "the error must point at the sanctioned absence check")
			require.NotNil(t, result)
			assert.Equal(t, common.StatusFailed, result.Status)
		})
	}
}

// TestAssertion_NullComparisonStillSpeaksAboutAbsence proves the ONE
// sanctioned way to talk about a missing field keeps working: `== null`
// passes on an absent operand and `!= null` fails on one. Without this
// control the rule above would have made absence unassertable.
func TestAssertion_NullComparisonStillSpeaksAboutAbsence(t *testing.T) {
	t.Run("== null passes on absent", func(t *testing.T) {
		step, adapter := newAbsentFixture("response.missing == null", common.ResponseFields{})
		result, err := engine.ExecuteStep(step, engine.NewState(), adapter, nil)
		require.NoError(t, err)
		assert.Equal(t, common.StatusPassed, result.Status)
	})
	t.Run("!= null fails on absent, as a verdict not an error", func(t *testing.T) {
		step, adapter := newAbsentFixture("response.missing != null", common.ResponseFields{})
		result, err := engine.ExecuteStep(step, engine.NewState(), adapter, nil)
		require.Error(t, err)
		var ae *engine.AssertionError
		require.ErrorAs(t, err, &ae, "a failed null check is an assertion verdict, not an authoring error")
		assert.Equal(t, common.StatusFailed, result.Status)
	})
}

// TestAssertion_PresentOperandUnchanged is the control for ordinary use:
// a field that exists compares exactly as before, including a present
// field whose value happens to be the empty string.
func TestAssertion_PresentOperandUnchanged(t *testing.T) {
	cases := []struct {
		expr string
		pass bool
	}{
		{"response.status_code == 200", true},
		{"response.status_code != 500", true},
		{"response.status_code >= 200", true},
		{"response.status_code == 404", false},
		{"response.empty == null", true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			step, adapter := newAbsentFixture(tc.expr, common.ResponseFields{"status_code": 200, "empty": ""})
			result, err := engine.ExecuteStep(step, engine.NewState(), adapter, nil)
			if tc.pass {
				require.NoError(t, err)
				assert.Equal(t, common.StatusPassed, result.Status)
				return
			}
			var ae *engine.AssertionError
			require.ErrorAs(t, err, &ae)
			assert.Equal(t, common.StatusFailed, result.Status)
		})
	}
}

// TestAssertion_SilentCaptureSurfacesAtTheAssertion pins the capture chain
// the review found: a capture of an absent AVP is a silent no-op, so a later
// `assert: granted_units != 0` used to pass against a value that was never
// captured. The capture stays lenient (it runs on failed steps too and must
// not bury the real failure), but the assertion that depended on it now
// errors — the false pass is closed at the point where it would have lied.
func TestAssertion_SilentCaptureSurfacesAtTheAssertion(t *testing.T) {
	state := engine.NewState()

	captureStep := &scenario.Step{
		ID: "ccr", Protocol: "diameter", Action: "send", Target: "h:3868", Message: "CCR",
		Capture: map[string]string{"granted_units": "response.Granted-Service-Unit"},
	}
	captureAdapter := &mockAdapter{result: &common.StepResult{
		Status:   common.StatusPassed,
		Response: common.ResponseFields{"Result-Code": 2001}, // no Granted-Service-Unit
	}}
	_, err := engine.ExecuteStep(captureStep, state, captureAdapter, nil)
	require.NoError(t, err, "a capture that finds nothing is skipped, not failed")
	_, captured := state.Get("granted_units")
	assert.False(t, captured, "nothing must have been stored for the absent AVP")

	assertStep, assertAdapter := newAbsentFixture("granted_units != 0", common.ResponseFields{})
	result, err := engine.ExecuteStep(assertStep, state, assertAdapter, nil)
	require.Error(t, err, "the assertion that depended on the missing capture must error, not pass")
	assert.Contains(t, err.Error(), "granted_units")
	assert.Equal(t, common.StatusFailed, result.Status)
}

// TestEvaluateAssertionPublic_AbsentOperandIsAnError covers the standalone
// `cellrift assert` command's entry point, which was reproduced as passing
// `missing_field != 503` against a JSON input lacking that key.
func TestEvaluateAssertionPublic_AbsentOperandIsAnError(t *testing.T) {
	state := engine.NewState()
	state.Set("present", "x")

	err := engine.EvaluateAssertionPublic("missing_field != 503", state)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not present")

	require.NoError(t, engine.EvaluateAssertionPublic("present == x", state))
	require.NoError(t, engine.EvaluateAssertionPublic("missing_field == null", state))
}
