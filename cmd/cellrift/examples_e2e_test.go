package main

// examples_e2e_test.go runs the flow-control and SCTP worked examples through
// cfg.runScenario — the same entry point `cellrift run` uses — against the
// in-process mocks. Every shipped example is either proven here or in a
// sibling *_e2e_test.go, so a checked-in scenario that has silently stopped
// doing what its header claims turns the build red.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunScenario_ParallelWaitExample proves register_parallel_wait.yaml:
// both sub-steps of the cross-protocol parallel group reach their peers and
// pass, the wait step really sleeps its duration_ms, and the assert_state
// after the group sees the registration the SIP sub-step produced.
func TestRunScenario_ParallelWaitExample(t *testing.T) {
	uas := sip.NewMockUAS(t)
	defer uas.Close()
	hss := diameter.NewStrictMockHSS(t)
	defer hss.Close()

	cfg := &runConfig{
		timeout: 3 * time.Second,
		vars:    []string{"hss_target=" + hss.Addr(), "pcscf_target=" + uas.Addr()},
	}
	start := time.Now()
	result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", "register_parallel_wait.yaml"))
	require.NotNil(t, result)
	require.Equal(t, common.StatusPassed, result.Status, "steps: %+v", stepErrors(result))

	for _, id := range []string{"diameter_uar", "sip_register", "verify_registered"} {
		step := findStep(t, result, id)
		require.NotNil(t, step.Result, id)
		assert.Equal(t, common.StatusPassed, step.Result.Status, "%s: %s", id, step.Result.Error)
	}

	settle := findStep(t, result, "settle")
	assert.Equal(t, "wait", settle.Action)
	assert.GreaterOrEqual(t, settle.Result.DurationMs, int64(490), "the wait must actually sleep")
	assert.GreaterOrEqual(t, time.Since(start), 490*time.Millisecond)

	assert.Len(t, hss.RequestsOfCommand(diameter.CmdUserAuthorization), 1)
	assert.Len(t, uas.ReceivedRequests(), 1)
}

// TestRunScenario_SCTPAbortExample proves sctp_abort.yaml over a real SCTP
// association. It skips where the kernel has none, exactly as the adapter
// tests do: asserting an "aborted" outcome against a TCP substitute would be
// the confusion the feature exists to prevent.
func TestRunScenario_SCTPAbortExample(t *testing.T) {
	hss := diameter.NewSCTPMockHSS(t) // skips without kernel SCTP
	defer hss.Close()
	if tr, warnings, err := common.NewSCTPOrFallbackTransport(":0"); err == nil {
		_ = tr.Close()
		if len(warnings) > 0 {
			t.Skipf("real SCTP unavailable, transport was substituted: %v", warnings)
		}
	}

	cfg := &runConfig{timeout: 3 * time.Second, vars: []string{"hss_target=" + hss.Addr()}}
	result := cfg.runScenario(filepath.Join(scenarioDir(), "diameter", "sctp_abort.yaml"))
	require.NotNil(t, result)
	assert.Equal(t, common.StatusFailed, result.Status, "step 1 fails by design")

	aborted := findStep(t, result, "uar_sctp_abort")
	require.NotNil(t, aborted.Result)
	assert.Equal(t, common.StatusFailed, aborted.Result.Status)
	assert.Equal(t, impair.ConnStateAborted, aborted.Result.Response[impair.ConnectionStateKey])
	assert.NotContains(t, aborted.Result.Error, "assertion", "the step's own assertions must hold: %s", aborted.Result.Error)

	live := findStep(t, result, "uar_liveness")
	require.NotNil(t, live.Result)
	assert.Equal(t, common.StatusPassed, live.Result.Status, live.Result.Error)

	assert.Len(t, hss.RequestsOfCommand(diameter.CmdCapabilitiesExchange), 2,
		"the abort must force a fresh association and handshake for step 2")
}

// TestRunScenario_DelayMsExample proves register_impaired.yaml's third step:
// delay_ms holds a complete answer and is reported in impair_delay_ms.
func TestRunScenario_DelayMsExample(t *testing.T) {
	uas := sip.NewMockUAS(t)
	defer uas.Close()

	cfg := &runConfig{timeout: 5 * time.Second, targetHost: uas.Addr()}
	result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", "register_impaired.yaml"))
	require.NotNil(t, result)
	require.Equal(t, common.StatusPassed, result.Status, "steps: %+v", stepErrors(result))

	held := findStep(t, result, "register_held_answer")
	require.NotNil(t, held.Result)
	assert.GreaterOrEqual(t, held.Result.DurationMs, int64(190))
	delay, ok := held.Result.Response[impair.ImpairDelayKey]
	require.True(t, ok)
	assert.GreaterOrEqual(t, delay.(int64), int64(200))
	assert.Equal(t, 200, held.Result.Response["status_code"], "the answer was received before the hold, not lost to it")
}

// stepErrors summarises a run's failed steps for a test's failure message.
func stepErrors(result *engine.RunResult) map[string]string {
	out := map[string]string{}
	for _, s := range result.Steps {
		if s.Result != nil && s.Result.Status != common.StatusPassed {
			out[s.StepID] = s.Result.Error
		}
	}
	return out
}

// TestRunScenario_RegistrationExamplesPass proves register_basic.yaml and
// register_deregister.yaml end to end against both mocks. register_basic's
// final assert_state: registered had been impossible to pass since v1.0 —
// the state did not exist — and no test ran the file, so nothing said so.
func TestRunScenario_RegistrationExamplesPass(t *testing.T) {
	// Both files declare transport: sctp for Diameter, as a lab would; the
	// mock HSS listens on TCP, so force the same substitution the interop
	// CI job uses rather than requiring kernel SCTP for a wiring test.
	t.Setenv("CELLRIFT_TRANSPORT_FALLBACK", "tcp")
	for _, name := range []string{"register_basic.yaml", "register_deregister.yaml"} {
		t.Run(name, func(t *testing.T) {
			uas := sip.NewMockUAS(t)
			defer uas.Close()
			hss := diameter.NewStrictMockHSS(t)
			defer hss.Close()

			cfg := &runConfig{
				timeout: 3 * time.Second,
				vars:    []string{"hss_target=" + hss.Addr(), "pcscf_target=" + uas.Addr()},
			}
			result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", name))
			require.NotNil(t, result)
			assert.Equal(t, common.StatusPassed, result.Status, "steps: %+v", stepErrors(result))
		})
	}
}

// TestRunScenario_LibraryFilesAgainstMocks is the table that closes the
// gap "shipped, documented, never run": every remaining example whose
// outcome is not pinned by a dedicated test above runs here through
// cfg.runScenario against the mock its protocol needs, and must produce the
// outcome its own header promises — passed, or failed-by-design.
func TestRunScenario_LibraryFilesAgainstMocks(t *testing.T) {
	t.Setenv("CELLRIFT_TRANSPORT_FALLBACK", "tcp") // Diameter files declare sctp; the mock HSS is TCP

	type tc struct {
		file       string
		want       common.StepStatus
		failedStep string // for a failed-by-design file: the one step expected to fail
	}
	sipCases := []tc{
		{file: "ims/basic_call.yaml", want: common.StatusPassed},
		{file: "ims/call_rejected.yaml", want: common.StatusFailed, failedStep: "send_invite"},
	}
	hssCases := []tc{
		{file: "diameter/uar_uaa.yaml", want: common.StatusPassed},
		{file: "diameter/credit_control.yaml", want: common.StatusPassed},
		{file: "diameter/s6a_air_ulr.yaml", want: common.StatusPassed},
		{file: "diameter/cx_mar_sar.yaml", want: common.StatusPassed},
	}

	for _, c := range sipCases {
		t.Run(c.file, func(t *testing.T) {
			uas := sip.NewMockUAS(t)
			defer uas.Close()
			if c.failedStep != "" {
				uas.SetResponder(sip.ErrorResponder(486, "Busy Here"))
			}
			cfg := &runConfig{timeout: 3 * time.Second, targetHost: uas.Addr()}
			checkLibraryOutcome(t, cfg.runScenario(filepath.Join(scenarioDir(), c.file)), c.want, c.failedStep)
		})
	}
	for _, c := range hssCases {
		t.Run(c.file, func(t *testing.T) {
			hss := diameter.NewStrictMockHSS(t)
			defer hss.Close()
			cfg := &runConfig{timeout: 3 * time.Second, targetHost: hss.Addr()}
			checkLibraryOutcome(t, cfg.runScenario(filepath.Join(scenarioDir(), c.file)), c.want, c.failedStep)
		})
	}
}

func TestRunScenario_BasicCallTLSAgainstMock(t *testing.T) {
	serverCfg, _ := sip.SelfSignedTLSConfig(t)
	uas := sip.NewTLSMockUAS(t, serverCfg)
	defer uas.Close()
	cfg := &runConfig{timeout: 3 * time.Second, targetHost: uas.Addr()}
	result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", "basic_call_tls.yaml"))
	checkLibraryOutcome(t, result, common.StatusPassed, "")
	assert.NotEmpty(t, result.Warnings, "insecure_skip_verify: true must be reported as a run warning")
}

// checkLibraryOutcome asserts a run's status and, for a failed-by-design
// file, that exactly the promised step failed and every other step passed.
func checkLibraryOutcome(t *testing.T, result *engine.RunResult, want common.StepStatus, failedStep string) {
	t.Helper()
	require.NotNil(t, result, "the file must parse, validate and build adapters")
	assert.Equal(t, want, result.Status, "steps: %+v", stepErrors(result))
	for _, s := range result.Steps {
		require.NotNil(t, s.Result, s.StepID)
		if s.StepID == failedStep {
			assert.Equal(t, common.StatusFailed, s.Result.Status, s.StepID)
			continue
		}
		assert.Equal(t, common.StatusPassed, s.Result.Status, "%s: %s", s.StepID, s.Result.Error)
	}
}
