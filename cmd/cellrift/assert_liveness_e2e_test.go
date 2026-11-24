package main

// assert_liveness_e2e_test.go is the production-path proof for the
// timed_out and connection_state assertion keys. Both are proven at the
// adapter level already, but no adapter test touches cmd/cellrift/main.go's own
// wiring — and two production-only defects have already been found
// exactly there: adapters constructed without impair.Wrap, and
// --target-host failing to recurse into sub-steps. response.timed_out and
// response.connection_state are exactly the kind of value that could look
// correct in an adapter unit test and still never reach a real run's result
// if main.go's own plumbing dropped it somewhere between the adapter and the
// reporter — so this file runs the checked-in worked example through
// cfg.runScenario, the same entry point cellrift run itself uses.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunScenario_AssertLivenessAfterFault runs
// scenarios/ims/register_assert_liveness.yaml against two healthy mocks and
// proves the worked example's documented outcome: the two reset steps fail
// with connection_state == "reset" and timed_out == false, and the two
// liveness steps pass with timed_out == false and no connection_state key at
// all.
func TestRunScenario_AssertLivenessAfterFault(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()
	uas := sip.NewTCPMockUAS(t, sip.TCPFaultNone)
	defer uas.Close()

	// cfg.targetHost is deliberately left empty: applyTargetHostOverride sets
	// ONE target on every step, and this scenario needs SIP pointed at one
	// mock and Diameter at another. The scenario declares its targets as
	// variables instead, overridden below via cfg.vars.
	cfg := &runConfig{
		timeout: 2 * time.Second,
		vars: []string{
			"hss_target=" + hss.Addr(),
			"pcscf_target=" + uas.Addr(),
		},
	}

	result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", "register_assert_liveness.yaml"))
	require.NotNil(t, result)
	assert.Equal(t, common.StatusFailed, result.Status,
		"two of this scenario's four steps destroy their own connection and report failed by design; "+
			"the per-step assertions below are what actually matter")

	for _, id := range []string{"diameter_reset", "sip_reset"} {
		step := findStep(t, result, id)
		require.NotNil(t, step.Result)
		assert.Equal(t, common.StatusFailed, step.Result.Status, id)
		assert.Equal(t, impair.ConnStateReset, step.Result.Response[impair.ConnectionStateKey], id)
		assert.Equal(t, false, step.Result.Response[impair.TimedOutKey],
			"%s: a faulted step returns before any receive, so timed_out must be a present false, never true and never missing", id)
	}

	for _, id := range []string{"diameter_liveness", "sip_liveness"} {
		step := findStep(t, result, id)
		require.NotNil(t, step.Result)
		assert.Equal(t, common.StatusPassed, step.Result.Status, "%s error: %v", id, step.Result.Error)
		timedOut, present := step.Result.Response[impair.TimedOutKey]
		require.True(t, present, "%s: timed_out must be present on a successful step too", id)
		assert.Equal(t, false, timedOut, id)
		assert.NotContains(t, step.Result.Response, impair.ConnectionStateKey,
			"%s: connection_state appears only when a fault fired", id)
	}
}

// TestRunScenario_TimedOutSurvivesProductionWiring runs the same scenario
// file against the same healthy Diameter mock, but a SIP mock that accepts
// and reads a request and then never answers. This is the production-path
// proof of timed_out's true case, which the worked example itself cannot
// demonstrate against a healthy peer — sip_liveness fails here instead of
// passing, and it fails with timed_out == true, not a connection fault.
func TestRunScenario_TimedOutSurvivesProductionWiring(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()
	uas := sip.NewTCPMockUAS(t, sip.TCPFaultSilence)
	defer uas.Close()

	cfg := &runConfig{
		timeout: 500 * time.Millisecond,
		vars: []string{
			"hss_target=" + hss.Addr(),
			"pcscf_target=" + uas.Addr(),
		},
	}

	start := time.Now()
	result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", "register_assert_liveness.yaml"))
	elapsed := time.Since(start)
	require.NotNil(t, result)
	assert.Less(t, elapsed, 10*time.Second, "a silent peer must cost the step timeout, not hang the run")

	silent := findStep(t, result, "sip_liveness")
	require.NotNil(t, silent.Result)
	assert.Equal(t, common.StatusFailed, silent.Result.Status)
	assert.Equal(t, true, silent.Result.Response[impair.TimedOutKey],
		"a peer that accepted the request and then stayed silent is exactly what timed_out reports")
	assert.NotContains(t, silent.Result.Response, impair.ConnectionStateKey,
		"peer silence is not a connection fault; the two keys are mutually exclusive by construction")

	// The Diameter half still succeeds against its own healthy mock, proving
	// timed_out is per-step and not a run-level flag.
	live := findStep(t, result, "diameter_liveness")
	require.NotNil(t, live.Result)
	assert.Equal(t, common.StatusPassed, live.Result.Status, "error: %v", live.Result.Error)
	assert.Equal(t, false, live.Result.Response[impair.TimedOutKey])
}
