package engine_test

import (
	"testing"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin two coupled rules about assertions on a step the adapter
// already failed (a connection fault, a non-2xx, a timeout):
//
//  1. They RUN. impair.FaultedResult always promised "the step's assertions
//     still run", but evaluation used to be gated on StatusPassed, so every
//     assertion on a failed step was dead code — the shipped connection-fault
//     scenario's three `response.connection_state == ...` checks never
//     executed once.
//  2. They can only ADD a failure. A passing assertion does not rescue a step
//     the adapter failed (that would silently change what a fault step
//     means), and a failing one is appended to the adapter's own error rather
//     than overwriting it, so the root cause is never lost.

func failedFixture(assertExpr string) (*scenario.Step, *mockAdapter) {
	return &scenario.Step{
			ID: "s", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER",
			Assert: []string{assertExpr},
		}, &mockAdapter{result: &common.StepResult{
			Status:   common.StatusFailed,
			Error:    "connection reset by peer after send",
			Response: common.ResponseFields{"connection_state": "reset"},
		}}
}

func TestFailedStep_PassingAssertionRunsButDoesNotRescue(t *testing.T) {
	step, adapter := failedFixture("response.connection_state == reset")
	result, err := engine.ExecuteStep(step, engine.NewState(), adapter, nil)

	require.NoError(t, err, "a passing assertion on a failed step is not itself an error")
	assert.Equal(t, common.StatusFailed, result.Status, "the adapter's verdict stands")
	assert.Equal(t, "connection reset by peer after send", result.Error, "the root-cause error is untouched")
}

func TestFailedStep_FailingAssertionAppendsToRootCause(t *testing.T) {
	step, adapter := failedFixture("response.connection_state == closed")
	result, err := engine.ExecuteStep(step, engine.NewState(), adapter, nil)

	require.Error(t, err, "the assertion genuinely failed and must be reported")
	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Contains(t, result.Error, "connection reset by peer after send", "the adapter's own error must be preserved")
	assert.Contains(t, result.Error, "assertion failed", "the assertion failure must be visible too")
	assert.Contains(t, result.Error, "actual: reset")
}

func TestFailedStep_AbsentOperandStillSurfaces(t *testing.T) {
	// A timed-out step has no response at all; asserting on a field it
	// could not have is an authoring error that should be visible, appended
	// after the timeout so the timeout stays the headline.
	step := &scenario.Step{
		ID: "s", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER",
		Assert: []string{"response.status_code == 200"},
	}
	adapter := &mockAdapter{result: &common.StepResult{
		Status: common.StatusFailed,
		Error:  "receive timeout: i/o timeout",
	}}
	result, err := engine.ExecuteStep(step, engine.NewState(), adapter, nil)

	require.Error(t, err)
	assert.Contains(t, result.Error, "receive timeout: i/o timeout")
	assert.Contains(t, result.Error, "is not present")
}

// TestAssertion_QuotedRightHandSideMatchesUnquotedValue pins quote stripping.
// In YAML, `- x == "truncated"` is a plain scalar whose quotes are literal
// characters, so the comparison used to be against `"truncated"` with the
// quotes and could never match. Every shipped connection-fault assertion is
// written this way; they were only ever green because they never ran.
func TestAssertion_QuotedRightHandSideMatchesUnquotedValue(t *testing.T) {
	cases := []struct {
		name string
		expr string
		pass bool
	}{
		{"double quotes", `response.state == "reset"`, true},
		{"single quotes", `response.state == 'reset'`, true},
		{"unquoted still works", `response.state == reset`, true},
		{"quoted mismatch still fails", `response.state == "closed"`, false},
		{"!= with quotes", `response.state != "closed"`, true},
		{"quoted numeric", `response.code == "200"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step := &scenario.Step{
				ID: "s", Protocol: "sip", Action: "send", Target: "h:5060", Message: "OPTIONS",
				Assert: []string{tc.expr},
			}
			adapter := &mockAdapter{result: &common.StepResult{
				Status:   common.StatusPassed,
				Response: common.ResponseFields{"state": "reset", "code": 200},
			}}
			result, err := engine.ExecuteStep(step, engine.NewState(), adapter, nil)
			if tc.pass {
				require.NoError(t, err)
				assert.Equal(t, common.StatusPassed, result.Status)
				return
			}
			require.Error(t, err)
			assert.Equal(t, common.StatusFailed, result.Status)
		})
	}
}

// TestAssertion_PartialQuotesAreLiteral is the boundary: only a fully wrapped
// matching pair is stripped. A value that merely begins with a quote (a
// quoted SIP display name inside a longer header) compares literally.
func TestAssertion_PartialQuotesAreLiteral(t *testing.T) {
	step := &scenario.Step{
		ID: "s", Protocol: "sip", Action: "send", Target: "h:5060", Message: "OPTIONS",
		Assert: []string{`response.from == "Alice" <sip:alice@ims.local>`},
	}
	adapter := &mockAdapter{result: &common.StepResult{
		Status:   common.StatusPassed,
		Response: common.ResponseFields{"from": `"Alice" <sip:alice@ims.local>`},
	}}
	result, err := engine.ExecuteStep(step, engine.NewState(), adapter, nil)
	require.NoError(t, err, "a leading quote with no matching trailing quote must not be stripped")
	assert.Equal(t, common.StatusPassed, result.Status)
}
